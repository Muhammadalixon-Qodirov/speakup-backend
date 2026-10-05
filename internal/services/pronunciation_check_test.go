package services

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/speak-up/backend/internal/config"
)

// ensureConfig allocates config.App, which only config.Load() sets at boot and
// is therefore nil under test.
func ensureConfig() {
	if config.App == nil {
		config.App = &config.Config{}
	}
}

// withSidecar points config at a stub sidecar for the duration of a test.
func withSidecar(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	ensureConfig()
	srv := httptest.NewServer(h)
	ensureConfig()
	prev := config.App.TalaffuzURL
	config.App.TalaffuzURL = srv.URL
	t.Cleanup(func() {
		config.App.TalaffuzURL = prev
		srv.Close()
	})
	return srv
}

func TestCheckPronunciationParsesVerdict(t *testing.T) {
	var gotPassage, gotMethod, gotPath string
	var gotBody []byte

	withSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotPassage = r.URL.Query().Get("matn")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"fonemalar": ["s","ɪ","ŋ","k"],
			"sozlar": [
				{"soz":"think","kutilgan":"θ ɪ ŋ k","eshitilgan":"s ɪ ŋ k","holat":"xato",
				 "izohlar":[{"tur":"xato","matn":"«th» o'rniga «s» eshitildi."}],
				 "vaqt":[0.42,0.91]},
				{"soz":"cat","kutilgan":"k æ t","eshitilgan":"k æ t","holat":"ok",
				 "izohlar":[],"vaqt":[0.95,1.30]}
			],
			"vaqt": 0.48,
			"davomiylik": 3.4
		}`)
	})

	check, err := CheckPronunciation([]byte("RIFFfake-wav-bytes"), "think cat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The passage must travel as a query parameter and the audio as the body:
	// that is the contract the sidecar's /api/tani expects.
	if gotMethod != "POST" || gotPath != "/api/tani" {
		t.Errorf("called %s %s, want POST /api/tani", gotMethod, gotPath)
	}
	if gotPassage != "think cat" {
		t.Errorf("passage = %q, want %q", gotPassage, "think cat")
	}
	if string(gotBody) != "RIFFfake-wav-bytes" {
		t.Errorf("body = %q, audio was not forwarded verbatim", gotBody)
	}

	if len(check.Words) != 2 {
		t.Fatalf("got %d words, want 2", len(check.Words))
	}
	w0 := check.Words[0]
	if w0.Word != "think" || w0.Status != "xato" || w0.Expected != "θ ɪ ŋ k" || w0.Heard != "s ɪ ŋ k" {
		t.Errorf("first word parsed wrong: %+v", w0)
	}
	if len(w0.Span) != 2 || w0.Span[0] != 0.42 {
		t.Errorf("span not parsed: %v", w0.Span)
	}
	if len(w0.Notes) != 1 || w0.Notes[0].Kind != "xato" {
		t.Errorf("notes not parsed: %+v", w0.Notes)
	}
	if check.Duration != 3.4 {
		t.Errorf("duration = %v, want 3.4", check.Duration)
	}
}

func TestWordResultsFromTreatsNearMissAsAcceptable(t *testing.T) {
	// "kichik" is a vowel slightly off target. Counting it as an error would
	// push feedback accuracy below the level at which it still helps, so only
	// "xato" is wrong.
	check := &PronunciationCheck{Words: []PronunciationWord{
		{Word: "think", Status: "xato"},
		{Word: "ship", Status: "kichik"},
		{Word: "cat", Status: "ok"},
	}}
	got := WordResultsFrom(check)
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3", len(got))
	}
	want := map[string]bool{"think": false, "ship": true, "cat": true}
	for _, r := range got {
		if want[r.Word] != r.OK {
			t.Errorf("%s: OK = %v, want %v", r.Word, r.OK, want[r.Word])
		}
	}
	if WordResultsFrom(nil) != nil {
		t.Error("nil check should produce nil results")
	}
}

func TestCheckPronunciationSurfacesSidecarReason(t *testing.T) {
	withSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"detail":"Audio juda qisqa"}`)
	})
	_, err := CheckPronunciation([]byte("x"), "hello there friend")
	if err == nil {
		t.Fatal("expected an error")
	}
	// The learner needs to be told to re-record, so the reason must survive.
	if !strings.Contains(err.Error(), "Audio juda qisqa") {
		t.Errorf("error lost the sidecar's reason: %v", err)
	}
}

func TestCheckPronunciationValidatesInput(t *testing.T) {
	withSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("sidecar should not be called for invalid input")
	})
	if _, err := CheckPronunciation(nil, "some text"); err == nil {
		t.Error("empty audio should be rejected locally")
	}
	if _, err := CheckPronunciation([]byte("x"), "   "); err == nil {
		t.Error("blank passage should be rejected locally")
	}
}

func TestRenderReferenceDefaults(t *testing.T) {
	var voice, speed string
	withSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		voice = r.URL.Query().Get("ovoz")
		speed = r.URL.Query().Get("tezlik")
		w.Header().Set("Content-Type", "audio/wav")
		io.WriteString(w, "RIFFwav")
	})

	wav, err := RenderReference("I think so.", "", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(wav) != "RIFFwav" {
		t.Errorf("audio not returned verbatim: %q", wav)
	}
	// American voice, because the phoneme targets come from US CMUdict; a
	// British reading would model a pronunciation the grader marks down.
	if voice != "af_heart" {
		t.Errorf("default voice = %q, want af_heart", voice)
	}
	// Slower than natural so the learner can imitate it.
	if speed != "0.85" {
		t.Errorf("default speed = %q, want 0.85", speed)
	}
}

func TestTalaffuzDisabledWhenUnset(t *testing.T) {
	ensureConfig()
	prev := config.App.TalaffuzURL
	config.App.TalaffuzURL = ""
	t.Cleanup(func() { config.App.TalaffuzURL = prev })

	if TalaffuzEnabled() {
		t.Error("empty URL should disable the feature")
	}
	if TalaffuzReady() {
		t.Error("disabled sidecar cannot be ready")
	}
	// A dev box without the sidecar must get a clear error, not a panic or a
	// hang against a nonexistent host.
	if _, err := CheckPronunciation([]byte("x"), "hello there"); err == nil {
		t.Error("expected an error when the sidecar is not configured")
	}
}
