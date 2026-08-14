package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/calvertjadon/docu-kiosk/internal/auth"
	"github.com/calvertjadon/docu-kiosk/internal/database"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// emptyKeyToken simulates the pre-fix broker, which signed JWTs with a nil
// key (jwtKey was never configured): such tokens must now be rejected.
func emptyKeyToken() string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Issuer:    auth.Issuer,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		Subject:   uuid.New().String(),
	})
	ss, err := token.SignedString([]byte(nil))
	if err != nil {
		panic(err)
	}
	return ss
}

// setupAuthServer builds the broker exactly like production (NewServer) on a
// shared-cache in-memory SQLite database. Unlike the package's default test
// DB, the pool may hold several connections to the same database, so the
// concurrent-refresh test races at the SQL layer the way it would against a
// real database, and tests can inject failures (e.g. triggers) over a second
// connection.
func setupAuthServer(t *testing.T) (*server, *httptest.Server, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:auth-%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrateTestDB(t, db)
	s, err := NewServer(testConfig(), database.New(db))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.httpServer.Handler)
	t.Cleanup(ts.Close)
	return s, ts, db
}

// loginResult carries the parts of a successful login the auth tests assert
// on: the access JWT and the parsed refresh_token cookie.
type loginResult struct {
	jwt    string
	cookie *http.Cookie
}

// wantSessionCookie is the exact raw Set-Cookie the broker must emit for a
// live refresh credential: host-only Path=/, HttpOnly, Secure, SameSite=Strict,
// and no Domain, Max-Age, or Expires (a browser-session cookie).
func wantSessionCookie(value string) string {
	return "refresh_token=" + value + "; Path=/; HttpOnly; Secure; SameSite=Strict"
}

// wantClearedCookie is the exact raw Set-Cookie for logout: an empty value
// and Max-Age=0, since deleting a cookie requires an expiry.
const wantClearedCookie = "refresh_token=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Strict"

// assertSessionCookie verifies the raw header and parsed attributes of a
// live refresh credential cookie.
func assertSessionCookie(t *testing.T, raw string, c *http.Cookie) {
	t.Helper()
	if raw != wantSessionCookie(c.Value) {
		t.Errorf("Set-Cookie = %q, want %q", raw, wantSessionCookie(c.Value))
	}
	if c.Name != refreshTokenCookie || c.Path != "/" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie = name %q path %q HttpOnly %v Secure %v SameSite %v; want the session-cookie attributes", c.Name, c.Path, c.HttpOnly, c.Secure, c.SameSite)
	}
	if c.Domain != "" || c.MaxAge != 0 || c.RawExpires != "" || !c.Expires.IsZero() {
		t.Errorf("cookie carries persistence attributes (Domain/Max-Age/Expires): %+v", c)
	}
}

// assertJWTOnlyBody verifies the exact JSON member set of a successful
// login/refresh response: the access JWT and nothing else — the refresh
// credential never appears in a body.
func assertJWTOnlyBody(t *testing.T, body string) string {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, body)
	}
	if len(payload) != 1 {
		t.Fatalf("body member set = %v, want exactly [jwt]", memberNames(payload))
	}
	var jwt string
	if err := json.Unmarshal(payload["jwt"], &jwt); err != nil || jwt == "" {
		t.Fatalf("body missing jwt: %s", body)
	}
	return jwt
}

func memberNames(m map[string]json.RawMessage) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	return names
}

// doLogin posts credentials and returns the access JWT plus the refresh
// credential exactly as the broker handed it to a browser (raw Set-Cookie and
// the parsed cookie).
func doLogin(t *testing.T, ts *httptest.Server, username, password string) loginResult {
	t.Helper()
	body := strings.NewReader(fmt.Sprintf(`{"username": %q, "password": %q}`, username, password))
	res, err := http.Post(ts.URL+"/login", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", res.StatusCode, data)
	}
	assertNoStore(t, res)

	jwt := assertJWTOnlyBody(t, string(data))

	raw := res.Header.Get("Set-Cookie")
	cookie, err := http.ParseSetCookie(raw)
	if err != nil || cookie == nil || cookie.Name != refreshTokenCookie {
		t.Fatalf("login did not set a refresh_token cookie: %q (%v)", raw, err)
	}
	assertSessionCookie(t, raw, cookie)
	return loginResult{jwt: jwt, cookie: cookie}
}

