package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const testSecret = "0123456789abcdef0123456789abcdef" // 32 bytes

func TestMakeJWT(t *testing.T) {
	userID := uuid.New()
	tokenString, err := generateJWT(userID, []byte(testSecret), time.Minute)
	if err != nil {
		t.Errorf("failed to generate jwt: %s", err)
	}

	if strings.Contains(tokenString, userID.String()) {
		t.Errorf("hashing produced unexpected result: %s", err)
	}
}

func TestValidateJWT(t *testing.T) {
	userID := uuid.New()
	tokenString, _ := generateJWT(userID, []byte(testSecret), time.Minute)

	got, err := validateJWT(tokenString, []byte(testSecret))
	if err != nil {
		t.Error(err)
	}

	if got != userID {
		t.Errorf("validation failed; expected %s got %s", userID, got)
	}
}

func TestExpiredJWTIsRejected(t *testing.T) {
	userID := uuid.New()
	tokenString, _ := generateJWT(userID, []byte(testSecret), -1*time.Millisecond)

	if _, err := validateJWT(tokenString, []byte(testSecret)); err == nil {
		t.Error("expired token should fail validation")
	}
}

func TestBadSecretIsRejected(t *testing.T) {
	userID := uuid.New()
	tokenString, _ := generateJWT(userID, []byte(testSecret), time.Minute)

	if _, err := validateJWT(tokenString, []byte("bad secret")); err == nil {
		t.Error("bad secret should fail validation")
	}
}

func TestBadTokenIsRejected(t *testing.T) {
	if _, err := validateJWT("bad token", []byte(testSecret)); err == nil {
		t.Error("bad secret should fail validation")
	}
}

func TestWrongIssuerIsRejected(t *testing.T) {
	claims := jwt.RegisteredClaims{
		Issuer:    "someone-else",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		Subject:   uuid.New().String(),
	}
	tokenString, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := validateJWT(tokenString, []byte(testSecret)); err == nil {
		t.Error("token with wrong issuer should fail validation")
	}
}

func TestNoneAlgorithmIsRejected(t *testing.T) {
	// A token claiming the "none" algorithm must never be accepted.
	tokenString, err := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		Subject:   uuid.New().String(),
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := validateJWT(tokenString, []byte(testSecret)); err == nil {
		t.Error("alg=none token should fail validation")
	}
}
