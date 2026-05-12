package auth

import (
	"encoding/base64"
	"testing"
)

func TestGenerateToken(t *testing.T) {
	name := "John Doe"
	secret := "my super secret password"

	auth := New(secret)

	message := []byte(name)
	mac := auth.generateHMAC(message)
	payload := append(message, mac...)
	expected := base64.RawURLEncoding.EncodeToString(payload)

	actual, err := auth.GenerateToken(name)
	if err != nil {
		t.Error(err)
	}

	if actual != expected {
		t.Errorf("invalid token. expected %s got %s", expected, actual)
	}
}

func TestValidateToken(t *testing.T) {
	name := "John Doe"
	auth := New("my super secret password")

	token, err := auth.GenerateToken(name)
	if err != nil {
		t.Fatalf("failed to generate token: %s", err)
	}

	got, err := auth.ValidateToken(token)
	if err != nil {
		t.Fatalf("failed to validate token: %s", err)
	}

	if got != name {
		t.Errorf("wrong name. expected %s got %s", name, got)
	}
}

func TestValidateTokenWrongSecret(t *testing.T) {
	token, _ := New("secret-a").GenerateToken("kiosk-1")

	_, err := New("secret-b").ValidateToken(token)
	if err == nil {
		t.Error("expected validation to fail with wrong secret")
	}
}

func TestValidateTokenTampered(t *testing.T) {
	token, _ := New("secret").GenerateToken("kiosk-1")

	_, err := New("secret").ValidateToken(token + "x")
	if err == nil {
		t.Error("expected validation to fail with tampered token")
	}
}
