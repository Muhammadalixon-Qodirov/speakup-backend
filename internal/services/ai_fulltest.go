package services

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
)

// StartFullTest creates a new full test report and analyzes Part 1.
func StartFullTest(userID uuid.UUID, topicID string, audioData []byte, format string, questions []string) (*models.FullTestReport, error) {
	// Check weekly limit (admins bypass)
	var user models.User
	if err := database.DB.First(&user, "id = ?", userID).Error; err != nil {
		return nil, fmt.Errorf("user not found")
	}
	if !user.IsAdmin {
		used, _, _ := CountWeeklyAISpeakingUses(userID)
		limit := WeeklyLimitFor(user.IsPremium) + SumWeeklyLeaderboardAIBonus(userID.String())
		if used >= limit {
			return nil, fmt.Errorf("Haftalik limit tugadi. Premium oling yoki keyingi haftani kuting.")
		}
	}

	wr, err := transcribeRetry(audioData, format)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(wr.Text) == "" {
		return nil, fmt.Errorf("Ovoz aniqlanmadi")
	}

	analysis, err := analyzeRetry(1, wr.Text, questions, metrics(wr))
	if err != nil {
		return nil, err
	}

	fb, _ := json.Marshal(analysis)
	report := &models.FullTestReport{
		UserID:             userID,
		TopicID:            topicID,
		Status:             "pending_part2",
		Part1Transcript:    wr.Text,
		Part1Fluency:       analysis.FluencyScore,
		Part1Lexical:       analysis.LexicalScore,
		Part1Grammar:       analysis.GrammarScore,
		Part1Pronunciation: analysis.PronunciationScore,
		Part1Band:          analysis.OverallBand,
		Part1Feedback:      string(fb),
		TotalWords:         len(strings.Fields(wr.Text)),
		TotalDuration:      int(wr.Duration),
	}

	if err := database.DB.Create(report).Error; err != nil {
		return nil, err
	}

	log.Info().Str("test_id", report.ID.String()).Float64("p1", analysis.OverallBand).Msg("Full test: Part 1 done")
	return report, nil
}

// SubmitPart2 adds Part 2 analysis to existing test.
func SubmitPart2(testID, userID uuid.UUID, audioData []byte, format string, questions []string) (*models.FullTestReport, error) {
	var report models.FullTestReport
	if err := database.DB.Where("id = ? AND user_id = ? AND status = ?", testID, userID, "pending_part2").First(&report).Error; err != nil {
		return nil, fmt.Errorf("test topilmadi yoki Part 2 allaqachon yuborilgan")
	}

	wr, err := transcribeRetry(audioData, format)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(wr.Text) == "" {
		return nil, fmt.Errorf("Ovoz aniqlanmadi")
	}

	analysis, err := analyzeRetry(2, wr.Text, questions, metrics(wr))
	if err != nil {
		return nil, err
	}

	fb, _ := json.Marshal(analysis)
	database.DB.Model(&report).Updates(map[string]interface{}{
		"status":              "pending_part3",
		"part2_transcript":    wr.Text,
		"part2_fluency":       analysis.FluencyScore,
		"part2_lexical":       analysis.LexicalScore,
		"part2_grammar":       analysis.GrammarScore,
		"part2_pronunciation": analysis.PronunciationScore,
		"part2_band":          analysis.OverallBand,
		"part2_feedback":      string(fb),
		"total_words":         report.TotalWords + len(strings.Fields(wr.Text)),
		"total_duration":      report.TotalDuration + int(wr.Duration),
	})

	database.DB.First(&report, "id = ?", testID)
	log.Info().Str("test_id", testID.String()).Float64("p2", analysis.OverallBand).Msg("Full test: Part 2 done")
	return &report, nil
}

// SubmitPart3 adds Part 3 analysis and calculates overall.
func SubmitPart3(testID, userID uuid.UUID, audioData []byte, format string, questions []string) (*models.FullTestReport, error) {
	var report models.FullTestReport
	if err := database.DB.Where("id = ? AND user_id = ? AND status = ?", testID, userID, "pending_part3").First(&report).Error; err != nil {
		return nil, fmt.Errorf("test topilmadi yoki Part 3 allaqachon yuborilgan")
	}

	wr, err := transcribeRetry(audioData, format)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(wr.Text) == "" {
		return nil, fmt.Errorf("Ovoz aniqlanmadi")
	}

	analysis, err := analyzeRetry(3, wr.Text, questions, metrics(wr))
	if err != nil {
		return nil, err
	}

	fb, _ := json.Marshal(analysis)
	totalWords := report.TotalWords + len(strings.Fields(wr.Text))
	totalDur := report.TotalDuration + int(wr.Duration)
	wpm := 0.0
	if totalDur > 0 {
		wpm = float64(totalWords) / (float64(totalDur) / 60.0)
	}

	// Overall = weighted: Part1 20% + Part2 30% + Part3 50% (real IELTS weighting)
	overall := roundToHalf(report.Part1Band*0.2 + report.Part2Band*0.3 + analysis.OverallBand*0.5)

	// Collect all grammar errors from all parts
	var allErrors, allSuggs []string
	collectFromFeedback(report.Part1Feedback, &allErrors, &allSuggs)
	collectFromFeedback(report.Part2Feedback, &allErrors, &allSuggs)
	allErrors = append(allErrors, analysis.GrammarErrors...)
	allSuggs = append(allSuggs, analysis.Suggestions...)
	// Deduplicate
	allErrors = unique(allErrors)
	allSuggs = unique(allSuggs)

	errJSON, _ := json.Marshal(allErrors)
	suggJSON, _ := json.Marshal(allSuggs)

	database.DB.Model(&report).Updates(map[string]interface{}{
		"status":              "completed",
		"part3_transcript":    wr.Text,
		"part3_fluency":       analysis.FluencyScore,
		"part3_lexical":       analysis.LexicalScore,
		"part3_grammar":       analysis.GrammarScore,
		"part3_pronunciation": analysis.PronunciationScore,
		"part3_band":          analysis.OverallBand,
		"part3_feedback":      string(fb),
		"overall_band":        overall,
		"grammar_errors":      string(errJSON),
		"suggestions":         string(suggJSON),
		"total_words":         totalWords,
		"total_duration":      totalDur,
		"words_per_minute":    wpm,
	})

	database.DB.First(&report, "id = ?", testID)
	log.Info().Str("test_id", testID.String()).Float64("overall", overall).Msg("Full test: COMPLETED")
	return &report, nil
}

