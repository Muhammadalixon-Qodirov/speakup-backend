package services

import (
	"strings"
	"testing"
)

func TestSplitSentences(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"I like tea.", 1},
		{"I like tea. My brother likes coffee.", 2},
		{"Do you walk there? It is not far. I go every day.", 3},
		{"What a day! I was so tired. Then I slept. Now I feel better.", 4},
		// No terminator: still one sentence, not zero.
		{"I like tea", 1},
		{"", 0},
	}
	for _, c := range cases {
		if got := len(splitSentences(c.in)); got != c.want {
			t.Errorf("splitSentences(%q) = %d sentences, want %d", c.in, got, c.want)
		}
	}
}

func TestFirstProperNoun(t *testing.T) {
	// Sentence-initial capitals are normal; mid-sentence ones are names.
	if w, found := firstProperNoun([]string{"The weather is cold today."}); found {
		t.Errorf("no proper noun expected, got %q", w)
	}
	// "I" is the one word always capitalised mid-sentence.
	if w, found := firstProperNoun([]string{"Today I walk to work."}); found {
		t.Errorf(`"I" should not count as a proper noun, got %q`, w)
	}
	if w, found := firstProperNoun([]string{"We visited Tashkent last spring."}); !found {
		t.Error("expected Tashkent to be flagged")
	} else if w != "Tashkent" {
		t.Errorf("flagged %q, want Tashkent", w)
	}
	// Second sentence of a passage is checked too.
	if _, found := firstProperNoun([]string{"It was cold.", "Then Peter arrived."}); !found {
		t.Error("expected a proper noun in the second sentence to be flagged")
	}
}

func TestLooksCohesive(t *testing.T) {
	// Linked by a pronoun opening.
	if !looksCohesive([]string{"My brother works in a shop.", "He starts early every morning."}) {
		t.Error("pronoun link should count as cohesive")
	}
	// Linked by a shared content word.
	if !looksCohesive([]string{"I often cook dinner at home.", "Cooking helps me relax."}) {
		t.Error("shared content word should count as cohesive")
	}
	// Unrelated sentences stapled together.
	if looksCohesive([]string{"My brother works in a shop.", "Mountains are very tall."}) {
		t.Error("unrelated sentences should not count as cohesive")
	}
	// Single sentence is trivially cohesive.
	if !looksCohesive([]string{"I like tea."}) {
		t.Error("a single sentence should be cohesive")
	}
}

// Realistic passages must actually get through the gate. Each filter is cheap
// on its own, but together they can reject nearly everything - and that failure
// mode is invisible in production except as a generator that burns LLM calls
// and adds almost nothing. These cases pin the gate open.
func TestValidatePassageAcceptsRealisticText(t *testing.T) {
	cases := []struct {
		bucket bucket
		text   string
	}{
		{bucket{"food", "A1", 1, 0}, "I think my mother makes the best soup."},
		{bucket{"family", "A2", 2, 0},
			"My brother works in a small shop near the park. He starts work very early every morning."},
		{bucket{"travel", "B1", 2, 0},
			"We travel to the mountains almost every summer. The journey takes about three hours by bus."},
		{bucket{"work", "B1", 3, 0},
			"I usually finish my work before six in the evening. Then I walk home through the quiet streets. This helps me think about something other than the office."},
		{bucket{"study", "A2", 4, 0},
			"I study English three times every week. The lessons are not very long. My teacher thinks my reading is good. I still want to speak with more confidence."},
	}
	for _, c := range cases {
		got, reason := validatePassage(c.bucket, c.text)
		if reason != "" {
			t.Errorf("rejected a realistic %s/%d-sentence passage: %s\n  text: %q",
				c.bucket.Level, c.bucket.SentenceCount, reason, c.text)
			continue
		}
		if got.WordCount == 0 || got.Phonemes == "" {
			t.Errorf("accepted but did not fill in fields: %+v", got)
		}
	}
}

func TestValidatePassageRejects(t *testing.T) {
	cases := []struct {
		name   string
		bucket bucket
		text   string
		expect string
	}{
		{"wrong sentence count", bucket{"food", "A1", 1, 0},
			"I like tea. I like coffee.", "want 1 sentences"},
		{"proper noun", bucket{"travel", "A2", 1, 0},
			"We travelled to Paris by train last spring.", "proper noun"},
		{"unknown word", bucket{"food", "A1", 1, 0},
			"I really enjoyed the zzzqux with my dinner.", "unknown words"},
		{"too hard for the level", bucket{"work", "A1", 1, 0},
			"The committee postponed its deliberations indefinitely yesterday.", "level A1"},
		{"sentence too short", bucket{"food", "A1", 1, 0}, "I eat.", "sentence of"},
		{"quotation marks", bucket{"food", "A1", 1, 0},
			`She said "hello" to the friendly shop worker.`, "forbidden symbols"},
	}
	for _, c := range cases {
		_, reason := validatePassage(c.bucket, c.text)
		if reason == "" {
			t.Errorf("%s: should have been rejected, was accepted", c.name)
			continue
		}
		if !strings.Contains(reason, c.expect) {
			t.Errorf("%s: reason = %q, want it to mention %q", c.name, reason, c.expect)
		}
	}
}
