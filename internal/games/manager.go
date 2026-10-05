package games

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/safego"
)

// inviteTTL is how long a pending invite stays open before it auto-
// expires. Long enough to read + decide, short enough that a forgotten
// invite doesn't linger on the partner's screen.
const inviteTTL = 60 * time.Second

// staleActiveAfter is a safety net: an "active" game running far longer
// than any real game could is treated as wedged and cleared so it can
// never permanently block new invites in a session.
const staleActiveAfter = 15 * time.Minute

// Game is a single live mini-game instance. State lives only in memory
// (like chat) - a server restart drops in-flight games, which is fine:
// they're tied to a live audio session and trivially restartable.
type Game struct {
	ID        string
	Type      GameType
	Mode      Mode
	SessionID string
	Players   [2]Player
	Status    Status
	CreatedAt time.Time

	engine Engine
	State  interface{} // engine-owned per-game state
	mgr    *Manager    // owning manager, for engine-triggered async resolution

	deadline    time.Time
	timer       *time.Timer
	inviteTimer *time.Timer
	gen         int // timer generation; guards against stale firings
}

// Manager owns every live game and all the shared lifecycle. A single
// mutex guards the whole map + per-game fields; games are short and
// low-contention so finer locking isn't worth the complexity. DB writes
// are always done OFF the lock (via safego goroutines).
type Manager struct {
	mu    sync.Mutex
	games map[string]*Game
}

// Default is the process-wide game manager.
var Default = &Manager{games: make(map[string]*Game)}

// Short, stable error codes — surfaced to the client as `game_error`
// codes and mapped to localized toasts on the frontend, so keep them
// terse and don't reword without updating the client i18n keys.
var (
	errUnknownGame = errors.New("unknown_game")
	errBusy        = errors.New("busy")
	errHasInvite   = errors.New("has_invite")
	errNotFound    = errors.New("not_found")
	errNotInvitee  = errors.New("not_invitee")
	errNotActive   = errors.New("not_active")
	errNotPlayer   = errors.New("not_player")
)

// playerIndex returns 0/1 for a userID, or -1 if not a player.
func (g *Game) playerIndex(userID string) int {
	switch userID {
	case g.Players[0].ID:
		return 0
	case g.Players[1].ID:
		return 1
	}
	return -1
}