// readBody drains and closes the response body, returning its contents.
func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// assertNoStore requires the response to be uncacheable, as every auth
// endpoint response — success or error — must be.
func assertNoStore(t *testing.T, res *http.Response) {
	t.Helper()
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// assertBearerChallenge requires a rejected access JWT to advertise the
// Bearer scheme, as every 401 from the auth middleware must.
func assertBearerChallenge(t *testing.T, res *http.Response) {
	t.Helper()
	if got := res.Header.Get("WWW-Authenticate"); got != "Bearer" {
		t.Errorf("WWW-Authenticate = %q, want Bearer", got)
	}
}

// refreshPosts posts to /refresh with the refresh credential carried only in
// the refresh_token cookie (the contract's sole transport) and returns the
// response; callers must close the body.
func refreshPosts(t *testing.T, ts *httptest.Server, refreshToken string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/refresh", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: refreshTokenCookie, Value: refreshToken})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// logoutPosts posts to /logout with the refresh credential carried only in
// the refresh_token cookie. An empty refreshToken sends no cookie at all.
func logoutPosts(t *testing.T, ts *httptest.Server, refreshToken string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	if refreshToken != "" {
		req.AddCookie(&http.Cookie{Name: refreshTokenCookie, Value: refreshToken})
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// writeAdminIndex materializes the SPA entry (WebDistDir/index.html, the
// static root the broker resolves relative to its working directory) so
// /admin serving can be exercised without a frontend build. An existing file
// is left untouched.
func writeAdminIndex(t *testing.T) {
	t.Helper()
	distDir := WebDistDir
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(distDir, "index.html")
	const marker = "<!doctype html><html><body>admin-spa</body></html>\n"
	if _, err := os.Stat(indexPath); err == nil {
		return
	}
	if err := os.WriteFile(indexPath, []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(indexPath)
		_ = os.Remove(distDir)
		_ = os.Remove(filepath.Dir(distDir))
	})
}

func TestLoginSuccess(t *testing.T) {
	_, ts, _ := setupAuthServer(t)

	login := doLogin(t, ts, "admin", "admin1234")
	if login.jwt == "" || login.cookie.Value == "" {
		t.Fatal("expected jwt and refresh cookie")
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	_, ts, _ := setupAuthServer(t)

	res, err := http.Post(ts.URL+"/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"wrong-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
	assertNoStore(t, res)
	if got := res.Header.Get("Set-Cookie"); got != "" {
		t.Errorf("rejected login set a cookie: %q", got)
	}
	// The failure body stays the opaque generic text: no credential detail.
	if string(body) != `{"error":"invalid credentials"}` {
		t.Errorf("error body = %s, want the generic invalid-credentials text", body)
	}
}

func TestLoginDoesNotEnumerateUsers(t *testing.T) {
	_, ts, _ := setupAuthServer(t)

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
	_, ts, _ := setupAuthServer(t)

	res, err := http.Post(ts.URL+"/login", "application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	assertNoStore(t, res)
}

func TestRefreshRequiresCookie(t *testing.T) {
	_, ts, _ := setupAuthServer(t)
	login := doLogin(t, ts, "admin", "admin1234")

	// A bearer header is not a fallback transport for rotation: the refresh
	// credential is accepted only via the cookie.
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/refresh", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+login.cookie.Value)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refresh with bearer but no cookie: status = %d, want 401 (body %s)", res.StatusCode, body)
	}
	assertNoStore(t, res)
	if got := res.Header.Get("Set-Cookie"); got != "" {
		t.Errorf("rejected refresh set a cookie: %q", got)
	}

	// No cookie at all is rejected the same way.
	res2, err := http.Post(ts.URL+"/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refresh without cookie: status = %d, want 401", res2.StatusCode)
	}
	assertNoStore(t, res2)
}

func TestRefreshRotatesToken(t *testing.T) {
	_, ts, _ := setupAuthServer(t)
	first := doLogin(t, ts, "admin", "admin1234")

	res := refreshPosts(t, ts, first.cookie.Value)
	body := readBody(t, res)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("refresh status = %d, body = %s", res.StatusCode, body)
	}
	assertNoStore(t, res)
	if jwt := assertJWTOnlyBody(t, body); jwt == "" {
		t.Fatal("refresh response missing jwt")
	}
	rotated := res.Header.Get("Set-Cookie")
	cookie, err := http.ParseSetCookie(rotated)
	if err != nil || cookie == nil {
		t.Fatalf("refresh did not set a refresh_token cookie: %q (%v)", rotated, err)
	}
	assertSessionCookie(t, rotated, cookie)
	if cookie.Value == first.cookie.Value {
		t.Error("refresh token was not rotated")
	}

	// Replaying the rotated-away value must now fail, and the rejection must
	// not mint a successor.
	res2 := refreshPosts(t, ts, first.cookie.Value)
	body2 := readBody(t, res2)
	if res2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed refresh status = %d, want 401 (body %s)", res2.StatusCode, body2)
	}
	assertNoStore(t, res2)
	if got := res2.Header.Get("Set-Cookie"); got != "" {
		t.Errorf("replayed refresh set a cookie: %q", got)
	}
}

func TestRefreshConcurrentExchangeSingleSuccess(t *testing.T) {
	_, ts, _ := setupAuthServer(t)
	login := doLogin(t, ts, "admin", "admin1234")

	// Two concurrent exchanges of the same credential must produce exactly
	// one success and one rejection: rotation is a single atomic step, so
	// the loser gets no successor and no new cookie.
	const attempts = 2
	statuses := make(chan int, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodPost, ts.URL+"/refresh", nil)
			if err != nil {
				statuses <- -1
				return
			}
			req.AddCookie(&http.Cookie{Name: refreshTokenCookie, Value: login.cookie.Value})
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				statuses <- -1
				return
			}
			defer res.Body.Close()
			_, _ = io.Copy(io.Discard, res.Body)
			statuses <- res.StatusCode
		}()
	}
	wg.Wait()
	close(statuses)

	var ok, unauthorized int
	for status := range statuses {
		switch status {
		case http.StatusOK:
			ok++
		case http.StatusUnauthorized:
			unauthorized++
		default:
			t.Errorf("concurrent refresh status = %d, want 200 or 401", status)
		}
	}
	if ok != 1 || unauthorized != 1 {
		t.Fatalf("concurrent exchange produced %dx200 and %dx401, want exactly one of each", ok, unauthorized)
	}
}

