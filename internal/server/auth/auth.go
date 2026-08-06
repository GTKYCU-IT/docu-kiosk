// Package auth groups login, refresh, and auth-middleware handlers.
package auth

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/calvertjadon/docu-kiosk/internal/auth"
	"github.com/calvertjadon/docu-kiosk/internal/database"
)

// AuthenticatedHandler is an HTTP handler that receives the authenticated user.
type AuthenticatedHandler func(w http.ResponseWriter, r *http.Request, user database.User)

type Handlers struct {
	authModule *auth.AuthModule
}

func NewHandlers(authModule *auth.AuthModule) *Handlers {
	return &Handlers{authModule: authModule}
}

func respondWithError(w http.ResponseWriter, msg string, code int, err error) {
	if err != nil {
		msg = fmt.Sprintf("%s: %s", msg, err)
	}
	log.Println(msg)

	respondWithJSON(w, code, struct {
		Error string `json:"error"`
	}{
		Error: msg,
	})
}

func respondWithJSON(w http.ResponseWriter, code int, payload any) {
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

// AuthMiddleware wraps an AuthenticatedHandler, validating the bearer token
// before calling the handler.
func (h *Handlers) AuthMiddleware(handler AuthenticatedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenString, err := auth.GetBearerToken(r.Header)
		if err != nil {
			respondWithError(w, "invalid token", http.StatusBadRequest, err)
			return
		}

		user, err := h.authModule.Validate(r.Context(), tokenString)
		if err != nil {
			respondWithError(w, "invalid token", http.StatusUnauthorized, err)
			return
		}

		handler(w, r, user)
	}
}

// POST /login
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	params := struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{}

	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		respondWithError(w, "Missing username or password", http.StatusBadRequest, err)
		func() { _ = r.Body.Close() }()
		return
	}

	jwt, refreshToken, err := h.authModule.Login(r.Context(), params.Username, params.Password)
	if err != nil {
		respondWithError(w, "Invalid credentials", http.StatusBadRequest, err)
		return
	}

	respondWithJSON(w, http.StatusOK, struct {
		JWT          string `json:"jwt"`
		RefreshToken string `json:"refresh_token"`
	}{
		JWT:          jwt,
		RefreshToken: refreshToken,
	})
}

// POST /refresh
func (h *Handlers) Refresh(w http.ResponseWriter, r *http.Request) {
	refreshTokenString, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, "failed to authenticate user", http.StatusUnauthorized, err)
		return
	}

	jwt, newRefreshToken, err := h.authModule.RotateRefresh(r.Context(), refreshTokenString)
	if err != nil {
		respondWithError(w, "bad refresh token", http.StatusUnauthorized, err)
		return
	}

	respondWithJSON(w, http.StatusOK, struct {
		JWT          string `json:"jwt"`
		RefreshToken string `json:"refresh_token"`
	}{
		JWT:          jwt,
		RefreshToken: newRefreshToken,
	})
}
