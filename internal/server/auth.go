package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/auth"
	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/google/uuid"
)

type AuthenticatedHandler func(w http.ResponseWriter, r *http.Request, user database.User)

func (s *server) ensureAuthMiddlware(handler AuthenticatedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenString, err := auth.GetBearerToken(r.Header)
		if err != nil {
			respondWithError(w, "invalid token", http.StatusBadRequest, err)
			return
		}

		userID, err := auth.ValidateJWT(tokenString, s.jwtKey)
		if err != nil {
			respondWithError(w, "invalid token", http.StatusUnauthorized, err)
			return
		}

		user, err := s.db.GetUser(r.Context(), userID.String())
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

func (s *server) handleAuthenticate(w http.ResponseWriter, r *http.Request) {
	params := struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{}

	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		respondWithError(w, "Missing username or password", http.StatusBadRequest, err)
		func() { _ = r.Body.Close() }()
		return
	}

	user, err := s.db.GetUserByUsername(r.Context(), params.Username)
	if err != nil {
		respondWithError(w, "Invalid credentials", http.StatusBadRequest, err)
		return
	}

	if !auth.CheckPasswordHash(params.Password, user.Password) {
		respondWithError(w, "Invalid credentials", http.StatusBadRequest, nil)
		return
	}

	userID, err := uuid.Parse(user.ID)
	if err != nil {
		respondWithError(w, "Database error", http.StatusInternalServerError, err)
	}

	tokenString, err := auth.GenerateJWT(userID, s.jwtKey, time.Minute*5)
	if err != nil {
		respondWithError(w, "Token creation failed", http.StatusInternalServerError, err)
	}

	refreshToken := auth.MakeRefreshToken()

	// TODO: persist refresh token to DB

	respondWithJSON(w, http.StatusOK, struct {
		JWT          string `json:"jwt"`
		RefreshToken string `json:"refresh_token"`
	}{
		JWT:          tokenString,
		RefreshToken: refreshToken,
	})
}
