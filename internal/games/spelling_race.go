package games

import "time"

func init() { register(SpellingRace, newSpellingRace) }

// Spelling / Dictation Race — 5 rounds, 30s each. The client reads a
// sentence aloud via the browser's speech synthesis (the text is sent
// but never shown); both players type what they hear. Score rewards
// accuracy first, speed second:
//
//	score = wordAccuracy×100 − seconds×0.5   (floored at 0)
//
// Highest total over 5 rounds wins.
func newSpellingRace() Engine {
	loadBanks()
	pool := dictationAll() // embedded seed + AI-generated sentences
	const rounds = 5
	idxs := pickIndices(len(pool), rounds)

	targets := make([]string, len(idxs))
	prompts := make([]M, len(idxs))
	reveals := make([]M, len(idxs))
	for i, ix := range idxs {
		s := pool[ix]
		targets[i] = s.Text
		// `sentence` is for client-side TTS only - the UI must not render
		// it. `level` lets the UI show difficulty.
		prompts[i] = M{"sentence": s.Text, "level": s.Level}
		reveals[i] = M{"target": s.Text}
	}

	e := &roundsEngine{
		gtype:     SpellingRace,
		duration:  30 * time.Second,
		revealDur: 3 * time.Second,
		prompts:   prompts,
		reveals:   reveals,
	}
	e.evaluate = func(round int, answer string, elapsed time.Duration) (string, float64, M) {
		acc := seqAccuracy(answer, targets[round])
		pts := acc*100 - elapsed.Seconds()*0.5
		if pts < 0 {
			pts = 0
		}
		return "score", pts, M{"accuracy": round1(acc * 100)}
	}
	return e
}
