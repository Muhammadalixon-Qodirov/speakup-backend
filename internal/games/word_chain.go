package games

import (
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"time"
)

func init() { register(WordChain, func() Engine { return &wordChain{} }) }

// wordChain — competitive. The system picks a random START LETTER; players then
// alternate words. Each word must start with the REQUIRED letter (the random
// start letter on move 1, then the previous word's last letter), be a real
// English word (>= 3 letters) and not repeat.
//
// Invalid submissions (wrong letter / repeat / not a word / too short) are
// REJECTED so the player can retry within the timer - they DON'T lose the
// match. Only running out of time loses, so a valid-but-unlisted word never
// costs you the game. Score = sum of SPEED POINTS: the quicker each reply, the
// more points it earns.
type wordChain struct {
	turn        int
	last        string
	startLetter byte
	used        map[string]struct{}
	links       int
	history     []string
	turnStart   time.Time // when the current player's turn began (for speed points)
	scores      [2]int    // accumulated per-player speed points
}

const (
	wordChainTurnTimeout = 10 * time.Second
	wordChainMinLen      = 3
)

// wordChainStartLetters are "fair" opening letters - common enough that the
// first player can actually find a word in time (no q/x/z trap on move 1).
var wordChainStartLetters = []byte("abcdefghilmnoprstuw")

func (w *wordChain) Info() Info {
	return Info{Type: WordChain, Mode: Competitive, MaxDuration: 15 * time.Minute}
}

func (w *wordChain) Start(c *Ctx) {
	w.turn = 0
	w.last = ""
	w.startLetter = wordChainStartLetters[rand.Intn(len(wordChainStartLetters))]
	w.used = make(map[string]struct{})
	w.links = 0
	w.history = nil
	w.scores = [2]int{}
	w.turnStart = time.Now()
	c.G.State = w
	c.SetTimer(wordChainTurnTimeout)
	c.Snapshot(w.public())
}

type wordMovePayload struct {
	Word string `json:"word"`
}

// requiredLetter is the letter the next word must start with: the random start
// letter on the first move, then the previous word's last letter.
func (w *wordChain) requiredLetter() byte {
	if w.last != "" {
		return w.last[len(w.last)-1]
	}
	return w.startLetter
}

func (w *wordChain) Move(c *Ctx, p int, payload []byte) error {
	if p != w.turn {
		return errors.New("not_your_turn")
	}
	var mv wordMovePayload
	if err := json.Unmarshal(payload, &mv); err != nil {
		return errors.New("bad_payload")
	}
	word, alpha := normalizeWord(mv.Word)

	// Invalid submissions are rejected (retry within the timer), NOT a loss.
	if !alpha || len(word) < wordChainMinLen {
		return errors.New("too_short")
	}
	if word[0] != w.requiredLetter() {
		return errors.New("wrong_letter")
	}
	if _, seen := w.used[word]; seen {
		return errors.New("repeated_word")
	}
	if !IsWord(word) {
		return errors.New("not_a_word")
	}

	// Valid link: award speed points (faster reply = more) and pass the turn.
	w.scores[p] += speedPoints(time.Since(w.turnStart))
	w.used[word] = struct{}{}
	w.history = append(w.history, word)
	w.last = word
	w.links++
	w.turn = Other(w.turn)
	w.turnStart = time.Now()
	c.SetTimer(wordChainTurnTimeout)
	c.Update(w.public())
	return nil
}

// Timeout — the player on turn ran out of time and loses. This is the ONLY way
// to lose: invalid moves just get rejected for a retry.
func (w *wordChain) Timeout(c *Ctx) {
	w.end(c, Other(w.turn), "timeout")
}

func (w *wordChain) end(c *Ctx, winnerIdx int, reason string) {
	c.End(Outcome{
		WinnerIdx: winnerIdx,
		Score:     [2]int{w.scores[0], w.scores[1]}, // speed points per player
		Summary: M{
			"reason":    reason,
			"links":     w.links,
			"words":     w.history,
			"scores":    []int{w.scores[0], w.scores[1]},
			"loser_idx": Other(winnerIdx),
		},
	})
}

// speedPoints rewards a quick reply: the seconds left on the 10s clock when the
// word was submitted (rounded up), minimum 1. Instant ≈ 10, slow ≈ 1.
func speedPoints(elapsed time.Duration) int {
	pts := int(math.Ceil((wordChainTurnTimeout - elapsed).Seconds()))
	if pts < 1 {
		pts = 1
	}
	return pts
}

func (w *wordChain) public() M {
	return M{
		"turn":            w.turn,
		"last_word":       w.last,
		"required_letter": string(w.requiredLetter()),
		"links":           w.links,
		"count":           len(w.history),
		"scores":          []int{w.scores[0], w.scores[1]},
	}
}
