package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Payment struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	UserID               uuid.UUID `gorm:"type:uuid;not null;index" json:"user_id"`
	Provider             string    `gorm:"size:32;not null" json:"provider"` // payme | click
	AmountTiyin          int       `gorm:"not null" json:"amount_tiyin"`
	Status               string    `gorm:"size:32;default:'pending';index" json:"status"` // pending | paid | failed | cancelled
	ProviderTransactionID *string  `gorm:"size:256" json:"provider_transaction_id"`
	Months               int       `gorm:"default:1" json:"months"`

	User User `gorm:"foreignKey:UserID" json:"-"`
}

func (p *Payment) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}
