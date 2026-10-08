package persona

import (
	"context"
	"github.com/xingexin/catbot/internal/domain/persona"
	personarepo "github.com/xingexin/catbot/internal/domain/persona/repository"
	"github.com/xingexin/catbot/internal/infra/idgen"
	"github.com/xingexin/catbot/internal/infra/store"
)

type Service struct{ Store store.Store }

func (a *Service) Save(ctx context.Context, p persona.Persona) (persona.Persona, error) {
	if err := persona.Validate(p); err != nil {
		return p, err
	}
	unlock, err := a.Store.Lock(ctx, "personas")
	if err != nil {
		return p, err
	}
	defer unlock()
	if p.ID == "" {
		p.ID = idgen.New()
	}
	var old persona.Persona
	if err := personarepo.New(a.Store).Get(ctx, p.ID, &old); err == nil {
		p.Version = old.Version + 1
	} else {
		p.Version = 1
	}
	if p.Default {
		all, err := personarepo.New(a.Store).List(ctx)
		if err != nil {
			return p, err
		}
		for _, other := range all {
			if other.ID != p.ID && other.Default {
				other.Default = false
				if err := personarepo.New(a.Store).Save(ctx, other); err != nil {
					return p, err
				}
			}
		}
	}
	if err := personarepo.New(a.Store).Save(ctx, p); err != nil {
		return p, err
	}
	return p, nil
}
func (a *Service) Bootstrap(ctx context.Context) error {
	ps, err := personarepo.New(a.Store).List(ctx)
	if err != nil {
		return err
	}
	if len(ps) > 0 {
		return nil
	}
	p := persona.Persona{ID: "secretary", Name: "小助理", Description: "清楚、可靠的个人秘书", SystemPrompt: "你是一位个人秘书。用简洁自然的中文沟通，记清用户要求，通过工具完成事务，诚实说明执行状态。", Examples: []persona.Example{}, Version: 1, Default: true}
	return personarepo.New(a.Store).Save(ctx, p)
}
