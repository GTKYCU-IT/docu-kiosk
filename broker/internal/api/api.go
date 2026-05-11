package api

import (
	"docu-kiosk/broker/internal/domain"
	"net/http"
)

type API struct {
	http.ServeMux
	store domain.ClientStore
}

func NewAPI(store domain.ClientStore) *API {
	api := &API{
		store: store,
	}

	api.HandleFunc("POST /clients", api.handleRegister)

	return api
}
