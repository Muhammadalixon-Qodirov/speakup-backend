package services

import "testing"

func sub(expected, heard string, final bool) Substitution {
	return Substitution{Expected: expected, Heard: &heard, WordFinal: final}
}

func dropped(expected string, final bool) Substitution {
	return Substitution{Expected: expected, Heard: nil, WordFinal: final}
}

// Every case here was produced by the sidecar on real speechocean762 audio that
// human annotators marked fully correct. They are the measured false positives
// the policy exists to remove - if any of them starts being reported again, the
// feature is back to telling learners they got right answers wrong.
func TestApplyConfidenceRemovesMeasuredFalsePositives(t *testing.T) {
	check := &PronunciationCheck{Words: []PronunciationWord{
		// [w] and [u] are the same articulation; the recogniser just wrote it
		// the other way.
		{Word: "WIN", Status: "xato", Substitutions: []Substitution{sub("w", "u", false)}},
		// Assimilation before /p/ and r-vocalisation: both normal English.
		{Word: "IMPORTANT", Status: "xato", Substitutions: []Substitution{
			sub("m", "n", false), sub("ɹ", "ʊ", false)}},
		// Plain recogniser confusion.
		{Word: "LAST", Status: "xato", Substitutions: []Substitution{sub("l", "n", false)}},
		// Onset aspiration, reported by the sidecar as if it were word-final.
		{Word: "DO", Status: "xato", Substitutions: []Substitution{sub("d", "t", false)}},
		// Function word reducing before a vowel ("was an").
		{Word: "WAS", Status: "xato", Substitutions: []Substitution{sub("z", "s", true)}},
	}}

	softened := ApplyConfidence(check)
	if softened != 5 {
		t.Errorf("softened %d verdicts, want all 5", softened)
	}
	for _, w := range check.Words {
		if w.Status == "xato" {
			t.Errorf("%s still reported as an error: %+v", w.Word, w.Substitutions)
		}
	}
}

// The errors Uzbek and Russian speakers actually make must survive, otherwise
// the filter has removed the feature along with the noise.
func TestApplyConfidenceKeepsRealErrors(t *testing.T) {
	cases := []struct {
		name string
		word PronunciationWord
	}{
		{"think -> sink", PronunciationWord{Word: "think", Status: "xato",
			Substitutions: []Substitution{sub("θ", "s", false)}}},
		{"this -> zis", PronunciationWord{Word: "this", Status: "xato",
			Substitutions: []Substitution{sub("ð", "z", false)}}},
		{"west -> vest", PronunciationWord{Word: "west", Status: "xato",
			Substitutions: []Substitution{sub("w", "v", false)}}},
		{"very -> wery", PronunciationWord{Word: "very", Status: "xato",
			Substitutions: []Substitution{sub("v", "w", false)}}},
		{"lab -> lap (final devoicing)", PronunciationWord{Word: "lab", Status: "xato",
			Substitutions: []Substitution{sub("b", "p", true)}}},
		{"bad -> bat (final devoicing, content word)", PronunciationWord{Word: "bad", Status: "xato",
			Substitutions: []Substitution{sub("d", "t", true)}}},
		{"dropped th", PronunciationWord{Word: "three", Status: "xato",
			Substitutions: []Substitution{dropped("θ", false)}}},
	}
	for _, c := range cases {
		check := &PronunciationCheck{Words: []PronunciationWord{c.word}}
		ApplyConfidence(check)
		if check.Words[0].Status != "xato" {
			t.Errorf("%s: downgraded a real error to %q", c.name, check.Words[0].Status)
		}
	}
}

func TestApplyConfidenceNeverUpgrades(t *testing.T) {
	// A word the sidecar accepted must never become an error, whatever the
	// substitution list says.
	check := &PronunciationCheck{Words: []PronunciationWord{
		{Word: "think", Status: "ok", Substitutions: []Substitution{sub("θ", "s", false)}},
		{Word: "ship", Status: "kichik", Substitutions: []Substitution{sub("ɪ", "i", false)}},
	}}
	if n := ApplyConfidence(check); n != 0 {
		t.Errorf("softened %d, want 0", n)
	}
	if check.Words[0].Status != "ok" || check.Words[1].Status != "kichik" {
		t.Errorf("statuses changed: %q, %q", check.Words[0].Status, check.Words[1].Status)
	}
}

func TestApplyConfidenceUnheardWordBecomesHint(t *testing.T) {
	// No substitutions recorded means the model heard nothing for the word -
	// usually bad audio or a skipped word, not a pronunciation error.
	check := &PronunciationCheck{Words: []PronunciationWord{
		{Word: "elephant", Status: "xato", Substitutions: nil},
	}}
	if n := ApplyConfidence(check); n != 1 {
		t.Errorf("softened %d, want 1", n)
	}
	if check.Words[0].Status != "kichik" {
		t.Errorf("status = %q, want kichik", check.Words[0].Status)
	}
}

func TestApplyConfidenceNilSafe(t *testing.T) {
	if n := ApplyConfidence(nil); n != 0 {
		t.Errorf("nil check softened %d, want 0", n)
	}
}
