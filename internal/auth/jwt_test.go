package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const fakeSecret = "mysecret"

func TestMakeJWT(t *testing.T) {
	userID := uuid.New()
	tokenString, err := GenerateJWT(userID, []byte(fakeSecret), time.Minute)
	if err != nil {
		t.Errorf("failed to generate jwt: %s", err)
	}

	if strings.Contains(tokenString, userID.String()) {
		t.Errorf("hashing produced unexpected result: %s", err)
	}
}

func TestValidateJWT(t *testing.T) {
	userID := uuid.New()
	tokenString, _ := GenerateJWT(userID, []byte(fakeSecret), time.Minute)

	got, err := ValidateJWT(tokenString, []byte(fakeSecret))
	if err != nil {
		t.Error(err)
	}

	if got != userID {
		t.Errorf("validation failed; expected %s got %s", userID, got)
	}
}

func TestExpiredJWTIsRejected(t *testing.T) {
	userID := uuid.New()
	tokenString, _ := GenerateJWT(userID, []byte(fakeSecret), -1*time.Millisecond)

	if _, err := ValidateJWT(tokenString, []byte(fakeSecret)); err == nil {
		t.Error("expired token should fail vaildation")
	}
}

func TestBadSecretIsRejected(t *testing.T) {
	userID := uuid.New()
	tokenString, _ := GenerateJWT(userID, []byte(fakeSecret), time.Minute)

	if _, err := ValidateJWT(tokenString, []byte("bad secret")); err == nil {
		t.Error("bad secret should fail validation")
	}
}

func TestBadTokenIsRejected(t *testing.T) {
	if _, err := ValidateJWT("bad token", []byte(fakeSecret)); err == nil {
		t.Error("bad secret should fail validation")
	}
}
