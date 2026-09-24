package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"agentTest/internal/domain"
	"agentTest/internal/store"
)

// freeze retains self-contained plugin bundles until an administrator removes
// unused versions. Symlinks and node_modules are deliberately not executable
// package dependencies; plugin authors bundle dependencies at build time.
func (m *Manager) freeze(ctx context.Context, dir string, manifest domain.Manifest) (string, error) {
	type file struct {
		name string
		data []byte
	}
	files := []file{}
	hash := sha256.New()
	size := int64(0)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("plugin packages cannot contain symlinks")
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return errors.New("plugin package contains a nonregular file")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		size += info.Size()
		if size > 100<<20 {
			return errors.New("plugin package exceeds 100 MB")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, _ = io.WriteString(hash, rel+"\x00")
		_, _ = hash.Write(b)
		_, _ = hash.Write([]byte{0})
		files = append(files, file{rel, b})
		return nil
	})
	if err != nil {
		return "", err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	key := manifest.ID + "@" + manifest.Version
	unlock, err := m.Store.Lock(ctx, "package:"+key)
	if err != nil {
		return "", err
	}
	defer unlock()
	var old struct {
		Digest    string `json:"digest"`
		Directory string `json:"directory"`
	}
	if err := m.Store.Get(ctx, "plugin-package", key, &old); err == nil {
		if old.Digest != digest {
			return "", errors.New("package contents changed without a version bump")
		}
		if _, err := os.Stat(old.Directory); err == nil {
			return old.Directory, nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	target := filepath.Join(m.DataDir, "plugin-packages", key+"-"+digest[:16])
	if err := os.MkdirAll(target, 0700); err != nil {
		return "", err
	}
	for _, f := range files {
		path := filepath.Join(target, f.name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, f.data, 0600); err != nil {
			return "", err
		}
	}
	old.Digest = digest
	old.Directory = target
	return target, m.Store.Put(ctx, "plugin-package", key, old)
}
