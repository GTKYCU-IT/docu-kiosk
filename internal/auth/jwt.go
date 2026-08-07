package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// issuer is the JWT "iss" claim. Validation requires it to match, so tokens
// minted for one broker deployment cannot be replayed against another.
const issuer = "docu-kiosk"

func generateJWT(userID uuid.UUID, key []byte, expiresIn time.Duration) (string, error) {
	now := time.Now().UTC()
	claims := &jwt.RegisteredClaims{
		Issuer:    issuer,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(expiresIn)),
		Subject:   userID.String(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	ss, err := token.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return ss, nil
}

func validateJWT(tokenString string, key []byte) (uuid.UUID, error) {
	claims := jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(
		tokenString,
		&claims,
		func(token *jwt.Token) (any, error) {
			return key, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(issuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return uuid.UUID{}, err
	}

	subject, err := token.Claims.GetSubject()
	if err != nil {
		return uuid.UUID{}, err
	}

	userID, err := uuid.Parse(subject)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("jwt subject is not a uuid: %w", err)
	}
	return userID, nil
}
