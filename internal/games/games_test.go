package games

import (
	"encoding/json"
	"testing"
	"time"
)

// captureNotifier records emitted events so tests can assert on the flow.
type capture struct {
	events []capEvent
}
type capEvent struct {
	user  string
	event string
	data  M
}

func (c *capture) install() {
	Notify = func(userID, event string, data interface{}) {
		m, _ := data.(M)
		c.events = append(c.events, capEvent{userID, event, m})
	}
}
func (c *capture) last(event string) (M, bool) {
	for i := len(c.events) - 1; i >= 0; i-- {
		if c.events[i].event == event {
			return c.events[i].data, true
		}
	}
	return nil, false
}
func (c *capture) reset() { c.events = nil }

func TestDictionaryAndCEFR(t *testing.T) {
	if !IsWord("apple") || !IsWord("elephant") {
		t.Fatal("expected common words to be in the dictionary")
	}
	if IsWord("zzqx") {
		t.Fatal("nonsense should not be a word")
	}
	if CEFRPoints("cat") != 1 {
		t.Errorf("cat (A1) should score 1, got %d", CEFRPoints("cat"))
	}
	if CEFRPoints("ephemeral") != 4 {
		t.Errorf("ephemeral (C2) should score 4, got %d", CEFRPoints("ephemeral"))
	}
	if CEFRPoints("zzqx") != 0 {
		t.Errorf("non-word should score 0, got %d", CEFRPoints("zzqx"))
	}
}

func TestFreqFallbackLevels(t *testing.T) {
	// Words absent from cefr.tsv now get a frequency-derived level instead
	// of always defaulting to basic (1).
	if lvl := CEFRLevel("smartphone"); lvl == "" {
		t.Error("smartphone should get a frequency-derived CEFR level, got none")
	}
	if pts := CEFRPoints("photosynthesis"); pts < 2 {
		t.Errorf("photosynthesis (rare/technical) should score >=2 via frequency, got %d", pts)
	}
	// A genuinely obscure word stays basic (not inflated).
	if pts := CEFRPoints("zyzzyva"); pts != 1 {
		t.Errorf("ultra-rare word should stay basic (1), got %d", pts)
	}
}

func mvBytes(field, val string) []byte {
	b, _ := json.Marshal(M{field: val})
	return b
}

func TestWordChainFlow(t *testing.T) {
	c := &capture{}
	c.install()
	m := &Manager{games: make(map[string]*Game)}

	from := Player{ID: "11111111-1111-1111-1111-111111111111", Name: "A"}
	to := Player{ID: "22222222-2222-2222-2222-222222222222", Name: "B"}
	id, err := m.Invite("sess1", from, to, WordChain)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Accept(id, to.ID); err != nil {
		t.Fatal(err)
	}
	st, ok := c.last("game_start")
	if !ok {
		t.Fatal("expected game_start")
	}
	if st["game_type"] != string(WordChain) {
		t.Fatalf("unexpected start: %+v", st)
	}

	state := st["state"].(M)
	if _, ok := state["required_letter"]; !ok {
		t.Fatal("expected a required_letter (random start letter)")
	}

	// An invalid submission must NOT end the game - it's rejected for a retry.
	c.reset()
	_ = m.Move(id, from.ID, mvBytes("word", "zzzznotaword"))
	if _, ok := c.last("game_over"); ok {
		t.Fatal("an invalid word must NOT lose the game (retry allowed)")
	}
	if _, ok := c.last("game_error"); !ok {
		t.Fatal("expected a game_error for the invalid word")
	}
}

func TestWordChainTimeoutLoses(t *testing.T) {
	w := &wordChain{}
	c := &Ctx{G: &Game{Players: [2]Player{{ID: "A"}, {ID: "B"}}}}
	w.Start(c)
	// The player on turn (0) runs out of time -> the other player (1) wins.
	tc := &Ctx{G: c.G}
	w.Timeout(tc)
	if !tc.ended {
		t.Fatal("timeout should end the game")
	}
	if tc.outcome.WinnerIdx != 1 {
		t.Errorf("on timeout the player NOT on turn should win, got %d", tc.outcome.WinnerIdx)
	}
}

