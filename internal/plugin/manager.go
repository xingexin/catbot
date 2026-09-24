package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"agentTest/internal/domain"
	"agentTest/internal/secret"
	"agentTest/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)

type Manager struct {
	Store                  store.Store
	Vault                  *secret.Vault
	Root, DataDir, HostURL string
	Token                  func(string) string
	mu                     sync.Mutex
	processes              map[string]*process
}
type process struct {
	session *mcp.ClientSession
	log     *os.File
}

func New(s store.Store, v *secret.Vault, root, data, host string, token func(string) string) *Manager {
	return &Manager{Store: s, Vault: v, Root: root, DataDir: data, HostURL: host, Token: token, processes: map[string]*process{}}
}
func Validate(schema map[string]any, value any) error {
	b, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	var doc any
	if err = json.Unmarshal(b, &doc); err != nil {
		return err
	}
	c := jsonschema.NewCompiler()
	if err = c.AddResource("urn:secretary:input", doc); err != nil {
		return err
	}
	s, err := c.Compile("urn:secretary:input")
	if err != nil {
		return fmt.Errorf("invalid JSON schema: %w", err)
	}
	b, err = json.Marshal(value)
	if err != nil {
		return err
	}
	var input any
	if err = json.Unmarshal(b, &input); err != nil {
		return err
	}
	if err = s.Validate(input); err != nil {
		return fmt.Errorf("input validation failed: %w", err)
	}
	return nil
}

