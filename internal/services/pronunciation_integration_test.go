package services

import (
	"os"
	"testing"

	"github.com/speak-up/backend/internal/config"
)

// End-to-end against a real sidecar. Skipped unless TALAFFUZ_TEST_URL is set,
// because CI has no sidecar and no model files - the unit tests cover the
// contract with a stub.
//
// Run it with a live sidecar and a sample recording:
//
//	TALAFFUZ_TEST_URL=http://127.0.0.1:7881 \
//	TALAFFUZ_TEST_WAV=/path/to/sample.wav \
//	TALAFFUZ_TEST_TEXT="IT WAS AN IMPORTANT WIN" \
//	go test ./internal/services/ -run TestSidecarEndToEnd -v
func TestSidecarEndToEnd(t *testing.T) {
	url := os.Getenv("TALAFFUZ_TEST_URL")
	wavPath := os.Getenv("TALAFFUZ_TEST_WAV")
	text := os.Getenv("TALAFFUZ_TEST_TEXT")
	if url == "" || wavPath == "" || text == "" {
		t.Skip("set TALAFFUZ_TEST_URL, TALAFFUZ_TEST_WAV and TALAFFUZ_TEST_TEXT to run")
	}

	ensureConfig()
	prev := config.App.TalaffuzURL
	config.App.TalaffuzURL = url
	t.Cleanup(func() { config.App.TalaffuzURL = prev })

	if !TalaffuzReady() {
		t.Fatalf("sidecar at %s is not ready", url)
	}

	wav, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatalf("cannot read %s: %v", wavPath, err)
	}

	check, err := CheckPronunciation(wav, text)
	if err != nil {
		t.Fatalf("CheckPronunciation: %v", err)
	}
	if len(check.Words) == 0 {
		t.Fatal("no words came back - the passage text may not have reached the sidecar")
	}
	if check.Duration <= 0 {
		t.Errorf("duration = %v, want > 0", check.Duration)
	}

	// The structured substitutions are what the confidence policy runs on. If
	// the sidecar stops sending them, every verdict silently becomes a hint.
	sawSubstitution := false
	for _, w := range check.Words {
		if len(w.Substitutions) > 0 {
			sawSubstitution = true
			for _, s := range w.Substitutions {
				if s.Expected == "" {
					t.Errorf("%s: substitution with no expected phoneme: %+v", w.Word, s)
				}
			}
		}
	}

	rawErrors := 0
	for _, w := range check.Words {
		if w.Status == "xato" {
			rawErrors++
		}
	}
	softened := ApplyConfidence(check)
	keptErrors := 0
	for _, w := range check.Words {
		if w.Status == "xato" {
			keptErrors++
		}
	}

	t.Logf("%d words | %.1fs audio in %.2fs | raw errors %d -> kept %d (softened %d) | substitutions present: %v",
		len(check.Words), check.Duration, check.TookSec,
		rawErrors, keptErrors, softened, sawSubstitution)

	results := WordResultsFrom(check)
	if len(results) != len(check.Words) {
		t.Errorf("WordResultsFrom returned %d results for %d words", len(results), len(check.Words))
	}
}
