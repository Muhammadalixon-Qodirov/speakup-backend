package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Target sounds the Pronunciation trainer drills. These are the errors Uzbek
// and Russian L1 speakers actually make (docs/TADQIQOT.md §6), not the full
// phoneme inventory: a short, high-confidence list keeps false positives down,
// and research says feedback below ~66% accuracy hurts learning rather than
// helping it.
const (
	SoundTH          = "TH"           // /θ/ -> [s]   think -> sink
	SoundDH          = "DH"           // /ð/ -> [z]   this  -> zis
	SoundW           = "W"            // /w/ -> [v]   west  -> vest
	SoundV           = "V"            // /v/ -> [w]/[f]
	SoundAE          = "AE"           // /æ/          cat
	SoundIYIH        = "IY_IH"        // /iː/ ~ /ɪ/   sheep / ship
	SoundFinalVoiced = "FINAL_VOICED" // word-final devoicing: lab -> lap
)

// PronunciationTopic values. Deliberately a closed list rather than free text:
// passages are generated and validated ahead of time, bucketed by
// (topic, level, sentence_count). A free-text topic would fragment the pool
// into buckets nothing can pre-fill, forcing slow unvalidated generation on
// the request path. The themes double as IELTS Speaking Part 1/3 subjects.
var PronunciationTopics = []string{
	"work", "study", "family", "travel", "food",
	"technology", "sport", "environment", "hobbies", "city",
}

// PronunciationPassage is one read-aloud exercise: 1-4 cohesive sentences the
// learner reads while recording.
//
// Every word is guaranteed to be in CMUdict, which is what makes the feedback
// trustworthy: the "correct" pronunciation is looked up, never guessed by a
// grapheme-to-phoneme model. A guessed reference marks correct speech as wrong,
// and that is the single largest source of false errors in this kind of system.
//
// Phonemes are computed once, at insert time, so the request path never runs
// g2p. AudioPath points at the pre-rendered TTS reference ("how it should
// sound"), rendered once per passage instead of on every request.
type PronunciationPassage struct {
	ID   uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Text string    `gorm:"size:600;not null;uniqueIndex" json:"text"`

	SentenceCount int    `gorm:"not null;index:idx_pron_bucket" json:"sentence_count"`
	Level         string `gorm:"size:8;not null;index:idx_pron_bucket" json:"level"`
	Topic         string `gorm:"size:32;not null;index:idx_pron_bucket" json:"topic"`
	WordCount     int    `gorm:"not null" json:"word_count"`

	// Space-separated ARPAbet for the whole passage, word order preserved.
	Phonemes string `gorm:"type:text;not null" json:"phonemes"`
	// Comma-separated subset of the Sound* constants present in this passage.
	TargetSounds string `gorm:"size:128;not null;default:''" json:"target_sounds"`

	// Pre-rendered reference audio, relative to UPLOAD_DIR. Empty until the
	// TTS job has rendered it; selection prefers passages that have one.
	AudioPath string `gorm:"size:255;not null;default:''" json:"audio_path"`

	CreatedAt time.Time `json:"created_at"`
}

func (PronunciationPassage) TableName() string { return "pronunciation_passages" }

func (p *PronunciationPassage) BeforeCreate(*gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}

// UserPassageSeen records that a user has already been given a passage.
//
// This, not pool size, is what stops repetition: the same passage going to
// different users is fine and expected, so the pool only has to be large
// enough that one person does not run out. It also means the generator can
// slow down once the pool is built instead of running at full rate forever.
type UserPassageSeen struct {
	UserID    uuid.UUID `gorm:"type:uuid;primaryKey" json:"user_id"`
	PassageID uuid.UUID `gorm:"type:uuid;primaryKey" json:"passage_id"`
	SeenAt    time.Time `gorm:"not null;default:NOW()" json:"seen_at"`
}

func (UserPassageSeen) TableName() string { return "user_passage_seen" }

// UserPronunciationWord tracks one learner's record on one word.
//
// The point is the opposite of the passage table: words the learner got WRONG
// should come back, spaced out, because repeated targeted practice on
// individual sounds is the one thing the literature shows a large effect for.
// So passages are never repeated, while difficult words deliberately are.
type UserPronunciationWord struct {
	UserID uuid.UUID `gorm:"type:uuid;primaryKey" json:"user_id"`
	Word   string    `gorm:"size:64;primaryKey" json:"word"`

	// Which target sound this word was counted against, when it maps to one.
	Sound string `gorm:"size:16;not null;default:''" json:"sound"`

	Attempts int `gorm:"not null;default:0" json:"attempts"`
	Errors   int `gorm:"not null;default:0" json:"errors"`

	LastSeenAt time.Time  `gorm:"not null;default:NOW()" json:"last_seen_at"`
	NextDueAt  *time.Time `gorm:"index" json:"next_due_at"`
}

func (UserPronunciationWord) TableName() string { return "user_pronunciation_words" }
