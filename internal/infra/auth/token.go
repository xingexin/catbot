package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

type Tokens struct{ Key string }

func (a *Tokens) Token(scope string) string {
	m := hmac.New(sha256.New, []byte(a.Key))
	_, _ = m.Write([]byte(scope))
	return base64.RawURLEncoding.EncodeToString([]byte(scope)) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (a *Tokens) VerifyToken(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	scope := string(b)
	return scope, hmac.Equal([]byte(token), []byte(a.Token(scope)))
}
