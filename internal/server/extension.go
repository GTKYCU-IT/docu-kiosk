package server

import (
	"net/http"
	"strings"
)

func extensionFileServer() http.Handler {
	fs := http.FileServer(http.Dir("./extension/public"))
	return http.StripPrefix("/extension", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".crx"):
			w.Header().Set("Content-Type", "application/x-chrome-extension")
		case strings.HasSuffix(r.URL.Path, ".xml"):
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		}
		fs.ServeHTTP(w, r)
	}))
}
