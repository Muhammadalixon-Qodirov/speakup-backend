package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ScheduledSession is a speaking appointment between two friends.
//
// Only a premium user can create one - that is the whole commercial
// point of the feature. The invited partner needs nothing: they receive
// it, see it in the app and can walk into the call at the agreed time.
// A free user who wants to be the one doing the scheduling is exactly
// the user we are trying to convert.
type ScheduledSession struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	// User1 is the organiser (the premium side), User2 the invitee.
	// Kept as the original column names so existing rows survive.
	User1ID uuid.UUID `gorm:"type:uuid;not null;index" json:"user1_id"`
	User2ID uuid.UUID `gorm:"type:uuid;not null;index" json:"user2_id"`

	ScheduledAt time.Time `gorm:"not null;index" json:"scheduled_at"`

	// pending | confirmed | declined | started | cancelled | missed
	//
	// "open" is deliberately NOT a stored state: whether the join window
	// is live is a pure function of the clock, and a stored flag would
	// need a cron tick to stay truthful - which is exactly the kind of
	// drift that shows a user a dead button.
	Status string `gorm:"size:16;default:'pending';index" json:"status"`

	// What the organiser wants to practise ("IELTS Part 2", "free talk").
	Note string `gorm:"size:256" json:"note"`

	SessionID *uuid.UUID `gorm:"type:uuid" json:"session_id"`

	ConfirmedAt   *time.Time `json:"confirmed_at"`
	CancelledByID *uuid.UUID `gorm:"type:uuid" json:"cancelled_by_id"`

	// Notification bookkeeping. Both are idempotence guards for the
	// once-a-minute reminder sweep - without them a slow tick or a
	// restart would send the same "15 daqiqadan keyin" twice.
	RemindedAt      *time.Time `json:"reminded_at"`
	StartNotifiedAt *time.Time `json:"start_notified_at"`

	User1   *User    `gorm:"foreignKey:User1ID" json:"user1,omitempty"`
	User2   *User    `gorm:"foreignKey:User2ID" json:"user2,omitempty"`
	Session *Session `gorm:"foreignKey:SessionID" json:"session,omitempty"`
}

// Scheduled session statuses.
const (
	ScheduledPending   = "pending"
	ScheduledConfirmed = "confirmed"
	ScheduledDeclined  = "declined"
	ScheduledStarted   = "started"
	ScheduledCancelled = "cancelled"
	ScheduledMissed    = "missed"
)

// ScheduledJoinWindowMinutes is how long the call stays joinable once
// the agreed time arrives. Ten minutes is short on purpose: the promise
// sold to the organiser is "we will both be there at 20:00", and a
// window that stayed open for an hour would turn an appointment back
// into the ordinary queue.
const ScheduledJoinWindowMinutes = 10

// ScheduledReminderLead is how far ahead the "starting soon" nudge goes
// out - long enough to put the phone down and find somewhere quiet.
const ScheduledReminderLead = 15 * time.Minute

func (s *ScheduledSession) BeforeCreate(tx *gorm.DB) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	if s.Status == "" {
		s.Status = ScheduledPending
	}
	return nil
}

// IsLive reports whether the two sides may walk into the call right now.
func (s *ScheduledSession) IsLive(now time.Time) bool {
	if s.Status != ScheduledPending && s.Status != ScheduledConfirmed {
		return false
	}
	return !now.Before(s.ScheduledAt) && now.Before(s.WindowEnd())
}

// WindowEnd is the moment the appointment stops being joinable.
func (s *ScheduledSession) WindowEnd() time.Time {
	return s.ScheduledAt.Add(ScheduledJoinWindowMinutes * time.Minute)
}

// IsUpcoming reports whether the appointment is still in the future.
func (s *ScheduledSession) IsUpcoming(now time.Time) bool {
	if s.Status != ScheduledPending && s.Status != ScheduledConfirmed {
		return false
	}
	return now.Before(s.ScheduledAt)
}

// Partner returns the other participant's ID for a given viewer.
func (s *ScheduledSession) Partner(viewerID uuid.UUID) uuid.UUID {
	if s.User1ID == viewerID {
		return s.User2ID
	}
	return s.User1ID
}
