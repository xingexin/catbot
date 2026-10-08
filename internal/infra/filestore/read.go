package filestore

import (
	"io"
	"os"
)

func (s Store) Open(id string) (io.ReadSeekCloser, error) { return os.Open(s.Path(id)) }
