package proxy

import "time"

// Config is the runtime configuration for the OpenAI-compatible proxy.
// Secrets are never logged.
type Config struct {
	Addr              string
	SessionFile       string
	OutpostURL        string
	InstanceTokenFile string
	TokensFile        string
	Models            []string
	DefaultModel      string
	Timeout           time.Duration
	AppVersion        string
	Surface           string
	OutpostID         string
	WorkingSessionID  string
	Timezone          string
	Locale            string
	ThinkingEffort    string
	KeepSessions      bool
}
