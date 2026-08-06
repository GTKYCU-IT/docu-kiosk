package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

// RespondWithError writes an error JSON response.
func RespondWithError(w http.ResponseWriter, msg string, code int, err error) {
	if err != nil {
		msg = fmt.Sprintf("%s: %s", msg, err)
	}
	log.Println(msg)

	RespondWithJSON(w, code, struct {
		Error string `json:"error"`
	}{
		Error: msg,
	})
}

// RespondWithJSON writes a JSON response with the given status code.
func RespondWithJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Error marshalling JSON: %s\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(code)
	w.Write(data)
}
