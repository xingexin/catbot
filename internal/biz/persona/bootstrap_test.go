package persona

import (
	"errors"
	"testing"

	domainpersona "github.com/xingexin/catbot/internal/domain/persona"
	"github.com/xingexin/catbot/internal/infra/store"
)

func TestBootstrapDoesNotReviveDeletedDefaultPersona(t *testing.T) {
	for _, previouslyDeleted := range []bool{false, true} {
		name := "first install"
		if previouslyDeleted {
			name = "default identity previously deleted"
		}
		t.Run(name, func(t *testing.T) {
			s := store.NewMemory()
			if previouslyDeleted {
				if err := s.Put(t.Context(), "persona", "secretary", domainpersona.Persona{ID: "secretary", Name: "old persona"}); err != nil {
					t.Fatal(err)
				}
				if err := store.Purge(t.Context(), s, []store.RecordRef{{Kind: "persona", ID: "secretary"}}, nil); err != nil {
					t.Fatal(err)
				}
			}
			service := &Service{Store: s}
			for range 2 {
				if err := service.Bootstrap(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			personas, err := store.All[domainpersona.Persona](t.Context(), s, "persona")
			if err != nil {
				t.Fatal(err)
			}
			if len(personas) != 1 || !personas[0].Default || personas[0].Version != 1 || personas[0].ID == "" {
				t.Fatalf("bootstrap did not create exactly one default persona: %+v", personas)
			}
			if !previouslyDeleted {
				if personas[0].ID != "secretary" {
					t.Fatal("first installation changed its existing default identity", personas[0].ID)
				}
				return
			}
			if personas[0].ID == "secretary" {
				t.Fatal("deleted identity was recycled")
			}
			var old domainpersona.Persona
			if err := s.Get(t.Context(), "persona", "secretary", &old); !errors.Is(err, store.ErrNotFound) {
				t.Fatal("deleted persona reappeared", err)
			}
			purged, err := store.Purged(t.Context(), s, "persona", "secretary")
			if err != nil || !purged {
				t.Fatal("old deletion guard was removed", err)
			}
		})
	}
}
