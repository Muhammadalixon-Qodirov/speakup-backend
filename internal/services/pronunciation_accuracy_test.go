package services

import (
	"encoding/json"
	"os"
	"testing"
)

// This is the measurement that decides whether the feature is safe to show.
//
// testdata/speechocean762_verdicts.json is real output: four speechocean762
// recordings put through the actual sidecar (ZIPA + the comparison logic), with
// the human per-word annotations alongside. Humans marked every one of these 22
// words correctly pronounced.
//
// The raw sidecar called 5 of them errors - a 23% false-positive rate with no
// true positives to show for it. Published work puts the threshold at which
// corrective feedback stops helping and starts hurting at around 66% accuracy,
// so the raw verdict cannot be shown to a learner as a mistake.
//
// The test asserts the confidence policy brings that to zero on this data. It is
// a small sample and not proof of general accuracy - it is a regression guard,
// so a later change to the thresholds cannot quietly reintroduce the problem.
type verdictFixture struct {
	Sample string `json:"sample"`
	Text   string `json:"text"`
	Words  []struct {
		Word          string         `json:"soz"`
		Status        string         `json:"holat"`
		Expected      string         `json:"kutilgan"`
		Heard         string         `json:"eshitilgan"`
		Substitutions []Substitution `json:"almashtirishlar"`
		HumanOK       *bool          `json:"human_ok"`
	} `json:"words"`
}

func loadVerdicts(t *testing.T) []verdictFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/speechocean762_verdicts.json")
	if err != nil {
		t.Fatalf("fixture missing: %v", err)
	}
	var out []verdictFixture
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("fixture unreadable: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("fixture is empty")
	}
	return out
}

func TestConfidencePolicyOnRealAudio(t *testing.T) {
	fixtures := loadVerdicts(t)

	var humanCorrect, rawFalsePositive, filteredFalsePositive int

	for _, f := range fixtures {
		// Rebuild the check exactly as the sidecar would have returned it.
		check := &PronunciationCheck{}
		for _, w := range f.Words {
			check.Words = append(check.Words, PronunciationWord{
				Word:          w.Word,
				Expected:      w.Expected,
				Heard:         w.Heard,
				Status:        w.Status,
				Substitutions: w.Substitutions,
			})
		}

		for i, w := range f.Words {
			if w.HumanOK == nil || !*w.HumanOK {
				continue
			}
			humanCorrect++
			if check.Words[i].Status == "xato" {
				rawFalsePositive++
			}
		}

		ApplyConfidence(check)

		for i, w := range f.Words {
			if w.HumanOK == nil || !*w.HumanOK {
				continue
			}
			if check.Words[i].Status == "xato" {
				filteredFalsePositive++
				t.Errorf("%s: %q still called an error (expected %s, heard %s, subs %+v)",
					f.Sample, w.Word, w.Expected, w.Heard, w.Substitutions)
			}
		}
	}

	if humanCorrect == 0 {
		t.Fatal("fixture has no human-confirmed words to measure against")
	}

	t.Logf("human-correct words: %d | raw false positives: %d (%.0f%%) | after policy: %d (%.0f%%)",
		humanCorrect,
		rawFalsePositive, 100*float64(rawFalsePositive)/float64(humanCorrect),
		filteredFalsePositive, 100*float64(filteredFalsePositive)/float64(humanCorrect))

	// Guard the premise: if the fixture ever stops containing the problem, this
	// test is no longer measuring anything and should be regenerated.
	if rawFalsePositive == 0 {
		t.Error("fixture no longer exhibits raw false positives - regenerate it")
	}
}
