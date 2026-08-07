// Package protocol defines the typed wire messages exchanged with kiosks.
package protocol

import "encoding/json"

// Message is the sealed interface implemented by every kiosk wire message.
// The unexported isMessage method keeps implementations confined to this
// package.
type Message interface {
	isMessage()
}

// Greeting is sent to a kiosk when it connects.
type Greeting struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func (Greeting) isMessage() {}

// NewGreeting returns a connection greeting for the named kiosk.
func NewGreeting(name string) Greeting {
	return Greeting{Name: name, Type: "connected"}
}

// Sign instructs a kiosk to open a signing session at URL.
type Sign struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

func (Sign) isMessage() {}

// NewSign returns a sign message for the given URL.
func NewSign(url string) Sign {
	return Sign{Type: "sign", URL: url}
}

// Marshal is the single wire-marshal path for kiosk messages. All messages
// sent to kiosks must be serialized through this function.
func Marshal(m Message) ([]byte, error) {
	return json.Marshal(m)
}
