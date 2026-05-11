package server

import (
	"docu-kiosk/broker/internal/api"
	"net/http"
)

func NewRouter(api *api.API) *http.ServeMux {
	router := http.NewServeMux()

	fileServer := http.FileServer(http.Dir("./static"))
	router.Handle("GET /static/", http.StripPrefix("/static/", fileServer))
	router.Handle("/api/", http.StripPrefix("/api/", api))

	return router
}
