package games

import (
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/speak-up/backend/internal/safego"
)

func init() { register(StoryChain, func() Engine { return &storyChain{} }) }

// storyChain — a parallel creative-writing duel. Both players get the SAME
// story opener and have one window to write their OWN continuation (they never
// see each other's while writing). When both submit, or the timer runs out, an
// LLM judges both continuations (coherence, grammar, creativity, meaningful
// progression) and the higher score wins. Both stories are revealed at the end.
type storyChain struct {
	starter   string
	texts     [2]string
	submitted [2]bool
	resolving bool // LLM is judging; no more moves accepted
}

const (
	storyDuration   = 120 * time.Second // 2 minutes to write
	storyMaxTextLen = 600               // a few sentences
)

var storyStarters = []string{
	"One day, a small cat found a mysterious door in the middle of the forest.",
	"Nobody believed Maya when she said the old lighthouse still had someone living in it.",
	"The train was already moving when Daniel realised he'd boarded the wrong one.",
	"On her first morning in the new city, Lena heard music coming from an empty apartment.",
	"The package had no return address, only the words: \"Open me at midnight.\"",
	"Everyone in the village woke up one morning to find the river had turned bright blue.",
	"It was supposed to be an ordinary job interview, until the lights suddenly went out.",
	"The map led them to a beach that wasn't on any official chart.",
}

func (s *storyChain) Info() Info {
	return Info{Type: StoryChain, Mode: Competitive, MaxDuration: 5 * time.Minute}
}

func (s *storyChain) Start(c *Ctx) {
	s.starter = storyStarters[rand.Intn(len(storyStarters))]
	c.G.State = s
	c.SetTimer(storyDuration)
	c.Snapshot(s.public())
}

type storyMovePayload struct {
	Text string `json:"text"`
}

func (s *storyChain) Move(c *Ctx, p int, payload []byte) error {
	if s.resolving {
		return errors.New("judging")
	}
	if s.submitted[p] {
		return errors.New("already_submitted")
	}
	var mv storyMovePayload
	if err := json.Unmarshal(payload, &mv); err != nil {
		return errors.New("bad_payload")
	}
	text := strings.TrimSpace(mv.Text)
	if text == "" {
		return errors.New("empty")
	}
	if utf8.RuneCountInString(text) > storyMaxTextLen {
		return errors.New("too_long")
	}
	s.texts[p] = text
	s.submitted[p] = true

	if s.submitted[0] && s.submitted[1] {
		s.beginJudging(c)
		return nil
	}
	c.Update(s.public()) // only WHO has submitted - never the other's text
	return nil
}

// Timeout — the 2-minute window is up; judge whatever was submitted.
func (s *storyChain) Timeout(c *Ctx) {
	s.beginJudging(c)
}

// beginJudging freezes the game and kicks off async LLM judging off the lock
// (so it never freezes other games), then resolves via the owning manager.
func (s *storyChain) beginJudging(c *Ctx) {
	s.resolving = true
	c.ClearTimer()
	c.Update(M{"phase": "judging", "starter": s.starter})

	gameID := c.G.ID
	mgr := c.G.mgr
	starter, t0, t1 := s.starter, s.texts[0], s.texts[1]
	safego.Go("games.storyJudge", func() {
		outcome := judgeStory(starter, t0, t1)
		if mgr != nil {
			mgr.ResolveStory(gameID, outcome)
		}
	})
}

func (s *storyChain) public() M {
	return M{
		"phase":     "writing",
		"starter":   s.starter,
		"submitted": []bool{s.submitted[0], s.submitted[1]},
	}
}

// judgeStory turns the two continuations into a competitive Outcome via the LLM
// judge, ALWAYS returning a valid result: a length-based fallback if the judge
// is unwired, errors, or panics.
func judgeStory(starter, t0, t1 string) (out Outcome) {
	out = storyFallback(starter, t0, t1)
	if StoryJudgeFn == nil {
		return
	}
	defer func() {
		if recover() != nil {
			out = storyFallback(starter, t0, t1)
		}
	}()
	j, err := StoryJudgeFn(starter, t0, t1)
	if err != nil {
		return
	}
	winner := -1
	if j.Score0 > j.Score1 {
		winner = 0
	} else if j.Score1 > j.Score0 {
		winner = 1
	}
	out = Outcome{
		WinnerIdx: winner,
		Score:     [2]int{int(math.Round(j.Score0)), int(math.Round(j.Score1))},
		Summary: M{
			"starter":  starter,
			"stories":  []string{t0, t1},
			"scores":   []float64{j.Score0, j.Score1},
			"feedback": []string{j.Feedback0, j.Feedback1},
			"verdict":  j.Verdict,
		},
	}
	return
}

// storyFallback scores by word count when the LLM judge isn't available, so the
// duel still produces a winner instead of hanging.
func storyFallback(starter, t0, t1 string) Outcome {
	w0 := len(strings.Fields(t0))
	w1 := len(strings.Fields(t1))
	winner := -1
	if w0 > w1 {
		winner = 0
	} else if w1 > w0 {
		winner = 1
	}
	return Outcome{
		WinnerIdx: winner,
		Score:     [2]int{w0, w1},
		Summary: M{
			"starter": starter,
			"stories": []string{t0, t1},
			"note":    "scored by length (AI unavailable)",
		},
	}
}
