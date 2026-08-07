package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestVersionEndpoint(t *testing.T) {
	_, ts := setupTestServer(t)

	res, err := http.Get(ts.URL + "/api/version")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}

	var body struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Version == "" {
		t.Error("version field is empty")
	}
	if body.Commit == "" {
		t.Error("commit field is empty")
	}
}