func TestWordChainSpeedPoints(t *testing.T) {
	if p := speedPoints(0); p != 10 {
		t.Errorf("instant reply should be 10, got %d", p)
	}
	if p := speedPoints(5 * time.Second); p != 5 {
		t.Errorf("5s reply should be 5, got %d", p)
	}
	if p := speedPoints(9500 * time.Millisecond); p != 1 {
		t.Errorf("9.5s reply should floor to 1, got %d", p)
	}
	if p := speedPoints(20 * time.Second); p != 1 {
		t.Errorf("over-time should floor to 1, got %d", p)
	}
}

func TestRoundsEngineSynonym(t *testing.T) {
	c := &capture{}
	c.install()
	m := &Manager{games: make(map[string]*Game)}

	from := Player{ID: "33333333-3333-3333-3333-333333333333", Name: "A"}
	to := Player{ID: "44444444-4444-4444-4444-444444444444", Name: "B"}
	id, err := m.Invite("sess2", from, to, SynonymDuel)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Accept(id, to.ID); err != nil {
		t.Fatal(err)
	}
	st, _ := c.last("game_start")
	state := st["state"].(M)
	if state["phase"] != "answer" {
		t.Fatalf("expected answer phase, got %v", state["phase"])
	}
	prompt := state["prompt"].(M)
	if prompt["word"] == nil || prompt["qtype"] == nil {
		t.Fatalf("expected a word + qtype prompt, got %+v", prompt)
	}

	// retryOnWrong: a definitely-wrong answer must NOT lock the player or
	// advance the round - they can try again within the timer.
	c.reset()
	_ = m.Move(id, from.ID, mvBytes("answer", "zzzznotaword"))
	upd, ok := c.last("game_update")
	if !ok {
		t.Fatal("expected game_update after a (wrong) answer")
	}
	us := upd["state"].(M)
	if us["phase"] != "answer" {
		t.Fatalf("wrong answer should keep the answer phase (retry), got %v", us["phase"])
	}
	if answered := us["answered"].([]bool); answered[0] {
		t.Fatal("a wrong answer must not lock the player (retry allowed)")
	}
}

func TestWordAccuracy(t *testing.T) {
	if got := wordAccuracy("the cat sat", "the cat sat"); got != 1 {
		t.Errorf("identical should be 1.0, got %f", got)
	}
	if got := wordAccuracy("the cat", "the cat sat"); got < 0.66 || got > 0.67 {
		t.Errorf("2/3 expected ~0.666, got %f", got)
	}
}

func TestDictationPool(t *testing.T) {
	// Without a DB the AI pool is empty, but the embedded seed bank must still
	// be available so the game works.
	RefreshDictationPool()
	if n := DictationCount(); n < 99 {
		t.Errorf("expected at least the ~99 seed sentences, got %d", n)
	}
	if len(dictationAll()) < 99 {
		t.Error("dictationAll should include the embedded seed bank")
	}
}

func TestSeqAccuracy(t *testing.T) {
	target := "the cat sat on the mat"
	if got := seqAccuracy(target, target); got != 1 {
		t.Errorf("identical should be 1.0, got %v", got)
	}
	// Scrambled order must score low (order matters, unlike wordAccuracy).
	if got := seqAccuracy("mat the on sat cat the", target); got >= 0.9 {
		t.Errorf("scrambled should score low, got %v", got)
	}
	// Extra junk words must lower the score (precision penalty).
	if got := seqAccuracy("the cat sat on the mat foo bar baz qux", target); got >= 0.85 {
		t.Errorf("extra words should lower accuracy, got %v", got)
	}
	// One substitution -> ~5/6.
	if got := seqAccuracy("the cat sat on the rug", target); got < 0.8 || got > 0.86 {
		t.Errorf("one substitution should be ~0.83, got %v", got)
	}
}

func TestComputeXP(t *testing.T) {
	if xp := computeXP(Competitive, Outcome{WinnerIdx: 0}); xp != [2]int{10, 3} {
		t.Errorf("win/loss XP wrong: %v", xp)
	}
	if xp := computeXP(Competitive, Outcome{WinnerIdx: -1}); xp != [2]int{6, 6} {
		t.Errorf("draw XP wrong: %v", xp)
	}
	if xp := computeXP(Icebreaker, Outcome{WinnerIdx: -1}); xp != [2]int{3, 3} {
		t.Errorf("icebreaker XP wrong: %v", xp)
	}
}

