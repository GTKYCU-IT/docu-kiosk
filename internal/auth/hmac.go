package auth

import (
	"crypto/hmac"
	"crypto/sha256"
)

func (a *Auth) generateHMAC(message []byte) []byte {
	mac := hmac.New(sha256.New, a.key())
	mac.Write(message)
	return mac.Sum(nil)
}
