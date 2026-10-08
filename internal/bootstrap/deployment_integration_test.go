//go:build integration

package bootstrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xingexin/catbot/internal/config"
	"github.com/xingexin/catbot/internal/infra/idgen"
	"github.com/xingexin/catbot/internal/infra/store"
	infratemporal "github.com/xingexin/catbot/internal/infra/temporal"
	workertemporal "github.com/xingexin/catbot/internal/worker/temporal"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"
)

func TestRealBootstrapWithoutNapCat(t *testing.T) {
	dsn, address := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_TEMPORAL_ADDRESS")
	if dsn == "" || address == "" {
		t.Skip("isolated secretary_test database and real Temporal required")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Path != "/secretary_test" {
		t.Fatal("bootstrap integration requires secretary_test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "bootstrap_test_" + strings.ReplaceAll(idgen.New(), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer done()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	var externalCalls atomic.Int32
	blocker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		externalCalls.Add(1)
		http.Error(w, "external integrations must not be called during bootstrap", http.StatusServiceUnavailable)
	}))
	defer blocker.Close()
	for key, value := range map[string]string{
		"DATABASE_URL": parsed.String(), "TEMPORAL_ADDRESS": address, "TEMPORAL_NAMESPACE": "default",
		"NAPCAT_ENABLED": "false", "ONEBOT_URL": "", "ONEBOT_TOKEN": "", "NAPCAT_WEB_URL": blocker.URL,
		"QQ_APP_ID": "", "QQ_SECRET": "", "QQ_USER_OPENID": "", "QQ_CONFIG_ID": "", "QQ_PERSONA_ID": "", "QQ_BASE_URL": blocker.URL,
		"DATA_DIR": t.TempDir(), "PLUGIN_DIR": t.TempDir(), "MASTER_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"ADMIN_PASSWORD": "bootstrap-only-password", "RUNTIME_TOKEN": "bootstrap-only-token", "RUNTIME_URL": blocker.URL, "INTERNAL_URL": blocker.URL,
		"COOKIE_SECURE": "false", "MAX_UPLOAD_MB": "100", "LISTEN_ADDR": "127.0.0.1:0",
	} {
		t.Setenv(key, value)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NapCatEnabled != "false" || cfg.OneBotURL != "" {
		t.Fatal("disabled NapCat configuration was not loaded")
	}
	records, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer records.Close()
	temporalClient, err := client.DialContext(ctx, client.Options{HostPort: cfg.TemporalAddress, Namespace: cfg.TemporalNamespace})
	if err != nil {
		t.Fatal(err)
	}
	defer temporalClient.Close()
	app, err := New(records, cfg.App)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := registerChannels(app, cfg); err != nil {
		t.Fatal(err)
	}
	scheduler := infratemporal.New(temporalClient, records)
	scheduler.TaskQueue = "integration-bootstrap-" + idgen.New()
	app.Tasks.Scheduler = scheduler
	app.Execution.ExecutionClosed = scheduler.ExecutionClosed
	if err := app.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	worker := workertemporal.New(temporalClient, scheduler.TaskQueue, app.Execution)
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	defer worker.Stop()
	app.Start()
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	httpClient := server.Client()
	httpClient.Timeout = 5 * time.Second
	health, err := httpClient.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	var healthBody map[string]any
	err = json.NewDecoder(health.Body).Decode(&healthBody)
	health.Body.Close()
	if err != nil || health.StatusCode != http.StatusOK || healthBody["status"] != "ok" {
		t.Fatalf("main service unhealthy: status=%d error=%v", health.StatusCode, err)
	}
	if err := app.Tasks.Ping(ctx); err != nil {
		t.Fatal("Temporal is not connected", err)
	}
	login, err := httpClient.Post(server.URL+"/api/login", "application/json", bytes.NewBufferString(`{"password":"bootstrap-only-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	if login.StatusCode != http.StatusOK || len(login.Cookies()) == 0 {
		t.Fatalf("admin login unavailable: %d", login.StatusCode)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/qq", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range login.Cookies() {
		request.AddCookie(cookie)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	err = json.NewDecoder(response.Body).Decode(&body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || body["napcatWebUrl"] != "" {
		t.Fatalf("admin channel page failed or exposed local login: status=%d error=%v", response.StatusCode, err)
	}
	status := app.Messaging.ChannelStatus(ctx, "onebot")
	if status.Configured || status.State != "unconfigured" {
		t.Fatalf("empty OneBot endpoint reported configured: %s", status.State)
	}
	app.Close()
	if externalCalls.Load() != 0 {
		t.Fatalf("startup attempted %d external requests", externalCalls.Load())
	}
	t.Log("NAPCAT_ENABLED=false: real PostgreSQL + Temporal worker + application workers + HTTP health/admin routes started; local QQ login hidden; external calls=0")
}
