package config

type Options struct {
	MaxUploadMB                                                                         int
	DataDir, PluginDir, InternalURL, RuntimeURL, RuntimeToken, MasterKey, AdminPassword string
	QQAppID, QQUser                                                                     string
	QQConfigID, QQPersonaID                                                             string
	CookieSecure                                                                        bool
}