// ensure timers don't leak goroutines in a trivially short test
func TestInviteExpiryNoPanic(t *testing.T) {
	c := &capture{}
	c.install()
	m := &Manager{games: make(map[string]*Game)}
	from := Player{ID: "55555555-5555-5555-5555-555555555555", Name: "A"}
	to := Player{ID: "66666666-6666-6666-6666-666666666666", Name: "B"}
	id, _ := m.Invite("sess3", from, to, VocabSprint)
	m.Decline(id, to.ID)
	if _, ok := m.games[id]; ok {
		t.Fatal("declined game should be removed")
	}
	_ = time.Millisecond
}

func TestVocabRateLimit(t *testing.T) {
	c := &Ctx{G: &Game{}}

	// Rapid second word -> too_fast (anti-flood rate cap).
	v := &vocabSprint{topic: "travel"}
	v.seen[0] = map[string]struct{}{}
	v.seen[1] = map[string]struct{}{}
	if err := v.Move(c, 0, mvBytes("word", "travel")); err != nil {
		t.Fatalf("first word should be accepted, got %v", err)
	}
	if err := v.Move(c, 0, mvBytes("word", "hotel")); err == nil || err.Error() != "too_fast" {
		t.Fatalf("rapid second word should be too_fast, got %v", err)
	}

	// Hitting the per-player word cap -> max_reached.
	v2 := &vocabSprint{topic: "travel"}
	v2.seen[0] = map[string]struct{}{}
	v2.seen[1] = map[string]struct{}{}
	for i := 0; i < vocabMaxWords; i++ {
		v2.words[0] = append(v2.words[0], scoredWord{})
	}
	if err := v2.Move(c, 0, mvBytes("word", "journey")); err == nil || err.Error() != "max_reached" {
		t.Fatalf("over-cap word should be max_reached, got %v", err)
	}
}

func TestRoundsEngineMultiAnswer(t *testing.T) {
	e := &roundsEngine{
		gtype:       SynonymDuel,
		duration:    30 * time.Second,
		revealDur:   time.Second,
		multiAnswer: true,
		prompts:     []M{{"word": "happy", "qtype": "synonym"}},
		reveals:     []M{{"answers": []string{"glad", "joyful", "merry"}}},
	}
	e.evaluate = func(_ int, answer string, _ time.Duration) (string, float64, M) {
		for _, a := range []string{"glad", "joyful", "merry"} {
			if normAnswer(answer) == a {
				return "full", 0, nil
			}
		}
		return "wrong", 0, nil
	}
	c := &Ctx{G: &Game{}}
	e.Start(c)
	// Player 0 lists 3 distinct correct + a duplicate + a wrong one. Reset the
	// rate-limit clock each time so this tests the COUNTING, not throttling.
	for _, w := range []string{"glad", "joyful", "glad", "table", "merry"} {
		e.lastMove[0] = time.Time{}
		_ = e.Move(c, 0, mvBytes("answer", w))
	}
	if e.score[0] != 3 {
		t.Errorf("expected 3 distinct correct answers, got %v", e.score[0])
	}
	// A rapid second submission (no reset) must be throttled.
	e.lastMove[1] = time.Now()
	if err := e.Move(c, 1, mvBytes("answer", "glad")); err == nil || err.Error() != "too_fast" {
		t.Fatalf("rapid submission should be too_fast, got %v", err)
	}
}

func TestSynonymLearningCache(t *testing.T) {
	// An AI-accepted answer (not in the embedded bank) must count as correct.
	synCacheMu.Lock()
	acceptedSyn = map[string]map[string]struct{}{
		synKey("happy", "synonym"): {"elated": {}},
	}
	synCacheMu.Unlock()
	defer func() {
		synCacheMu.Lock()
		acceptedSyn = nil
		synCacheMu.Unlock()
	}()

	if !isAcceptedSynonym("happy", "synonym", "elated") {
		t.Error("'elated' should be an accepted synonym of 'happy'")
	}
	if isAcceptedSynonym("happy", "synonym", "table") {
		t.Error("'table' should not be accepted")
	}
	if isAcceptedSynonym("happy", "antonym", "elated") {
		t.Error("type must match: 'elated' is not a happy ANTONYM")
	}
}

