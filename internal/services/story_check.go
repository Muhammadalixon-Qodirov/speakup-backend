package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/games"
)

// JudgeStory scores both players' Story Chain continuations with the LLM:
// coherence with the opener, grammar, creativity, and meaningful progression.
// Returns a 0-100 score + short feedback per player. Wired into
// games.StoryJudgeFn at startup; called off the game lock (async).
func JudgeStory(starter, story0, story1 string) (games.StoryJudgement, error) {
	prompt := fmt.Sprintf(`Two players continued the SAME story opener in a writing duel.

Opener: "%s"

Player A's continuation:
"%s"

Player B's continuation:
"%s"

Score EACH continuation from 0 to 100 on: coherence with the opener, grammar,
creativity, and meaningful progression of the story. Be fair and specific.
Respond as JSON:
{"score_a": 0, "score_b": 0,
 "feedback_a": "one short sentence for player A",
 "feedback_b": "one short sentence for player B",
 "verdict": "one short sentence on who continued better and why"}`,
		starter, story0, story1)

	reqBody, _ := json.Marshal(map[string]interface{}{
		"model": config.App.GroqLLMModel,
		"messages": []map[string]string{
			{"role": "system", "content": "You are a fair, encouraging creative-writing judge. JSON only."},
			{"role": "user", "content": prompt},
		},
		"temperature":     0.2,
		"max_tokens":      1500,
		"response_format": map[string]string{"type": "json_object"},
	})

	var j games.StoryJudgement
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
			ScoreA    float64 `json:"score_a"`
			ScoreB    float64 `json:"score_b"`
			FeedbackA string  `json:"feedback_a"`
			FeedbackB string  `json:"feedback_b"`
			Verdict   string  `json:"verdict"`
		}
		if err := json.Unmarshal([]byte(chat.Choices[0].Message.Content), &out); err != nil {
			return err
		}
		j = games.StoryJudgement{
			Score0:    out.ScoreA,
			Score1:    out.ScoreB,
			Feedback0: out.FeedbackA,
			Feedback1: out.FeedbackB,
			Verdict:   out.Verdict,
		}
		return nil
	})
	return j, err
}
