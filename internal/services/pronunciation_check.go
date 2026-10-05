package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/speak-up/backend/internal/config"
)

// The sidecar runs the phoneme recogniser on CPU. Measured on a 4-core box:
// 6 s of audio took 0.48 s, 20 s took 1.85 s, 30 s took 3.53 s. A 4-sentence
// passage is about 20 s of speech, so these timeouts are generous multiples of
// the real cost - they exist to catch a wedged container, not slow inference.
//
// The sidecar serialises inference behind a lock, so a queued request waits for
// the one ahead of it. checkTimeout covers that wait as well.
const (
	talaffuzCheckTimeout  = 90 * time.Second
	talaffuzTTSTimeout    = 60 * time.Second
	talaffuzHealthTimeout = 5 * time.Second
)

// PronunciationWord is one word's verdict.
//
// Holat (status) is "ok", "kichik" (a vowel slightly off) or "xato" (a consonant
// error or a dropped sound). It is a hint, not a ruling: phoneme-level accuracy
// is modest even in the best published systems, so the UI should present this as
// advice and never as a score the learner cannot argue with.
type PronunciationWord struct {
	Word     string `json:"soz"`
	Expected string `json:"kutilgan"`
	Heard    string `json:"eshitilgan"`
	Status   string `json:"holat"`
	Notes    []struct {
		Kind string `json:"tur"`
		Text string `json:"matn"`
	} `json:"izohlar"`
	// Span is [start, end] in seconds within the learner's recording, which is
	// what lets the UI play back just that word.
	Span []float64 `json:"vaqt"`
	// Substitutions is the structured form of what went wrong, which the
	// confidence policy reads instead of parsing the Uzbek note text.
	Substitutions []Substitution `json:"almashtirishlar"`
}

// PronunciationCheck is the sidecar's verdict on one attempt.
type PronunciationCheck struct {
	Phonemes []string            `json:"fonemalar"`
	Words    []PronunciationWord `json:"sozlar"`
	TookSec  float64             `json:"vaqt"`
	Duration float64             `json:"davomiylik"`
}

// TalaffuzEnabled reports whether the sidecar is configured at all. A dev box
// without it should degrade to "feature unavailable" rather than 500s.
func TalaffuzEnabled() bool {
	return strings.TrimSpace(config.App.TalaffuzURL) != ""
}

// TalaffuzReady reports whether the sidecar has finished loading its model.
// It returns 503 for the first few seconds after a restart, which is a normal
// state and not an error worth alerting on.
func TalaffuzReady() bool {
	if !TalaffuzEnabled() {
		return false
	}
	req, err := http.NewRequest("GET", strings.TrimRight(config.App.TalaffuzURL, "/")+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{Timeout: talaffuzHealthTimeout}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// CheckPronunciation sends the learner's recording and the passage they were
// reading to the sidecar, and returns the per-word verdict.
//
// The passage text is required. This is the whole reason the read-aloud mode
// exists: knowing what was meant to be said raises phoneme-level agreement from
// roughly 0.48 to 0.66 in the published figures, because the reference
// pronunciation is looked up instead of inferred from what the model thinks it
// heard.
func CheckPronunciation(audioWAV []byte, passage string) (*PronunciationCheck, error) {
	if !TalaffuzEnabled() {
		return nil, fmt.Errorf("pronunciation sidecar is not configured")
	}
	if len(audioWAV) == 0 {
		return nil, fmt.Errorf("empty audio")
	}
	if strings.TrimSpace(passage) == "" {
		return nil, fmt.Errorf("passage text is required")
	}

	endpoint := fmt.Sprintf("%s/api/tani?matn=%s",
		strings.TrimRight(config.App.TalaffuzURL, "/"), url.QueryEscape(passage))

	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(audioWAV))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "audio/wav")

	resp, err := (&http.Client{Timeout: talaffuzCheckTimeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("sidecar unreachable: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// The sidecar reports its own reasons ("Audio juda qisqa", "Audio
		// o'qilmadi") which are useful to pass through to the client.
		return nil, fmt.Errorf("sidecar error %d: %s", resp.StatusCode, sidecarDetail(body))
	}

	var out PronunciationCheck
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("sidecar returned unreadable JSON: %w", err)
	}
	return &out, nil
}

// RenderReference asks the sidecar to speak the passage, for the "how it should
// sound" playback.
//
// Voice defaults to an American one to stay consistent with the reference
// dictionary: our phoneme targets come from CMUdict, which is US English, so a
// British reading would demonstrate a pronunciation the grader then marks down.
// Speed defaults below 1.0 because the point is to be imitable, not fluent.
func RenderReference(passage, voice string, speed float64) ([]byte, error) {
	if !TalaffuzEnabled() {
		return nil, fmt.Errorf("pronunciation sidecar is not configured")
	}
	if strings.TrimSpace(passage) == "" {
		return nil, fmt.Errorf("passage text is required")
	}
	if voice == "" {
		voice = "af_heart"
	}
	if speed <= 0 {
		speed = 0.85
	}

	endpoint := fmt.Sprintf("%s/api/tts?matn=%s&ovoz=%s&tezlik=%.2f",
		strings.TrimRight(config.App.TalaffuzURL, "/"),
		url.QueryEscape(passage), url.QueryEscape(voice), speed)

	resp, err := (&http.Client{Timeout: talaffuzTTSTimeout}).Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("sidecar unreachable: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sidecar error %d: %s", resp.StatusCode, sidecarDetail(body))
	}
	return body, nil
}

// sidecarDetail pulls FastAPI's "detail" field out of an error body, falling
// back to the raw text, and keeps it short enough to log.
func sidecarDetail(body []byte) string {
	var e struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &e) == nil && e.Detail != "" {
		return e.Detail
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// WordResultsFrom turns the sidecar's verdict into the per-word outcomes the
// learner's history is folded from.
//
// Only "xato" counts as wrong. "kichik" - a vowel a little off target - is
// deliberately treated as acceptable: the published false-positive rates at
// phoneme level are high enough that treating every near miss as an error would
// push feedback accuracy below the level where it still helps rather than harms.
func WordResultsFrom(check *PronunciationCheck) []WordResult {
	if check == nil {
		return nil
	}
	out := make([]WordResult, 0, len(check.Words))
	for _, w := range check.Words {
		out = append(out, WordResult{Word: w.Word, OK: w.Status != "xato"})
	}
	return out
}
