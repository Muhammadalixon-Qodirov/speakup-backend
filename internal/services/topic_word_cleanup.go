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

// topicWordMinUses gates which accumulated words reach the LLM: a word must
// have been accepted at least this many times before review. This is the
// anti-poisoning guard - a single user can't push junk into the trusted bank.
const topicWordMinUses = 3

// topicWordBatchCap bounds how many words per topic go in one prompt.
const topicWordBatchCap = 200

// CleanupTopicWords reviews pending Vocabulary Sprint words with the LLM and
// marks each as trusted (on-topic) or rejected (clearly off-topic) using a
// lenient prompt. Wired to a weekly cron. Idempotent: re-running only ever
// re-reviews words still pending.
func CleanupTopicWords() {
	var rows []models.TopicWord
	if err := database.DB.
		Where("status = ? AND uses >= ?", models.TopicWordPending, topicWordMinUses).
		Find(&rows).Error; err != nil {
		log.Warn().Err(err).Msg("topicwords: load pending failed")
		return
	}
	if len(rows) == 0 {
		return
	}

	byTopic := map[string][]string{}
	for _, r := range rows {
		byTopic[r.Topic] = append(byTopic[r.Topic], r.Word)
	}

	trusted, rejected := 0, 0
	for topic, words := range byTopic {
		remove, err := reviewTopicWords(topic, words)
		if err != nil {
			log.Warn().Err(err).Str("topic", topic).Msg("topicwords: LLM review failed")
			continue // leave pending; retry next week
		}
		removeSet := map[string]struct{}{}
		for _, w := range remove {
			removeSet[strings.ToLower(strings.TrimSpace(w))] = struct{}{}
		}
		now := time.Now()
		for _, w := range words {
			status := models.TopicWordTrusted
			if _, bad := removeSet[w]; bad {
				status = models.TopicWordRejected
				rejected++
			} else {
				trusted++
			}
			database.DB.Model(&models.TopicWord{}).
				Where("topic = ? AND word = ?", topic, w).
				Updates(map[string]interface{}{"status": status, "last_reviewed_at": now})
		}
	}

	games.RefreshTopicCache()
	log.Info().Int("trusted", trusted).Int("rejected", rejected).Msg("topicwords: weekly cleanup done")
}

// reviewTopicWords asks the LLM which words are clearly unrelated to the
// topic. The prompt is deliberately lenient - vectors already err toward
// inclusion, so the AI's job is only to strip obvious off-topic noise.
func reviewTopicWords(topic string, words []string) ([]string, error) {
	if len(words) > topicWordBatchCap {
		words = words[:topicWordBatchCap]
	}
	prompt := fmt.Sprintf(`Topic: "%s".
Players typed these English words for this topic:
%s

Return ONLY the words that are CLEARLY UNRELATED to the topic. Be LENIENT - if
a word could reasonably relate to the topic in ANY context, KEEP it (do not
list it). When in doubt, keep.
Respond as JSON: {"remove": ["word1", "word2"]}`, topic, strings.Join(words, ", "))

	reqBody, _ := json.Marshal(map[string]interface{}{
		"model": config.App.GroqLLMModel,
		"messages": []map[string]string{
			{"role": "system", "content": "You are a vocabulary moderator for an English learning game. JSON only."},
			{"role": "user", "content": prompt},
		},
		"temperature":     0.0,
		"max_tokens":      2500,
		"response_format": map[string]string{"type": "json_object"},
	})

	var remove []string
	err := TryWithKeys("llm", func(apiKey string) error {
		req, err := http.NewRequest("POST", groqAPIBase+"/chat/completions", bytes.NewReader(reqBody))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
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
		var out struct {
			Remove []string `json:"remove"`
		}
		if err := json.Unmarshal([]byte(chat.Choices[0].Message.Content), &out); err != nil {
			return err
		}
		remove = out.Remove
		return nil
	})
	return remove, err
}
