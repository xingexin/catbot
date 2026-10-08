package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

type Environment struct {
	App                                                                      Options
	DatabaseURL, TemporalAddress, TemporalNamespace, ListenAddr              string
	QQSecret, QQBaseURL, OneBotURL, OneBotToken, NapCatEnabled, NapCatWebURL string
}

func Env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func Load() (Environment, error) {
	var c Environment
	if os.Getenv("ADMIN_PASSWORD") == "" || os.Getenv("RUNTIME_TOKEN") == "" {
		return c, errors.New("ADMIN_PASSWORD and RUNTIME_TOKEN are required")
	}
	data, err := filepath.Abs(Env("DATA_DIR", "data"))
	if err != nil {
		return c, err
	}
	plugins, err := filepath.Abs(Env("PLUGIN_DIR", "plugins"))
	if err != nil {
		return c, err
	}
	mb, err := strconv.Atoi(Env("MAX_UPLOAD_MB", "100"))
	if err != nil || mb < 1 || mb > 1024 {
		return c, errors.New("MAX_UPLOAD_MB must be 1..1024")
	}
	c.App = Options{MaxUploadMB: mb, DataDir: data, PluginDir: plugins, InternalURL: Env("INTERNAL_URL", "http://127.0.0.1:8080"), RuntimeURL: Env("RUNTIME_URL", "http://127.0.0.1:8091"), RuntimeToken: os.Getenv("RUNTIME_TOKEN"), MasterKey: os.Getenv("MASTER_KEY"), AdminPassword: os.Getenv("ADMIN_PASSWORD"), CookieSecure: os.Getenv("COOKIE_SECURE") == "true", QQAppID: os.Getenv("QQ_APP_ID"), QQUser: os.Getenv("QQ_USER_OPENID"), QQConfigID: os.Getenv("QQ_CONFIG_ID"), QQPersonaID: os.Getenv("QQ_PERSONA_ID")}
	c.DatabaseURL = os.Getenv("DATABASE_URL")
	c.TemporalAddress = Env("TEMPORAL_ADDRESS", "127.0.0.1:7233")
	c.TemporalNamespace = Env("TEMPORAL_NAMESPACE", "default")
	c.ListenAddr = Env("LISTEN_ADDR", ":8080")
	c.QQSecret = os.Getenv("QQ_SECRET")
	c.QQBaseURL = os.Getenv("QQ_BASE_URL")
	c.OneBotURL = os.Getenv("ONEBOT_URL")
	c.OneBotToken = os.Getenv("ONEBOT_TOKEN")
	c.NapCatEnabled = Env("NAPCAT_ENABLED", "true")
	c.NapCatWebURL = os.Getenv("NAPCAT_WEB_URL")
	return c, nil
}
