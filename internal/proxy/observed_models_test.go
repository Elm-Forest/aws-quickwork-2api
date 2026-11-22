package proxy

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "strings"
    "testing"

    "quickwork-openai/internal/config"
)

func TestParseTurnUsageModelIDs(t *testing.T) {
    cases := []struct {
        name string
        wire string
        resolved string
        served string
    }{
        {"snake_case", `{"input_tokens":10,"output_tokens":3,"resolved_model_id":"us.anthropic.claude-opus-5-5","served_model_id":"global.anthropic.claude-opus-5-5"}`, "us.anthropic.claude-opus-5-5", "global.anthropic.claude-opus-5-5"},
        {"camel_case", `{"resolvedModelId":"us.anthropic.claude-haiku-5-5","servedModelId":"served.haiku"}`, "us.anthropic.claude-haiku-5-5", "served.haiku"},
        {"no_fields", `{"input_tokens":8}`, "", ""},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            u, err := parseTurnUsage(json.RawMessage(tc.wire))
            if err != nil { t.Fatal(err) }
            if u.ResolvedModelID != tc.resolved || u.ServedModelID != tc.served {
                t.Fatalf("parsed wrong model IDs: %+v", u)
            }
        })
    }
    var total Usage
    a, _ := parseTurnUsage(json.RawMessage(cases[0].wire))
    b, _ := parseTurnUsage(json.RawMessage(cases[2].wire))
    total.add(a)
    total.add(b)
    if total.ResolvedModelID != cases[0].resolved { t.Fatal("discarded last known model id") }
    m := usageMap(total)
    if m["resolved_model_id"] != cases[0].resolved { t.Fatal("missing resolved_model_id in response usage") }
}

func TestObservedModelsPersistenceAndMapping(t *testing.T) {
    dir := t.TempDir()
    configPath := filepath.Join(dir, "config.json")
    registry := newModelRegistry(configPath)
    registry.record("smart", Usage{ResolvedModelID: "us.anthropic.claude-opus-5-5"})
    registry.record("fast", Usage{ServedModelID: "us.anthropic.claude-haiku-5-5"})
    registry.record("smart", Usage{ResolvedModelID: "us.anthropic.claude-opus-5-5", ServedModelID: "obsolete.other-model"})
    registry.record("fast", Usage{ResolvedModelID: "../../inject"})
    registry.record("unknown", Usage{ResolvedModelID: "us.anthropic.ignored"})
    entries := registry.list()
    if len(entries) != 2 { t.Fatalf("expected two genuine observations, got %v", entries) }
    path := filepath.Join(dir, "data", "observed-models.json")
    info, err := os.Stat(path)
    if err != nil { t.Fatal(err) }
    if info.Mode().Perm() != 0o600 { t.Fatalf("registry permissions: %v", info.Mode().Perm()) }
    restored := newModelRegistry(configPath)
    if mode, ok := restored.lookup("us.anthropic.claude-opus-5-5"); !ok || mode != "smart" {
        t.Fatalf("incorrect routing: %q %v", mode, ok)
    }
    if mode, ok := restored.lookup("us.anthropic.claude-haiku-5-5"); !ok || mode != "fast" {
        t.Fatalf("incorrect served routing: %q %v", mode, ok)
    }
    cfg := config.Default()
    cfg.Path = configPath
    cfg.Models = []string{"smart", "fast", "balanced", "auto"}
    s := NewServer(&cfg, nil)
    if got := s.mapModel("us.anthropic.claude-opus-5-5"); got != "smart" {
        t.Fatalf("expected proxy to use agent_mode smart; got %s", got)
    }
    if got := s.mapModel("advanced"); got != "advanced" { t.Fatalf("advanced ignored: %s", got) }
}

func TestModelListIncludesObservedOnlyAfterSuccess(t *testing.T) {
    cfg := config.Default()
    cfg.Path = filepath.Join(t.TempDir(), "config.json")
    cfg.APIKeys = []string{"sk-test"}
    srv := NewServer(&cfg, nil)
    handler := srv.Handler()
    request := func(authorized bool) *httptest.ResponseRecorder {
        t.Helper()
        req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
        if authorized { req.Header.Set("Authorization", "Bearer sk-test") }
        rec := httptest.NewRecorder()
        handler.ServeHTTP(rec, req)
        return rec
    }
    if got := request(false).Code; got != http.StatusUnauthorized { t.Fatalf("unexpected unauthorized status: %d", got) }
    before := request(true)
    if before.Code != http.StatusOK { t.Fatalf("initial GET status: %d", before.Code) }
    if strings.Contains(before.Body.String(), "us.anthropic") {
        t.Fatal("pretended to know models before any observed turn")
    }
    srv.models.record("smart", Usage{ResolvedModelID: "us.anthropic.claude-opus-5-5"})
    after := request(true)
    var models struct { Data []map[string]any `json:"data"` }
    if err := json.Unmarshal(after.Body.Bytes(), &models); err != nil { t.Fatal(err) }
    if len(models.Data) != 5 { t.Fatalf("expected alias models + observed ID, got %v", models.Data) }
    var found bool
    for _, item := range models.Data {
        if item["id"] == "us.anthropic.claude-opus-5-5" {
            found = true
            if item["agent_mode"] != "smart" || item["direct_selection"] != false ||
                item["routing"] != "observed_agent_mode" {
                t.Fatalf("model metadata wrong: %#v", item)
            }
        }
    }
    if !found { t.Fatal("observed model absent from /v1/models") }
}
