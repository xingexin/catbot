package artifact

import (
	"context"
	"fmt"
	artifactdomain "github.com/xingexin/catbot/internal/domain/artifact"
	"github.com/xingexin/catbot/internal/infra/filestore"
	"github.com/xingexin/catbot/internal/infra/idgen"
	"github.com/xingexin/catbot/internal/infra/store"
	"io"
	"path/filepath"
	"time"
)

type Service struct {
	Store       store.Store
	Files       filestore.Store
	MaxUploadMB int
}

func (a *Service) Upload(ctx context.Context, name, mime, pluginID string, src io.Reader) (artifactdomain.Artifact, error) {
	v := artifactdomain.Artifact{ID: idgen.New(), Name: filepath.Base(name), Mime: mime, PluginID: pluginID, CreatedAt: time.Now().UTC()}
	n, err := a.Files.Write(v.ID, src, int64(a.MaxUploadMB)<<20)
	if err != nil {
		return v, fmt.Errorf("upload failed or file exceeds %d MB", a.MaxUploadMB)
	}
	v.Size = n
	if err := a.Store.Put(ctx, "artifact", v.ID, v); err != nil {
		_ = a.Files.Remove(v.ID)
		return v, err
	}
	return v, nil
}
func (a *Service) Find(ctx context.Context, id string) (artifactdomain.Artifact, error) {
	var v artifactdomain.Artifact
	err := a.Store.Get(ctx, "artifact", id, &v)
	return v, err
}
func (a *Service) SaveResult(ctx context.Context, v artifactdomain.Artifact) (artifactdomain.Artifact, error) {
	return v, a.Store.Put(ctx, "artifact", v.ID, v)
}