func (m *Manager) Register(ctx context.Context, dir string) (domain.Plugin, error) {
	root, err := filepath.EvalSymlinks(m.Root)
	if err != nil {
		return domain.Plugin{}, err
	}
	full, err := filepath.EvalSymlinks(filepath.Join(root, dir))
	if err != nil {
		return domain.Plugin{}, err
	}
	rel, err := filepath.Rel(root, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return domain.Plugin{}, errors.New("plugin must be inside PLUGIN_DIR")
	}
	b, err := os.ReadFile(filepath.Join(full, "plugin.json"))
	if err != nil {
		return domain.Plugin{}, err
	}
	var manifest domain.Manifest
	if err = json.Unmarshal(b, &manifest); err != nil {
		return domain.Plugin{}, err
	}
	if !identifier.MatchString(manifest.ID) || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`).MatchString(manifest.Version) || manifest.Name == "" || manifest.Entry == "" {
		return domain.Plugin{}, errors.New("invalid plugin manifest")
	}
	entry, err := filepath.EvalSymlinks(filepath.Join(full, manifest.Entry))
	if err != nil {
		return domain.Plugin{}, err
	}
	entryRel, _ := filepath.Rel(full, entry)
	if strings.HasPrefix(entryRel, "..") {
		return domain.Plugin{}, errors.New("entry must be inside plugin package")
	}
	unlock, err := m.Store.Lock(ctx, "plugin-config:"+manifest.ID)
	if err != nil {
		return domain.Plugin{}, err
	}
	defer unlock()
	seen := map[string]bool{}
	for i := range manifest.Tools {
		t := &manifest.Tools[i]
		if !identifier.MatchString(t.Name) || seen[t.Name] || len(manifest.ID+"__"+t.Name) > 64 {
			return domain.Plugin{}, errors.New("invalid or duplicate tool name")
		}
		seen[t.Name] = true
		t.PluginID = manifest.ID
		t.Version = manifest.Version
		if t.TimeoutSec <= 0 {
			t.TimeoutSec = 60
		}
		if t.TimeoutSec > 3600 {
			return domain.Plugin{}, errors.New("tool timeout exceeds 3600 seconds")
		}
		if len(t.InputSchema) == 0 || t.InputSchema["type"] != "object" {
			return domain.Plugin{}, errors.New("tool inputSchema is required")
		}
	}
	if manifest.ConfigSchema == nil {
		manifest.ConfigSchema = map[string]any{"type": "object"}
	}
	frozen, err := m.freeze(ctx, full, manifest)
	if err != nil {
		return domain.Plugin{}, err
	}
	p := domain.Plugin{ID: manifest.ID, Manifest: manifest, Directory: frozen, Config: map[string]any{}, Secrets: map[string]string{}, Grants: []string{}}
	var old domain.Plugin
	if err := m.Store.Get(ctx, "plugin", p.ID, &old); err == nil {
		if old.Manifest.Version == manifest.Version && old.Directory != frozen {
			return p, errors.New("a new package must use a new version")
		}
		p.Config = old.Config
		p.Secrets = old.Secrets
		p.Grants = old.Grants
		p.Enabled = old.Enabled
	}
	err = m.Store.Put(ctx, "plugin", p.ID, p)
	return p, err
}

func (m *Manager) Configure(ctx context.Context, id string, cfg map[string]any, grants []string) (domain.Plugin, error) {
	unlock, err := m.Store.Lock(ctx, "plugin-config:"+id)
	if err != nil {
		return domain.Plugin{}, err
	}
	defer unlock()
	var p domain.Plugin
	if err := m.Store.Get(ctx, "plugin", id, &p); err != nil {
		return p, err
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	if p.Secrets == nil {
		p.Secrets = map[string]string{}
	}
	properties, _ := p.Manifest.ConfigSchema["properties"].(map[string]any)
	for name, prop := range properties {
		obj, _ := prop.(map[string]any)
		if obj["format"] != "password" {
			continue
		}
		if value, ok := cfg[name].(string); ok && value != "" {
			id := "plugin-" + p.ID + "-" + name + "-" + domain.ID()
			if err := m.Vault.Set(ctx, id, p.Manifest.Name+" / "+name, value); err != nil {
				return p, err
			}
			p.Secrets[name] = id
		}
		delete(cfg, name)
	}
	p.Config = cfg
	p.Grants = grants
	merged, err := m.configuration(ctx, p)
	if err != nil {
		return p, err
	}
	if err = Validate(p.Manifest.ConfigSchema, merged); err != nil {
		return p, err
	}
	err = m.Store.Put(ctx, "plugin", p.ID, p)
	return p, err
}
func (m *Manager) configuration(ctx context.Context, p domain.Plugin) (map[string]any, error) {
	cfg := map[string]any{}
	for k, v := range p.Config {
		cfg[k] = v
	}
	for k, id := range p.Secrets {
		value, err := m.Vault.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		cfg[k] = value
	}
	return cfg, nil
}
func snapshotKey(p domain.Plugin) string {
	b, _ := json.Marshal(p)
	hash := sha256.Sum256(b)
	return p.ID + "@" + p.Manifest.Version + "-" + hex.EncodeToString(hash[:8])
}

// Pin records the currently configured package and permissions for one execution.
func Pin(ctx context.Context, s store.Store, p domain.Plugin) (string, error) {
	key := snapshotKey(p)
	return key, s.Put(ctx, "plugin-version", key, p)
}
func (m *Manager) Snapshots(ctx context.Context) (map[string]string, error) {
	ps, err := store.All[domain.Plugin](ctx, m.Store, "plugin")
	if err != nil {
		return nil, err
	}
	versions := map[string]string{}
	for _, p := range ps {
		if !p.Enabled {
			continue
		}
		key := snapshotKey(p)
		if err := m.Store.Put(ctx, "plugin-version", key, p); err != nil {
			return nil, err
		}
		versions[p.ID] = key
	}
	return versions, nil
}
func (m *Manager) Tools(ctx context.Context, versions map[string]string, allow []string) ([]domain.Tool, error) {
	out := []domain.Tool{}
	ids := []string{}
	for id := range versions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		var p domain.Plugin
		if err := m.Store.Get(ctx, "plugin-version", versions[id], &p); err != nil {
			return nil, err
		}
		for _, t := range p.Manifest.Tools {
			t.Name = p.ID + "__" + t.Name
			if allow != nil && !slices.Contains(allow, t.Name) {
				continue
			}
			if !granted(p.Grants, t.Permissions) {
				continue
			}
			out = append(out, t)
		}
	}
	return out, nil
}
func granted(grants, permissions []string) bool {
	for _, p := range permissions {
		if !slices.Contains(grants, p) {
			return false
		}
	}
	return true
}
func (m *Manager) process(ctx context.Context, key string) (*process, domain.Plugin, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var p domain.Plugin
	if err := m.Store.Get(ctx, "plugin-version", key, &p); err != nil {
		return nil, p, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if running := m.processes[key]; running != nil {
		return running, p, nil
	}
	cfg, err := m.configuration(ctx, p)
	if err != nil {
		return nil, p, err
	}
	if err = Validate(p.Manifest.ConfigSchema, cfg); err != nil {
		return nil, p, err
	}
	b, _ := json.Marshal(cfg)
	logDir := filepath.Join(m.DataDir, "plugin-logs")
	if err = os.MkdirAll(logDir, 0700); err != nil {
		return nil, p, err
	}
	log, err := os.OpenFile(filepath.Join(logDir, key+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, p, err
	}
	cmd := exec.Command("node", filepath.Join(p.Directory, p.Manifest.Entry))
	if err := os.MkdirAll(filepath.Join(m.DataDir, "plugin-home", p.ID), 0700); err != nil {
		_ = log.Close()
		return nil, p, err
	}
	cmd.Dir = p.Directory
	cmd.Stderr = log
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(m.DataDir, "plugin-home", p.ID), "SECRETARY_PLUGIN_CONFIG=" + string(b), "SECRETARY_HOST_URL=" + m.HostURL, "SECRETARY_HOST_TOKEN=" + m.Token("plugin:"+key)}
	client := mcp.NewClient(&mcp.Implementation{Name: "secretary", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		_ = log.Close()
		return nil, p, fmt.Errorf("start plugin %s: %w", p.ID, err)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		_ = session.Close()
		_ = log.Close()
		return nil, p, err
	}
	names := map[string]bool{}
	for _, t := range tools.Tools {
		names[t.Name] = true
	}
	for _, t := range p.Manifest.Tools {
		if !names[t.Name] {
			_ = session.Close()
			_ = log.Close()
			return nil, p, fmt.Errorf("plugin did not expose tool %s", t.Name)
		}
	}
	proc := &process{session: session, log: log}
	m.processes[key] = proc
	return proc, p, nil
}
func (m *Manager) Health(ctx context.Context, id string) error {
	var p domain.Plugin
	if err := m.Store.Get(ctx, "plugin", id, &p); err != nil {
		return err
	}
	key := snapshotKey(p)
	if err := m.Store.Put(ctx, "plugin-version", key, p); err != nil {
		return err
	}
	proc, _, err := m.process(ctx, key)
	if err != nil {
		return err
	}
	return proc.session.Ping(ctx, nil)
}

type operation struct {
	Fingerprint string `json:"fingerprint"`
	Status      string `json:"status"`
	Result      any    `json:"result"`
}

func (m *Manager) CallPinned(ctx context.Context, key, name string, args map[string]any, operationID string) (any, error) {
	var p domain.Plugin
	if err := m.Store.Get(ctx, "plugin-version", key, &p); err != nil {
		return nil, err
	}
	var spec *domain.Tool
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
	if !granted(p.Grants, spec.Permissions) {
		return nil, errors.New("tool permission denied")
	}
	if err := Validate(spec.InputSchema, args); err != nil {
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
	proc, _, err := m.process(ctx, key)
	if err != nil {
		return nil, err
	}
	result, err := proc.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args, Meta: mcp.Meta{"secretary/operationId": operationID}})
	if err != nil {
		m.mu.Lock()
		if m.processes[key] == proc {
			delete(m.processes, key)
		}
		m.mu.Unlock()
		_ = proc.session.Close()
		_ = proc.log.Close()
		return nil, fmt.Errorf("plugin transport interrupted: %w", err)
	}
	if result.IsError {
		parts := []string{}
		for _, c := range result.Content {
			if t, ok := c.(*mcp.TextContent); ok {
				parts = append(parts, t.Text)
			}
		}
		return nil, fmt.Errorf("plugin tool failed: %s", strings.Join(parts, "\n"))
	}
	var value any = result.StructuredContent
	if value == nil {
		parts := []string{}
		for _, c := range result.Content {
			if t, ok := c.(*mcp.TextContent); ok {
				parts = append(parts, t.Text)
			}
		}
		text := strings.Join(parts, "\n")
		if json.Unmarshal([]byte(text), &value) != nil {
			value = map[string]any{"text": text}
		}
	}
	if spec.OutputSchema != nil {
		if err := Validate(spec.OutputSchema, value); err != nil {
			return nil, fmt.Errorf("invalid tool output: %w", err)
		}
	}
	if err := m.Store.Put(ctx, "operation", operationID, operation{Status: "completed", Result: value, Fingerprint: fingerprint}); err != nil {
		return nil, err
	}
	return value, nil
}
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, p := range m.processes {
		_ = p.session.Close()
		_ = p.log.Close()
		delete(m.processes, key)
	}
}
