package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

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

		userID, err := auth.ValidateJWT(tokenString, s.jwtKey)
		if err != nil {
			respondWithError(w, "invalid token", http.StatusUnauthorized, err)
			return
		}

		user, err := s.db.GetUser(r.Context(), userID)
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

	user, err := s.db.GetUserByUsername(r.Context(), params.Username)
	if err != nil {
		respondWithError(w, "Invalid credentials", http.StatusBadRequest, err)
		return
	}

	if !auth.CheckPasswordHash(params.Password, user.Password) {
		respondWithError(w, "Invalid credentials", http.StatusBadRequest, nil)
		return
	}

	tokenString, err := auth.GenerateJWT(user.ID, s.jwtKey, time.Second*15)
	if err != nil {
		respondWithError(w, "Token creation failed", http.StatusInternalServerError, err)
		return
	}

	refreshToken, err := s.db.MakeRefreshToken(r.Context(), user.ID)
	if err != nil {
		respondWithError(w, "Refresh token creation failed", http.StatusInternalServerError, err)
		return
	}

	respondWithJSON(w, http.StatusOK, struct {
		JWT          string `json:"jwt"`
		RefreshToken string `json:"refresh_token"`
	}{
		JWT:          tokenString,
		RefreshToken: refreshToken.Token,
	})
}

func (s *server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	refreshTokenString, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, "failed to authenticate user", http.StatusUnauthorized, err)
		return
	}

	rt, err := s.db.GetRefreshToken(r.Context(), refreshTokenString)
	if err != nil {
		respondWithError(w, "failed to authenticate user", http.StatusUnauthorized, err)
		return
	}

	if isRevoked(rt) || isExpired(rt) {
		respondWithError(w, "bad refresh token", http.StatusUnauthorized, err)
		return
	}

	// issue new jwt

	token, err := auth.GenerateJWT(rt.UserID, s.jwtKey, time.Second*15)
	if err != nil {
		respondWithError(w, "failed to create jwt", http.StatusInternalServerError, err)
		return
	}

	// invalidate existing refresh token

	if err := s.db.RevokeRefreshToken(r.Context(), rt.Token); err != nil {
		respondWithError(w, "failed to revoke refresh token", http.StatusInternalServerError, err)
		return
	}

	rt, err = s.db.MakeRefreshToken(r.Context(), rt.UserID)
	if err != nil {
		respondWithError(w, "failed to create refresh token", http.StatusInternalServerError, err)
		return
	}

	respondWithJSON(w, http.StatusOK, struct {
		JWT          string `json:"jwt"`
		RefreshToken string `json:"refresh_token"`
	}{
		JWT:          token,
		RefreshToken: rt.Token,
	})
}

func isRevoked(rt database.RefreshToken) bool {
	revoked := rt.RevokedAt != nil
	if revoked {
		slog.Info("revoked")
	}
	return revoked
}

func isExpired(rt database.RefreshToken) bool {
	expired := rt.ExpiresAt.UTC().Before(time.Now().UTC())
	if expired {
		slog.Info("expired: " + rt.ExpiresAt.String())
	}
	return expired
}
