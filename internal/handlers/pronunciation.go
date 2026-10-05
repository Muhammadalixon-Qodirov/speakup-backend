package handlers

import (
	"io"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
)

// Read-aloud pronunciation practice: the user picks a topic and how many
// sentences they want, reads the passage aloud, and gets a per-word verdict
// back together with a reference reading.
//
// Why read-aloud and not free speech: knowing the target text lifts
// phoneme-level agreement from roughly 0.48 to 0.66 in the published figures,
// because the correct pronunciation is looked up rather than inferred from what
// the recogniser thinks it heard. Free-speech analysis stays in /ai/check.

// GetPronunciationOptions handles GET /pronunciation/options
// Everything the picker needs, so the client does not hardcode any of it.
func GetPronunciationOptions(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	level := "B1"
	if user.Level != nil && *user.Level != "" {
		level = *user.Level
	}

	return utils.Success(c, fiber.Map{
		"topics": models.PronunciationTopics,
		// 1-4 sentences is about 6 to 20 seconds of speech. The cap is not
		// arbitrary: past ~30 s the phoneme alignment drifts and the verdict
		// gets noticeably less reliable.
		"sentence_counts": []int{1, 2, 3, 4},
		"level":           level,
		"available":       services.TalaffuzEnabled(),
		"ready":           services.TalaffuzReady(),
	})
}

// GetPronunciationPassage handles GET /pronunciation/passage?topic=&sentences=&level=
func GetPronunciationPassage(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	topic := strings.ToLower(strings.TrimSpace(c.Query("topic")))
	if topic != "" && !validPronTopic(topic) {
		return utils.BadRequest(c, "Bunday mavzu yo'q")
	}

	sentences, err := strconv.Atoi(c.Query("sentences", "2"))
	if err != nil || sentences < 1 || sentences > 4 {
		return utils.BadRequest(c, "sentences 1 dan 4 gacha bo'lishi kerak")
	}

	level := strings.ToUpper(strings.TrimSpace(c.Query("level")))
	if level == "" {
		if user.Level != nil && *user.Level != "" {
			level = strings.ToUpper(*user.Level)
		} else {
			level = "B1"
		}
	}
	if !validPronLevel(level) {
		return utils.BadRequest(c, "Daraja A1..C2 bo'lishi kerak")
	}

	passage, err := services.NextPassage(user.ID, topic, sentences, level)
	if err != nil {
		// An empty pool is the expected state right after the feature ships:
		// the generator cron fills it overnight. Say so rather than 500.
		log.Warn().Err(err).Str("level", level).Int("sentences", sentences).
			Msg("pronunciation: no passage available")
		return utils.Error(c, fiber.StatusServiceUnavailable,
			"Hozircha bu daraja uchun matn tayyor emas, keyinroq urinib ko'ring")
	}

	return utils.Success(c, fiber.Map{
		"id":             passage.ID,
		"text":           passage.Text,
		"sentence_count": passage.SentenceCount,
		"word_count":     passage.WordCount,
		"level":          passage.Level,
		"topic":          passage.Topic,
		"target_sounds":  splitNonEmpty(passage.TargetSounds),
	})
}