// GetFullTestReport returns a completed test.
func GetFullTestReport(testID, userID uuid.UUID) (*models.FullTestReport, error) {
	var r models.FullTestReport
	if err := database.DB.Where("id = ? AND user_id = ?", testID, userID).First(&r).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

// GetUserFullTests returns user's full test history.
func GetUserFullTests(userID uuid.UUID, limit int) ([]models.FullTestReport, error) {
	var r []models.FullTestReport
	err := database.DB.Where("user_id = ?", userID).Order("created_at DESC").Limit(limit).Find(&r).Error
	return r, err
}

// --- helpers ---

type partMetrics struct {
	WordCount  int
	Duration   int
	WPM        float64
	PauseCount int
	Confidence float64
}

func metrics(wr *WhisperResponse) partMetrics {
	wc := len(strings.Fields(wr.Text))
	dur := int(wr.Duration)
	wpm := 0.0
	if dur > 0 {
		wpm = float64(wc) / (float64(dur) / 60.0)
	}
	pc := 0
	for i := 1; i < len(wr.Segments); i++ {
		if wr.Segments[i].Start-wr.Segments[i-1].End > 2.0 {
			pc++
		}
	}
	conf := 0.0
	if len(wr.Segments) > 0 {
		t := 0.0
		for _, s := range wr.Segments {
			t += math.Exp(s.AvgLogprob)
		}
		conf = t / float64(len(wr.Segments))
	}
	return partMetrics{wc, dur, wpm, pc, conf}
}

// transcribeRetry runs Whisper transcription against every usable key
// until one succeeds. TryWithKeys handles classification + cooldown so
// the user never sees "429 try again in 6m" as long as we have more keys.
func transcribeRetry(audioData []byte, format string) (*WhisperResponse, error) {
	var out *WhisperResponse
	err := TryWithKeys("whisper", func(apiKey string) error {
		wr, err := transcribeWithKey(audioData, format, apiKey)
		if err != nil {
			return err
		}
		out = wr
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("whisper: %w", err)
	}
	return out, nil
}

// analyzeRetry runs the LLM IELTS analysis against every usable key
// until one succeeds.
func analyzeRetry(partNum int, text string, questions []string, m partMetrics) (*IELTSAnalysis, error) {
	var out *IELTSAnalysis
	err := TryWithKeys("llm", func(apiKey string) error {
		a, err := analyzePartWithKey(partNum, text, questions, m, apiKey)
		if err != nil {
			return err
		}
		out = a
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func analyzePartWithKey(part int, transcript string, questions []string, m partMetrics, apiKey string) (*IELTSAnalysis, error) {
	partName := map[int]string{1: "Part 1 (warm-up)", 2: "Part 2 (long turn)", 3: "Part 3 (discussion)"}[part]
	qList := strings.Join(questions, "\n- ")

	prompt := fmt.Sprintf(`IELTS examiner. Score %s. Questions asked:
- %s

Transcript: "%s"
Stats: %d words | %ds | %.0f WPM | %d pauses | %.2f conf

Part-specific:
%s

Score 1-9 (0.5 steps). JSON only:
{"fluency_score":0,"fluency_feedback":"","lexical_score":0,"lexical_feedback":"","grammar_score":0,"grammar_feedback":"","pronunciation_score":0,"pronunciation_feedback":"","overall_band":0,"grammar_errors":["err->fix"],"suggestions":["tip"]}`,
		partName, qList, transcript, m.WordCount, m.Duration, m.WPM, m.PauseCount, m.Confidence,
		partRules(part))

	return callLLM(prompt, apiKey)
}

func partRules(part int) string {
	switch part {
	case 1:
		return `Part1: Short answers expected (20-40s each). Check: direct answers, basic elaboration, natural pace. Don't penalize short answers - Part 1 is meant to be brief.`
	case 2:
		return `Part2: Long turn 1-2min. Check: covered all bullet prompts? Developed ideas? Coherent narrative? Penalize if under 1min or missed key prompts.`
	case 3:
		return `Part3: Discussion 30-60s per question. Check: depth of analysis, opposing views explored, examples given. This is the hardest part - score strictly.`
	}
	return ""
}

func callLLM(prompt, apiKey string) (*IELTSAnalysis, error) {
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

func collectFromFeedback(fb string, errors, suggs *[]string) {
	if fb == "" {
		return
	}
	var a IELTSAnalysis
	if json.Unmarshal([]byte(fb), &a) == nil {
		*errors = append(*errors, a.GrammarErrors...)
		*suggs = append(*suggs, a.Suggestions...)
	}
}

func unique(s []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range s {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
