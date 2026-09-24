package secret

import (
	"agentTest/internal/store"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
)

type Vault struct {
	store store.Store
	aead  cipher.AEAD
}
type Record struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Ciphertext string `json:"ciphertext"`
}

func New(s store.Store, key string) (*Vault, error) {
	b, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(b) != 32 {
		return nil, errors.New("MASTER_KEY must be base64 encoded 32 bytes")
	}
	block, err := aes.NewCipher(b)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{store: s, aead: aead}, nil
}
func (v *Vault) Set(ctx context.Context, id, name, value string) error {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sealed := v.aead.Seal(nonce, nonce, []byte(value), []byte(id))
	return v.store.Put(ctx, "secret", id, Record{ID: id, Name: name, Ciphertext: base64.StdEncoding.EncodeToString(sealed)})
}
func (v *Vault) Get(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	var r Record
	if err := v.store.Get(ctx, "secret", id, &r); err != nil {
		return "", err
	}
	b, err := base64.StdEncoding.DecodeString(r.Ciphertext)
	if err != nil || len(b) < v.aead.NonceSize() {
		return "", errors.New("invalid encrypted credential")
	}
	n := v.aead.NonceSize()
	plain, err := v.aead.Open(nil, b[:n], b[n:], []byte(id))
	if err != nil {
		return "", errors.New("credential decryption failed")
	}
	return string(plain), nil
}
