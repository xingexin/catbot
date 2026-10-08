package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	domainplugin "github.com/xingexin/catbot/internal/domain/plugin"
)

type Runtime struct {
	DataDir, HostURL string
	Token            func(string) string
	mu               sync.Mutex
	processes        map[string]*process
}

type process struct {
	session *mcp.ClientSession
	log     *os.File
}

func NewRuntime(data, host string, token func(string) string) *Runtime {
	return &Runtime{DataDir: data, HostURL: host, Token: token, processes: map[string]*process{}}
}

func (r *Runtime) process(ctx context.Context, key string, p domainplugin.Plugin, configuration func(context.Context) (map[string]any, error)) (*process, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	if running := r.processes[key]; running != nil {
		return running, nil
	}
	cfg, err := configuration(ctx)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(cfg)
	logDir := filepath.Join(r.DataDir, "plugin-logs")
	if err = os.MkdirAll(logDir, 0700); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(logDir, key+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("node", filepath.Join(p.Directory, p.Manifest.Entry))
	if err := os.MkdirAll(filepath.Join(r.DataDir, "plugin-home", p.ID), 0700); err != nil {
		_ = log.Close()
		return nil, err
	}
	cmd.Dir = p.Directory
	cmd.Stderr = log
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(r.DataDir, "plugin-home", p.ID), "SECRETARY_PLUGIN_CONFIG=" + string(b), "SECRETARY_HOST_URL=" + r.HostURL, "SECRETARY_HOST_TOKEN=" + r.Token("plugin:"+key)}
	client := mcp.NewClient(&mcp.Implementation{Name: "secretary", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		_ = log.Close()
		return nil, fmt.Errorf("start plugin %s: %w", p.ID, err)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		_ = session.Close()
		_ = log.Close()
		return nil, err
	}
	names := map[string]bool{}
	for _, t := range tools.Tools {
		names[t.Name] = true
	}
	for _, t := range p.Manifest.Tools {
		if !names[t.Name] {
			_ = session.Close()
			_ = log.Close()
			return nil, fmt.Errorf("plugin did not expose tool %s", t.Name)
		}
	}
	proc := &process{session: session, log: log}
	r.processes[key] = proc
	return proc, nil
}

func (r *Runtime) Health(ctx context.Context, key string, p domainplugin.Plugin, configuration func(context.Context) (map[string]any, error)) error {
	proc, err := r.process(ctx, key, p, configuration)
	if err != nil {
		return err
	}
	return proc.session.Ping(ctx, nil)
}

func (r *Runtime) Call(ctx context.Context, key string, p domainplugin.Plugin, configuration func(context.Context) (map[string]any, error), name string, args map[string]any, operationID string) (any, error) {
	proc, err := r.process(ctx, key, p, configuration)
	if err != nil {
		return nil, err
	}
	result, err := proc.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args, Meta: mcp.Meta{"secretary/operationId": operationID}})
	if err != nil {
		r.mu.Lock()
		if r.processes[key] == proc {
			delete(r.processes, key)
		}
		r.mu.Unlock()
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
	return value, nil
}

func (r *Runtime) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, p := range r.processes {
		_ = p.session.Close()
		_ = p.log.Close()
		delete(r.processes, key)
	}
}
