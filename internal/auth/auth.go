// Package auth provides password hashing, JWT issuance/validation, and the
// AuthModule seam that owns login and refresh-token rotation.
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/database"
)

// jwtTTL is how long access tokens are valid. Refresh tokens last 60 days
// (set in sql/queries/refresh_tokens.sql) and are rotated on every use, so a
// short access-token lifetime is safe.
const jwtTTL = 15 * time.Second

// minJWTKeyLen is the minimum accepted JWT signing key size. golang-jwt does
// not enforce a minimum for HS256, so we do — an empty or short key would make
// tokens trivially forgeable.
const minJWTKeyLen = 32

// Sentinel errors returned by AuthModule so HTTP handlers can map them to
// status codes without leaking internal error details to the client.
var (
	ErrInvalidCredentials  = errors.New("invalid credentials")
	ErrInvalidRefreshToken = errors.New("invalid refresh token")
)

// AuthModule owns login, refresh rotation, and JWT validation. It is the
// single seam for authentication logic — HTTP handlers become thin adapters
// that parse JSON and call these methods.
type AuthModule struct {
	db     *database.Queries
	jwtKey []byte
}

// NewAuthModule creates an AuthModule with the given database handle and JWT
// signing key. The key must be at least 32 bytes; anything shorter is
// rejected so a misconfiguration fails at startup instead of producing
// forgeable tokens.
func NewAuthModule(db *database.Queries, jwtKey []byte) (*AuthModule, error) {
	if len(jwtKey) < minJWTKeyLen {
		return nil, fmt.Errorf("jwt key must be at least %d bytes, got %d", minJWTKeyLen, len(jwtKey))
	}
	return &AuthModule{db: db, jwtKey: jwtKey}, nil
}

// Login authenticates a user by username and password, returning a short-lived
// JWT and a long-lived refresh token. Both "unknown user" and "wrong password"
// return ErrInvalidCredentials so the response is identical either way.
func (a *AuthModule) Login(ctx context.Context, username, password string) (jwt string, refreshToken string, err error) {
	user, err := a.db.GetUserByUsername(ctx, username)
	if err != nil {
		return "", "", ErrInvalidCredentials
	}

	if !CheckPasswordHash(password, user.Password) {
		return "", "", ErrInvalidCredentials
	}

	jwt, err = generateJWT(user.ID, a.jwtKey, jwtTTL)
	if err != nil {
		return "", "", fmt.Errorf("generate jwt: %w", err)
	}

	rt, err := a.db.MakeRefreshToken(ctx, user.ID)
	if err != nil {
		return "", "", fmt.Errorf("create refresh token: %w", err)
	}

	return jwt, rt.Token, nil
}

// RotateRefresh exchanges a valid refresh token for a new JWT and a new
// refresh token. The old refresh token is revoked (rotation), so a stolen
// token can only be used once.
func (a *AuthModule) RotateRefresh(ctx context.Context, token string) (jwt string, newRefreshToken string, err error) {
	rt, err := a.db.GetRefreshToken(ctx, token)
	if err != nil {
		return "", "", ErrInvalidRefreshToken
	}

	if rt.RevokedAt != nil || time.Now().UTC().After(rt.ExpiresAt) {
		return "", "", ErrInvalidRefreshToken
	}

	jwt, err = generateJWT(rt.UserID, a.jwtKey, jwtTTL)
	if err != nil {
		return "", "", fmt.Errorf("generate jwt: %w", err)
	}

	if err := a.db.RevokeRefreshToken(ctx, rt.Token); err != nil {
		return "", "", fmt.Errorf("revoke refresh token: %w", err)
	}

	newRT, err := a.db.MakeRefreshToken(ctx, rt.UserID)
	if err != nil {
		return "", "", fmt.Errorf("create refresh token: %w", err)
	}

	return jwt, newRT.Token, nil
}

// Validate checks a JWT bearer token and returns the authenticated user.
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
