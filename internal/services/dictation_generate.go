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
)

const (
	// dictationTarget is the pool size the replenisher grows toward, so players
	// keep getting fresh sentences instead of repeats.
	dictationTarget = 400
	// dictationBatchSize caps how many sentences one run generates.
	dictationBatchSize = 40
)

// ReplenishDictation tops up the Dictation Race pool with AI-generated
// sentences whenever it's below target. Wired to a daily cron - it stops once
// the pool reaches dictationTarget. Idempotent: duplicates are skipped.
func ReplenishDictation() {
	have := games.DictationCount()
	if have >= dictationTarget {
		return
	}
	need := dictationTarget - have
	if need > dictationBatchSize {
		need = dictationBatchSize
	}

	gen, err := generateDictation(need)
	if err != nil {
		log.Warn().Err(err).Msg("dictation: generation failed")
		return
	}

	existing := games.DictationTexts()
	added := 0
	for _, s := range gen {
		text := strings.TrimSpace(s.Text)
		key := strings.ToLower(text)
		if text == "" || len(text) > 200 {
			continue
		}
		if _, dup := existing[key]; dup {
			continue
		}
		lvl := strings.ToUpper(strings.TrimSpace(s.Level))
		if !validDictationLevel(lvl) {
			lvl = "B1"
		}
		existing[key] = struct{}{}
		database.DB.Exec(
			`INSERT INTO dictation_sentences (id, text, level, created_at)
			 VALUES (gen_random_uuid(), ?, ?, NOW())
			 ON CONFLICT (text) DO NOTHING`, text, lvl)
		added++
	}

	games.RefreshDictationPool()
	log.Info().Int("added", added).Int("pool", games.DictationCount()).Msg("dictation: replenished")
}

func validDictationLevel(l string) bool {
	switch l {
	case "A1", "A2", "B1", "B2", "C1", "C2":
		return true
	}
	return false
}

type genSentence struct {
	Text  string `json:"text"`
	Level string `json:"level"`
}

// generateDictation asks the LLM for n fresh, level-graded dictation sentences.
func generateDictation(n int) ([]genSentence, error) {
	prompt := fmt.Sprintf(`Generate %d English sentences for a listening dictation game.
Spread them across CEFR levels A1, A2, B1, B2 and C1. Each sentence must be
clear, natural, self-contained, 5-14 words, with NO quotation marks or unusual
symbols. Avoid proper names that are hard to spell.
Respond as JSON: {"sentences": [{"text": "...", "level": "A1"}, ...]}`, n)

	reqBody, _ := json.Marshal(map[string]interface{}{
		"model": config.App.GroqLLMModel,
		"messages": []map[string]string{
			{"role": "system", "content": "You generate clean English learning content. JSON only."},
			{"role": "user", "content": prompt},
		},
		"temperature":     0.8,
		"max_tokens":      2000,
		"response_format": map[string]string{"type": "json_object"},
	})

	var out []genSentence
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
			Sentences []genSentence `json:"sentences"`
		}
		if err := json.Unmarshal([]byte(chat.Choices[0].Message.Content), &parsed); err != nil {
			return err
		}
		out = parsed.Sentences
		return nil
	})
	return out, err
}
