package api

import (
	"docu-kiosk/broker/internal/domain"
	"encoding/json"
	"fmt"
	"net/http"
)

// POST /clients
func (api *API) handleRegister(w http.ResponseWriter, r *http.Request) {
	// decode json and extract name
	type Params struct {
		Name string `json:"name"`
	}

	var params Params
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		http.Error(w, "failed to decode params", http.StatusBadRequest)
	}

	// create client
	client := domain.NewClient(params.Name)

	// save client to store
	if err := api.store.SaveClient(client); err != nil {
		http.Error(w, "failed to save client", http.StatusInternalServerError)
	}

	// redirect to /ws?id=<client.ID>&initial=true
	http.Redirect(w, r, fmt.Sprintf("/ws?id=%s&initial=true", client.ID), http.StatusCreated)
}
