package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/calvertjadon/docu-kiosk/internal/auth"
	"github.com/calvertjadon/docu-kiosk/internal/database"
)

// refreshTokenCookie is the only transport for the refresh credential: a
// session-scoped browser cookie. The credential never appears in JSON
// bodies, query strings, or bearer headers, so application JavaScript and
// non-browser clients cannot obtain it.
const refreshTokenCookie = "refresh_token"

type AuthenticatedHandler func(w http.ResponseWriter, r *http.Request, user database.User)

func (s *server) ensureAuthMiddleware(handler AuthenticatedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenString, err := auth.GetBearerToken(r.Header)
		if err != nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			s.respondWithError(w, "missing bearer token", http.StatusUnauthorized, nil)
			return
		}

		user, err := s.authModule.Validate(r.Context(), tokenString)
		if err != nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
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

// setRefreshTokenCookie hands the refresh credential to the browser as a
// session cookie: custody is session-scoped and nothing is stored durably,
// so the credential dies with the session.
func (s *server) setRefreshTokenCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshTokenCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

// clearRefreshTokenCookie deletes the refresh cookie. Deletion is the one
// legitimate use of an expiry: a browser will not drop the cookie otherwise.
func (s *server) clearRefreshTokenCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshTokenCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Credential exchanges must never be cached by the browser or any
	// intermediary, on success or failure.
	w.Header().Set("Cache-Control", "no-store")
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

	// The refresh credential travels only in the Set-Cookie; the JSON body
	// carries the short-lived access JWT and nothing else.
	s.setRefreshTokenCookie(w, refreshToken)
	s.respondWithJSON(w, http.StatusOK, struct {
		JWT string `json:"jwt"`
	}{JWT: jwt})
}

func (s *server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	// Rotation is driven exclusively by the cookie; a bearer token is never
	// a fallback transport for the refresh credential.
	cookie, err := r.Cookie(refreshTokenCookie)
	if err != nil {
		s.respondWithError(w, "missing refresh token cookie", http.StatusUnauthorized, nil)
		return
	}

	jwt, refreshToken, err := s.authModule.RotateRefresh(r.Context(), cookie.Value)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidRefreshToken) {
			s.respondWithError(w, "invalid refresh token", http.StatusUnauthorized, nil)
			return
		}
		s.respondWithError(w, "refresh failed", http.StatusInternalServerError, err)
		return
	}

	s.setRefreshTokenCookie(w, refreshToken)
	s.respondWithJSON(w, http.StatusOK, struct {
		JWT string `json:"jwt"`
	}{JWT: jwt})
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	cookie, err := r.Cookie(refreshTokenCookie)
	if err != nil {
		s.respondWithError(w, "missing refresh token cookie", http.StatusUnauthorized, nil)
		return
	}

	if err := s.authModule.Logout(r.Context(), cookie.Value); err != nil {
		if errors.Is(err, auth.ErrInvalidRefreshToken) {
			s.respondWithError(w, "invalid refresh token", http.StatusUnauthorized, nil)
			return
		}
		// The credential is still valid server-side, so the browser keeps
		// its cookie: clearing it here would strand the user with no way to
		// retry, and the ADR forbids reporting sign-out before revocation
		// succeeds.
		s.respondWithError(w, "logout failed", http.StatusInternalServerError, err)
		return
	}

	// Revocation succeeded; only now drop the browser's copy.
	s.clearRefreshTokenCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
