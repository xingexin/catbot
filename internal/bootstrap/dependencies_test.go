package bootstrap

import (
	"encoding/json"
	"github.com/xingexin/catbot/internal/infra/store"
	"net/http"
)

// Fault injection updates explicit consumers; production composition has no mutable facade.
func setTestStore(a *App, s store.Store) {
	a.Store = s
	a.Conversation.Store = s
	a.Agents.Store = s
	a.Artifacts.Store = s
	a.Personas.Store = s
	a.Tasks.Store = s
	a.Steps.Store = s
	a.Execution.Store = s
	a.Tools.Store = s
	a.Plugins.Store = s
	a.Messaging.Store = s
	a.Mail.Store = s
	a.System.Store = s
	a.Dispatcher.Store = s
}
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
