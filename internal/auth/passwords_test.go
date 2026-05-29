package auth

import "testing"

func TestHashEmptyPassword(t *testing.T) {
	hash, err := HashPassword("")
	if err != nil {
		t.Error(err)
	}

	if hash == "" {
		t.Error("hash result should not be empty")
	}

	match := CheckPasswordHash("", hash)
	if !match {
		t.Errorf("bad hash result: %s", hash)
	}
}
