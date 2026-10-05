package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
)

const groqAPIBase = "https://api.groq.com/openai/v1"

type WhisperResponse struct {
	Text     string           `json:"text"`
	Segments []WhisperSegment `json:"segments"`
	Duration float64          `json:"duration"`
}

type WhisperSegment struct {
	Start      float64 `json:"start"`
	End        float64 `json:"end"`
	Text       string  `json:"text"`
	AvgLogprob float64 `json:"avg_logprob"`
}

type IELTSAnalysis struct {
	FluencyScore          float64  `json:"fluency_score"`
	FluencyFeedback       string   `json:"fluency_feedback"`
	LexicalScore          float64  `json:"lexical_score"`
	LexicalFeedback       string   `json:"lexical_feedback"`
	GrammarScore          float64  `json:"grammar_score"`
	GrammarFeedback       string   `json:"grammar_feedback"`
	PronunciationScore    float64  `json:"pronunciation_score"`
	PronunciationFeedback string   `json:"pronunciation_feedback"`
	OverallBand           float64  `json:"overall_band"`
	GrammarErrors         []string `json:"grammar_errors"`
	Suggestions           []string `json:"suggestions"`
}

// AnalyzeSpeaking - STANDALONE AI speaking test.
// User o'zi gapiradi, AI tekshiradi. Session bilan bog'liq emas.
func AnalyzeSpeaking(userID uuid.UUID, audioData []byte, audioFormat string, topic string) (*models.SpeakingReport, error) {
	// Step 1: Whisper STT - TryWithKeys auto-rotates through every usable
	// key in priority order so the user never sees a rate-limit error
	// while we still have backup keys to try.
	log.Info().Str("user_id", userID.String()).Msg("AI: Whisper transcription")
	var whisperResp *WhisperResponse
	err := TryWithKeys("whisper", func(apiKey string) error {
		wr, err := transcribeWithKey(audioData, audioFormat, apiKey)
		if err != nil {
			return err
		}
		whisperResp = wr
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("whisper: %w", err)
	}

	if strings.TrimSpace(whisperResp.Text) == "" {
		return nil, fmt.Errorf("Ovoz aniqlanmadi - iltimos qayta gapiring")
	}

	// Metrics
	wordCount := len(strings.Fields(whisperResp.Text))
	durationSec := int(whisperResp.Duration)
	wpm := 0.0
	if durationSec > 0 {
		wpm = float64(wordCount) / (float64(durationSec) / 60.0)
	}

	pauseCount := 0
	for i := 1; i < len(whisperResp.Segments); i++ {
		gap := whisperResp.Segments[i].Start - whisperResp.Segments[i-1].End
		if gap > 2.0 {
			pauseCount++
		}
	}

	avgConfidence := 0.0
	if len(whisperResp.Segments) > 0 {
		totalConf := 0.0
		for _, seg := range whisperResp.Segments {
			totalConf += math.Exp(seg.AvgLogprob)
		}
		avgConfidence = totalConf / float64(len(whisperResp.Segments))
	}

	// Step 2: LLama IELTS analysis - same auto-rotation across LLM keys.
	log.Info().Str("user_id", userID.String()).Int("words", wordCount).Msg("AI: IELTS analysis")
	var analysis *IELTSAnalysis
	err = TryWithKeys("llm", func(apiKey string) error {
		a, err := analyzeIELTSWithKey(whisperResp.Text, topic, wordCount, durationSec, pauseCount, wpm, avgConfidence, apiKey)
		if err != nil {
			return err
		}
		analysis = a
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("IELTS analysis: %w", err)
	}

	grammarErrorsJSON, _ := json.Marshal(analysis.GrammarErrors)
	suggestionsJSON, _ := json.Marshal(analysis.Suggestions)

	report := &models.SpeakingReport{
		UserID:                userID,
		Topic:                 topic,
		Transcript:            whisperResp.Text,
		WordCount:             wordCount,
		DurationSec:           durationSec,
		FluencyScore:          analysis.FluencyScore,
		LexicalScore:          analysis.LexicalScore,
		GrammarScore:          analysis.GrammarScore,
		PronunciationScore:    analysis.PronunciationScore,
		OverallBand:           analysis.OverallBand,
		FluencyFeedback:       analysis.FluencyFeedback,
		LexicalFeedback:       analysis.LexicalFeedback,
		GrammarFeedback:       analysis.GrammarFeedback,
		PronunciationFeedback: analysis.PronunciationFeedback,
		GrammarErrors:         string(grammarErrorsJSON),
		Suggestions:           string(suggestionsJSON),
		WordsPerMinute:        wpm,
		PauseCount:            pauseCount,
		AvgConfidence:         avgConfidence,
	}

	if err := database.DB.Create(report).Error; err != nil {
		return nil, fmt.Errorf("failed to save report: %w", err)
	}

	log.Info().Str("user_id", userID.String()).Float64("band", analysis.OverallBand).Msg("AI: Done")
	return report, nil
}

func transcribeWithKey(audioData []byte, format string, apiKey string) (*WhisperResponse, error) {
	const maxWhisperBytes = 24 * 1024 * 1024 // Groq ~25MB; 413 oldini olish uchun yubormasdan tekshiramiz
	if len(audioData) > maxWhisperBytes {
		return nil, fmt.Errorf("request too large: audio %d MB (max 24MB) - iltimos qisqaroq yozing", len(audioData)/(1024*1024))
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", "audio."+format)
	if err != nil {
		return nil, err
	}
	part.Write(audioData)

	writer.WriteField("model", config.App.GroqWhisperModel)
	writer.WriteField("response_format", "verbose_json")
	writer.WriteField("language", "en")
	writer.WriteField("temperature", "0")
	writer.Close()

	req, err := http.NewRequest("POST", groqAPIBase+"/audio/transcriptions", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// The upload dominates this request: Groq transcribes in seconds,
	// but a two-minute recording leaving a phone on a weak mobile uplink
	// does not. Every timeout in the key ledger is this, not Groq being
	// slow - so give the upload room instead of failing the user and
	// then re-uploading the same audio against the next key.
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("whisper error %d: %s", resp.StatusCode, string(respBody))
	}

	var result WhisperResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func analyzeIELTSWithKey(transcript, topic string, wordCount, durationSec, pauseCount int, wpm, confidence float64, apiKey string) (*IELTSAnalysis, error) {
	topicContext := ""
	if topic != "" {
		topicContext = fmt.Sprintf("\n- Topic/Question: \"%s\"", topic)
	}

	prompt := fmt.Sprintf(`You are a certified IELTS Speaking examiner with 15 years experience. Score using EXACT band descriptors from real exams below. Be strict - do NOT inflate.

SPEECH: "%s"%s
Stats: %d words | %ds | %.1f WPM | %d pauses>2s | %.2f confidence

=== BAND DESCRIPTORS (from real IELTS exam assessments) ===

FLUENCY & COHERENCE:
Band 5: Flow breaks when elaborating. Only basic connectives (and/but/so). Self-correction disrupts coherence. Example: "uh I would like uh to uh watching TV" - hesitation searching for language.
Band 5.5: Maintains flow on familiar topics, struggles on unfamiliar. Should paraphrase questions before answering but doesn't. Needs boosters ("that's a good question but I could say that") but can't use them.
Band 6: Flow at length on ANY topic. Judicious fillers/boosters to buy time. Connectives: however/although/in the meantime. Risk: overdose of fillers marks down. Example: uses "I don't know" as filler repeatedly.
Band 6.5: Speaks at length easily. Hesitation for ideas, not language. Develops topics but doesn't explore opposing views.
Band 7: Easy to follow. Paraphrases questions naturally. Explores topics from multiple angles. "I think X, on the other hand, some people believe Y."
Band 7.5: Develops answers with explanations AND examples naturally. Uses discourse markers: "also, either, basically, at some point, on the other hand, unless, however." Content-related hesitations only.
Band 8: Effortless. Picks real examples (Messi vs Ronaldo comparison). Steers conversation toward comfortable topics. Part 3: compares both sides with depth. Minor: may not develop ALL Part 3 answers equally.
Band 8.5: Expresses thoughts clearly and effortlessly. Minor self-repetitions don't impede message. Develops ALL parts fully.
Band 9: Native-like. "you know", "I mean" - natural fillers. Explores both sides spontaneously. Never searches for language - only ideas. Answers Part 3 by considering "what would someone who disagrees think?"

LEXICAL:
Band 5: "if you ask me" repeated 3+ times. "illustrate" instead of "show" - wrong register. "not my cup of tea" attempted but limited range. "wear perfumes" - good collocation found.
Band 5.5: "connected to owners" → should be "attached." "work of muscles" → struggles, finally arrives at "physical activity." Wrong word, eventually self-corrects.
Band 6: "not my dream house", "it's not my thing", "soothing experience", "calming experience", "if my memory serves me right" - idiomatic. "includes trees" → "has trees" - inappropriate but understood. "proficient" → "professionals." "one of the most advantages" → "one of the best advantages."
Band 6.5: Topic-specific: "revenue generation", "hospitality", "cultural heritage", "budget." Paraphrases questions. No idiomatic mastery but solid range.
Band 7: "keen on", "piece of cake", "close-knit" - strong idioms. Overuses "actually." Colloquial AND idiomatic. Warned about word repetition.
Band 7.5: "spacious", "something comes to my mind", "take out of comfort zone", "worked out", "personal growth" - less common vocabulary used naturally.
Band 8: "done with my homework" (figurative), "break the ice", "polish it" (figurative). Vocabulary wide-ranging but occasional inaccuracies. Not quite Band 9 precision.
Band 8.5: "tactile experience", "diverse taste in literature", "profound insight", "immerse myself", "mobilize support", "instilling values" - precise, academic, natural.
Band 9: Native speaker level. Topic-specific vocabulary for ANY topic (art/animals/books/photography). Every word precisely chosen. Idiomatic + colloquial + academic - all natural.

GRAMMAR:
Band 5: "she enjoy" → "enjoys." "read" tense confusion. "learn perfect" → "perfectly." "our environment isn't is dangerous" - broken structure. No conditionals/subordinate clauses. "I am agree" → "I agree."
Band 5.5: "it will helps" → "will help." "other people needs" → "need." "under the world" → "in the world." "it damage" → "it damages." "I'm not agreed" → "I don't agree." MINOR but FREQUENT = stays at 5.5.
Band 6: Complex structures attempted: "as we see, global warming is getting..." "people have funds" → "having fun." "who don't want" → "who doesn't want." Few errors, mostly subtle.
Band 6.5: Range of tenses. Errors infrequent, don't impede communication. Can construct conditional sentences.
Band 7: "as far as..." used correctly. Subject-verb agreement mostly correct. Some inconsistencies remain. Schwa insertion errors for certain L1 backgrounds.
Band 7.5: Present perfect + present perfect continuous correct. "I've been living there for five years." Largely error-free. Complex structures natural, not forced.
Band 8: Small errors infrequent, never serious. Not systematic - just occasional slips. "arised" → "arose." Could improve but minimal.
Band 8.5: Error-free with both simple and complex structures. Subordinate and conditional clauses used beautifully. Subject-verb agreement consistent throughout.
Band 9: Massive range. Tiny slips ("in holiday" → "on holiday") same as native speakers - does NOT reduce score. Can discuss ANY topic grammatically.

PRONUNCIATION:
Band 5: Understandable. No major issues. Basic clarity.
Band 6: Clear. "apartment" not fully articulated. "can/can't" distinction unclear. Stress words not always clear. Minor L1 interference.
Band 7: Good clarity. Schwa errors possible (L1 specific: "estress"/"eschool"). Individual mispronunciations: "dreaming" → "dreamy."
Band 7.5: Natural speed - not too fast, not too slow. Good intonation. Linked sounds clear. Everything easy to understand.
Band 8: Very clear but may speak at low volume/monotone. Voice projection matters - too quiet = not Band 9. Intonation effective but not as dynamic as native.
Band 8.5: Great articulation. Stress patterns and intonation convey meaning naturally.
Band 9: Every syllable crystal clear. Natural intonation conveying meaning. Connected speech perfect. Not about accent - about clarity. 100%% understandable.

CRITICAL SCORING RULES:
- "if you ask me" / "to be honest" 3+ times → lexical -0.5
- Grammar errors MINOR but FREQUENT (5+) → grammar stays 5-5.5
- Native-like tiny slips at Band 8+ → do NOT penalize
- Complex structures WITH errors = 6-6.5 | WITHOUT errors = 7+
- Searching for LANGUAGE → fluency down | Searching for IDEAS → no penalty
- Lists without explanation in Part 3 = fluency down (should pick 1-2 and develop)
- Paraphrasing questions before answering = fluency +0.5
- Showing opposing views = fluency/coherence +0.5
- Using real examples (Messi/Ronaldo style) = strong development
- Overall = average of 4, rounded to 0.5
- LOW confidence (<0.7) from speech recognition = pronunciation issues likely

JSON ONLY:
{"fluency_score":0,"fluency_feedback":"","lexical_score":0,"lexical_feedback":"","grammar_score":0,"grammar_feedback":"","pronunciation_score":0,"pronunciation_feedback":"","overall_band":0,"grammar_errors":["error -> correction"],"suggestions":["tip"]}`, transcript, topicContext, wordCount, durationSec, wpm, pauseCount, confidence)

	reqBody, _ := json.Marshal(map[string]interface{}{
		"model": config.App.GroqLLMModel,
		"messages": []map[string]string{
			{"role": "system", "content": "IELTS examiner. JSON only."},
			{"role": "user", "content": prompt},
		},
		"temperature":     0.3,
		"max_tokens":      2000,
		"response_format": map[string]string{"type": "json_object"},
	})

	return sendLLMRequest(reqBody, apiKey)
}

// coerceAnalysisJSON: LLM ba'zan kutilgan string maydonni massiv (yoki aksincha)
// qaytaradi -> "cannot unmarshal array into Go struct field". Massiv bo'lib kelgan
// string-maydonlarni birlashtiramiz, string bo'lib kelgan massiv-maydonlarni o'raymiz,
// score massiv bo'lsa birinchisini olamiz. Shunda parsing yiqilmaydi.
func coerceAnalysisJSON(content string) string {
	var raw map[string]interface{}
	if json.Unmarshal([]byte(content), &raw) != nil {
		return content
	}
	for _, k := range []string{"fluency_feedback", "lexical_feedback", "grammar_feedback", "pronunciation_feedback"} {
		if arr, ok := raw[k].([]interface{}); ok {
			parts := make([]string, 0, len(arr))
			for _, e := range arr {
				parts = append(parts, fmt.Sprintf("%v", e))
			}
			raw[k] = strings.Join(parts, ". ")
		}
	}
	for _, k := range []string{"fluency_score", "lexical_score", "grammar_score", "pronunciation_score", "overall_band"} {
		if arr, ok := raw[k].([]interface{}); ok && len(arr) > 0 {
			raw[k] = arr[0]
		}
	}
	// grammar_errors / suggestions are []string. Three shapes arrive in
	// practice and all three have to survive:
	//
	//   "a"                      → ["a"]
	//   ["a", "b"]               → unchanged
	//   [["a","b"], {"x":1}]     → ["a b", "map[x:1]"]
	//
	// The third one is what still broke after the first fix: Go reports a
	// non-string ELEMENT of a []string as "cannot unmarshal array into
	// Go struct field IELTSAnalysis.grammar_errors of type string", where
	// "of type string" is the element type, not the field. Flattening
	// every element to a string makes the shape irrelevant.
	for _, k := range []string{"grammar_errors", "suggestions"} {
		switch v := raw[k].(type) {
		case string:
			raw[k] = []interface{}{v}
		case []interface{}:
			flat := make([]interface{}, 0, len(v))
			for _, e := range v {
				switch item := e.(type) {
				case string:
					flat = append(flat, item)
				case []interface{}:
					// Nested list - join it into one line.
					parts := make([]string, 0, len(item))
					for _, sub := range item {
						parts = append(parts, fmt.Sprintf("%v", sub))
					}
					flat = append(flat, strings.Join(parts, " "))
				default:
					flat = append(flat, fmt.Sprintf("%v", item))
				}
			}
			raw[k] = flat
		}
	}
	if fixed, err := json.Marshal(raw); err == nil {
		return string(fixed)
	}
	return content
}

// sendLLMRequest sends a pre-built request body to Groq LLM and parses IELTS analysis.
func sendLLMRequest(reqBody []byte, apiKey string) (*IELTSAnalysis, error) {
	req, err := http.NewRequest("POST", groqAPIBase+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("LLM error %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, err
	}
	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("LLM returned no response")
	}

	var analysis IELTSAnalysis
	jsonContent := coerceAnalysisJSON(chatResp.Choices[0].Message.Content)
	if err := json.Unmarshal([]byte(jsonContent), &analysis); err != nil {
		return nil, fmt.Errorf("LLM JSON error: %w", err)
	}

	analysis.FluencyScore = roundToHalf(clampScore(analysis.FluencyScore))
	analysis.LexicalScore = roundToHalf(clampScore(analysis.LexicalScore))
	analysis.GrammarScore = roundToHalf(clampScore(analysis.GrammarScore))
	analysis.PronunciationScore = roundToHalf(clampScore(analysis.PronunciationScore))
	analysis.OverallBand = roundToHalf(
		(analysis.FluencyScore + analysis.LexicalScore + analysis.GrammarScore + analysis.PronunciationScore) / 4,
	)

	return &analysis, nil
}

func clampScore(s float64) float64 {
	if s < 1.0 {
		return 1.0
	}
	if s > 9.0 {
		return 9.0
	}
	return s
}

func roundToHalf(s float64) float64 {
	return math.Round(s*2) / 2
}

// GetUserReports returns all AI reports for a user.
func GetUserReports(userID uuid.UUID, limit int) ([]models.SpeakingReport, error) {
	var reports []models.SpeakingReport
	err := database.DB.Where("user_id = ?", userID).Order("created_at DESC").Limit(limit).Find(&reports).Error
	return reports, err
}

// GetReportByID returns a single report (only if owned by user).
func GetReportByID(reportID, userID uuid.UUID) (*models.SpeakingReport, error) {
	var report models.SpeakingReport
	err := database.DB.Where("id = ? AND user_id = ?", reportID, userID).First(&report).Error
	if err != nil {
		return nil, err
	}
	return &report, nil
}

// ============ WEEKLY USAGE LIMITS ============

const (
	freeWeeklyLimit    = 1 // free users: 1 AI test per week
	premiumWeeklyLimit = 2 // premium users: 2 AI tests per week
)

// WeeklyLimitFor returns the weekly AI check limit for a user.
func WeeklyLimitFor(isPremium bool) int {
	if isPremium {
		return premiumWeeklyLimit
	}
	return freeWeeklyLimit
}

// CountWeeklyAISpeakingUses counts AI tests (simple + full) in the last 7 days.
func CountWeeklyAISpeakingUses(userID uuid.UUID) (int, *time.Time, error) {
	weekAgo := time.Now().AddDate(0, 0, -7)

	var simpleCount int64
	database.DB.Model(&models.SpeakingReport{}).
		Where("user_id = ? AND created_at >= ?", userID, weekAgo).
		Count(&simpleCount)

	var fullCount int64
	database.DB.Model(&models.FullTestReport{}).
		Where("user_id = ? AND created_at >= ?", userID, weekAgo).
		Count(&fullCount)

	// Find oldest report in the window (for resets_at)
	var oldest *time.Time
	var row struct{ CreatedAt *time.Time }
	database.DB.Raw(`
		SELECT MIN(created_at) as created_at FROM (
			SELECT created_at FROM speaking_reports WHERE user_id = ? AND created_at >= ?
			UNION ALL
			SELECT created_at FROM full_test_reports WHERE user_id = ? AND created_at >= ?
		) combined
	`, userID, weekAgo, userID, weekAgo).Scan(&row)
	if row.CreatedAt != nil {
		t := row.CreatedAt.AddDate(0, 0, 7)
		oldest = &t
	}

	return int(simpleCount + fullCount), oldest, nil
}

// GetWeeklyAISpeakingUsage returns usage info for the frontend.
func GetWeeklyAISpeakingUsage(userID uuid.UUID, isPremium bool) (map[string]interface{}, error) {
	used, resetsAt, err := CountWeeklyAISpeakingUses(userID)
	if err != nil {
		return nil, err
	}
	bonus := SumWeeklyLeaderboardAIBonus(userID.String())
	limit := WeeklyLimitFor(isPremium) + bonus
	result := map[string]interface{}{
		"used":              used,
		"limit":             limit,
		"remaining":         max(0, limit-used),
		"is_premium":        isPremium,
		"leaderboard_bonus": bonus,
	}
	if resetsAt != nil {
		result["resets_at"] = resetsAt.Format(time.RFC3339)
	}
	return result, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Topics are in ai_topics.go
