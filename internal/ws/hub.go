package ws

import (
	"sync"

	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/safego"
)

// maxClientsPerUser caps how many simultaneous WebSocket connections
// any single user can hold open. Tabs, devices and reconnects all eat
// into this. The oldest connection is closed when the limit is hit so
// a forgotten background tab can never lock out a freshly opened one.
const maxClientsPerUser = 5

// Hub manages all active WebSocket connections.
// It's the central registry - every connect/disconnect goes through here.
type Hub struct {
	// All connected clients: socketID → *Client
	clients map[string]*Client

	// Reverse lookup: userID → []*Client. A user can be online from
	// multiple tabs/devices at once; previous code overwrote the slot
	// every time which silently disconnected the older tab. Now every
	// live connection is tracked and message broadcasting fans out.
	users map[string][]*Client

	// Channels for thread-safe operations
	register   chan *Client
	unregister chan *Client

	mu sync.RWMutex
}

// Global hub instance. Always access through GetHub() from outside the
// ws package when there's any chance the caller runs before InitHub().
var H *Hub

// GetHub returns the global hub, or nil if it hasn't been initialised yet.
// Callers should nil-check before using:
//
//	if hub := ws.GetHub(); hub != nil { hub.SendToUser(...) }
func GetHub() *Hub { return H }

func NewHub() *Hub {
	return &Hub{
		clients:    make(map[string]*Client),
		users:      make(map[string][]*Client),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Run starts the hub's main loop - call this as a goroutine. Each
// iteration is wrapped so a panic in one register/unregister event
// can't take down the entire WebSocket subsystem.
func (h *Hub) Run() {
	for {
		safego.Run("hub.iteration", func() {
			h.processOne()
		})
	}
}

func (h *Hub) processOne() {
	select {
	case client := <-h.register:
		h.mu.Lock()
		h.clients[client.ID] = client
		existing := h.users[client.UserID]
		// Enforce the per-user cap. We boot the OLDEST client so a
		// stale tab can never block a fresh login. The boot is done
		// outside the mutex below to avoid blocking other connects.
		var booted *Client
		if len(existing) >= maxClientsPerUser {
			booted = existing[0]
			existing = existing[1:]
		}
		h.users[client.UserID] = append(existing, client)
		total := len(h.clients)
		h.mu.Unlock()
		if booted != nil {
			go h.bootStaleClient(booted)
		}
		log.Info().
			Str("user_id", client.UserID).
			Str("socket_id", client.ID).
			Int("total_online", total).
			Int("user_clients", len(existing)+1).
			Msg("user connected")

	case client := <-h.unregister:
		h.mu.Lock()
		if _, ok := h.clients[client.ID]; ok {
			delete(h.clients, client.ID)
			h.removeFromUser(client)
			close(client.Send)
		}
		total := len(h.clients)
		h.mu.Unlock()
		log.Info().
			Str("user_id", client.UserID).
			Int("total_online", total).
			Msg("user disconnected")
	}
}

// removeFromUser drops a single client from the per-user slice. Caller
// must already hold h.mu.
func (h *Hub) removeFromUser(client *Client) {
	list := h.users[client.UserID]
	if len(list) == 0 {
		return
	}
	out := list[:0]
	for _, c := range list {
		if c.ID != client.ID {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		delete(h.users, client.UserID)
	} else {
		h.users[client.UserID] = out
	}
}

// bootStaleClient asks an over-the-cap client to disconnect. We send a
// shutdown notice then close the Send channel via the unregister
// channel to keep all bookkeeping centralised.
func (h *Hub) bootStaleClient(client *Client) {
	defer func() {
		// SendJSON / send may panic on a closed channel - protect the
		// hub goroutine from a misbehaving client.
		if r := recover(); r != nil {
			log.Warn().Interface("panic", r).Msg("bootStaleClient recovered")
		}
	}()
	client.SendJSON("force_disconnect", map[string]string{
		"reason": "too_many_connections",
	})
	h.unregister <- client
}

// GetClientByUserID finds the most recently connected client for the
// given user. Used for one-shot signalling where any live connection
// will do (the user is logged in on this device, so messages reach them).
func (h *Hub) GetClientByUserID(userID string) *Client {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	list := h.users[userID]
	if len(list) == 0 {
		return nil
	}
	return list[len(list)-1]
}

// GetClientsByUserID returns ALL live clients for a user - used for
// fan-out broadcasts (e.g., notify every device the user has open).
func (h *Hub) GetClientsByUserID(userID string) []*Client {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	list := h.users[userID]
	if len(list) == 0 {
		return nil
	}
	out := make([]*Client, len(list))
	copy(out, list)
	return out
}

// OnlineCount returns number of connected sockets (not unique users).
func (h *Hub) OnlineCount() int {
	if h == nil {
		return 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// OnlineUserCount returns number of unique connected users.
func (h *Hub) OnlineUserCount() int {
	if h == nil {
		return 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.users)
}

// BroadcastJSON fans out an event to every connected client. Errors
// from individual sends are swallowed - one slow client shouldn't block
// the whole broadcast (SendJSON internally drops on a full send buffer).
func (h *Hub) BroadcastJSON(eventType string, payload interface{}) {
	if h == nil {
		return
	}
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.clients))
	for _, c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()
	for _, c := range clients {
		c.SendJSON(eventType, payload)
	}
}

// BroadcastJSONExcept is BroadcastJSON minus the given user's connections.
// Used by the "partner is searching" pulse so the user who triggered the
// search doesn't ping themselves.
func (h *Hub) BroadcastJSONExcept(excludeUserID, eventType string, payload interface{}) {
	if h == nil {
		return
	}
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.clients))
	for _, c := range h.clients {
		if c.UserID == excludeUserID {
			continue
		}
		clients = append(clients, c)
	}
	h.mu.RUnlock()
	for _, c := range clients {
		c.SendJSON(eventType, payload)
	}
}

// Shutdown gracefully closes all WebSocket connections.
func (h *Hub) Shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, client := range h.clients {
		client.SendJSON("server_shutdown", map[string]string{
			"message": "Server is restarting, please reconnect",
		})
		close(client.Send)
	}

	h.clients = make(map[string]*Client)
	h.users = make(map[string][]*Client)
	log.Info().Msg("WebSocket hub shut down - all clients notified")
}

// InitHub creates and starts the global hub. The Run loop is launched
// via safego so a panic inside Run() (which already has its own per-
// iteration recovery) is double-protected.
func InitHub() {
	H = NewHub()
	safego.Go("hub.Run", H.Run)
	log.Info().Msg("WebSocket hub started")
}
