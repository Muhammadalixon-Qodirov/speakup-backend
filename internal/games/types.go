package games

import (
	"time"
)

// GameType identifies a mini-game. String values are the wire contract
// shared with the frontend - do not rename without updating the client.
type GameType string

const (
	StoryChain  GameType = "story_chain"
	WordChain   GameType = "word_chain"
	VocabSprint GameType = "vocab_sprint"
	// Phase 2 — synonym/antonym bank.
	SynonymDuel GameType = "synonym_duel"
	// Phase 3 — browser TTS (Dictation Race), no external API.
	SpellingRace GameType = "spelling_race"
)

// Mode distinguishes competitive games (a winner + XP split) from
// collaborative icebreakers (no winner, both get participation XP).
type Mode string

const (
	Competitive Mode = "competitive"
	Icebreaker  Mode = "icebreaker"
)

// Status is the lifecycle state of a single game instance.
type Status string

const (
	StatusPending  Status = "pending"  // invited, awaiting accept
	StatusActive   Status = "active"   // in progress
	StatusFinished Status = "finished" // completed normally
	StatusAborted  Status = "aborted"  // cancelled / disconnected
)

// M is a convenience alias for a JSON-ish payload map.
type M = map[string]interface{}

// Player is one participant. Index 0 is always the inviter.
type Player struct {
	ID   string
	Name string
}

// Info describes a game for the launcher menu and lifecycle rules.
type Info struct {
	Type GameType `json:"type"`
	Mode Mode     `json:"mode"`
	// MaxDuration is a hard ceiling after which the manager force-ends
	// the game even if no round timer fired (safety net against a
	// wedged game holding memory forever). 0 = no ceiling.
	MaxDuration time.Duration `json:"-"`
}

// Outcome is the engine's verdict when a game ends. The manager turns
// this into XP, a persisted GameResult row, and the `game_over` event.
type Outcome struct {
	// WinnerIdx is 0 or 1 for the winning player, or -1 for a draw /
	// collaborative game (no winner).
	WinnerIdx int
	Score     [2]int
	// Summary is shown on the result screen (game-specific: word lists,
	// the finished story, per-round breakdown, etc.).
	Summary M
}

// Notify is injected at startup (main wires it to ws.SendToUser) so the
// games package can push events to clients WITHOUT importing the ws
// package - which would create an import cycle (ws → games already).
// Mirrors the existing services.ReferralBonusFor injection pattern.
var Notify func(userID, event string, data interface{})

func emitTo(userID, event string, data interface{}) {
	if Notify != nil {
		Notify(userID, event, data)
	}
}

// StoryJudgement is the LLM's verdict on a Story Chain duel: a 0-100 score and
// short feedback per player, plus a one-line verdict on who continued better.
type StoryJudgement struct {
	Score0, Score1       float64
	Feedback0, Feedback1 string
	Verdict              string
}

// StoryJudgeFn, when wired (main → services.JudgeStory), scores both players'
// Story Chain continuations with the LLM (coherence, grammar, creativity,
// meaningful continuation). Injected to avoid a games → services cycle; nil
// falls back to a length-based score so the game always resolves.
var StoryJudgeFn func(starter, story0, story1 string) (StoryJudgement, error)

// Engine is implemented by every mini-game. The manager owns all the
// shared lifecycle (invite/accept/timeout/persist); an engine only
// implements its own rules.
type Engine interface {
	Info() Info
	// Start initialises game-specific state and emits the opening
	// snapshot. Called once when both players are in.
	Start(c *Ctx)
	// Move processes player `p`'s (0 or 1) action.
	Move(c *Ctx, p int, payload []byte) error
	// Timeout fires when a round timer set via c.SetTimer expires.
	Timeout(c *Ctx)
}

// registry maps a GameType to a factory that builds a fresh engine
// instance per game (engines hold per-game state, so each game gets
// its own).
var registry = map[GameType]func() Engine{}

func register(t GameType, factory func() Engine) {
	registry[t] = factory
}

// IsRegistered reports whether a game type is implemented + enabled.
func IsRegistered(t GameType) bool {
	_, ok := registry[t]
	return ok
}
