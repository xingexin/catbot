package idgen

import (
	"crypto/rand"
	"encoding/hex"
)

// New creates an opaque identifier with the existing persisted format.
func New() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
