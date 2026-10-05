package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/speak-up/backend/internal/config"
)

// ImprovedSpeech is what the LLM returns for band improvement.
type ImprovedSpeech struct {
	OriginalBand float64        `json:"original_band"`
	TargetBand   float64        `json:"target_band"`
	Original     string         `json:"original"`
	Improved     string         `json:"improved"`
	Changes      []SpeechChange `json:"changes"`
	Tips         []string       `json:"tips"`
}

// SpeechChange shows one specific improvement.
type SpeechChange struct {
	Original string `json:"original"`
	Improved string `json:"improved"`
	Reason   string `json:"reason"`
	Category string `json:"category"` // vocabulary | grammar | linking | collocation | structure
}

// ImproveSpeech takes a transcript + current band and returns an improved version for target band.
func ImproveSpeech(transcript string, currentBand, targetBand float64) (*ImprovedSpeech, error) {
	// Truncate very long transcripts - 4000 chars covers all 3 parts.
	if len(transcript) > 4000 {
		transcript = transcript[:4000]
	}

	prompt := fmt.Sprintf(`You are an IELTS Speaking coach. The student scored Band %.1f. Improve their speech to Band %.1f.

STUDENT'S ORIGINAL SPEECH:
"%s"

TASK:
1. Rewrite the ENTIRE speech at Band %.1f level. Keep the SAME ideas, upgrade vocabulary, grammar, linking words, collocations.
2. IMPORTANT: The text has "Part 1 (Interview):", "Part 2 (Long Turn):", "Part 3 (Discussion):" labels. Keep these labels in BOTH original and improved fields exactly as they appear.
3. List the most important changes.

RULES:
- Keep Part labels (Part 1, Part 2, Part 3) in both original and improved text on separate lines
- Upgrade vocabulary, linking, collocations, grammar
- Category must be one of: vocabulary, grammar, linking, collocation, structure
- Max 7 changes, 3-4 tips

Respond ONLY with this JSON:
{
  "original_band": %.1f,
  "target_band": %.1f,
  "original": "Part 1 (Interview):\n...\n\nPart 2 (Long Turn):\n...\n\nPart 3 (Discussion):\n...",
  "improved": "Part 1 (Interview):\n...\n\nPart 2 (Long Turn):\n...\n\nPart 3 (Discussion):\n...",
  "changes": [
    {"original": "I think", "improved": "I firmly believe that", "reason": "Stronger opinion marker for Band 7+", "category": "vocabulary"},
    {"original": "very important", "improved": "plays a crucial role", "reason": "Collocation instead of basic adjective", "category": "collocation"},
    {"original": "Also", "improved": "Furthermore", "reason": "Advanced discourse marker", "category": "linking"}
  ],
  "tips": ["tip1", "tip2"]
}`, currentBand, targetBand, transcript, targetBand, currentBand, targetBand)

	reqBody, _ := json.Marshal(map[string]interface{}{
		"model": config.App.GroqLLMModel,
		"messages": []map[string]string{
			{"role": "system", "content": "IELTS coach. Improve speech to target band. JSON only."},
			{"role": "user", "content": prompt},
		},
		"temperature":     0.4,
		"max_tokens":      4096,
		"response_format": map[string]string{"type": "json_object"},
	})

	var raw string
	err := TryWithKeys("llm", func(apiKey string) error {
		s, err := sendLLMRaw(reqBody, apiKey)
		if err != nil {
			return err
		}
		raw = s
		return nil
	})
	if err != nil {
		return nil, err
	}

	var result ImprovedSpeech
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("JSON parse error: %w", err)
	}

	result.OriginalBand = currentBand
	result.TargetBand = targetBand
	result.Original = transcript

	return &result, nil
}

// sendLLMRaw sends request and returns raw content string (not parsed as IELTSAnalysis).
func sendLLMRaw(reqBody []byte, apiKey string) (string, error) {
	req, err := http.NewRequest("POST", groqAPIBase+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("LLM error %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", err
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("LLM no response")
	}

	return chatResp.Choices[0].Message.Content, nil
}
