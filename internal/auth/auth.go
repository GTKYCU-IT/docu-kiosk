// Package auth
package auth

type Auth struct {
	sharedSecret string
}

func New(sharedSecret string) *Auth {
	return &Auth{sharedSecret: sharedSecret}
}

func (a *Auth) key() []byte {
	return []byte(a.sharedSecret)
}
