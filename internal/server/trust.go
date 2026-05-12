package server

import (
	"net/http"
	"os"
)

const certPath = "/data/caddy/pki/authorities/local/root.crt"

// GET /trust
func (s *server) handleTrust(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(certPath)
	if err != nil {
		http.Error(w, "certificate not available", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="docu-kiosk-ca.crt"`)
	w.Write(data)
}
