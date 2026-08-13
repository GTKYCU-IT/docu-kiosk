// Package auth provides password hashing, JWT issuance/validation, and the
// AuthModule seam that owns login and refresh-token rotation.
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/google/uuid"
)

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

// store is the persistence seam for authentication: the users and refresh
// tokens AuthModule's operations need. *database.Queries implements it in
// production; tests inject fakes to exercise error paths without a database.
type store interface {
	CountUsers(ctx context.Context) (int64, error)
	CreateUser(ctx context.Context, arg database.CreateUserParams) (database.User, error)
	GetUserByUsername(ctx context.Context, username string) (database.User, error)
	GetUser(ctx context.Context, id uuid.UUID) (database.User, error)
	GetRefreshToken(ctx context.Context, token string) (database.RefreshToken, error)
	MakeRefreshToken(ctx context.Context, arg database.MakeRefreshTokenParams) (database.RefreshToken, error)
	RevokeRefreshToken(ctx context.Context, token string) error
}

// TokenLifetimes bundles the two token lifetimes AuthModule issues tokens
// with: the short-lived JWT and the long-lived refresh token. Carrying the
// pair as one value keeps them together across constructor call sites, so
// callers cannot swap or drop one of them.
type TokenLifetimes struct {
	JWTTTL     time.Duration // lifetime of issued JWTs
	RefreshTTL time.Duration // lifetime of issued refresh tokens
}

// AuthModule owns login, refresh rotation, JWT validation, and the admin-user
// bootstrap. It is the single seam for authentication logic — HTTP handlers
// and the server bootstrap become thin adapters that parse input and call
// these methods. Token lifetimes are injected so the policy lives in config,
// not here.
type AuthModule struct {
	store     store
	jwtKey    []byte
	lifetimes TokenLifetimes
}

// NewAuthModule creates an AuthModule that persists through db, signs JWTs
// with jwtKey, and issues tokens with the given lifetimes. The key must be at
// least 32 bytes; anything shorter is rejected so a misconfiguration fails at
// startup instead of producing forgeable tokens.
func NewAuthModule(db *database.Queries, jwtKey []byte, lifetimes TokenLifetimes) (*AuthModule, error) {
	if len(jwtKey) < minJWTKeyLen {
		return nil, fmt.Errorf("jwt key must be at least %d bytes, got %d", minJWTKeyLen, len(jwtKey))
	}
	return newAuthModule(db, jwtKey, lifetimes), nil
}

// newAuthModule builds an AuthModule around any store; tests use it with a
// fake.
func newAuthModule(s store, jwtKey []byte, lifetimes TokenLifetimes) *AuthModule {
	return &AuthModule{store: s, jwtKey: jwtKey, lifetimes: lifetimes}
}

// newRefreshToken creates and persists a refresh token for userID, expiring
// after the module's refresh TTL.
func (a *AuthModule) newRefreshToken(ctx context.Context, userID uuid.UUID) (database.RefreshToken, error) {
	rt, err := a.store.MakeRefreshToken(ctx, database.MakeRefreshTokenParams{
		UserID:    userID,
		ExpiresAt: time.Now().UTC().Add(a.lifetimes.RefreshTTL),
	})
	if err != nil {
		return database.RefreshToken{}, fmt.Errorf("create refresh token: %w", err)
	}
	return rt, nil
}

// signJWT signs a JWT for userID, expiring after the module's JWT TTL.
func (a *AuthModule) signJWT(userID uuid.UUID) (string, error) {
	jwt, err := generateJWT(userID, a.jwtKey, a.lifetimes.JWTTTL)
	if err != nil {
		return "", fmt.Errorf("generate jwt: %w", err)
	}
	return jwt, nil
}

// Login authenticates a user by username and password, returning a short-lived
// JWT and a long-lived refresh token. Both "unknown user" and "wrong password"
// return ErrInvalidCredentials so the response is identical either way.
func (a *AuthModule) Login(ctx context.Context, username, password string) (jwt string, refreshToken string, err error) {
	user, err := a.store.GetUserByUsername(ctx, username)
	if err != nil {
		return "", "", ErrInvalidCredentials
	}

	if !CheckPasswordHash(password, user.Password) {
		return "", "", ErrInvalidCredentials
	}

	jwt, err = a.signJWT(user.ID)
	if err != nil {
		return "", "", err
	}

	rt, err := a.newRefreshToken(ctx, user.ID)
	if err != nil {
		return "", "", err
	}

	return jwt, rt.Token, nil
}

// RotateRefresh exchanges a valid refresh token for a new JWT and a new
// refresh token. The old refresh token is revoked (rotation), so a stolen
// token can only be used once.
func (a *AuthModule) RotateRefresh(ctx context.Context, token string) (jwt string, newRefreshToken string, err error) {
	rt, err := a.store.GetRefreshToken(ctx, token)
	if err != nil {
		return "", "", ErrInvalidRefreshToken
	}

	if rt.RevokedAt != nil || time.Now().UTC().After(rt.ExpiresAt) {
		return "", "", ErrInvalidRefreshToken
	}

	jwt, err = a.signJWT(rt.UserID)
	if err != nil {
		return "", "", err
	}

	if err := a.store.RevokeRefreshToken(ctx, rt.Token); err != nil {
		return "", "", fmt.Errorf("revoke refresh token: %w", err)
	}

	newRT, err := a.newRefreshToken(ctx, rt.UserID)
	if err != nil {
		return "", "", err
	}

	return jwt, newRT.Token, nil
}

// Validate checks a JWT bearer token and returns the authenticated user.
func (a *AuthModule) Validate(ctx context.Context, tokenString string) (database.User, error) {
	userID, err := validateJWT(tokenString, a.jwtKey)
	if err != nil {
		return database.User{}, err
	}

	user, err := a.store.GetUser(ctx, userID)
	if err != nil {
		return database.User{}, err
	}

	return user, nil
}

// EnsureAdminUser bootstraps the admin user on first boot. When the users
// table already has users it is a no-op — existing credentials are never
// validated or reset. On an empty table it enforces the credential policy
// (non-empty username and password, password at least 8 characters), hashes
// the password, and creates a UUID-backed user. It owns every bootstrap rule
// and database operation, so the server only supplies credentials.
func (a *AuthModule) EnsureAdminUser(username, password string) error {
	count, err := a.store.CountUsers(context.Background())
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if count > 0 {
		return nil
	}

	if username == "" || password == "" {
		return errors.New("AUTH_USERNAME and AUTH_PASSWORD are required on first boot (users table is empty)")
	}
	if len(password) < 8 {
		return errors.New("AUTH_PASSWORD must be at least 8 characters")
	}

	hash, err := HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	if _, err := a.store.CreateUser(context.Background(), database.CreateUserParams{
		ID:       uuid.New(),
		Username: username,
		Password: hash,
	}); err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}

	return nil
}