func TestLogoutSuccess(t *testing.T) {
	_, ts, _ := setupAuthServer(t)
	login := doLogin(t, ts, "admin", "admin1234")

	res := logoutPosts(t, ts, login.cookie.Value)
	body := readBody(t, res)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204 (body %s)", res.StatusCode, body)
	}
	assertNoStore(t, res)
	if got := res.Header.Get("Set-Cookie"); got != wantClearedCookie {
		t.Errorf("logout Set-Cookie = %q, want %q", got, wantClearedCookie)
	}
	if body != "" {
		t.Errorf("logout body = %q, want empty", body)
	}

	// The revoked credential is dead server-side: it can neither refresh nor
	// sign out a second time.
	res2 := refreshPosts(t, ts, login.cookie.Value)
	body2 := readBody(t, res2)
	if res2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refresh after logout status = %d, want 401 (body %s)", res2.StatusCode, body2)
	}
	res3 := logoutPosts(t, ts, login.cookie.Value)
	readBody(t, res3)
	if res3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("second logout status = %d, want 401", res3.StatusCode)
	}
}

func TestLogoutRequiresCookie(t *testing.T) {
	_, ts, _ := setupAuthServer(t)
	doLogin(t, ts, "admin", "admin1234")

	// No cookie at all.
	res := logoutPosts(t, ts, "")
	body := readBody(t, res)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("logout without cookie status = %d, want 401 (body %s)", res.StatusCode, body)
	}
	assertNoStore(t, res)
	if got := res.Header.Get("Set-Cookie"); got != "" {
		t.Errorf("rejected logout set a cookie: %q", got)
	}

	// An unknown cookie value.
	res2 := logoutPosts(t, ts, "not-a-real-token")
	readBody(t, res2)
	if res2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("logout with unknown cookie status = %d, want 401", res2.StatusCode)
	}
	assertNoStore(t, res2)
	if got := res2.Header.Get("Set-Cookie"); got != "" {
		t.Errorf("rejected logout set a cookie: %q", got)
	}
}

