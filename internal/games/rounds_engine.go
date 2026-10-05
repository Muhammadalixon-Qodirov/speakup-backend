package games

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
)

// roundsEngine is a reusable base for round-based competitive games where
// BOTH players answer the same prompt each round, scores accumulate, and
// the higher total wins. It powers two games via configuration:
//
//	synonym_duel  — multi-answer (list as many as you can), 5 rounds
//	spelling_race — accuracy-minus-time score, 5 rounds
//
// Each round runs in two phases: "answer" (players submit, timed) then a
// brief "reveal" (correct answer shown) before advancing.
type roundsEngine struct {
	gtype     GameType
	duration  time.Duration // answer window per round
	revealDur time.Duration // how long the answer is shown between rounds

	// Order-bonus scoring knobs (firstPts/secondPts/partialPts). Currently
	// unused - synonym_duel moved to multiAnswer and grammar_fix was removed -
	// but kept for any future order-bonus game. Accuracy games ignore them and
	// evaluate returns the points directly.
	orderBonus                      bool
	firstPts, secondPts, partialPts float64
	tieByTime                       bool
	// retryOnWrong lets a player re-answer after a wrong guess (instead of
	// being locked for the round) - so a valid-but-unlisted synonym being
	// rejected isn't fatal. Off by default; synonym_duel turns it on.
	retryOnWrong bool
	// multiAnswer turns the round into a "list as many as you can" mode: no
	// locking, each DISTINCT correct answer scores +1, and the round ends only
	// when the timer fires. Used by synonym_duel; the other round-games keep
	// the single order-bonus answer.
	multiAnswer bool

	prompts []M // per-round public prompt (word, sentence, …)
	reveals []M // per-round reveal payload (correct answer, …)

	// evaluate grades a player's answer for a round. tier ∈ {"full",
	// "partial","wrong"} for order-bonus games; for accuracy games tier is
	// "score" and points carries the numeric result.
	evaluate func(round int, answer string, elapsed time.Duration) (tier string, points float64, detail M)

	// Optional self-learning hooks (used by synonym_duel). onWrong fires when
	// an answer doesn't match; onEnd fires once when the game finalizes. Both
	// nil by default, so the other round-games are unaffected.
	onWrong func(round int, answer string)
	onEnd   func()

	// --- mutable per-game state ---
	idx        int
	phase      string // "answer" | "reveal"
	answered   [2]bool
	score      [2]float64
	roundTime  [2]int64 // total ms spent answering, for tie-breaks
	firstTaken bool
	roundStart time.Time
	lastReveal M
	lastPoints [2]float64
	tiers      [2]string
	roundSeen  [2]map[string]struct{} // distinct correct answers this round (multiAnswer)
	lastMove   [2]time.Time           // last submission time per player (multiAnswer anti-flood)
}

// multiMinInterval rate-limits multiAnswer submissions: a bot can't spam the
// whole list and typing speed can't dominate. Real players type well under it.
const multiMinInterval = 200 * time.Millisecond

func (e *roundsEngine) Info() Info {
	return Info{Type: e.gtype, Mode: Competitive, MaxDuration: 10 * time.Minute}
}

func (e *roundsEngine) Start(c *Ctx) {
	e.idx = 0
	e.phase = "answer"
	c.G.State = e
	e.roundStart = time.Now()
	c.SetTimer(e.duration)
	c.Snapshot(e.public())
}

