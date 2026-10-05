package handlers

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/utils"
)

// GameLeaderboardEntry is one row of the in-session mini-game ranking.
type GameLeaderboardEntry struct {
	Rank               int      `json:"rank"`
	UserID             string   `json:"user_id"`
	Name               string   `json:"name"`
	PhotoURL           *string  `json:"photo_url"`
	Level              *string  `json:"level"`
	GamesPlayed        int      `json:"games_played"`
	GamesWon           int      `json:"games_won"`
	WinRate            float64  `json:"win_rate"` // 0..100
	GameXP             int      `json:"game_xp"`
	IsPremium          bool     `json:"is_premium"`
	IsCurrentUser      bool     `json:"is_current_user"`
	PinnedSpeakingBand *float64 `json:"pinned_speaking_band"`
}

// gameLeaderboardMaxScan caps how many rows we ever rank. The board only
// ever shows the top slice; anything past this is noise.
const gameLeaderboardMaxScan = 500

func winRate(won, played int) float64 {
	if played == 0 {
		return 0
	}
	return float64(won) / float64(played) * 100
}

func toGameEntry(u models.User, rank int, meID string) GameLeaderboardEntry {
	return GameLeaderboardEntry{
		Rank:               rank,
		UserID:             u.ID.String(),
		Name:               u.DisplayName(),
		PhotoURL:           u.PhotoURL,
		Level:              u.Level,
		GamesPlayed:        u.GamesPlayed,
		GamesWon:           u.GamesWon,
		WinRate:            winRate(u.GamesWon, u.GamesPlayed),
		GameXP:             u.GameXP,
		IsPremium:          u.IsPremium,
		IsCurrentUser:      u.ID.String() == meID,
		PinnedSpeakingBand: u.PinnedSpeakingBand,
	}
}

// GameLeaderboard handles GET /leaderboard/games?limit=50
//
// Ranks players by lifetime game XP (tie-break: wins, then games
// played). If the current user isn't in the returned slice, their own
// rank is appended so they always see where they stand.
func GameLeaderboard(c *fiber.Ctx) error {
	currentUser := middleware.GetCurrentUser(c)

	limit := c.QueryInt("limit", 50)
	if limit > 100 {
		limit = 100
	}
	if limit < 1 {
		limit = 50
	}

	ctx, cancel := context.WithTimeout(c.UserContext(), 5*time.Second)
	defer cancel()
	db := database.DB.WithContext(ctx)

	meID := ""
	if currentUser != nil {
		meID = currentUser.ID.String()
	}

	var users []models.User
	if err := db.
		Where("is_banned = ? AND is_active = ? AND games_played > 0", false, true).
		Order("game_xp DESC, games_won DESC, games_played DESC").
		Limit(min(limit, gameLeaderboardMaxScan)).
		Find(&users).Error; err != nil {
		return utils.Error(c, fiber.StatusInternalServerError, "Failed to load leaderboard")
	}

	entries := make([]GameLeaderboardEntry, 0, len(users))
	inList := false
	for i, u := range users {
		e := toGameEntry(u, i+1, meID)
		if e.IsCurrentUser {
			inList = true
		}
		entries = append(entries, e)
	}

	// If the current user has played but isn't in the top slice, compute
	// their absolute rank and append it so they always see themselves.
	if currentUser != nil && !inList && currentUser.GamesPlayed > 0 {
		var ahead int64
		db.Model(&models.User{}).
			Where("is_banned = ? AND is_active = ? AND games_played > 0", false, true).
			Where("game_xp > ? OR (game_xp = ? AND games_won > ?)",
				currentUser.GameXP, currentUser.GameXP, currentUser.GamesWon).
			Count(&ahead)
		entries = append(entries, toGameEntry(*currentUser, int(ahead)+1, meID))
	}

	return utils.Success(c, entries)
}