func TestLogoutInternalFailurePreservesCookie(t *testing.T) {
	_, ts, db := setupAuthServer(t)
	login := doLogin(t, ts, "admin", "admin1234")

	// Make revocation fail while lookups still work: any UPDATE on
	// refresh_tokens aborts, which is exactly the persistence failure the
	// 500 path exists for.
	if _, err := db.Exec(`CREATE TRIGGER fail_revoke BEFORE UPDATE ON refresh_tokens BEGIN SELECT RAISE(ABORT, 'injected revoke failure'); END;`); err != nil {
		t.Fatal(err)
	}

	res := logoutPosts(t, ts, login.cookie.Value)
	body := readBody(t, res)
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("logout status = %d, want 500 (body %s)", res.StatusCode, body)
	}
	assertNoStore(t, res)
	if got := res.Header.Get("Set-Cookie"); got != "" {
		t.Errorf("failed logout cleared the cookie: %q", got)
	}

	// The credential was never revoked: once the failure is removed the
	// same cookie still refreshes, so a 500 must not have burned it.
	if _, err := db.Exec(`DROP TRIGGER fail_revoke`); err != nil {
		t.Fatal(err)
	}
	res2 := refreshPosts(t, ts, login.cookie.Value)
	body2 := readBody(t, res2)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("refresh after failed logout status = %d, want 200 (body %s)", res2.StatusCode, body2)
	}
}

func TestSessionsAreIndependent(t *testing.T) {
	_, ts, _ := setupAuthServer(t)
	a := doLogin(t, ts, "admin", "admin1234")
	b := doLogin(t, ts, "admin", "admin1234")
	if a.cookie.Value == b.cookie.Value {
		t.Fatal("two logins produced the same refresh credential")
	}

	// Rotating A must not touch B.
	resA := refreshPosts(t, ts, a.cookie.Value)
	readBody(t, resA)
	if resA.StatusCode != http.StatusOK {
		t.Fatalf("refresh A status = %d, want 200", resA.StatusCode)
	}
	rotatedA, err := http.ParseSetCookie(resA.Header.Get("Set-Cookie"))
	if err != nil || rotatedA == nil {
		t.Fatalf("refresh A did not set a refresh_token cookie: %v", err)
	}
	resB := refreshPosts(t, ts, b.cookie.Value)
	readBody(t, resB)
	if resB.StatusCode != http.StatusOK {
		t.Fatalf("refresh B status = %d, want 200", resB.StatusCode)
	}
	rotatedB, err := http.ParseSetCookie(resB.Header.Get("Set-Cookie"))
	if err != nil || rotatedB == nil {
		t.Fatalf("refresh B did not set a refresh_token cookie: %v", err)
	}

	// Signing out of A (its live value) leaves B usable, and A's
	// rotated-away value stays dead.
	resLogoutA := logoutPosts(t, ts, rotatedA.Value)
	readBody(t, resLogoutA)
	if resLogoutA.StatusCode != http.StatusNoContent {
		t.Fatalf("logout A status = %d, want 204", resLogoutA.StatusCode)
	}
	resReplayA := refreshPosts(t, ts, a.cookie.Value)
	readBody(t, resReplayA)
	if resReplayA.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refresh A after logout status = %d, want 401", resReplayA.StatusCode)
	}
	resB2 := refreshPosts(t, ts, rotatedB.Value)
	readBody(t, resB2)
	if resB2.StatusCode != http.StatusOK {
		t.Fatalf("refresh B after A logout status = %d, want 200", resB2.StatusCode)
	}
}

