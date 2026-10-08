package auth

import (
	"crypto/sha256"
	"crypto/subtle"
)

func PasswordMatches(want, got string) bool {
	a, b := sha256.Sum256([]byte(want)), sha256.Sum256([]byte(got))
	return want != "" && subtle.ConstantTimeCompare(a[:], b[:]) == 1
}
