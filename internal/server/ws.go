package server

import (
	"encoding/json"
	"net"
	"net/http"

	"github.com/coder/websocket"
)

// GET /ws
func (s *server) handleWS(w http.ResponseWriter, r *http.Request) {
	kioskIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		http.Error(w, "could not get kiosk ip", http.StatusInternalServerError)
		return
	}

	k, err := s.db.GetKioskByIP(r.Context(), kioskIP)
	if err != nil {
		http.Error(w, "unregistered ip", http.StatusUnauthorized)
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

	s.hub.Register(k.ID, conn)
	defer s.hub.Unregister(k.ID)

	ctx := r.Context()

	data, _ := json.Marshal(map[string]string{"type": "connected", "name": k.Name})
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		return
	}

	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
	}
}
