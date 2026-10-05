package handlers

import (
	"encoding/json"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
)

type ImproveRequest struct {
	ReportID   string  `json:"report_id"`
	Transcript string  `json:"transcript"`
	Band       float64 `json:"band"`
	TargetBand float64 `json:"target_band"`
}

// ImproveSpeech handles POST /ai/improve
// Takes a transcript + current band → returns improved version for target band.
// Can be called with report_id (loads transcript from DB) or with raw transcript.
func ImproveSpeech(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	var req ImproveRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Noto'g'ri so'rov")
	}

	transcript := req.Transcript
	currentBand := req.Band

	// If report_id given - load from DB (try simple report, then full test)
	if req.ReportID != "" {
		rid, err := uuid.Parse(req.ReportID)
		if err != nil {
			return utils.BadRequest(c, "Noto'g'ri report ID")
		}

		report, err := services.GetReportByID(rid, user.ID)
		if err == nil {
			transcript = report.Transcript
			currentBand = report.OverallBand
		} else {
			// Try full test report
			full, ferr := services.GetFullTestReport(rid, user.ID)
			if ferr != nil {
				return utils.NotFound(c, "Report topilmadi")
			}
			// Label each part so the LLM (and the user) can tell them apart.
			var parts []string
			if full.Part1Transcript != "" {
				parts = append(parts, "Part 1 (Interview):\n"+full.Part1Transcript)
			}
			if full.Part2Transcript != "" {
				parts = append(parts, "Part 2 (Long Turn):\n"+full.Part2Transcript)
			}
			if full.Part3Transcript != "" {
				parts = append(parts, "Part 3 (Discussion):\n"+full.Part3Transcript)
			}
			transcript = strings.Join(parts, "\n\n")
			currentBand = full.OverallBand
		}
	}

	if transcript == "" {
		return utils.BadRequest(c, "Transcript kerak")
	}
	if currentBand < 1 || currentBand > 9 {
		return utils.BadRequest(c, "Band 1-9 orasida bo'lishi kerak")
	}

	// If already at the maximum band, return a special response instead
	// of asking the LLM to "improve from 9.0 to 9.0" (which is meaningless).
	if currentBand >= 9.0 {
		return utils.Success(c, map[string]interface{}{
			"message": "Siz allaqachon eng yuqori darajadasiz! Tabriklaymiz!",
			"recommendations": []string{
				"IELTS 9.0 - maksimum ball. Bu darajani saqlab qolish uchun har kuni speaking practice qiling.",
				"Yangi mavzularda o'zingizni sinab ko'ring.",
				"Boshqalarga yordam berib, mentorlik qilishni boshlang.",
			},
			"current_band": currentBand,
			"target_band":  9.0,
			"is_max":       true,
		})
	}

	// Target band - default: current + 1.0
	targetBand := req.TargetBand
	if targetBand <= currentBand {
		targetBand = currentBand + 1.0
	}
	if targetBand > 9.0 {
		targetBand = 9.0
	}

	result, err := services.ImproveSpeech(transcript, currentBand, targetBand)
	if err != nil {
		return utils.Error(c, fiber.StatusUnprocessableEntity, err.Error())
	}

	// Save improve result to the report so the user can view it later.
	if req.ReportID != "" {
		rid, _ := uuid.Parse(req.ReportID)
		if jsonBytes, err := json.Marshal(result); err == nil {
			jsonStr := string(jsonBytes)
			// Try full test first, then simple report.
			res := database.DB.Model(&models.FullTestReport{}).
				Where("id = ? AND user_id = ?", rid, user.ID).
				Update("improve_result", jsonStr)
			if res.RowsAffected == 0 {
				database.DB.Model(&models.SpeakingReport{}).
					Where("id = ? AND user_id = ?", rid, user.ID).
					Update("improve_result", jsonStr)
			}
		}
	}

	return utils.Success(c, result)
}
