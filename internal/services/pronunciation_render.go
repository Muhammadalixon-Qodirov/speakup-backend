package services

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
)

// Passages are immutable, so their reference reading is too: rendering it once
// and storing the file keeps text-to-speech off the request path entirely. A
// learner opening an exercise then gets the audio as a static file instead of
// waiting on the sidecar, and the sidecar's CPU stays available for the thing
// only it can do - scoring recordings.
const (
	pronRenderPerRun = 60
	// Rendering is cheap next to recognition, but this runs on the same two
	// cores as live scoring, so pace it.
	pronRenderPause = 250 * time.Millisecond
)

// pronAudioSubdir sits under UploadDir, which is a Docker volume in production.
// Anything written outside it is lost on the next deploy.
const pronAudioSubdir = "pronunciation"

// RenderPendingReferences renders reference audio for passages that do not have
// it yet. Wired to a cron that runs shortly after the generator.
//
// Safe to run at any time: it only touches rows with an empty AudioPath, and a
// sidecar that is down or still loading simply means the work waits for the next
// run.
func RenderPendingReferences() {
	if !TalaffuzEnabled() {
		return
	}
	if !TalaffuzReady() {
		log.Info().Msg("pronunciation: sidecar not ready, skipping render run")
		return
	}

	dir := filepath.Join(config.App.UploadDir, pronAudioSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Error().Err(err).Str("dir", dir).Msg("pronunciation: cannot create audio dir")
		return
	}

	var passages []models.PronunciationPassage
	if err := database.DB.
		Where("audio_path = ''").
		Order("created_at ASC").
		Limit(pronRenderPerRun).
		Find(&passages).Error; err != nil {
		log.Warn().Err(err).Msg("pronunciation: render scan failed")
		return
	}
	if len(passages) == 0 {
		return
	}

	done, failed := 0, 0
	for _, p := range passages {
		wav, err := RenderReference(p.Text, "", 0)
		if err != nil {
			failed++
			log.Warn().Err(err).Str("passage_id", p.ID.String()).
				Msg("pronunciation: render failed")
			// A sidecar that has gone away will fail for every remaining row;
			// stop rather than hammering it.
			if failed >= 3 {
				break
			}
			continue
		}

		name := p.ID.String() + ".wav"
		full := filepath.Join(dir, name)
		// Write to a temporary file and rename, so a crash mid-write cannot
		// leave a truncated WAV that the client would fail to play.
		tmp := full + ".part"
		if err := os.WriteFile(tmp, wav, 0o644); err != nil {
			failed++
			log.Error().Err(err).Str("path", tmp).Msg("pronunciation: write failed")
			continue
		}
		if err := os.Rename(tmp, full); err != nil {
			failed++
			os.Remove(tmp)
			log.Error().Err(err).Str("path", full).Msg("pronunciation: rename failed")
			continue
		}

		rel := fmt.Sprintf("%s/%s", pronAudioSubdir, name)
		if err := database.DB.Model(&models.PronunciationPassage{}).
			Where("id = ?", p.ID).
			Update("audio_path", rel).Error; err != nil {
			// The file is on disk but the row does not point at it. Harmless:
			// the next run re-renders and overwrites the same path.
			log.Warn().Err(err).Str("passage_id", p.ID.String()).
				Msg("pronunciation: audio_path update failed")
			continue
		}
		done++
		time.Sleep(pronRenderPause)
	}

	log.Info().Int("rendered", done).Int("failed", failed).
		Msg("pronunciation: reference audio run finished")
}
