package games

import "testing"

func TestPronDictLoads(t *testing.T) {
	// A truncated or missing asset would otherwise only show up later as
	// every generated passage being rejected for "unknown words".
	if n := PronDictSize(); n < 100000 {
		t.Fatalf("dictionary looks truncated: %d words", n)
	}
}

func TestPronLookup(t *testing.T) {
	cases := []struct {
		word     string
		phonemes string
		level    string
		sounds   []string
	}{
		{"think", "TH IH1 NG K", "A1", []string{"IY_IH", "TH"}},
		{"this", "DH IH1 S", "A1", []string{"DH", "IY_IH"}},
		{"cat", "K AE1 T", "A1", []string{"AE"}},
		// Inflected form: in the dictionary, but carries no CEFR level.
		{"walked", "W AO1 K T", "", []string{"W"}},
	}
	for _, c := range cases {
		e, ok := PronLookup(c.word)
		if !ok {
			t.Errorf("%q: not found", c.word)
			continue
		}
		if e.Phonemes != c.phonemes {
			t.Errorf("%q: phonemes = %q, want %q", c.word, e.Phonemes, c.phonemes)
		}
		if e.Level != c.level {
			t.Errorf("%q: level = %q, want %q", c.word, e.Level, c.level)
		}
		if len(e.Sounds) != len(c.sounds) {
			t.Errorf("%q: sounds = %v, want %v", c.word, e.Sounds, c.sounds)
		}
	}
}

func TestPronLookupIsCaseInsensitive(t *testing.T) {
	if _, ok := PronLookup("Think"); !ok {
		t.Error(`"Think" should resolve the same as "think"`)
	}
}

func TestPronTokens(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		// Contractions stay whole; sentence punctuation is stripped.
		{"I don't think so.", []string{"i", "don't", "think", "so"}},
		{"Hello, world!", []string{"hello", "world"}},
		// Hyphenated forms split: cmudict stores the parts separately.
		{"a well-known fact", []string{"a", "well", "known", "fact"}},
		{"", nil},
	}
	for _, c := range cases {
		got := PronTokens(c.in)
		if len(got) != len(c.want) {
			t.Errorf("PronTokens(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("PronTokens(%q) = %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}

func TestPronAnalyseFlagsUnknownWords(t *testing.T) {
	// This is the accuracy guarantee: a word with no human-written
	// transcription must be reported, because scoring it would mean comparing
	// the learner against a guessed reference.
	//
	// Note what is NOT covered here: cmudict contains plenty of proper nouns
	// ("tashkent" among them), so membership alone does not exclude names.
	// Keeping them out of exercises is the generator's job, not this one's.
	_, _, unknown := PronAnalyse("I enjoyed the zzzqux yesterday.")
	if !hasString(unknown, "zzzqux") {
		t.Errorf("expected 'zzzqux' to be unknown, got %v", unknown)
	}

	phonemes, sounds, unknown := PronAnalyse("I think this is a thin cat.")
	if len(unknown) != 0 {
		t.Errorf("everyday words should all be known, got unknown %v", unknown)
	}
	if phonemes == "" {
		t.Error("phonemes should not be empty")
	}
	for _, want := range []string{"TH", "DH", "AE"} {
		if !hasString(sounds, want) {
			t.Errorf("expected target sound %q in %v", want, sounds)
		}
	}
}

func TestPronLevelFits(t *testing.T) {
	// An A1 sentence should pass at A1.
	if ok, share, cov := PronLevelFits("The cat is on the big red box.", "A1", 0.85); !ok {
		t.Errorf("simple sentence failed A1: share = %.2f, coverage = %.2f", share, cov)
	}
	// Advanced vocabulary must not pass as A1. The trap this guards against:
	// the hard words carry no CEFR level at all, so counting only graded words
	// would score this 100% A1 on the strength of "the" alone.
	if ok, _, cov := PronLevelFits(
		"The unprecedented deterioration jeopardised subsequent negotiations.", "A1", 0.85); ok {
		t.Errorf("C-level sentence wrongly accepted as A1 (coverage %.2f)", cov)
	}
	// A genuinely B2 passage should not pass as A1 either.
	if ok, _, _ := PronLevelFits(
		"The committee will consider the proposal despite the obvious financial risk.",
		"A1", 0.85); ok {
		t.Error("B-level sentence wrongly accepted as A1")
	}
	// Nothing graded to judge by: must not claim a level.
	if ok, _, _ := PronLevelFits("Tashkent Samarkand", "A1", 0.85); ok {
		t.Error("ungradable text should not pass a level check")
	}
	if ok, _, _ := PronLevelFits("The cat sat.", "Z9", 0.85); ok {
		t.Error("unknown level should not pass")
	}
}

func hasString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