func TestStoryJudge(t *testing.T) {
	prev := StoryJudgeFn
	defer func() { StoryJudgeFn = prev }()

	// Mocked judge: player 0 scores higher -> wins.
	StoryJudgeFn = func(starter, s0, s1 string) (StoryJudgement, error) {
		return StoryJudgement{Score0: 90, Score1: 40, Verdict: "A continued better"}, nil
	}
	o := judgeStory("Once upon a time...", "A long, vivid continuation.", "meh")
	if o.WinnerIdx != 0 {
		t.Errorf("expected player 0 to win, got winner %d", o.WinnerIdx)
	}
	if o.Score != [2]int{90, 40} {
		t.Errorf("expected scores [90 40], got %v", o.Score)
	}

	// Fallback (no judge wired) -> scored by word count.
	StoryJudgeFn = nil
	o2 := judgeStory("opener", "one two three four", "one")
	if o2.WinnerIdx != 0 {
		t.Errorf("fallback: the longer continuation should win, got %d", o2.WinnerIdx)
	}
}

func TestStoryDuelFlow(t *testing.T) {
	prev := StoryJudgeFn
	defer func() { StoryJudgeFn = prev }()

	judged := make(chan struct{}, 1)
	StoryJudgeFn = func(starter, s0, s1 string) (StoryJudgement, error) {
		defer func() { judged <- struct{}{} }()
		return StoryJudgement{Score0: 80, Score1: 20}, nil
	}

	m := &Manager{games: make(map[string]*Game)}
	from := Player{ID: "77777777-7777-7777-7777-777777777777", Name: "A"}
	to := Player{ID: "88888888-8888-8888-8888-888888888888", Name: "B"}
	id, err := m.Invite("sessD", from, to, StoryChain)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Accept(id, to.ID); err != nil {
		t.Fatal(err)
	}
	// Both write their own continuation; the second submission triggers the
	// async judge, which resolves the duel on the owning manager.
	if err := m.Move(id, from.ID, mvBytes("text", "A great, coherent continuation.")); err != nil {
		t.Fatal(err)
	}
	if err := m.Move(id, to.ID, mvBytes("text", "Short.")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-judged:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the duel to be judged after both submitted")
	}
}

func TestProfanityFilter(t *testing.T) {
	if !IsProfane("fuck") {
		t.Error("expected 'fuck' to be flagged as profane")
	}
	if IsProfane("travel") {
		t.Error("clean word 'travel' wrongly flagged as profane")
	}
}

func TestTopicRelevanceVectors(t *testing.T) {
	// 'football' is a sport seed -> must be clearly on-topic; a home/family
	// word must NOT be on-topic for sport.
	if c := relevanceClass("sport", "football"); c != "on" {
		t.Errorf("football should be on-topic for sport, got %q", c)
	}
	if c := relevanceClass("sport", "mother"); c == "on" {
		t.Errorf("mother should not be on-topic for sport, got %q", c)
	}
}

func TestTopicWordSoftScoring(t *testing.T) {
	// Trusted/rejected caches drive deterministic scoring, no vector needed.
	topicCacheMu.Lock()
	trustedWords = map[string]map[string]struct{}{"sport": {"goal": {}}}
	rejectedWords = map[string]map[string]struct{}{"sport": {"pizza": {}}}
	topicCacheMu.Unlock()
	defer func() {
		topicCacheMu.Lock()
		trustedWords, rejectedWords = nil, nil
		topicCacheMu.Unlock()
	}()

	if pts := topicWordPoints("sport", "pizza", 4); pts != 0.2 {
		t.Errorf("rejected (off-topic) word should score 0.2, got %v", pts)
	}
	if pts := topicWordPoints("sport", "goal", 4); pts != 4 {
		t.Errorf("trusted (on-topic) word should get full base (=4), got %v", pts)
	}
}