// Invite creates a pending game and notifies the partner. `from` is the
// inviter (index 0), `to` the partner (index 1). The caller (ws layer)
// must have already verified they're partners in the session.
func (m *Manager) Invite(sessionID string, from, to Player, t GameType) (string, error) {
	factory, ok := registry[t]
	if !ok {
		return "", errUnknownGame
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Reconcile any existing game for this session so a stale invite can
	// never block a fresh one (the root cause of spurious "busy" errors):
	//   • ACTIVE game  → a real game is in progress, so block with errBusy
	//     (unless it's wedged far past any real duration → clear it).
	//   • PENDING and I'm the invitee → the partner already invited ME;
	//     I should accept theirs, not start a competitor (errHasInvite).
	//   • PENDING and I'm the inviter → supersede my old invite (e.g. a
	//     re-tap, or both sides invited at once) and create a fresh one.
	for id, g := range m.games {
		if g.SessionID != sessionID {
			continue
		}
		if g.Status == StatusActive {
			if time.Since(g.CreatedAt) > staleActiveAfter {
				m.abortLocked(g, "stale")
				continue
			}
			return "", errBusy
		}
		if g.Status == StatusPending {
			if g.Players[1].ID == from.ID {
				return "", errHasInvite
			}
			if g.inviteTimer != nil {
				g.inviteTimer.Stop()
			}
			delete(m.games, id)
			emitTo(g.Players[0].ID, "game_declined", M{"game_id": id, "reason": "superseded"})
			emitTo(g.Players[1].ID, "game_declined", M{"game_id": id, "reason": "superseded"})
		}
	}

	eng := factory()
	g := &Game{
		ID:        uuid.New().String(),
		Type:      t,
		Mode:      eng.Info().Mode,
		SessionID: sessionID,
		Players:   [2]Player{from, to},
		Status:    StatusPending,
		CreatedAt: time.Now(),
		engine:    eng,
		mgr:       m,
	}
	m.games[g.ID] = g

	gameID := g.ID
	g.inviteTimer = time.AfterFunc(inviteTTL, func() {
		m.expireInvite(gameID)
	})

	emitTo(to.ID, "game_invited", M{
		"game_id":      g.ID,
		"game_type":    string(t),
		"mode":         string(g.Mode),
		"from_user_id": from.ID,
		"from_name":    from.Name,
		"expires_in":   int(inviteTTL / time.Second),
	})
	emitTo(from.ID, "game_invite_sent", M{
		"game_id":   g.ID,
		"game_type": string(t),
	})
	return gameID, nil
}

func (m *Manager) expireInvite(gameID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.games[gameID]
	if g == nil || g.Status != StatusPending {
		return
	}
	delete(m.games, gameID)
	emitTo(g.Players[0].ID, "game_declined", M{"game_id": gameID, "reason": "expired"})
	emitTo(g.Players[1].ID, "game_declined", M{"game_id": gameID, "reason": "expired"})
}

// Accept starts a pending game. Only the invited player (index 1) may
// accept.
func (m *Manager) Accept(gameID, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	g := m.games[gameID]
	if g == nil {
		return errNotFound
	}
	if g.Status != StatusPending {
		return errNotActive
	}
	if userID != g.Players[1].ID {
		return errNotInvitee
	}
	if g.inviteTimer != nil {
		g.inviteTimer.Stop()
		g.inviteTimer = nil
	}
	g.Status = StatusActive

	c := &Ctx{G: g}
	g.engine.Start(c)
	m.processStart(g, c)
	return nil
}

// Decline rejects a pending invite. Only the invited player may decline.
func (m *Manager) Decline(gameID, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	g := m.games[gameID]
	if g == nil {
		return errNotFound
	}
	if userID != g.Players[1].ID {
		return errNotInvitee
	}
	if g.inviteTimer != nil {
		g.inviteTimer.Stop()
	}
	delete(m.games, gameID)
	emitTo(g.Players[0].ID, "game_declined", M{"game_id": gameID, "reason": "declined"})
	return nil
}

// Move applies a player's action to an active game.
func (m *Manager) Move(gameID, userID string, payload []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	g := m.games[gameID]
	if g == nil {
		return errNotFound
	}
	if g.Status != StatusActive {
		return errNotActive
	}
	p := g.playerIndex(userID)
	if p < 0 {
		return errNotPlayer
	}

	c := &Ctx{G: g}
	if err := g.engine.Move(c, p, payload); err != nil {
		emitTo(userID, "game_error", M{"game_id": gameID, "code": err.Error()})
		return nil
	}
	m.processUpdate(g, c)
	return nil
}

// ResolveStory finalizes a game after an engine's async work (the Story Chain
// LLM judge) produces its outcome. Called from a goroutine; a no-op if the game
// already ended (abort/disconnect). Mirrors the lock + finalize path of Move.
func (m *Manager) ResolveStory(gameID string, o Outcome) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.games[gameID]
	if g == nil || g.Status != StatusActive {
		return
	}
	m.finalize(g, o)
}

// Abort ends a game early (a player tapped "quit"). Both sides get a
// `game_over` with aborted=true and no XP.
func (m *Manager) Abort(gameID, userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.games[gameID]
	if g == nil {
		return
	}
	if g.playerIndex(userID) < 0 {
		return
	}
	m.abortLocked(g, "quit")
}

// HandleDisconnect aborts any game the user is in when their socket
// drops. Called from the ws disconnect path.
func (m *Manager) HandleDisconnect(userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, g := range m.games {
		if g.playerIndex(userID) >= 0 {
			m.abortLocked(g, "disconnected")
		}
	}
}

// AbortSession clears any game tied to a speaking session when that
// session ends, so a leftover game can't block future invites or linger
// in memory. Players get a game_over(aborted) so any open game UI closes.
func (m *Manager) AbortSession(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, g := range m.games {
		if g.SessionID == sessionID {
			m.abortLocked(g, "session_ended")
		}
	}
}

// abortLocked tears down a game and notifies both players. Caller holds m.mu.
func (m *Manager) abortLocked(g *Game, reason string) {
	if g.timer != nil {
		g.timer.Stop()
	}
	if g.inviteTimer != nil {
		g.inviteTimer.Stop()
	}
	g.Status = StatusAborted
	delete(m.games, g.ID)
	for _, p := range g.Players {
		emitTo(p.ID, "game_over", M{
			"game_id": g.ID,
			"aborted": true,
			"reason":  reason,
		})
	}
}

// --- ctx processing (caller holds m.mu) ---

func (m *Manager) applyTimer(g *Game, c *Ctx) int64 {
	if c.clearTimer {
		if g.timer != nil {
			g.timer.Stop()
			g.timer = nil
		}
		g.deadline = time.Time{}
		return 0
	}
	if c.timerSet > 0 {
		if g.timer != nil {
			g.timer.Stop()
		}
		g.gen++
		gen := g.gen
		gameID := g.ID
		g.deadline = time.Now().Add(c.timerSet)
		g.timer = time.AfterFunc(c.timerSet, func() {
			m.fireTimeout(gameID, gen)
		})
	}
	if g.deadline.IsZero() {
		return 0
	}
	return g.deadline.UnixMilli()
}

