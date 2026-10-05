package games

import "time"

func init() { register(SynonymDuel, newSynonymDuel) }

// Synonym / Antonym Duel — 5 rounds, 30s each. The system shows a word and
// asks for synonyms OR antonyms; each player lists AS MANY as they can, and
// every DISTINCT correct answer scores +1. Highest total over the rounds wins.
// Answers are validated against the (large, self-learning) answer bank.
func newSynonymDuel() Engine {
	loadBanks()
	const rounds = 5
	idxs := pickIndices(len(synonymsDB), rounds)

	qs := make([]synonymQ, len(idxs))
	prompts := make([]M, len(idxs))
	reveals := make([]M, len(idxs))
	for i, ix := range idxs {
		q := synonymsDB[ix]
		qs[i] = q
		prompts[i] = M{"word": q.Word, "qtype": q.Type}
		reveals[i] = M{"answers": q.Answers}
	}

	// wrong collects answers that aren't in the bank, for the self-learning
	// loop (the weekly AI decides if they're genuine synonyms/antonyms).
	var wrong []synonymWrong

	e := &roundsEngine{
		gtype:       SynonymDuel,
		duration:    30 * time.Second,
		revealDur:   3 * time.Second,
		multiAnswer: true, // list as many as you can; each distinct correct = +1
		prompts:     prompts,
		reveals:     reveals,
	}
	e.evaluate = func(round int, answer string, _ time.Duration) (string, float64, M) {
		a := normAnswer(answer)
		for _, ans := range qs[round].Answers {
			if normAnswer(ans) == a {
				return "full", 0, nil
			}
		}
		// Not in the embedded bank - accept it if the AI already learned it.
		if isAcceptedSynonym(qs[round].Word, qs[round].Type, a) {
			return "full", 0, nil
		}
		return "wrong", 0, nil
	}
	e.onWrong = func(round int, answer string) {
		a := normAnswer(answer)
		if a == "" || !IsWord(a) { // only learn from real single words
			return
		}
		wrong = append(wrong, synonymWrong{qs[round].Word, qs[round].Type, a})
	}
	e.onEnd = func() { recordSynonymCandidates(wrong) }
	return e
}
