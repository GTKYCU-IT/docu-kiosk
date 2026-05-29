package auth

import "testing"

func TestRefreshToken(t *testing.T) {
	token1 := MakeRefreshToken()
	token2 := MakeRefreshToken()

	if len(token1) != 64 {
		t.Errorf("token is not 64 bytes: %d", len(token1))
	}

	if token1 == token2 {
		t.Errorf("tokens are not unique: %s == %s", token1, token2)
	}
}
