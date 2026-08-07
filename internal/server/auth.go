package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/calvertjadon/docu-kiosk/internal/auth"
	"github.com/calvertjadon/docu-kiosk/internal/database"
)

type AuthenticatedHandler func(w http.ResponseWriter, r *http.Request, user database.User)

func (s *server) ensureAuthMiddleware(handler AuthenticatedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenString, err := auth.GetBearerToken(r.Header)
		if err != nil {
			s.respondWithError(w, "missing bearer token", http.StatusUnauthorized, nil)
			return
		}

		user, err := s.authModule.Validate(r.Context(), tokenString)
		if err != nil {
			s.respondWithError(w, "invalid token", http.StatusUnauthorized, nil)
			return
		}

		handler(w, r, user)
	}
}

// handleProtected is the canonical authenticated-endpoint probe: it returns
// 200 only when the bearer token is valid.
func (s *server) handleProtected(w http.ResponseWriter, r *http.Request, user database.User) {
	w.WriteHeader(http.StatusOK)
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var params struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		s.respondWithError(w, "invalid request body", http.StatusBadRequest, nil)
		return
	}

	jwt, refreshToken, err := s.authModule.Login(r.Context(), params.Username, params.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			// Deliberately no error detail: the response must be identical
			// for unknown users and wrong passwords.
			s.respondWithError(w, "invalid credentials", http.StatusUnauthorized, nil)
			return
		}
		s.respondWithError(w, "login failed", http.StatusInternalServerError, err)
		return
	}

	s.respondWithJSON(w, http.StatusOK, struct {
		JWT          string `json:"jwt"`
		RefreshToken string `json:"refresh_token"`
	}{
		JWT:          jwt,
		RefreshToken: refreshToken,
	})
}

func (s *server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		s.respondWithError(w, "missing bearer token", http.StatusUnauthorized, nil)
		return
	}

	jwt, refreshToken, err := s.authModule.RotateRefresh(r.Context(), token)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidRefreshToken) {
			s.respondWithError(w, "invalid refresh token", http.StatusUnauthorized, nil)
			return
		}
		s.respondWithError(w, "refresh failed", http.StatusInternalServerError, err)
		return
	}

	s.respondWithJSON(w, http.StatusOK, struct {
		JWT          string `json:"jwt"`
		RefreshToken string `json:"refresh_token"`
	}{
		JWT:          jwt,
		RefreshToken: refreshToken,
	})
}
