package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/games"
	"github.com/speak-up/backend/internal/models"
)

// The read-aloud pool is bucketed by (topic, level, sentence_count):
// 10 topics x 6 levels x 4 lengths = 240 buckets. Filling every bucket to
// pronBucketTarget gives ~4800 passages, which is far more than one learner
// can exhaust - at three exercises a day it lasts years. What actually prevents
// repetition is user_passage_seen, not pool size, so the generator exists only
// to make each bucket deep enough to draw from, and can slow right down once
// the pool is built.
const (
	pronBucketTarget    = 20
	pronPassagesPerCall = 8
	pronMaxCallsPerRun  = 40 // ~320 passages/day while filling
)

// pronLevels are the levels we generate for. C2 is deliberately excluded: at
// that level the interesting signal is rhythm and stress, not segment accuracy,
// and the vocabulary gets rare enough that validation rejects most of it.
var pronLevels = []string{"A1", "A2", "B1", "B2", "C1"}

// bucket is one (topic, level, sentenceCount) combination.
type bucket struct {
	Topic         string
	Level         string
	SentenceCount int
	Have          int
}

// ReplenishPronunciation tops up the read-aloud passage pool. Wired to a daily
// cron. Idempotent: it only generates for buckets below target, and duplicate
// text is dropped by the unique index.
func ReplenishPronunciation() {
	buckets, err := thinnestBuckets()
	if err != nil {
		log.Warn().Err(err).Msg("pronunciation: bucket scan failed")
		return
	}
	if len(buckets) == 0 {
		return // pool is full
	}

	calls, added, rejected := 0, 0, 0
	for _, b := range buckets {
		if calls >= pronMaxCallsPerRun {
			break
		}
		calls++

		gen, err := generatePassages(b, pronPassagesPerCall)
		if err != nil {
			log.Warn().Err(err).Str("topic", b.Topic).Str("level", b.Level).
				Msg("pronunciation: generation failed")
			continue
		}
		for _, p := range gen {
			ok, reason := storePassage(b, p)
			if ok {
				added++
			} else {
				rejected++
				log.Debug().Str("reason", reason).Str("text", p.Text).
					Msg("pronunciation: passage rejected")
			}
		}
	}

	log.Info().Int("added", added).Int("rejected", rejected).Int("calls", calls).
		Int("pool", pronPoolSize()).Msg("pronunciation: replenished")
}

func pronPoolSize() int {
	var n int64
	database.DB.Model(&models.PronunciationPassage{}).Count(&n)
	return int(n)
}

