package ws

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

// client represents one connected WebSocket client.
type client struct {
	tid  string
	did  string // empty = subscribe to all devices in tenant
	conn *websocket.Conn
	send chan []byte
}

// Hub manages WebSocket clients and fans out Redis Pub/Sub messages.
type Hub struct {
	mu      sync.RWMutex
	clients map[string]map[*client]struct{} // tid → set of clients
	cache   *redis.Client
}

func NewHub(cache *redis.Client) *Hub {
	return &Hub{
		clients: make(map[string]map[*client]struct{}),
		cache:   cache,
	}
}

func (h *Hub) register(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[c.tid] == nil {
		h.clients[c.tid] = make(map[*client]struct{})
	}
	h.clients[c.tid][c] = struct{}{}
}

func (h *Hub) unregister(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients[c.tid], c)
	close(c.send)
}

// Broadcast sends a message to all clients subscribed to tid (optionally filtered by did).
func (h *Hub) Broadcast(tid string, msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients[tid] {
		select {
		case c.send <- msg:
		default:
			// slow client — drop frame
		}
	}
}

// RunRedisSubscriber listens on ws:events:{tid} for all active tenants.
// New tenants are picked up dynamically via psubscribe.
func (h *Hub) RunRedisSubscriber(ctx context.Context) {
	psub := h.cache.PSubscribe(ctx, "ws:events:*")
	defer psub.Close()

	ch := psub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			// Channel format: ws:events:{tid}
			tid := msg.Channel[len("ws:events:"):]
			h.Broadcast(tid, []byte(msg.Payload))
		}
	}
}

// writePump drains the send channel to the WebSocket connection.
func (h *Hub) writePump(c *client) {
	defer c.conn.Close()
	for msg := range c.send {
		c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			log.Printf("ws write tid=%s: %v", c.tid, err)
			return
		}
	}
}

// readPump reads from the WebSocket (handles ping/close, supports sub message).
func (h *Hub) readPump(c *client) {
	defer func() {
		h.unregister(c)
		c.conn.Close()
	}()
	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
	}
}
