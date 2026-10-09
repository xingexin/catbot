package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	domainplugin "github.com/xingexin/catbot/internal/domain/plugin"
	"github.com/xingexin/catbot/internal/infra/store"
)

type Packages struct {
	Store   store.Store
	Root    string
	DataDir string
}

func NewPackages(s store.Store, root, data string) *Packages {
	return &Packages{Store: s, Root: root, DataDir: data}
}

func (p *Packages) Read(dir string) (string, domainplugin.Manifest, error) {
	root, err := filepath.EvalSymlinks(p.Root)
	if err != nil {
		return "", domainplugin.Manifest{}, err
	}
	full, err := filepath.EvalSymlinks(filepath.Join(root, dir))
	if err != nil {
		return "", domainplugin.Manifest{}, err
	}
	rel, err := filepath.Rel(root, full)
	if err != nil || !filepath.IsLocal(rel) {
		return "", domainplugin.Manifest{}, errors.New("plugin must be inside PLUGIN_DIR")
	}
	b, err := os.ReadFile(filepath.Join(full, "plugin.json"))
	if err != nil {
		return "", domainplugin.Manifest{}, err
	}
	var manifest domainplugin.Manifest
	if err = json.Unmarshal(b, &manifest); err != nil {
		return "", domainplugin.Manifest{}, err
	}
	if err := domainplugin.ValidateManifestIdentity(manifest); err != nil {
		return "", manifest, err
	}
	if _, err := packageEntry(full, manifest.Entry); err != nil {
		return "", manifest, err
	}
	return full, manifest, nil
}

// packageEntry verifies that the entry will survive freezing and cannot resolve
// outside the package. Symlink components are rejected just like bundle files.
func packageEntry(dir, entry string) (string, error) {
	if !filepath.IsLocal(entry) || filepath.Clean(entry) == "." {
		return "", errors.New("entry must be a file inside plugin package")
	}
	rel := filepath.Clean(entry)
	path := dir
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		if excludedPackageName(part) {
			return "", errors.New("entry cannot be in a hidden path or node_modules; bundle it inside plugin package")
		}
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil {
			return "", fmt.Errorf("read plugin entry: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("plugin entry cannot contain symlinks")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return "", errors.New("plugin entry parent must be a directory")
		}
		if i == len(parts)-1 && !info.Mode().IsRegular() {
			return "", errors.New("plugin entry must be a regular file")
		}
	}
	return rel, nil
}

func excludedPackageName(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules"
}

func prepareBinaryEntry(dir string, manifest domainplugin.Manifest) error {
	if manifest.Runtime != "binary" {
		return nil
	}
	entry, err := packageEntry(dir, manifest.Entry)
	if err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(dir, entry), 0700); err != nil {
		return fmt.Errorf("make binary plugin entry executable: %w", err)
	}
	return nil
}

// Freeze retains self-contained plugin bundles until an administrator removes
// unused versions. Symlinks and node_modules are deliberately not executable
// package dependencies; plugin authors bundle dependencies at build time.
func (p *Packages) Freeze(ctx context.Context, dir string, manifest domainplugin.Manifest) (string, error) {
	if err := domainplugin.ValidateManifestIdentity(manifest); err != nil {
		return "", err
	}
	entry, err := packageEntry(dir, manifest.Entry)
	if err != nil {
		return "", err
	}
	type file struct {
		name string
		data []byte
	}
	files := []file{}
	hash := sha256.New()
	size := int64(0)
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
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
		if excludedPackageName(d.Name()) {
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
	unlock, err := p.Store.Lock(ctx, "package:"+key)
	if err != nil {
		return "", err
	}
	defer unlock()
	var old struct {
		Digest    string `json:"digest"`
		Directory string `json:"directory"`
	}
	if err := p.Store.Get(ctx, "plugin-package", key, &old); err == nil {
		if old.Digest != digest {
			return "", errors.New("package contents changed without a version bump")
		}
		if _, err := os.Stat(old.Directory); err == nil {
			return old.Directory, prepareBinaryEntry(old.Directory, manifest)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	target := filepath.Join(p.DataDir, "plugin-packages", key+"-"+digest[:16])
	if err := os.MkdirAll(target, 0700); err != nil {
		return "", err
	}
	for _, f := range files {
		path := filepath.Join(target, f.name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return "", err
		}
		mode := os.FileMode(0600)
		if manifest.Runtime == "binary" && f.name == entry {
			mode = 0700
		}
		if err := os.WriteFile(path, f.data, mode); err != nil {
			return "", err
		}
		if err := os.Chmod(path, mode); err != nil {
			return "", err
		}
	}
	old.Digest = digest
	old.Directory = target
	return target, p.Store.Put(ctx, "plugin-package", key, old)
}
