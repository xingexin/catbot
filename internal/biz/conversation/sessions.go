package conversation

import (
	"context"
	"errors"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	convrepo "github.com/xingexin/catbot/internal/domain/conversation/repository"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepo "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/domain/persona"
	personarepo "github.com/xingexin/catbot/internal/domain/persona/repository"
	"github.com/xingexin/catbot/internal/infra/idgen"
	"github.com/xingexin/catbot/internal/infra/store"
)

type SessionInput struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	ConfigID  string `json:"configId"`
	PersonaID string `json:"personaId"`
}

func (a *Service) SaveSession(ctx context.Context, in SessionInput) (conversation.Session, error) {
	if in.ID == "" {
		in.ID = idgen.New()
	}
	metaUnlock, err := a.Store.Lock(ctx, "session-meta:"+in.ID)
	if err != nil {
		return conversation.Session{}, err
	}
	defer metaUnlock()
	if err := lifecycleRepo.RequireActive(ctx, a.Store, lifecycle.ResourceSession, in.ID); err != nil {
		return conversation.Session{}, err
	}

	unlock, err := a.Store.Lock(ctx, "session:"+in.ID)
	if err != nil {
		return conversation.Session{}, err
	}
	defer unlock()
	referencesUnlock, err := a.Store.Lock(ctx, lifecycle.ReferenceLock)
	if err != nil {
		return conversation.Session{}, err
	}
	defer referencesUnlock()
	for resource, id := range map[lifecycle.Resource]string{lifecycle.ResourceConfig: in.ConfigID, lifecycle.ResourcePersona: in.PersonaID} {
		if err := lifecycleRepo.RequireActive(ctx, a.Store, resource, id); err != nil {
			return conversation.Session{}, err
		}
	}
	var c agent.Config
	var p persona.Persona
	if err := a.Store.Get(ctx, "config", in.ConfigID, &c); err != nil {
		return conversation.Session{}, err
	}
	if err := personarepo.New(a.Store).Get(ctx, in.PersonaID, &p); err != nil {
		return conversation.Session{}, err
	}
	s := conversation.Session{ID: in.ID, Channel: "web", Messages: []agent.Message{}, Native: map[string]string{}}
	if err := convrepo.New(a.Store).GetSession(ctx, in.ID, &s); err != nil && !errors.Is(err, store.ErrNotFound) {
		return conversation.Session{}, err
	}
	s.Title = in.Title
	s.ConfigID = in.ConfigID
	s.PersonaID = in.PersonaID
	if err := convrepo.New(a.Store).SaveSession(ctx, s); err != nil {
		return conversation.Session{}, err
	}
	return s, nil
}
func (a *Service) Retry(ctx context.Context, id string) (conversation.Run, error) {
	var old conversation.Run
	if err := convrepo.New(a.Store).GetRun(ctx, id, &old); err != nil {
		return conversation.Run{}, err
	}
	if err := old.CheckRetry(); err != nil {
		return conversation.Run{}, err
	}
	run, err := a.Submit(ctx, old.SessionID, old.Prompt, idgen.New())
	if err != nil {
		return conversation.Run{}, err
	}
	return run, nil
}
