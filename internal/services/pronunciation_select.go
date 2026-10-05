package services

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/games"
	"github.com/speak-up/backend/internal/models"
)

// How far ahead a word the learner got wrong is scheduled to come back. Spacing
// out repetition of the specific sounds someone struggles with is the one
// intervention the literature reports a large effect for, so difficult words
// are deliberately brought back while passages never repeat.
var pronReviewGaps = []time.Duration{
	3 * 24 * time.Hour,
	7 * 24 * time.Hour,
	14 * 24 * time.Hour,
	30 * 24 * time.Hour,
}

// NextPassage picks the read-aloud passage to give this learner.
//
// Selection has two jobs. It must never hand back a passage this user has
// already read - that is what makes the exercise feel endless - and among the
// unread ones it prefers passages that drill the sounds this user actually gets
// wrong, which is where the learning happens.
//
// sentenceCount is the learner's own choice of length (1-4). Four sentences is
// roughly twenty seconds of speech, which keeps the whole passage inside the
// window where phoneme-level scoring stays reliable.
func NextPassage(userID uuid.UUID, topic string, sentenceCount int, level string) (*models.PronunciationPassage, error) {
	if sentenceCount < 1 || sentenceCount > 4 {
		return nil, fmt.Errorf("sentenceCount must be 1-4, got %d", sentenceCount)
	}

	due := dueSounds(userID)

	// 1. Exactly what was asked for, unread.
	if p := pickUnseen(userID, topic, sentenceCount, level, due); p != nil {
		markSeen(userID, p.ID)
		return p, nil
	}
	// 2. Same length and level, any topic. Better to bend the topic than to
	//    repeat a passage or change the difficulty under the learner.
	if p := pickUnseen(userID, "", sentenceCount, level, due); p != nil {
		markSeen(userID, p.ID)
		return p, nil
	}
	// 3. Everything at this length and level has been read. Re-serve the one
	//    read longest ago rather than failing the request.
	var p models.PronunciationPassage
	err := database.DB.
		Joins("JOIN user_passage_seen s ON s.passage_id = pronunciation_passages.id AND s.user_id = ?", userID).
		Where("pronunciation_passages.sentence_count = ? AND pronunciation_passages.level = ?", sentenceCount, level).
		Order("s.seen_at ASC").
		First(&p).Error
	if err != nil {
		return nil, fmt.Errorf("no passage available for level %s, %d sentence(s): %w",
			level, sentenceCount, err)
	}
	markSeen(userID, p.ID)
	return &p, nil
}

// pickUnseen returns an unread passage from the bucket, preferring ones that
// exercise the learner's due sounds. topic may be empty to mean "any topic".
//
// Candidates are scored in Go rather than SQL: a bucket holds a couple of dozen
// rows, so fetching them is cheap, and the overlap rule stays readable instead
// of turning into a pile of CASE expressions.
func pickUnseen(userID uuid.UUID, topic string, sentenceCount int, level string, due map[string]struct{}) *models.PronunciationPassage {
	q := database.DB.
		Where("sentence_count = ? AND level = ?", sentenceCount, level).
		Where("id NOT IN (SELECT passage_id FROM user_passage_seen WHERE user_id = ?)", userID)
	if topic != "" {
		q = q.Where("topic = ?", topic)
	}

	var cands []models.PronunciationPassage
	if err := q.Limit(60).Find(&cands).Error; err != nil || len(cands) == 0 {
		return nil
	}

	bestScore := -1
	var tied []int
	for i, c := range cands {
		score := 0
		for _, s := range strings.Split(c.TargetSounds, ",") {
			if s == "" {
				continue
			}
			if _, want := due[s]; want {
				score++
			}
		}
		// Prefer a rendered reference audio: without it the learner cannot hear
		// how the passage should sound, which is half the exercise.
		if c.AudioPath != "" {
			score++
		}

		switch {
		case score > bestScore:
			bestScore, tied = score, []int{i}
		case score == bestScore:
			tied = append(tied, i)
		}
	}
	// Among equally good candidates, vary which one comes back.
	return &cands[tied[rand.Intn(len(tied))]]
}

