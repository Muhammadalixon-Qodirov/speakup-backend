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

// synonymMinUses gates which candidate answers reach the LLM: an answer must
// have been typed at least this many times before review (anti-poisoning +
// cost control). Gibberish never reaches here - only real words are recorded.
const synonymMinUses = 3

// synonymBatchCap bounds how many candidates per (word,type) go in one prompt.
const synonymBatchCap = 40

// CleanupSynonymCandidates reviews pending Synonym Duel answers with the LLM and
// marks each accepted (genuine synonym/antonym) or rejected. Accepted ones then
// count as correct at runtime via the in-memory cache. Wired to a weekly cron.
func CleanupSynonymCandidates() {
	var rows []models.SynonymCandidate
	if err := database.DB.
		Where("status = ? AND uses >= ?", models.SynonymPending, synonymMinUses).
		Find(&rows).Error; err != nil {
		log.Warn().Err(err).Msg("synonyms: load pending failed")
		return
	}
	if len(rows) == 0 {
		return
	}

	type key struct{ word, typ string }
	groups := map[key][]string{}
	for _, r := range rows {
		k := key{r.Word, r.Type}
		groups[k] = append(groups[k], r.Answer)
	}

	accepted, rejected := 0, 0
	for k, answers := range groups {
		valid, err := reviewSynonyms(k.word, k.typ, answers)
		if err != nil {
			log.Warn().Err(err).Str("word", k.word).Msg("synonyms: LLM review failed")
			continue // leave pending; retry next week
		}
		validSet := map[string]struct{}{}
		for _, w := range valid {
			validSet[strings.ToLower(strings.TrimSpace(w))] = struct{}{}
		}
		now := time.Now()
		for _, ans := range answers {
			status := models.SynonymRejected
			if _, ok := validSet[ans]; ok {
				status = models.SynonymAccepted
				accepted++
			} else {
				rejected++
			}
			database.DB.Model(&models.SynonymCandidate{}).
				Where("word = ? AND type = ? AND answer = ?", k.word, k.typ, ans).
				Updates(map[string]interface{}{"status": status, "last_reviewed_at": now})
		}
	}

	games.RefreshSynonymCache()
	log.Info().Int("accepted", accepted).Int("rejected", rejected).Msg("synonyms: weekly cleanup done")
}

// reviewSynonyms asks the LLM which candidate words are genuine synonyms (or
// antonyms) of the prompt word. Stricter than the topic prompt - a wrong accept
// would let players score with non-synonyms.
func reviewSynonyms(word, typ string, answers []string) ([]string, error) {
	if len(answers) > synonymBatchCap {
		answers = answers[:synonymBatchCap]
	}
	rel := "synonyms"
	if typ == "antonym" {
		rel = "antonyms"
	}
	prompt := fmt.Sprintf(`The English word is "%s". From the candidate list below,
return ONLY the words that are genuine %s of it. Be reasonably strict - exclude
words that are merely related, the same word, or a different part of speech.
Candidates: %s
Respond as JSON: {"valid": ["word1", "word2"]}`, word, rel, strings.Join(answers, ", "))

	reqBody, _ := json.Marshal(map[string]interface{}{
		"model": config.App.GroqLLMModel,
		"messages": []map[string]string{
			{"role": "system", "content": "You are a precise English thesaurus. JSON only."},
			{"role": "user", "content": prompt},
		},
		"temperature":     0.0,
		"max_tokens":      2500,
		"response_format": map[string]string{"type": "json_object"},
	})

	var valid []string
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
			Valid []string `json:"valid"`
		}
		if err := json.Unmarshal([]byte(chat.Choices[0].Message.Content), &out); err != nil {
			return err
		}
		valid = out.Valid
		return nil
	})
	return valid, err
}