func (e *roundsEngine) Move(c *Ctx, p int, payload []byte) error {
	if e.phase != "answer" {
		return errors.New("not_answer_phase")
	}
	var mv struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(payload, &mv); err != nil {
		return errors.New("bad_payload")
	}
	answer := strings.TrimSpace(mv.Answer)
	if answer == "" {
		return errors.New("empty")
	}

	if e.multiAnswer {
		return e.moveMulti(c, p, answer)
	}
	if e.answered[p] {
		return errors.New("already_answered")
	}

	elapsed := time.Since(e.roundStart)
	e.roundTime[p] += elapsed.Milliseconds()
	tier, points, detail := e.evaluate(e.idx, answer, elapsed)

	if tier == "wrong" && e.onWrong != nil {
		e.onWrong(e.idx, answer)
	}

	if e.orderBonus {
		switch tier {
		case "full":
			if e.firstTaken {
				points = e.secondPts
			} else {
				points = e.firstPts
				e.firstTaken = true
			}
		case "partial":
			points = e.partialPts
		default:
			points = 0
		}
	}

	e.score[p] += points
	e.lastPoints[p] = points
	e.tiers[p] = tier
	// A wrong guess doesn't lock the player when retryOnWrong is set - they can
	// try again within the timer. Any scoring answer (full/partial/score) locks.
	if !e.retryOnWrong || tier != "wrong" {
		e.answered[p] = true
	}
	_ = detail

	if e.answered[0] && e.answered[1] {
		e.enterReveal(c)
		return nil
	}
	c.Update(e.public())
	return nil
}

// moveMulti handles a "list as many as you can" round: no locking, each
// DISTINCT correct answer scores +1, wrong answers feed self-learning. The
// round ends only when the timer fires (never early).
func (e *roundsEngine) moveMulti(c *Ctx, p int, answer string) error {
	// Anti-flood: cap the submission rate (also stops typing speed dominating).
	now := time.Now()
	if !e.lastMove[p].IsZero() && now.Sub(e.lastMove[p]) < multiMinInterval {
		return errors.New("too_fast")
	}
	e.lastMove[p] = now

	tier, _, _ := e.evaluate(e.idx, answer, 0)
	if tier == "full" {
		na := normAnswer(answer)
		if e.roundSeen[p] == nil {
			e.roundSeen[p] = map[string]struct{}{}
		}
		if _, dup := e.roundSeen[p][na]; !dup {
			e.roundSeen[p][na] = struct{}{}
			e.score[p]++
		}
	} else if tier == "wrong" && e.onWrong != nil {
		e.onWrong(e.idx, answer)
	}
	c.Update(e.public())
	return nil
}

func (e *roundsEngine) Timeout(c *Ctx) {
	if e.phase == "answer" {
		// Unanswered players score 0 for the round.
		for p := 0; p < 2; p++ {
			if !e.answered[p] {
				e.tiers[p] = "wrong"
				e.lastPoints[p] = 0
			}
		}
		e.enterReveal(c)
		return
	}
	// reveal phase done → advance.
	e.advance(c)
}

func (e *roundsEngine) enterReveal(c *Ctx) {
	e.phase = "reveal"
	if e.idx < len(e.reveals) {
		e.lastReveal = e.reveals[e.idx]
	} else {
		e.lastReveal = M{}
	}
	c.SetTimer(e.revealDur)
	c.Update(e.public())
}

func (e *roundsEngine) advance(c *Ctx) {
	e.idx++
	if e.idx >= len(e.prompts) {
		e.finalize(c)
		return
	}
	e.phase = "answer"
	e.answered = [2]bool{}
	e.firstTaken = false
	e.lastPoints = [2]float64{}
	e.tiers = [2]string{}
	e.roundSeen = [2]map[string]struct{}{}
	e.roundStart = time.Now()
	c.SetTimer(e.duration)
	c.Update(e.public())
}

func (e *roundsEngine) finalize(c *Ctx) {
	s0, s1 := e.score[0], e.score[1]
	winner := -1
	switch {
	case s0 > s1:
		winner = 0
	case s1 > s0:
		winner = 1
	case e.tieByTime:
		if e.roundTime[0] < e.roundTime[1] {
			winner = 0
		} else if e.roundTime[1] < e.roundTime[0] {
			winner = 1
		}
	}
	if e.onEnd != nil {
		e.onEnd()
	}
	c.End(Outcome{
		WinnerIdx: winner,
		Score:     [2]int{int(math.Round(s0)), int(math.Round(s1))},
		Summary: M{
			"game_type": string(e.gtype),
			"rounds":    len(e.prompts),
			"scores":    []float64{round1(s0), round1(s1)},
			"time_ms":   []int64{e.roundTime[0], e.roundTime[1]},
		},
	})
}

