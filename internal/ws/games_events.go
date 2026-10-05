package ws

import (
	"encoding/json"

	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/games"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/services"
)

// SendToUser pushes an event to a user's most-recent live socket (if
// any). It's the bridge the games package uses to reach clients without
// importing ws - main wires `games.Notify = ws.SendToUser`, mirroring
// the existing services.* injection pattern. Safe to call for an
// offline user: it's a no-op.
func SendToUser(userID, event string, data interface{}) {
	if H == nil {
		return
	}
	if c := H.GetClientByUserID(userID); c != nil {
		c.SendJSON(event, data)
	}
}

// displayName resolves a user's display name with a single light query.
func displayName(userID string) string {
	var u models.User
	if err := database.DB.
		Select("first_name", "last_name").
		First(&u, "id = ?", userID).Error; err == nil {
		return u.DisplayName()
	}
	return "Player"
}

type gameInviteIn struct {
	SessionID string `json:"session_id"`
	GameType  string `json:"game_type"`
}

func handleGameInvite(c *Client, data json.RawMessage) {
	var in gameInviteIn
	if err := json.Unmarshal(data, &in); err != nil {
		return
	}

	partnerID, err := services.GetPartnerID(in.SessionID, c.UserID)
	if err != nil || partnerID == "" {
		c.SendJSON("game_error", map[string]string{"code": "no_partner"})
		return
	}
	// The partner must be online to play a real-time game.
	if H.GetClientByUserID(partnerID) == nil {
		c.SendJSON("game_error", map[string]string{"code": "partner_offline"})
		return
	}

	from := games.Player{ID: c.UserID, Name: displayName(c.UserID)}
	to := games.Player{ID: partnerID, Name: displayName(partnerID)}

	if _, err := games.Default.Invite(in.SessionID, from, to, games.GameType(in.GameType)); err != nil {
		c.SendJSON("game_error", map[string]string{"code": err.Error()})
	}
}

type gameIDIn struct {
	GameID string `json:"game_id"`
}

func handleGameAccept(c *Client, data json.RawMessage) {
	var in gameIDIn
	if err := json.Unmarshal(data, &in); err != nil || in.GameID == "" {
		return
	}
	if err := games.Default.Accept(in.GameID, c.UserID); err != nil {
		c.SendJSON("game_error", map[string]string{"game_id": in.GameID, "code": err.Error()})
	}
}

func handleGameDecline(c *Client, data json.RawMessage) {
	var in gameIDIn
	if err := json.Unmarshal(data, &in); err != nil || in.GameID == "" {
		return
	}
	_ = games.Default.Decline(in.GameID, c.UserID)
}

func handleGameMove(c *Client, data json.RawMessage) {
	var in gameIDIn
	if err := json.Unmarshal(data, &in); err != nil || in.GameID == "" {
		return
	}
	// Pass the full payload through; the engine reads its own fields
	// (word/text) and ignores game_id.
	if err := games.Default.Move(in.GameID, c.UserID, data); err != nil {
		c.SendJSON("game_error", map[string]string{"game_id": in.GameID, "code": err.Error()})
	}
}

func handleGameAbort(c *Client, data json.RawMessage) {
	var in gameIDIn
	if err := json.Unmarshal(data, &in); err != nil || in.GameID == "" {
		return
	}
	games.Default.Abort(in.GameID, c.UserID)
}

// handleGameDisconnect aborts any game the user is in. Called from the
// ws disconnect path (last socket only).
func handleGameDisconnect(userID string) {
	games.Default.HandleDisconnect(userID)
}

// handleSessionGamesCleanup clears any game tied to a session when the
// speaking session ends, so a leftover game can't block future invites.
func handleSessionGamesCleanup(sessionID string) {
	games.Default.AbortSession(sessionID)
}
