package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"quickwork-openai/internal/proxy"
)

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

type outpostInfo struct {
	URL       string `json:"url"`
	TokenFile string `json:"token_file"`
	PID       int    `json:"pid"`
}

func main() {
	addr := flag.String("addr", env("QW_PROXY_ADDR", "127.0.0.1:8080"), "listen address")
	sessionFile := flag.String("session", env("QW_SESSION_FILE", "data/session.json"), "outpost session JSON (written by the Python login helper)")
	outpostURL := flag.String("outpost", env("QW_OUTPOST_URL", ""), "local outpost base URL, e.g. http://127.0.0.1:8780")
	tokenFile := flag.String("instance-token-file", env("QW_INSTANCE_TOKEN_FILE", "data/instance_token"), "outpost instance token file")
	tokensFile := flag.String("tokens", env("QW_TOKENS_FILE", "data/tokens.json"), "proxy API key file (empty disables auth)")
	modelsCSV := flag.String("models", env("QW_MODELS", "smart,fast,balanced,auto"), "comma-separated model aliases")
	defaultModel := flag.String("default-model", env("QW_DEFAULT_MODEL", "smart"), "agent mode used when the request model is unknown")
	timeout := flag.Duration("timeout", 5*time.Minute, "per-request upstream timeout")
	appVersion := flag.String("app-version", env("QW_APP_VERSION", "1.2.4660"), "appVersion sent in the WS handshake")
	surface := flag.String("surface", env("QW_SURFACE", "QUICK_WORK_DESKTOP_WINDOWS"), "surfaceType sent in the WS handshake")
	outpostID := flag.String("outpost-id", env("QW_OUTPOST_ID", ""), "outpostId sent in the WS handshake")
	workingSessionID := flag.String("working-session-id", env("QW_WORKING_SESSION_ID", ""), "workingSessionId sent in the WS handshake")
	timezone := flag.String("timezone", env("QW_TIMEZONE", "UTC"), "user_timezone in user_message")
	locale := flag.String("locale", env("QW_LOCALE", "en-US"), "user_locale in user_message")
	thinking := flag.String("thinking", env("QW_THINKING_EFFORT", ""), "thinking_effort: off|low|medium|high")
	keepSessions := flag.Bool("keep-sessions", false, "keep upstream chat sessions instead of deleting them after each request")
	flag.Parse()

	// If the Python helper left an outpost descriptor, pick up its URL/token.
	if *outpostURL == "" || *tokenFile == "data/instance_token" {
		if body, err := os.ReadFile("data/outpost.json"); err == nil {
			var info outpostInfo
			if json.Unmarshal(body, &info) == nil {
				if *outpostURL == "" && info.URL != "" {
					*outpostURL = info.URL
				}
				if info.TokenFile != "" {
					*tokenFile = info.TokenFile
				}
			}
		}
	}

	models := make([]string, 0)
	for _, m := range strings.Split(*modelsCSV, ",") {
		if m = strings.TrimSpace(m); m != "" {
			models = append(models, m)
		}
	}
	cfg := &proxy.Config{
		Addr:              *addr,
		SessionFile:       *sessionFile,
		OutpostURL:        strings.TrimRight(*outpostURL, "/"),
		InstanceTokenFile: *tokenFile,
		TokensFile:        *tokensFile,
		Models:            models,
		DefaultModel:      *defaultModel,
		Timeout:           *timeout,
		AppVersion:        *appVersion,
		Surface:           *surface,
		OutpostID:         *outpostID,
		WorkingSessionID:  *workingSessionID,
		Timezone:          *timezone,
		Locale:            *locale,
		ThinkingEffort:    *thinking,
		KeepSessions:      *keepSessions,
	}

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           proxy.NewServer(cfg).Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	log.Printf("quickwork-openai listening on %s (session=%s outpost=%s auth=%v)",
		cfg.Addr, absOrSame(cfg.SessionFile), emptyAs(cfg.OutpostURL, "file"), fileExists(cfg.TokensFile))

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Printf("shutting down")
	_ = server.Close()
}

func absOrSame(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

func emptyAs(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
