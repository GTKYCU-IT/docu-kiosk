package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearer(t *testing.T) {
	cases := map[string]struct {
		input    string
		expected string
	}{
		"upper 'Bearer'":   {"Bearer mytoken123", "mytoken123"},
		"lower 'bearer'":   {"bearer mytoken123", "mytoken123"},
		"extra whitespace": {"   bearer  mytoken123         ", "mytoken123"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", tc.input)

			actual, err := GetBearerToken(req.Header)
			if err != nil {
				t.Error(err)
			}

			if actual != tc.expected {
				t.Fatalf("get bearer produced unexpected result: %s", actual)
			}
		})
	}
}
