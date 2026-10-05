package games

import "time"

// Ctx is the engine ⇄ manager bridge for a single Start/Move/Timeout
// call. The engine mutates it; when the engine returns, the manager
// reads the queued intent (snapshot/update/end + timer) and performs
// the side effects (sending events, scheduling timers, persisting).
//
// An engine NEVER sends events or touches timers directly - it only
// records intent here. This keeps all transport + concurrency in the
// manager and makes engines trivial to reason about.
type Ctx struct {
	G *Game

	// Exactly one of these flavours of outbound state is used per call:
	snapshot M       // → broadcast as game_start
	update   M       // → broadcast as game_update
	ended    bool    // → broadcast as game_over
	outcome  Outcome // valid when ended

	// Timer intent. timerSet>0 schedules/resets the round timer to that
	// duration; clearTimer cancels any pending timer. Mutually exclusive.
	timerSet   time.Duration
	clearTimer bool
}

// Snapshot sets the opening public state. Valid only inside Start; the
// manager broadcasts it as `game_start`.
func (c *Ctx) Snapshot(state M) { c.snapshot = state }

// Update sets the new public state, broadcast as `game_update`. Use in
// Move/Timeout to push the game forward.
func (c *Ctx) Update(state M) { c.update = state }

// SetTimer schedules (or resets) the round timer. When it fires the
// manager calls the engine's Timeout. The client receives the absolute
// deadline so it can render a countdown.
func (c *Ctx) SetTimer(d time.Duration) {
	c.timerSet = d
	c.clearTimer = false
}

// ClearTimer cancels any pending round timer (e.g. the game ended early
// or moved to an untimed phase).
func (c *Ctx) ClearTimer() {
	c.clearTimer = true
	c.timerSet = 0
}

// End finishes the game with the given outcome. The manager awards XP,
// persists a GameResult, and broadcasts `game_over`. Any pending timer
// is cancelled automatically.
func (c *Ctx) End(o Outcome) {
	c.ended = true
	c.outcome = o
	c.clearTimer = true
	c.timerSet = 0
}

// --- small helpers engines reuse ---

// Other returns the opponent's player index (0↔1).
func Other(p int) int {
	if p == 0 {
		return 1
	}
	return 0
}