// SubmitPronunciationAttempt handles POST /pronunciation/attempt
// Form fields: passage_id, audio (WAV, 16 kHz mono preferred).
func SubmitPronunciationAttempt(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	if !services.TalaffuzEnabled() {
		return utils.Error(c, fiber.StatusServiceUnavailable,
			"Talaffuz tekshiruvi hozir ishlamayapti")
	}

	passageID, err := uuid.Parse(c.FormValue("passage_id"))
	if err != nil {
		return utils.BadRequest(c, "passage_id noto'g'ri")
	}

	var passage models.PronunciationPassage
	if err := database.DB.First(&passage, "id = ?", passageID).Error; err != nil {
		return utils.NotFound(c, "Matn topilmadi")
	}

	file, err := c.FormFile("audio")
	if err != nil {
		return utils.BadRequest(c, "Audio fayl kerak (field: audio)")
	}
	f, err := file.Open()
	if err != nil {
		return utils.InternalError(c)
	}
	defer f.Close()

	audio, err := io.ReadAll(f)
	if err != nil {
		return utils.InternalError(c)
	}

	// The passage text comes from our own row, never from the client: it is the
	// reference the verdict is measured against, so letting the client supply
	// it would let it choose what "correct" means.
	check, err := services.CheckPronunciation(audio, passage.Text)
	if err != nil {
		log.Warn().Err(err).Str("user_id", user.ID.String()).
			Msg("pronunciation: check failed")
		return utils.Error(c, fiber.StatusUnprocessableEntity, err.Error())
	}

	// Soften verdicts the evidence does not support. Measured on human-annotated
	// recordings, the raw sidecar output called 23% of correctly pronounced
	// words errors, with no true positives at all - well past the point where
	// feedback stops helping. This runs before anything is shown or recorded,
	// so the learner's history is not poisoned either.
	softened := services.ApplyConfidence(check)

	// Fold the outcome into the learner's history. Words they got wrong come
	// back in later passages; this is the part that makes practice targeted
	// rather than random.
	services.RecordWordResults(user.ID, services.WordResultsFrom(check))

	wrong := 0
	for _, w := range check.Words {
		if w.Status == "xato" {
			wrong++
		}
	}

	return utils.Success(c, fiber.Map{
		"passage_id": passage.ID,
		"text":       passage.Text,
		"words":      check.Words,
		"phonemes":   check.Phonemes,
		"duration":   check.Duration,
		"took":       check.TookSec,
		"summary": fiber.Map{
			"total": len(check.Words),
			"wrong": wrong,
			// How many verdicts the confidence policy downgraded. Not for the
			// learner - it is here so the effect stays visible in logs and
			// admin views as the model or thresholds change.
			"softened": softened,
		},
		// The client should present this as advice. Phoneme-level precision is
		// modest even in the best published systems, so a confident "you said
		// this wrong" overstates what the model actually knows.
		"advisory": true,
	})
}

// GetPronunciationReference handles GET /pronunciation/passage/:id/reference
// Returns a WAV of the passage read aloud, slower than natural so it can be
// imitated.
func GetPronunciationReference(c *fiber.Ctx) error {
	if !services.TalaffuzEnabled() {
		return utils.Error(c, fiber.StatusServiceUnavailable,
			"Talaffuz tekshiruvi hozir ishlamayapti")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "id noto'g'ri")
	}

	var passage models.PronunciationPassage
	if err := database.DB.First(&passage, "id = ?", id).Error; err != nil {
		return utils.NotFound(c, "Matn topilmadi")
	}

	voice := c.Query("voice", "")
	speed, _ := strconv.ParseFloat(c.Query("speed", "0"), 64)

	// A pre-rendered file is the normal case: the cron renders every passage
	// once, and serving the file leaves the sidecar's two cores free for the
	// job only it can do. Redirect rather than proxy so the static handler and
	// the client's cache do the work.
	//
	// A custom voice or speed is the exception and still goes to the sidecar,
	// since the stored rendering is the default reading.
	if passage.AudioPath != "" && voice == "" && speed == 0 {
		return c.Redirect("/uploads/"+passage.AudioPath, fiber.StatusFound)
	}

	wav, err := services.RenderReference(passage.Text, voice, speed)
	if err != nil {
		log.Warn().Err(err).Str("passage_id", id.String()).
			Msg("pronunciation: reference render failed")
		return utils.Error(c, fiber.StatusBadGateway, "Ovoz tayyorlanmadi")
	}

	// Passages are immutable, so the rendering is too - let the client cache it
	// and stop asking the sidecar to redo the same synthesis.
	c.Set("Content-Type", "audio/wav")
	c.Set("Cache-Control", "public, max-age=604800, immutable")
	return c.Send(wav)
}

func validPronTopic(topic string) bool {
	for _, t := range models.PronunciationTopics {
		if t == topic {
			return true
		}
	}
	return false
}

func validPronLevel(level string) bool {
	switch level {
	case "A1", "A2", "B1", "B2", "C1", "C2":
		return true
	}
	return false
}

func splitNonEmpty(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}
