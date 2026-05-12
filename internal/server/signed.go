package server

import "net/http"

const signedHTML = `<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"></head>
<body>
<script>
  if (window.parent !== window) {
    window.parent.postMessage({ type: "signed" }, "*");
  }
</script>
</body>
</html>`

// GET /signed — DocuSign redirects here when a signing session ends.
// Sends a postMessage to the parent kiosk window so it can return to waiting.
func (s *server) handleSigned(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(signedHTML))
}
