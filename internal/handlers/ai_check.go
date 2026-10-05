package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
)

// AICheck handles POST /ai/check
// User o'zi gapirgan audio'ni yuboradi, AI IELTS-style tekshiradi.
// Bu 1v1 sessiya bilan bog'liq EMAS - alohida "AI Speaking Test" bo'limi.
func AICheck(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	topic := c.FormValue("topic", "")

	// Get audio file
	file, err := c.FormFile("audio")
	if err != nil {
		return utils.BadRequest(c, "Audio fayl kerak (field: audio)")
	}

	// No size checks here - the global Fiber BodyLimit (30 MB) is the
	// only ceiling, and the frontend stops the recorder at a sensible
	// duration. The Whisper service rejects anything genuinely empty.
	f, err := file.Open()
	if err != nil {
		return utils.InternalError(c)
	}
	defer f.Close()

	audioData, err := io.ReadAll(f)
	if err != nil {
		return utils.InternalError(c)
	}

	// Detect format from file content first, fall back to extension.
	// MediaRecorder on some browsers sends .mp3 extensions with WebM
	// content; relying solely on the filename causes Whisper failures.
	format := detectAudioFormat(audioData, file.Filename)

	// Run AI analysis (no session_id - standalone test)
	report, err := services.AnalyzeSpeaking(user.ID, audioData, format, topic)
	if err != nil {
		return utils.Error(c, fiber.StatusUnprocessableEntity, err.Error())
	}

	return utils.Success(c, report)
}

// GetMyAIReports handles GET /ai/reports?limit=10
func GetMyAIReports(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	limit := c.QueryInt("limit", 10)
	if limit > 50 {
		limit = 50
	}

	reports, err := services.GetUserReports(user.ID, limit)
	if err != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, reports)
}

// GetAIReport handles GET /ai/reports/:id
func GetAIReport(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	reportID := c.Params("id")

	rid, err := uuid.Parse(reportID)
	if err != nil {
		return utils.BadRequest(c, "Noto'g'ri report ID")
	}

	report, err := services.GetReportByID(rid, user.ID)
	if err != nil {
		return utils.NotFound(c, "Report topilmadi")
	}

	return utils.Success(c, report)
}

// GetAISpeakingUsage handles GET /ai/usage.
// Returns the caller's rolling 7-day AI Speaking quota snapshot so the
// frontend can render "X / Y this week" + disable the mic when empty.
func GetAISpeakingUsage(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	// Admins get unlimited
	if user.IsAdmin {
		return utils.Success(c, map[string]interface{}{
			"used": 0, "limit": 999, "remaining": 999, "is_premium": true,
		})
	}
	usage, err := services.GetWeeklyAISpeakingUsage(user.ID, user.IsPremium)
	if err != nil {
		return utils.InternalError(c)
	}
	return utils.Success(c, usage)
}

// GetAITopics handles GET /ai/topics
// Mavzular ro'yxati - savol + ideas + useful phrases bilan.
func GetAITopics(c *fiber.Ctx) error {
	topics := services.GetSpeakingTopics()
	return utils.Success(c, topics)
}

// ── Full IELTS Test helpers ──

func ftReadAudio(c *fiber.Ctx) ([]byte, string, error) {
	file, err := c.FormFile("audio")
	if err != nil {
		return nil, "", err
	}
	f, err := file.Open()
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, "", err
	}
	ext := detectAudioFormat(data, file.Filename)
	return data, ext, nil
}

// detectAudioFormat sniffs the first 512 bytes of the file to determine
// the real audio codec, then falls back to the filename extension when
// http.DetectContentType returns a generic MIME. This prevents Whisper
// failures caused by mislabelled uploads (e.g. .mp3 extension with WebM
// content from MediaRecorder).
func detectAudioFormat(data []byte, filename string) string {
	header := data
	if len(header) > 512 {
		header = header[:512]
	}
	detected := http.DetectContentType(header)

	switch {
	case strings.Contains(detected, "audio/mpeg"),
		strings.Contains(detected, "audio/mp3"):
		return "mp3"
	case strings.Contains(detected, "audio/webm"),
		strings.Contains(detected, "video/webm"):
		return "webm"
	case strings.Contains(detected, "audio/ogg"),
		strings.Contains(detected, "application/ogg"):
		return "ogg"
	case strings.Contains(detected, "audio/wav"),
		strings.Contains(detected, "audio/x-wav"):
		return "wav"
	case strings.Contains(detected, "audio/mp4"),
		strings.Contains(detected, "video/mp4"):
		return "m4a"
	}

	// Content-type detection returned something generic - fall back to
	// the filename extension (old behaviour).
	name := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(name, ".mp3"):
		return "mp3"
	case strings.HasSuffix(name, ".wav"):
		return "wav"
	case strings.HasSuffix(name, ".ogg"):
		return "ogg"
	case strings.HasSuffix(name, ".m4a"):
		return "m4a"
	}

	log.Debug().
		Str("detected", detected).
		Str("filename", filename).
		Msg("audio format: falling back to webm")
	return "webm"
}

func ftParseQuestions(c *fiber.Ctx) []string {
	raw := c.FormValue("questions", "[]")
	var qs []string
	json.Unmarshal([]byte(raw), &qs)
	return qs
}

// StartFullTest handles POST /ai/full-test/start
func StartFullTest(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	audioData, format, err := ftReadAudio(c)
	if err != nil {
		return utils.BadRequest(c, "Audio kerak")
	}
	report, err := services.StartFullTest(user.ID, c.FormValue("topic_id", ""), audioData, format, ftParseQuestions(c))
	if err != nil {
		return utils.Error(c, fiber.StatusUnprocessableEntity, err.Error())
	}
	return utils.Success(c, report)
}

// SubmitPart2 handles POST /ai/full-test/:id/part2
func SubmitPart2(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	testID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Noto'g'ri ID")
	}
	audioData, format, err := ftReadAudio(c)
	if err != nil {
		return utils.BadRequest(c, "Audio kerak")
	}
	report, err := services.SubmitPart2(testID, user.ID, audioData, format, ftParseQuestions(c))
	if err != nil {
		return utils.Error(c, fiber.StatusUnprocessableEntity, err.Error())
	}
	return utils.Success(c, report)
}

// SubmitPart3 handles POST /ai/full-test/:id/part3
func SubmitPart3(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	testID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Noto'g'ri ID")
	}
	audioData, format, err := ftReadAudio(c)
	if err != nil {
		return utils.BadRequest(c, "Audio kerak")
	}
	report, err := services.SubmitPart3(testID, user.ID, audioData, format, ftParseQuestions(c))
	if err != nil {
		return utils.Error(c, fiber.StatusUnprocessableEntity, err.Error())
	}
	return utils.Success(c, report)
}

// GetFullTest handles GET /ai/full-test/:id
func GetFullTest(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	testID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Noto'g'ri ID")
	}
	report, err := services.GetFullTestReport(testID, user.ID)
	if err != nil {
		return utils.NotFound(c, "Test topilmadi")
	}
	return utils.Success(c, report)
}

// ListFullTests handles GET /ai/full-tests?limit=10
func ListFullTests(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	limit := c.QueryInt("limit", 10)
	if limit > 50 {
		limit = 50
	}
	reports, err := services.GetUserFullTests(user.ID, limit)
	if err != nil {
		return utils.InternalError(c)
	}
	return utils.Success(c, reports)
}
