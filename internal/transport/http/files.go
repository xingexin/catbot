package httptransport

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	limit := int64(s.options.MaxUploadMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, limit+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		fail(w, fmt.Errorf("invalid upload or file exceeds %d MB", s.options.MaxUploadMB))
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		fail(w, err)
		return
	}
	defer file.Close()
	value, err := s.services.Artifacts.Upload(r.Context(), header.Filename, header.Header.Get("Content-Type"), "", file)
	respond(w, 201, value, err)
}
func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	artifact, file, err := s.services.Artifacts.Open(r.Context(), r.PathValue("id"))
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		fail(w, err)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", strings.ReplaceAll(artifact.Name, "\"", "")))
	http.ServeContent(w, r, artifact.Name, artifact.CreatedAt, file)
}
