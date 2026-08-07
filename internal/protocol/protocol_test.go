package protocol

import "testing"

func TestGreetingMarshal(t *testing.T) {
	data, err := Marshal(NewGreeting("lobby"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"name":"lobby","type":"connected"}`
	if string(data) != want {
		t.Errorf("expected %s, got %s", want, data)
	}
}

func TestSignMarshal(t *testing.T) {
	data, err := Marshal(NewSign("https://example.docusign.net/sign/abc123"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"type":"sign","url":"https://example.docusign.net/sign/abc123"}`
	if string(data) != want {
		t.Errorf("expected %s, got %s", want, data)
	}
}

func TestGreetingType(t *testing.T) {
	if got := NewGreeting("lobby").Type; got != "connected" {
		t.Errorf("expected type connected, got %q", got)
	}
}

func TestSignType(t *testing.T) {
	if got := NewSign("https://example.com").Type; got != "sign" {
		t.Errorf("expected type sign, got %q", got)
	}
}
