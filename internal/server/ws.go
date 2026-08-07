package server

import (
	"context"
	"net/http"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/protocol"
	"github.com/coder/websocket"
)

// GET /ws
func (s *server) handleWS(w http.ResponseWriter, r *http.Request) {
	kioskIP := s.realIP(r)

	k, err := s.db.GetKioskByIP(r.Context(), kioskIP)
	if err != nil {
		s.logger.Warn("ws connect rejected: unregistered ip", "ip", kioskIP)
		http.Error(w, "unregistered ip", http.StatusUnauthorized)
		return
	}

	// InsecureSkipVerify is safe here: the broker runs on an internal network
	// and the Vite dev proxy changes the Origin host during development.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.logger.Error("ws accept", "error", err, "kiosk_id", k.ID, "ip", kioskIP)
		return
	}
	defer conn.CloseNow()

	s.hub.Register(k.ID, conn)
	s.logger.Info("kiosk connected", "kiosk_id", k.ID, "name", k.Name, "ip", kioskIP)
	defer func() {
		s.hub.Unregister(k.ID)
		s.logger.Info("kiosk disconnected", "kiosk_id", k.ID, "name", k.Name)
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	data, err := protocol.Marshal(protocol.NewGreeting(k.Name))
	if err != nil {
		s.logger.Error("marshal greeting", "error", err, "kiosk_id", k.ID)
		return
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		return
	}

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
				err := conn.Ping(pingCtx)
				pingCancel()
				if err != nil {
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
	}
}
