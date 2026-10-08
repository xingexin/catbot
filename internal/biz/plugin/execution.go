package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xingexin/catbot/internal/domain/agent"
	domainplugin "github.com/xingexin/catbot/internal/domain/plugin"
	"github.com/xingexin/catbot/internal/infra/store"
	"time"
)

type operation struct {
	Fingerprint string `json:"fingerprint"`
	Status      string `json:"status"`
	Result      any    `json:"result"`
}

func (m *Manager) CallPinned(ctx context.Context, key, name string, args map[string]any, operationID string) (any, error) {
	var p domainplugin.Plugin
	if err := m.Store.Get(ctx, "plugin-version", key, &p); err != nil {
		return nil, err
	}
	var spec *agent.Tool
	for _, t := range p.Manifest.Tools {
		if t.Name == name {
			copy := t
			spec = &copy
			break
		}
	}
	if spec == nil {
		return nil, errors.New("unknown plugin tool")
	}
	if !domainplugin.Granted(p.Grants, spec.Permissions) {
		return nil, errors.New("tool permission denied")
	}
	if err := domainplugin.Validate(spec.InputSchema, args); err != nil {
		return nil, err
	}
	// Serialize a plugin's operations across versions so read/modify/write
	// operations such as IMAP cursor advancement cannot race.
	pluginUnlock, err := m.Store.Lock(ctx, "plugin-calls:"+p.ID)
	if err != nil {
		return nil, err
	}
	defer pluginUnlock()
	unlock, err := m.Store.Lock(ctx, "op:"+operationID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	var op operation
	fingerprintData, _ := json.Marshal(map[string]any{"key": key, "name": name, "args": args})
	sum := sha256.Sum256(fingerprintData)
	fingerprint := hex.EncodeToString(sum[:])
	if err := m.Store.Get(ctx, "operation", operationID, &op); err == nil {
		if op.Fingerprint != "" && op.Fingerprint != fingerprint {
			return nil, errors.New("operation ID reused with different tool arguments")
		}
		if op.Status == "completed" {
			return op.Result, nil
		}
		if !spec.RetrySafe {
			return nil, errors.New("previous tool outcome is uncertain; inspect before retrying")
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if err := m.Store.Put(ctx, "operation", operationID, operation{Status: "started", Fingerprint: fingerprint}); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(spec.TimeoutSec)*time.Second)
	defer cancel()
	value, err := m.runtime.Call(ctx, key, p, m.configurationFor(p), name, args, operationID)
	if err != nil {
		return nil, err
	}
	if spec.OutputSchema != nil {
		if err := domainplugin.Validate(spec.OutputSchema, value); err != nil {
			return nil, fmt.Errorf("invalid tool output: %w", err)
		}
	}
	if err := m.Store.Put(ctx, "operation", operationID, operation{Status: "completed", Result: value, Fingerprint: fingerprint}); err != nil {
		return nil, err
	}
	return value, nil
}
