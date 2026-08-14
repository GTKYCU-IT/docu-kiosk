package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/calvertjadon/docu-kiosk/internal/kiosks"
	"github.com/google/uuid"
)

// Registration problem type URIs follow RFC 9457. These exact strings are
// the stable client contract: the web classifier keys on the type field
// alone.
const (
	problemTypeAlreadyRegistered = "urn:docu-kiosk:problem:kiosk-already-registered"
	problemTypeNameConflict      = "urn:docu-kiosk:problem:kiosk-name-conflict"
	problemTypeInvalidName       = "urn:docu-kiosk:problem:invalid-kiosk-name"
	problemTypeMalformedRequest  = "urn:docu-kiosk:problem:malformed-request"
	problemTypeInternalError     = "urn:docu-kiosk:problem:internal-error"
)

// POST /api/kiosks
func (s *server) handleRegister(w http.ResponseWriter, r *http.Request) {
	type Params struct {
		Name string `json:"name"`
	}

	malformed := problem{
		Type:   problemTypeMalformedRequest,
		Title:  "Malformed Request",
		Status: http.StatusBadRequest,
		Detail: "The request body is not valid JSON.",
	}

	var params Params
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&params); err != nil {
		s.writeProblem(w, malformed, nil)
		return
	}
	// Reject trailing data after the first JSON value, so concatenated or
	// partially-consumed payloads cannot pass as a well-formed request.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		s.writeProblem(w, malformed, nil)
		return
	}

	err := s.kiosks.Register(r.Context(), s.realIP(r), params.Name)
	if err != nil {
		switch {
		case errors.Is(err, kiosks.ErrAlreadyRegistered):
			s.writeProblem(w, problem{
				Type:   problemTypeAlreadyRegistered,
				Title:  "Kiosk Already Registered",
				Status: http.StatusConflict,
				Detail: "A kiosk is already registered for this address.",
			}, nil)
		case errors.Is(err, kiosks.ErrNameTaken):
			s.writeProblem(w, problem{
				Type:   problemTypeNameConflict,
				Title:  "Kiosk Name in Use",
				Status: http.StatusConflict,
				Detail: "A different kiosk already uses this name.",
			}, nil)
		case errors.Is(err, kiosks.ErrInvalidName):
			s.writeProblem(w, problem{
				Type:   problemTypeInvalidName,
				Title:  "Invalid Kiosk Name",
				Status: http.StatusUnprocessableEntity,
				Detail: "The kiosk name is invalid.",
			}, nil)
		default:
			s.writeProblem(w, problem{
				Type:   problemTypeInternalError,
				Title:  "Internal Error",
				Status: http.StatusInternalServerError,
				Detail: "Failed to register the kiosk.",
			}, err)
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GET /api/kiosks
func (s *server) handleListKiosks(w http.ResponseWriter, r *http.Request) {
	type KioskResponse struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	}

	ks, err := s.kiosks.ListLive(r.Context(), s.hub.Statuses().LiveKioskIDs())
	if err != nil {
		s.respondWithError(w, "failed to list kiosks", http.StatusInternalServerError, err)
		return
	}

	list := make([]KioskResponse, 0, len(ks))
	for _, k := range ks {
		list = append(list, KioskResponse{ID: k.ID, Name: k.Name})
	}

	s.respondWithJSON(w, http.StatusOK, list)
}
