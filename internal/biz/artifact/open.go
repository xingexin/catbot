package artifact

import (
	"context"
	domainartifact "github.com/xingexin/catbot/internal/domain/artifact"
	"io"
)

func (a *Service) Open(ctx context.Context, id string) (domainartifact.Artifact, io.ReadSeekCloser, error) {
	v, err := a.Find(ctx, id)
	if err != nil {
		return v, nil, err
	}
	f, err := a.Files.Open(id)
	return v, f, err
}
