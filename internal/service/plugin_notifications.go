package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/xingexin/catbot/internal/domain"
)

func (a *App) pluginNotify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SessionID   string `json:"sessionId"`
		Text        string `json:"text"`
		OperationID string `json:"operationId"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	if in.SessionID == "" || strings.TrimSpace(in.Text) == "" || len(in.Text) > 128<<10 || strings.ContainsRune(in.Text, '\x00') || in.OperationID == "" || len(in.OperationID) > 512 {
		fail(w, errors.New("notification requires sessionId, nonempty text up to 128 KiB and stable operationId up to 512 bytes"))
		return
	}
	var session domain.Session
	if err := a.Store.Get(r.Context(), "session", in.SessionID, &session); err != nil {
		fail(w, err)
		return
	}
	if session.Channel != "" && session.Channel != "web" && session.Channel != "qq" {
		fail(w, errors.New("notifications require a user Web or QQ conversation"))
		return
	}
	p := r.Context().Value(pluginContextKey{}).(domain.Plugin)
	sum := sha256.Sum256([]byte(p.ID + "\x00" + in.OperationID))
	operationID := "plugin-notify:" + hex.EncodeToString(sum[:])
	n := notification{ID: "notification-" + operationID, PluginID: p.ID, SessionID: in.SessionID, Text: in.Text, OperationID: operationID}
	if err := a.notify(r.Context(), n, false); err != nil {
		fail(w, err)
		return
	}
	if err := a.Store.Get(r.Context(), "notification", n.ID, &n); err != nil {
		fail(w, err)
		return
	}
	JSON(w, http.StatusOK, n)
}
