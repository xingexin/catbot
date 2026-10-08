package secret

import (
	"encoding/base64"
	"github.com/xingexin/catbot/internal/store"
	"strings"
	"testing"
)

func TestCredentialEncryptionAndIdentityBinding(t *testing.T) {
	t.Parallel()
	s := store.NewMemory()
	v, err := New(s, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set(t.Context(), "one", "name", "very-private-value"); err != nil {
		t.Fatal(err)
	}
	var r Record
	if err := s.Get(t.Context(), "secret", "one", &r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.Ciphertext, "very-private") {
		t.Fatal("plaintext stored")
	}
	if value, err := v.Get(t.Context(), "one"); err != nil || value != "very-private-value" {
		t.Fatal(value, err)
	}
	if err := s.Put(t.Context(), "secret", "two", r); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(t.Context(), "two"); err == nil {
		t.Fatal("ciphertext moved between credential identities")
	}
}