// thinnestBuckets returns the under-filled buckets, emptiest first, so a run
// that hits pronMaxCallsPerRun spends its budget where the pool is weakest
// rather than topping up buckets that are already deep.
func thinnestBuckets() ([]bucket, error) {
	type row struct {
		Topic         string
		Level         string
		SentenceCount int
		Have          int
	}
	var rows []row
	err := database.DB.Model(&models.PronunciationPassage{}).
		Select("topic, level, sentence_count, COUNT(*) AS have").
		Group("topic, level, sentence_count").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	have := make(map[string]int, len(rows))
	for _, r := range rows {
		have[fmt.Sprintf("%s|%s|%d", r.Topic, r.Level, r.SentenceCount)] = r.Have
	}

	var out []bucket
	for _, topic := range models.PronunciationTopics {
		for _, level := range pronLevels {
			for n := 1; n <= 4; n++ {
				h := have[fmt.Sprintf("%s|%s|%d", topic, level, n)]
				if h < pronBucketTarget {
					out = append(out, bucket{topic, level, n, h})
				}
			}
		}
	}
	// Emptiest first.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Have < out[j-1].Have; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

type genPassage struct {
	Text string `json:"text"`
}

// validatePassage is the gate every generated passage must pass. It is kept
// free of database access so the rules can be tested directly: thresholds that
// are too strict burn LLM calls on passages that are then thrown away, and that
// is only visible if the rules can be exercised in a test.
//
// Nothing the model writes is trusted. The gate is what turns "an LLM wrote
// some sentences" into "every word has a human-written reference
// pronunciation, at the level we claim" - and the single most important check
// is dictionary membership, because a guessed reference pronunciation marks
// correct speech as wrong.
func validatePassage(b bucket, raw string) (models.PronunciationPassage, string) {
	text := strings.Join(strings.Fields(raw), " ")
	if text == "" {
		return models.PronunciationPassage{}, "empty"
	}
	if len(text) > 600 {
		return models.PronunciationPassage{}, "too long for the column"
	}

	// 1. No quotes or odd symbols: they confuse both the reader and the aligner.
	//    Apostrophes are absent from this set on purpose - contractions
	//    ("don't", "it's") are normal spoken English and are in the dictionary.
	if strings.ContainsAny(text, "\"“”‘()[]{}*_#@\\/|<>~`") {
		return models.PronunciationPassage{}, "forbidden symbols"
	}

	// 2. Sentence count must match what we asked for: the pool is served by
	//    this number, so a mismatch would hand the learner the wrong length.
	sentences := splitSentences(text)
	if len(sentences) != b.SentenceCount {
		return models.PronunciationPassage{}, fmt.Sprintf("want %d sentences, got %d", b.SentenceCount, len(sentences))
	}

	// 3. Each sentence 5-14 words. Long sentences make the reader stumble, and
	//    a stumble is not a pronunciation error but gets scored like one.
	for _, s := range sentences {
		if n := len(games.PronTokens(s)); n < 5 || n > 14 {
			return models.PronunciationPassage{}, fmt.Sprintf("sentence of %d words", n)
		}
	}

	// 4. Every word must be in the dictionary - the accuracy guarantee.
	phonemes, sounds, unknown := games.PronAnalyse(text)
	if len(unknown) > 0 {
		return models.PronunciationPassage{}, "unknown words: " + strings.Join(unknown, ",")
	}

	// 5. No proper nouns. Dictionary membership does not catch these - cmudict
	//    happily contains "tashkent" and thousands of other names - but they
	//    are poor exercise material: pronunciation varies by region, so the
	//    single reference transcription would mark reasonable speech wrong.
	if w, found := firstProperNoun(sentences); found {
		return models.PronunciationPassage{}, "proper noun: " + w
	}

	// 6. The claimed level has to hold up against our graded vocabulary. LLMs
	//    routinely ignore a CEFR instruction, so we measure instead of trusting.
	fits, share, coverage := games.PronLevelFits(text, b.Level, 0.85)
	if !fits {
		return models.PronunciationPassage{}, fmt.Sprintf(
			"level %s: %.0f%% of graded words fit, %.0f%% of words graded",
			b.Level, share*100, coverage*100)
	}

	// 7. Multi-sentence passages must read as one piece. A shared content word
	//    or a leading connective is a crude but cheap signal; without it the
	//    "passage" is just unrelated sentences stapled together.
	if b.SentenceCount > 1 && !looksCohesive(sentences) {
		return models.PronunciationPassage{}, "sentences not cohesive"
	}

	return models.PronunciationPassage{
		Text:          text,
		SentenceCount: b.SentenceCount,
		Level:         b.Level,
		Topic:         b.Topic,
		WordCount:     len(games.PronTokens(text)),
		Phonemes:      phonemes,
		TargetSounds:  strings.Join(sounds, ","),
	}, ""
}

// storePassage validates a generated passage and inserts it if it passes.
func storePassage(b bucket, p genPassage) (bool, string) {
	passage, reason := validatePassage(b, p.Text)
	if reason != "" {
		return false, reason
	}
	text := passage.Text

	// No duplicates. The check is case-insensitive because the model likes
	//    to re-emit the same passage with different capitalisation; the unique
	//    index on text is the backstop if two runs ever race here.
	var exists int64
	if err := database.DB.Model(&models.PronunciationPassage{}).
		Where("lower(text) = lower(?)", text).Count(&exists).Error; err != nil {
		return false, err.Error()
	}
	if exists > 0 {
		return false, "duplicate"
	}
	if err := database.DB.Create(&passage).Error; err != nil {
		return false, err.Error()
	}
	return true, ""
}

// Capitalised words that are ordinary vocabulary rather than names. Days and
// months do not need listing: they carry a CEFR level, which is the general
// rule below. These are the ones our graded list happens to omit, and the first
// of them is unavoidable in an app that teaches English.
var pronAllowedCapitals = map[string]struct{}{
	"I": {}, "English": {}, "British": {}, "American": {},
}

// firstProperNoun returns the first capitalised word that is neither sentence
// initial nor ordinary vocabulary - a cheap and effective name detector for
// generated text.
//
// A capitalised word carrying a CEFR level is ordinary vocabulary ("Monday",
// "January"); place and person names are absent from a graded word list, which
// is what separates "We met on Monday" from "We went to Paris".
func firstProperNoun(sentences []string) (string, bool) {
	for _, s := range sentences {
		for i, w := range strings.Fields(s) {
			if i == 0 {
				continue
			}
			trimmed := strings.Trim(w, ".,!?;:")
			if trimmed == "" {
				continue
			}
			r := []rune(trimmed)[0]
			if r < 'A' || r > 'Z' {
				continue
			}
			if _, ok := pronAllowedCapitals[trimmed]; ok {
				continue
			}
			if e, found := games.PronLookup(trimmed); found && e.Level != "" {
				continue
			}
			return trimmed, true
		}
	}
	return "", false
}

// splitSentences splits on . ! ? and drops empties.
func splitSentences(text string) []string {
	var out []string
	start := 0
	for i, r := range text {
		if r == '.' || r == '!' || r == '?' {
			s := strings.TrimSpace(text[start : i+1])
			if s != "" {
				out = append(out, s)
			}
			start = i + 1
		}
	}
	if tail := strings.TrimSpace(text[start:]); tail != "" {
		out = append(out, tail)
	}
	return out
}

// Openings that carry on from the previous sentence: pronouns, connectives,
// and the determiners and possessives that introduce something already in play
// ("The journey took three hours" after a sentence about travelling).
var pronConnectives = map[string]struct{}{
	"i": {}, "you": {}, "it": {}, "they": {}, "he": {}, "she": {}, "we": {},
	"this": {}, "that": {},
	"these": {}, "those": {}, "there": {}, "then": {}, "so": {}, "but": {},
	"and": {}, "because": {}, "however": {}, "also": {}, "still": {}, "yet": {},
	"afterwards": {}, "later": {}, "now": {},
	"the": {}, "a": {}, "an": {}, "my": {}, "our": {}, "his": {}, "her": {},
	"its": {}, "their": {}, "both": {},
}

// sameStem is a deliberately crude stem comparison: two words of four or more
// letters sharing their first four letters count as the same idea, so "cook"
// links to "cooking" and "travel" to "travelled".
//
// A real stemmer is not worth the dependency here. Cohesion is a quality
// signal, not an accuracy guarantee - a missed link only costs us one generated
// passage, and the LLM is asked for several at a time.
func sameStem(a, b string) bool {
	if len(a) < 4 || len(b) < 4 {
		return a == b
	}
	return a[:4] == b[:4]
}

// looksCohesive checks that consecutive sentences are linked, either by opening
// with a connective or determiner or by sharing a content word with the one
// before.
//
// This is deliberately a weak check, and it is worth being clear about what it
// does not do: it catches a blatant non-sequitur ("My brother works in a shop.
// Mountains are very tall.") and little else. A sentence opening with "The" is
// waved through, so unrelated-but-well-formed pairs still get in. Cohesion is
// mainly the prompt's job; a stricter rule here rejected more good passages
// than bad ones, which only wastes generation calls.
func looksCohesive(sentences []string) bool {
	for i := 1; i < len(sentences); i++ {
		prev := games.PronTokens(sentences[i-1])
		cur := games.PronTokens(sentences[i])
		if len(cur) == 0 {
			return false
		}
		if _, ok := pronConnectives[cur[0]]; ok {
			continue
		}
		shared := false
		for _, a := range cur {
			if len(a) < 4 { // skip function words; they link nothing
				continue
			}
			for _, b := range prev {
				if sameStem(a, b) {
					shared = true
					break
				}
			}
			if shared {
				break
			}
		}
		if !shared {
			return false
		}
	}
	return true
}

// generatePassages asks the LLM for n read-aloud passages for one bucket.
func generatePassages(b bucket, n int) ([]genPassage, error) {
	plural := "sentences"
	if b.SentenceCount == 1 {
		plural = "sentence"
	}

	prompt := fmt.Sprintf(`Write %d short passages for an English pronunciation
exercise. The learner will read each passage aloud, so it must sound natural
when spoken.

Rules for EVERY passage:
- Exactly %d %s. Each sentence 6 to 13 words.
- Topic: %s. CEFR level: %s - use only vocabulary a %s learner knows.
- The sentences must connect into one small coherent piece (same subject,
  pronouns referring back), not unrelated statements.
- Everyday words only. NO proper names, NO place names, NO brand names,
  NO abbreviations, NO numbers written as digits, NO quotation marks.
- Favour words containing these sounds where it still reads naturally:
  th (think, three), w vs v (west, very), short/long i (ship, sheep),
  the a in "cat".

Respond as JSON: {"passages": [{"text": "..."}, ...]}`,
		n, b.SentenceCount, plural, b.Topic, b.Level, b.Level)

	reqBody, _ := json.Marshal(map[string]interface{}{
		"model": config.App.GroqLLMModel,
		"messages": []map[string]string{
			{"role": "system", "content": "You write clean English learning content. JSON only."},
			{"role": "user", "content": prompt},
		},
		"temperature":     0.9, // variety matters more than polish here
		"max_tokens":      2000,
		"response_format": map[string]string{"type": "json_object"},
	})

	var out []genPassage
	err := TryWithKeys("llm", func(apiKey string) error {
		req, err := http.NewRequest("POST", groqAPIBase+"/chat/completions", bytes.NewReader(reqBody))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := (&http.Client{Timeout: 40 * time.Second}).Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			return fmt.Errorf("LLM error %d: %s", resp.StatusCode, string(body))
		}
		var chat struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(body, &chat); err != nil {
			return err
		}
		if len(chat.Choices) == 0 {
			return fmt.Errorf("LLM returned no choices")
		}
		var parsed struct {
			Passages []genPassage `json:"passages"`
		}
		if err := json.Unmarshal([]byte(chat.Choices[0].Message.Content), &parsed); err != nil {
			return err
		}
		out = parsed.Passages
		return nil
	})
	return out, err
}
