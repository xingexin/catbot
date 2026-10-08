package plugin

import (
	"io"
	"os"
	"path/filepath"
)

func (r *Runtime) Logs(id string) (string, error) {
	paths, _ := filepath.Glob(filepath.Join(r.DataDir, "plugin-logs", id+"@*.log"))
	out := ""
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		info, _ := file.Stat()
		if info != nil && info.Size() > 32768 {
			_, _ = file.Seek(-32768, io.SeekEnd)
		}
		b, _ := io.ReadAll(io.LimitReader(file, 32768))
		_ = file.Close()
		out += string(b)
	}
	return out, nil
}
