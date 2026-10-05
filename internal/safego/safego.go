// Package safego provides a tiny helper for launching goroutines that
// can never crash the whole process. Every fire-and-forget background
// task in this codebase should go through Go() so a panic in one
// notification handler doesn't take the server down with it.
package safego

import (
	"runtime/debug"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Alert rate limiter - max 3 alerts per minute to prevent Telegram spam
// when a goroutine enters a crash loop.
var (
	alertMu         sync.Mutex
	alertHistory    = make([]time.Time, 0, 10)
	suppressedCount int
	maxAlertsPerMin = 3
	alertWindow     = time.Minute
)

// shouldSendAlert returns true if we haven't exceeded the per-minute cap.
func shouldSendAlert() bool {
	alertMu.Lock()
	defer alertMu.Unlock()

	now := time.Now()
	cutoff := now.Add(-alertWindow)

	// Drop stale entries.
	filtered := alertHistory[:0]
	for _, t := range alertHistory {
		if t.After(cutoff) {
			filtered = append(filtered, t)
		}
	}
	alertHistory = filtered

	if len(alertHistory) >= maxAlertsPerMin {
		suppressedCount++
		return false
	}
	alertHistory = append(alertHistory, now)
	return true
}

// AlertHook, when set at startup, is invoked from the panic-recovery
// path so production crashes can be fanned out to a Telegram chat. We
// can't import internal/bot here (would cycle), so the bot package
// installs this hook during InitBot().
var AlertHook func(title string, body string)

// Go runs fn in a new goroutine guarded by a recover() block. If fn
// panics, the stack trace is logged, the optional AlertHook is fired,
// and the goroutine exits cleanly - the rest of the process keeps
// running. `name` is a short human-readable label that appears in logs
// and alerts so you can tell which background task blew up.
func Go(name string, fn func()) {
	go func() {
		defer recoverAndLog(name)
		fn()
	}()
}

// Run executes fn in the current goroutine with the same recover
// guarantee - useful inside long-running loops (hub event loop, etc.)
// where you don't want to start a new goroutine but still need a
// crash-safety net.
func Run(name string, fn func()) {
	defer recoverAndLog(name)
	fn()
}

func recoverAndLog(name string) {
	if r := recover(); r != nil {
		stack := string(debug.Stack())
		log.Error().
			Str("task", name).
			Interface("panic", r).
			Str("stack", stack).
			Msg("safego: recovered panic")
		if AlertHook != nil && shouldSendAlert() {
			// AlertHook is best-effort - wrap in another recover so a
			// faulty hook can't take us down on top of the original
			// panic.
			func() {
				defer func() { _ = recover() }()
				AlertHook("panic in "+name, truncate(stack, 1500))
			}()
		}
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
