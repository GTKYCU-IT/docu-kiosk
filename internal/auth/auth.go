package auth

import (
	"context"
	"errors"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/database"
)

// jwtTTL is how long access tokens are valid.
const jwtTTL = 15 * time.Second

// AuthModule owns login, refresh rotation, and JWT validation.
// It is the single seam for authentication logic — HTTP handlers
// become thin adapters that parse JSON and call these methods.
type AuthModule struct {
	db     *database.Queries
	jwtKey []byte
}

// NewAuthModule creates an AuthModule with the given database handle
// and JWT signing key.
func NewAuthModule(db *database.Queries, jwtKey []byte) *AuthModule {
	return &AuthModule{db: db, jwtKey: jwtKey}
}

// Login authenticates a user by username and password, returning a
// short-lived JWT and a long-lived refresh token.
func (a *AuthModule) Login(ctx context.Context, username, password string) (jwt string, refreshToken string, err error) {
	user, err := a.db.GetUserByUsername(ctx, username)
	if err != nil {
		return "", "", errors.New("invalid credentials")
	}

	if !CheckPasswordHash(password, user.Password) {
		return "", "", errors.New("invalid credentials")
	}

	jwt, err = generateJWT(user.ID, a.jwtKey, jwtTTL)
	if err != nil {
		return "", "", err
	}

	rt, err := a.db.MakeRefreshToken(ctx, user.ID)
	if err != nil {
		return "", "", err
	}

	return jwt, rt.Token, nil
}

// RotateRefresh exchanges a valid refresh token for a new JWT and a
// new refresh token. The old refresh token is revoked (rotation).
func (a *AuthModule) RotateRefresh(ctx context.Context, token string) (jwt string, newRefreshToken string, err error) {
	rt, err := a.db.GetRefreshToken(ctx, token)
	if err != nil {
		return "", "", errors.New("invalid refresh token")
	}

	if rt.RevokedAt != nil || time.Now().UTC().After(rt.ExpiresAt) {
		return "", "", errors.New("refresh token expired or revoked")
	}

	jwt, err = generateJWT(rt.UserID, a.jwtKey, jwtTTL)
	if err != nil {
		return "", "", err
	}

	if err := a.db.RevokeRefreshToken(ctx, rt.Token); err != nil {
		return "", "", err
	}

	newRT, err := a.db.MakeRefreshToken(ctx, rt.UserID)
	if err != nil {
		return "", "", err
	}

	return jwt, newRT.Token, nil
}

// Validate checks a JWT bearer token and returns the authenticated user.
// It handles both token extraction from headers and JWT validation.
func (a *AuthModule) Validate(ctx context.Context, tokenString string) (database.User, error) {
	userID, err := validateJWT(tokenString, a.jwtKey)
	if err != nil {
		return database.User{}, err
	}

	user, err := a.db.GetUser(ctx, userID)
	if err != nil {
		return database.User{}, err
	}

	return user, nil
}