// dueSounds returns the target sounds this learner is due to practise: ones
// they have got wrong and whose review date has arrived.
func dueSounds(userID uuid.UUID) map[string]struct{} {
	var rows []models.UserPronunciationWord
	database.DB.
		Where("user_id = ? AND errors > 0 AND sound <> ''", userID).
		Where("next_due_at IS NULL OR next_due_at <= ?", time.Now()).
		Find(&rows)

	out := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		out[r.Sound] = struct{}{}
	}
	return out
}

func markSeen(userID, passageID uuid.UUID) {
	database.DB.Exec(
		`INSERT INTO user_passage_seen (user_id, passage_id, seen_at)
		 VALUES (?, ?, NOW())
		 ON CONFLICT (user_id, passage_id) DO UPDATE SET seen_at = NOW()`,
		userID, passageID)
}

// WordResult is one word's outcome from a graded attempt.
type WordResult struct {
	Word string
	OK   bool // true when the word was pronounced acceptably
}

// RecordWordResults folds one attempt's per-word outcomes into the learner's
// history and schedules the words they got wrong to come back.
//
// A word pronounced correctly moves further out; a word got wrong comes back
// soon, at the first interval, because the point of the schedule is repetition
// where it is needed rather than an even drip of everything.
func RecordWordResults(userID uuid.UUID, results []WordResult) {
	now := time.Now()
	for _, r := range results {
		word := strings.ToLower(strings.TrimSpace(r.Word))
		if word == "" {
			continue
		}

		// Attribute the word to a target sound when it has exactly one, so the
		// selector can ask for "passages with /θ/" later. Words carrying
		// several sounds are left unattributed rather than guessed at.
		sound := ""
		if e, ok := games.PronLookup(word); ok && len(e.Sounds) == 1 {
			sound = e.Sounds[0]
		}

		// Read the current state, then write absolute values. The next review
		// date depends on the running attempt and error counts, so it cannot be
		// expressed as an increment inside the upsert.
		var rec models.UserPronunciationWord
		if err := database.DB.Where("user_id = ? AND word = ?", userID, word).
			First(&rec).Error; err != nil {
			rec = models.UserPronunciationWord{UserID: userID, Word: word}
		}
		rec.Attempts++
		if !r.OK {
			rec.Errors++
		}
		if rec.Sound == "" {
			rec.Sound = sound
		}

		var nextDue interface{} // NULL when there is nothing to review
		switch {
		case !r.OK:
			// Wrong: bring it back at the shortest interval.
			nextDue = now.Add(pronReviewGaps[0])
		case rec.Errors == 0:
			// Never missed: nothing to review.
			nextDue = nil
		default:
			// Correct again: push the next review one step further out.
			step := rec.Attempts - rec.Errors
			if step >= len(pronReviewGaps) {
				step = len(pronReviewGaps) - 1
			}
			nextDue = now.Add(pronReviewGaps[step])
		}

		// An explicit upsert, not gorm.Save: with a composite primary key Save
		// issues an UPDATE, which silently affects no rows the first time a
		// learner meets a word and would lose the result.
		database.DB.Exec(
			`INSERT INTO user_pronunciation_words
			     (user_id, word, sound, attempts, errors, last_seen_at, next_due_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (user_id, word) DO UPDATE SET
			     sound        = EXCLUDED.sound,
			     attempts     = EXCLUDED.attempts,
			     errors       = EXCLUDED.errors,
			     last_seen_at = EXCLUDED.last_seen_at,
			     next_due_at  = EXCLUDED.next_due_at`,
			userID, word, rec.Sound, rec.Attempts, rec.Errors, now, nextDue)
	}
}
