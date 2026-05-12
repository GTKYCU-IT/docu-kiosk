package auth

import (
	"bytes"
	"testing"
)

func TestGenerateHMAC(t *testing.T) {
	message := "hello world"
	secret := "supersecretkey"

	auth1 := New(secret)
	auth2 := New(secret)

	hmac1 := auth1.generateHMAC([]byte(message))
	hmac2 := auth2.generateHMAC([]byte(message))

	if !bytes.Equal(hmac1, hmac2) {
		t.Errorf("unexpected hmac result. %v != %v", hmac1, hmac2)
	}
}

func TestGenerateHMACDifferentKey(t *testing.T) {
	message := "hello world"

	auth1 := New("key 1")
	auth2 := New("key 2")

	hmac1 := auth1.generateHMAC([]byte(message))
	hmac2 := auth2.generateHMAC([]byte(message))

	if bytes.Equal(hmac1, hmac2) {
		t.Errorf("unexpected hmac result. %v == %v", hmac1, hmac2)
	}
}

func TestGenerateHMACDifferentMessage(t *testing.T) {
	secret := "supersecretkey"

	auth1 := New(secret)
	auth2 := New(secret)

	hmac1 := auth1.generateHMAC([]byte("message 1"))
	hmac2 := auth2.generateHMAC([]byte("message 2"))

	if bytes.Equal(hmac1, hmac2) {
		t.Errorf("unexpected hmac result. %v == %v", hmac1, hmac2)
	}
}