func TestAccessJWTSurvivesLogout(t *testing.T) {
	_, ts, _ := setupAuthServer(t)
	login := doLogin(t, ts, "admin", "admin1234")

	res := logoutPosts(t, ts, login.cookie.Value)
	readBody(t, res)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", res.StatusCode)
	}

	// Logout revokes only the refresh credential; the short-lived access
	// JWT remains valid until it expires on its own.
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/protected", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+login.jwt)
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("protected with pre-logout jwt status = %d, want 200", res2.StatusCode)
	}
}

func TestProtectedRequiresToken(t *testing.T) {
	_, ts, _ := setupAuthServer(t)

	res, err := http.Get(ts.URL + "/protected")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
	assertBearerChallenge(t, res)
}

func TestProtectedRejectsInvalidToken(t *testing.T) {
	_, ts, _ := setupAuthServer(t)

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
	assertBearerChallenge(t, res)
}

func TestProtectedAcceptsValidToken(t *testing.T) {
	_, ts, _ := setupAuthServer(t)

	login := doLogin(t, ts, "admin", "admin1234")

	req, err := http.NewRequest("GET", ts.URL+"/protected", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+login.jwt)
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
	_, ts, _ := setupAuthServer(t)

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
	assertBearerChallenge(t, res)
}

func TestAdminServesSPAEntry(t *testing.T) {
	writeAdminIndex(t)
	_, ts, _ := setupAuthServer(t)

	for _, path := range []string{"/admin", "/admin/"} {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body := readBody(t, res)
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", path, res.StatusCode)
		}
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s Content-Type = %q, want text/html", path, ct)
		}
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(body)), "<!doctype html") {
			t.Errorf("GET %s body = %q, want the SPA entry document", path, body)
		}
	}

	// HEAD is a safe navigation method: it must answer without a body.
	req, err := http.NewRequest(http.MethodHead, ts.URL+"/admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, res)
	if res.StatusCode != http.StatusOK {
		t.Errorf("HEAD /admin status = %d, want 200", res.StatusCode)
	}
	if body != "" {
		t.Errorf("HEAD /admin body = %q, want empty", body)
	}
}

func TestAdminFallbackRejectsUnsafeMethods(t *testing.T) {
	writeAdminIndex(t)
	_, ts, _ := setupAuthServer(t)

	// The fallback answers only navigation methods: any other method is 405,
	// so it can never shadow API or kiosk routes.
	for _, path := range []string{"/admin", "/admin/"} {
		res, err := http.Post(ts.URL+path, "text/plain", strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		readBody(t, res)
		if res.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST %s status = %d, want 405", path, res.StatusCode)
		}
		if allow := res.Header.Get("Allow"); !strings.Contains(allow, "GET") || !strings.Contains(allow, "HEAD") {
			t.Errorf("POST %s Allow = %q, want GET, HEAD", path, allow)
		}
	}
}

func TestAdminFallbackDoesNotShadowRoutes(t *testing.T) {
	writeAdminIndex(t)
	_, ts, _ := setupAuthServer(t)

	// The admin fallback must not intercept the auth or API surface.
	login := doLogin(t, ts, "admin", "admin1234")
	if login.jwt == "" {
		t.Fatal("login failed behind the admin fallback")
	}
	res, err := http.Get(ts.URL + "/api/version")
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, res)
	if res.StatusCode != http.StatusOK {
		t.Errorf("GET /api/version status = %d, want 200 (body %s)", res.StatusCode, body)
	}
}
