package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// emptyKeyToken simulates the pre-fix broker, which signed JWTs with a nil
// key (jwtKey was never configured): such tokens must now be rejected.
func emptyKeyToken() string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Issuer:    "docu-kiosk",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		Subject:   uuid.New().String(),
	})
	ss, err := token.SignedString([]byte(nil))
	if err != nil {
		panic(err)
	}
	return ss
}

// loginResponse mirrors the JSON returned by POST /login and POST /refresh.
type loginResponse struct {
	JWT          string `json:"jwt"`
	RefreshToken string `json:"refresh_token"`
}

// doLogin posts credentials and decodes a successful response.
func doLogin(t *testing.T, ts *httptest.Server, username, password string) loginResponse {
	t.Helper()
	status, body := login(t, ts, username, password)
	if status != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", status, body)
	}
	var resp loginResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("login body is not JSON: %v (%s)", err, body)
	}
	if resp.JWT == "" || resp.RefreshToken == "" {
		t.Fatalf("login response missing tokens: %+v", resp)
	}
	return resp
}

func TestLoginSuccess(t *testing.T) {
	_, ts := setupTestServer(t)

	resp := doLogin(t, ts, "admin", "admin1234")
	if resp.JWT == "" || resp.RefreshToken == "" {
		t.Fatal("expected jwt and refresh token")
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	_, ts := setupTestServer(t)

	status, _ := login(t, ts, "admin", "wrong-password")
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

func TestLoginDoesNotEnumerateUsers(t *testing.T) {
	_, ts := setupTestServer(t)

	// Both responses must be byte-identical: an attacker must not be able to
	// tell an unknown username from a wrong password.
	unknownStatus, unknownBody := login(t, ts, "nobody", "whatever")
	wrongPassStatus, wrongPassBody := login(t, ts, "admin", "whatever")

	if unknownStatus != http.StatusUnauthorized || wrongPassStatus != http.StatusUnauthorized {
		t.Fatalf("statuses = %d, %d; want 401, 401", unknownStatus, wrongPassStatus)
	}
	if unknownBody != wrongPassBody {
		t.Errorf("responses differ — user enumeration possible:\nunknown: %s\nwrongpw: %s", unknownBody, wrongPassBody)
	}
}

func TestLoginBadJSON(t *testing.T) {
	_, ts := setupTestServer(t)

	res, err := http.Post(ts.URL+"/login", "application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
}

func TestRefreshRotatesToken(t *testing.T) {
	_, ts := setupTestServer(t)

	first := doLogin(t, ts, "admin", "admin1234")

	// Exchange the refresh token once.
	req, err := http.NewRequest("POST", ts.URL+"/refresh", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+first.RefreshToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("refresh status = %d, body = %s", res.StatusCode, body)
	}
	var second loginResponse
	if err := json.Unmarshal(body, &second); err != nil {
		t.Fatal(err)
	}
	if second.RefreshToken == "" || second.JWT == "" {
		t.Fatal("refresh response missing tokens")
	}
	if second.RefreshToken == first.RefreshToken {
		t.Error("refresh token was not rotated")
	}

	// Replaying the old refresh token must now fail.
	req2, err := http.NewRequest("POST", ts.URL+"/refresh", nil)
	if err != nil {
		t.Fatal(err)
	}
	req2.Header.Set("Authorization", "Bearer "+first.RefreshToken)
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed refresh status = %d, want 401", res2.StatusCode)
	}
}

func TestProtectedRequiresToken(t *testing.T) {
	_, ts := setupTestServer(t)

	res, err := http.Get(ts.URL + "/protected")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
}

func TestProtectedRejectsInvalidToken(t *testing.T) {
	_, ts := setupTestServer(t)

	req, err := http.NewRequest("GET", ts.URL+"/protected", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
}

func TestProtectedAcceptsValidToken(t *testing.T) {
	_, ts := setupTestServer(t)

	resp := doLogin(t, ts, "admin", "admin1234")

	req, err := http.NewRequest("GET", ts.URL+"/protected", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+resp.JWT)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
}

func TestProtectedRejectsForgedToken(t *testing.T) {
	_, ts := setupTestServer(t)

	// A token signed with an empty key must not pass validation. This is the
	// exact forgery the pre-fix broker accepted (jwtKey was never set).
	req, err := http.NewRequest("GET", ts.URL+"/protected", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", emptyKeyToken()))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged token status = %d, want 401", res.StatusCode)
	}
}