func (e *roundsEngine) public() M {
	m := M{
		"phase":       e.phase,
		"round":       e.idx + 1,
		"total":       len(e.prompts),
		"scores":      []float64{round1(e.score[0]), round1(e.score[1])},
		"answered":    []bool{e.answered[0], e.answered[1]},
		"order_bonus": e.orderBonus,
		"multi":       e.multiAnswer,
	}
	if e.multiAnswer {
		// how many DISTINCT correct each player has found THIS round (the
		// opponent's words are never revealed, only the count).
		m["counts"] = []int{len(e.roundSeen[0]), len(e.roundSeen[1])}
	}
	if e.idx < len(e.prompts) {
		m["prompt"] = e.prompts[e.idx]
	}
	if e.phase == "reveal" {
		m["reveal"] = e.lastReveal
		if !e.multiAnswer {
			// Single-answer reveal: order-bonus points + per-player tier.
			m["round_points"] = []float64{round1(e.lastPoints[0]), round1(e.lastPoints[1])}
			m["tiers"] = []string{e.tiers[0], e.tiers[1]}
		}
		// In multi mode the per-round result is the count, already in m["counts"].
	}
	return m
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// --- shared answer-matching helpers ---

// normAnswer lowercases, trims, drops most punctuation and collapses
// whitespace - used to compare free-text answers leniently.
func normAnswer(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevSpace = false
		case r == ' ', r == '\t':
			if !prevSpace {
				b.WriteRune(' ')
				prevSpace = true
			}
		case r == '\'':
			// keep apostrophes meaningful (don't → dont handled below)
		default:
			// drop other punctuation
		}
	}
	return strings.TrimSpace(b.String())
}

// tokens splits a normalised string into word tokens.
func tokens(s string) []string {
	f := strings.Fields(normAnswer(s))
	return f
}

// wordAccuracy returns the fraction (0..1) of the target's words that
// appear in the answer (multiset overlap / target length). Order is
// ignored - a lenient word-overlap helper. (Dictation now uses the stricter
// seqAccuracy; this remains as a reusable utility.)
func wordAccuracy(answer, target string) float64 {
	tw := tokens(target)
	if len(tw) == 0 {
		return 0
	}
	have := map[string]int{}
	for _, w := range tokens(answer) {
		have[w]++
	}
	matched := 0
	for _, w := range tw {
		if have[w] > 0 {
			have[w]--
			matched++
		}
	}
	return float64(matched) / float64(len(tw))
}

// seqAccuracy is an ORDER-aware transcription accuracy in [0,1]: 1 minus the
// word-level edit (Levenshtein) distance, normalised by the longer length.
// Unlike wordAccuracy it respects word ORDER (scrambled words score low) and
// penalises EXTRA words (precision) - exactly what dictation needs.
// (wordAccuracy, the order-ignoring variant, stays as a general helper.)
func seqAccuracy(answer, target string) float64 {
	a := tokens(answer)
	t := tokens(target)
	if len(t) == 0 {
		return 0
	}
	denom := len(t)
	if len(a) > denom {
		denom = len(a)
	}
	sim := 1 - float64(wordEditDistance(a, t))/float64(denom)
	if sim < 0 {
		sim = 0
	}
	return sim
}

// wordEditDistance is the word-level Levenshtein distance between two token
// slices (insert/delete/substitute each cost 1).
func wordEditDistance(a, b []string) int {
	n, m := len(a), len(b)
	if n == 0 {
		return m
	}
	if m == 0 {
		return n
	}
	prev := make([]int, m+1)
	for j := 0; j <= m; j++ {
		prev[j] = j
	}
	for i := 1; i <= n; i++ {
		cur := make([]int, m+1)
		cur[0] = i
		for j := 1; j <= m; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[m]
}
