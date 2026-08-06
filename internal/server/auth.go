package server

import (
	"encoding/json"
	"net/http"

	"github.com/calvertjadon/docu-kiosk/internal/auth"
	"github.com/calvertjadon/docu-kiosk/internal/database"
)

type AuthenticatedHandler func(w http.ResponseWriter, r *http.Request, user database.User)

func (s *server) ensureAuthMiddlware(handler AuthenticatedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenString, err := auth.GetBearerToken(r.Header)
		if err != nil {
			respondWithError(w, "invalid token", http.StatusBadRequest, err)
			return
		}

		user, err := s.authModule.Validate(r.Context(), tokenString)
		if err != nil {
			respondWithError(w, "invalid token", http.StatusUnauthorized, err)
			return
		}

		handler(w, r, user)
	}
}

func (s *server) handleProtected(w http.ResponseWriter, r *http.Request, user database.User) {
	w.WriteHeader(http.StatusOK)
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	params := struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{}

	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		respondWithError(w, "Missing username or password", http.StatusBadRequest, err)
		func() { _ = r.Body.Close() }()
		return
	}

	jwt, refreshToken, err := s.authModule.Login(r.Context(), params.Username, params.Password)
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

func (s *server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	refreshTokenString, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, "failed to authenticate user", http.StatusUnauthorized, err)
		return
	}

	jwt, newRefreshToken, err := s.authModule.RotateRefresh(r.Context(), refreshTokenString)
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