func (m *Manager) processStart(g *Game, c *Ctx) {
	deadline := m.applyTimer(g, c)
	if c.ended {
		m.finalize(g, c.outcome)
		return
	}
	for idx, p := range g.Players {
		emitTo(p.ID, "game_start", M{
			"game_id":   g.ID,
			"game_type": string(g.Type),
			"mode":      string(g.Mode),
			"you_index": idx,
			"state":     c.snapshot,
			"deadline":  deadline,
		})
	}
}

func (m *Manager) processUpdate(g *Game, c *Ctx) {
	deadline := m.applyTimer(g, c)
	if c.ended {
		m.finalize(g, c.outcome)
		return
	}
	if c.update == nil {
		return
	}
	for _, p := range g.Players {
		emitTo(p.ID, "game_update", M{
			"game_id":  g.ID,
			"state":    c.update,
			"deadline": deadline,
		})
	}
}

func (m *Manager) fireTimeout(gameID string, gen int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.games[gameID]
	if g == nil || g.Status != StatusActive || g.gen != gen {
		return // stale or superseded timer
	}
	c := &Ctx{G: g}
	g.engine.Timeout(c)
	m.processUpdate(g, c)
}

// finalize awards XP, persists the result, and broadcasts game_over.
// Caller holds m.mu. DB work is offloaded to a goroutine.
func (m *Manager) finalize(g *Game, o Outcome) {
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
	g.Status = StatusFinished
	delete(m.games, g.ID)

	xp := computeXP(g.Mode, o)

	// Emit per-player game_over so each side gets their own perspective.
	for idx, p := range g.Players {
		result := "loss"
		if g.Mode == Icebreaker {
			result = "complete"
		} else if o.WinnerIdx < 0 {
			result = "draw"
		} else if o.WinnerIdx == idx {
			result = "win"
		}
		emitTo(p.ID, "game_over", M{
			"game_id":    g.ID,
			"game_type":  string(g.Type),
			"you_index":  idx,
			"result":     result,
			"winner_idx": o.WinnerIdx,
			"score":      []int{o.Score[0], o.Score[1]},
			"xp":         xp[idx],
			"summary":    o.Summary,
		})
	}

	// Persist + update stats off-lock.
	game := *g // shallow copy of the fields we need
	safego.Go("games.persist", func() {
		persistResult(&game, o, xp)
	})
}

// computeXP turns an outcome into per-player XP, per the spec:
//
//	win +10 / loss +3 / draw +6 each / collaborative +3 each.
func computeXP(mode Mode, o Outcome) [2]int {
	if mode == Icebreaker {
		return [2]int{3, 3}
	}
	switch {
	case o.WinnerIdx == 0:
		return [2]int{10, 3}
	case o.WinnerIdx == 1:
		return [2]int{3, 10}
	default:
		return [2]int{6, 6}
	}
}

func persistResult(g *Game, o Outcome, xp [2]int) {
	p1, err1 := uuid.Parse(g.Players[0].ID)
	p2, err2 := uuid.Parse(g.Players[1].ID)
	if err1 != nil || err2 != nil {
		return
	}

	var winner *uuid.UUID
	if g.Mode == Competitive && o.WinnerIdx >= 0 {
		w, _ := uuid.Parse(g.Players[o.WinnerIdx].ID)
		winner = &w
	}
	var sid *uuid.UUID
	if g.SessionID != "" {
		if s, err := uuid.Parse(g.SessionID); err == nil {
			sid = &s
		}
	}
	metaBytes, _ := json.Marshal(o.Summary)

	res := &models.GameResult{
		GameType:  string(g.Type),
		SessionID: sid,
		Player1ID: p1,
		Player2ID: p2,
		WinnerID:  winner,
		Score1:    o.Score[0],
		Score2:    o.Score[1],
		XP1:       xp[0],
		XP2:       xp[1],
		Meta:      string(metaBytes),
	}
	if err := database.DB.Create(res).Error; err != nil {
		return
	}

	// Atomic per-user stat bump.
	ids := [2]uuid.UUID{p1, p2}
	for i, id := range ids {
		won := 0
		if winner != nil && *winner == id {
			won = 1
		}
		database.DB.Exec(
			"UPDATE users SET games_played = games_played + 1, games_won = games_won + ?, game_xp = game_xp + ? WHERE id = ?",
			won, xp[i], id,
		)
	}
}
