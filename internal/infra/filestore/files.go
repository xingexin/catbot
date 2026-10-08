package filestore

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

type Store struct{ Root string }

func (s Store) Path(id string) string { return filepath.Join(s.Root, "files", id) }
func (s Store) Prepare() error        { return os.MkdirAll(filepath.Join(s.Root, "files"), 0700) }
func (s Store) Write(id string, src io.Reader, limit int64) (int64, error) {
	path := s.Path(id)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(f, io.LimitReader(src, limit+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || n > limit {
		_ = os.Remove(path)
		return n, errors.New("upload failed or file exceeds limit")
	}
	return n, nil
}
func (s Store) Remove(id string) error { return os.Remove(s.Path(id)) }
