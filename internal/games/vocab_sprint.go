package games

import (
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"time"
)

func init() { register(VocabSprint, func() Engine { return &vocabSprint{} }) }

// vocabSprint — competitive. Both players get the same topic and have 60
// seconds to type as many relevant words as they can. Words are scored by
// CEFR difficulty (A1/A2=1, B1/B2=2, C1/C2=4) AND topic relevance: a word
// clearly unrelated to the topic scores only 0.2. Higher total wins; equal
// totals draw. Players type SIMULTANEOUSLY - they never see each other's
// words until the reveal.
type vocabSprint struct {
	topic  string
	words  [2][]scoredWord
	seen   [2]map[string]struct{}
	points [2]float64
	lastAt [2]time.Time // last accepted-word time per player (anti-flood)
}

type scoredWord struct {
	Word   string  `json:"word"`
	Points float64 `json:"points"`
	Level  string  `json:"level"`
}

const (
	vocabDuration = 60 * time.Second

	// Anti-flood limits (server-authoritative). A modified client/bot can't
	// dump the dictionary for a huge score: words are capped both in rate
	// (min gap between accepted words) and in total per player. Real players
	// type well under both. Tunable.
	vocabMinInterval = 200 * time.Millisecond // min gap between accepted words
	vocabMaxWords    = 80                      // max accepted words per player
)

// vocabTopics — the prompt is a theme. Off-topic words aren't rejected but
// score only 0.2 (topic relevance comes from semantic vectors + the
// self-learning banks; see topic_learning.go). On/borderline-topic words earn
// their full CEFR difficulty, which rewards richer, relevant vocabulary.
var vocabTopics = []string{
	"travel", "food & cooking", "emotions", "business & work",
	"nature & animals", "technology", "health & fitness", "education",
	"sport", "music & art", "city life", "weather & seasons",
}

func (v *vocabSprint) Info() Info {
	return Info{Type: VocabSprint, Mode: Competitive, MaxDuration: 3 * time.Minute}
}

func (v *vocabSprint) Start(c *Ctx) {
	v.topic = vocabTopics[rand.Intn(len(vocabTopics))]
	v.seen[0] = make(map[string]struct{})
	v.seen[1] = make(map[string]struct{})
	c.G.State = v
	c.SetTimer(vocabDuration)
	c.Snapshot(v.public())
}

func (v *vocabSprint) Move(c *Ctx, p int, payload []byte) error {
	var mv wordMovePayload
	if err := json.Unmarshal(payload, &mv); err != nil {
		return errors.New("bad_payload")
	}
	word, alpha := normalizeWord(mv.Word)
	if !alpha || len(word) < 2 {
		return errors.New("invalid_word")
	}

	// Anti-flood (server-side): cap total words and the accept rate so a bot
	// can't spam the whole dictionary for a huge score. Checked before the
	// dictionary lookups so a flood is cheap to reject.
	if len(v.words[p]) >= vocabMaxWords {
		return errors.New("max_reached")
	}
	now := time.Now()
	if !v.lastAt[p].IsZero() && now.Sub(v.lastAt[p]) < vocabMinInterval {
		return errors.New("too_fast")
	}

	if _, dup := v.seen[p][word]; dup {
		return errors.New("duplicate")
	}
	if !IsWord(word) {
		return errors.New("not_a_word")
	}
	if IsProfane(word) {
		return errors.New("not_allowed")
	}

	// Base CEFR difficulty, modulated by how well the word fits the topic
	// (semantic vectors + the self-learning trusted/rejected banks).
	base := CEFRPoints(word)
	pts := topicWordPoints(v.topic, word, base)
	level := CEFRLevel(word)
	v.seen[p][word] = struct{}{}
	v.words[p] = append(v.words[p], scoredWord{Word: word, Points: pts, Level: level})
	v.points[p] += pts
	v.lastAt[p] = now

	// Live update: counts only, never the opponent's words.
	c.Update(v.public())
	return nil
}

func (v *vocabSprint) Timeout(c *Ctx) {
	winner := -1
	if v.points[0] > v.points[1] {
		winner = 0
	} else if v.points[1] > v.points[0] {
		winner = 1
	}

	// Feed the self-learning topic categoriser with every accepted word
	// (persists off the game lock, like persistResult).
	var typed []string
	for pi := 0; pi < 2; pi++ {
		for _, sw := range v.words[pi] {
			typed = append(typed, sw.Word)
		}
	}
	recordTopicWords(v.topic, typed)

	c.End(Outcome{
		WinnerIdx: winner,
		Score:     [2]int{int(math.Round(v.points[0])), int(math.Round(v.points[1]))},
		Summary: M{
			"topic":  v.topic,
			"words":  []interface{}{v.words[0], v.words[1]},
			"points": []float64{v.points[0], v.points[1]},
			"counts": []int{len(v.words[0]), len(v.words[1])},
		},
	})
}

func (v *vocabSprint) public() M {
	return M{
		"topic":  v.topic,
		"counts": []int{len(v.words[0]), len(v.words[1])},
		"points": []float64{v.points[0], v.points[1]},
	}
}
