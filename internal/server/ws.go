package server

import (
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"
)

// GET /ws
func (s *server) handleWS(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("kiosk-token")
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	name, err := s.auth.ValidateToken(cookie.Value)
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	// InsecureSkipVerify is safe here: the broker runs on an internal network
	// and the Vite dev proxy changes the Origin host during development.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	id := s.hub.Register(name, conn)
	defer s.hub.Unregister(id)

	ctx := r.Context()

	data, _ := json.Marshal(map[string]string{"type": "connected", "name": name})
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		return
	}

	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
	}
}
