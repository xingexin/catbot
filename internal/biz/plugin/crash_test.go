package plugin

import (
	"encoding/base64"
	"github.com/xingexin/catbot/internal/infra/store"
	"github.com/xingexin/catbot/internal/infra/vault"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginCrashLeavesUnsafeOperationUncertain(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "crash")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"crash","name":"crash fixture","version":"1.0.0","entry":"index.cjs","configSchema":{"type":"object"},"tools":[{"name":"write","inputSchema":{"type":"object"},"permissions":[],"timeoutSec":5,"retrySafe":false}]}`
	script := `const rl=require("node:readline").createInterface({input:process.stdin});
rl.on("line",line=>{const r=JSON.parse(line);if(r.id===undefined)return;
if(r.method==="tools/call")process.exit(2);
let result={};
if(r.method==="initialize")result={protocolVersion:r.params.protocolVersion,capabilities:{tools:{}},serverInfo:{name:"fixture",version:"1"}};
if(r.method==="tools/list")result={tools:[{name:"write",inputSchema:{type:"object"}}]};
process.stdout.write(JSON.stringify({jsonrpc:"2.0",id:r.id,result})+"\n");});`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.cjs"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	s := store.NewMemory()
	v, _ := vault.New(s, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	m := New(s, v, root, t.TempDir(), "", func(string) string { return "" })
	defer m.Close()
	p, err := m.Register(t.Context(), "crash")
	if err != nil {
		t.Fatal(err)
	}
	p.Enabled = true
	_ = s.Put(t.Context(), "plugin", p.ID, p)
	versions, err := m.Snapshots(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CallPinned(t.Context(), versions["crash"], "write", map[string]any{}, "stable"); err == nil {
		t.Fatal("crash accepted")
	}
	if _, err := m.CallPinned(t.Context(), versions["crash"], "write", map[string]any{}, "stable"); err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatal("unsafe retry:", err)
	}
}
