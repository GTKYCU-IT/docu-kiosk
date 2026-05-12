package auth

import (
	"bytes"
	"encoding/base64"
	"errors"
)

func (a *Auth) GenerateToken(name string) (string, error) {
	message := []byte(name)
	mac := a.generateHMAC(message)
	payload := append(message, mac...)
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

// ValidateToken verifies the token signature and returns the kiosk name it was issued for.
func (a *Auth) ValidateToken(token string) (string, error) {
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", err
	}

	if len(data) < 32 {
		return "", errors.New("invalid token")
	}

	nameBytes := data[:len(data)-32]
	macBytes := data[len(data)-32:]

	if !bytes.Equal(macBytes, a.generateHMAC(nameBytes)) {
		return "", errors.New("invalid token")
	}

	return string(nameBytes), nil
}
