package ws

import (
	"net/http"

	"github.com/gorilla/websocket"

	"github.com/setucore/setu-cloud/internal/api/middleware"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true // restrict in production to known origins
	},
}

// HandleWS upgrades the connection and registers the client with the hub.
// Query param ?did= optionally filters to a single device.
func HandleWS(hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tid := middleware.TIDFromContext(r.Context())
		if tid == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		c := &client{
			tid:  tid,
			did:  r.URL.Query().Get("did"),
			conn: conn,
			send: make(chan []byte, 256),
		}
		hub.register(c)
		go hub.writePump(c)
		hub.readPump(c) // blocks until disconnect
	}
}
