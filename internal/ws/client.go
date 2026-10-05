package ws

import (
	"encoding/json"
	"time"

	"github.com/gofiber/contrib/websocket"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/speak-up/backend/internal/safego"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 4096
)

// Client represents a single WebSocket connection.
type Client struct {
	ID     string          // unique socket ID
	UserID string          // authenticated user's UUID
	Conn   *websocket.Conn // underlying WebSocket connection
	Send   chan []byte     // outbound messages
}

// Message is the JSON format for all WebSocket communication.
// Client sends: {"event": "join_queue", "data": {...}}
// Server sends: {"event": "match_found", "data": {...}}
type Message struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// NewClient creates a client and registers it with the hub.
func NewClient(conn *websocket.Conn, userID string) *Client {
	client := &Client{
		ID:     uuid.New().String(),
		UserID: userID,
		Conn:   conn,
		Send:   make(chan []byte, 64),
	}
	H.register <- client
	return client
}

// ReadPump reads messages from the WebSocket connection.
// Runs in its own goroutine per client.
func (c *Client) ReadPump() {
	defer func() {
		H.unregister <- c
		HandleDisconnect(c)
		c.Conn.Close()
	}()

	c.Conn.SetReadLimit(maxMessageSize)
	c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, raw, err := c.Conn.ReadMessage()
		if err != nil {
			break
		}

		var msg Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Warn().Str("user_id", c.UserID).Msg("Invalid message format")
			continue
		}

		// Route the event - wrapped in safego so a panic inside any
		// single handler doesn't kill the client connection (and with
		// it, the user's active session). Without this, one malformed
		// signaling payload could disconnect a user mid-call.
		safego.Run("ws.HandleEvent", func() {
			HandleEvent(c, &msg)
		})
	}
}

// WritePump sends messages from the Send channel to the WebSocket connection.
// Runs in its own goroutine per client.
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Str("user_id", c.UserID).Msg("WritePump panic recovered")
		}
		ticker.Stop()
		c.Conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Hub closed the channel
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// SendJSON sends a JSON event to this client.
func (c *Client) SendJSON(event string, data interface{}) {
	dataBytes, err := json.Marshal(data)
	if err != nil {
		return
	}

	msg := Message{
		Event: event,
		Data:  dataBytes,
	}

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return
	}

	select {
	case c.Send <- msgBytes:
	default:
		// Channel full - client is slow, skip
		log.Warn().Str("user_id", c.UserID).Msg("Send channel full, dropping message")
	}
}
