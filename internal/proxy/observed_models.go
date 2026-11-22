package proxy

import (
    "encoding/json"
    "log"
    "os"
    "path/filepath"
    "regexp"
    "sort"
    "strings"
    "sync"
    "time"
)

// Model IDs are observations from turn_usage; Quick still routes by agent_mode.
type observedModel struct {
    ID        string    `json:"id"`
    AgentMode string    `json:"agent_mode"`
    LastSeen  time.Time `json:"last_seen"`
}

type modelRegistry struct {
    mu      sync.RWMutex
    path    string
    entries map[string]observedModel
}

var observedModelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{2,191}$`)
const maxObservedModels = 64

func isAgentMode(mode string) bool {
    switch mode {
    case "smart", "fast", "balanced", "advanced", "auto":
        return true
    default:
        return false
    }
}

func newModelRegistry(configPath string) *modelRegistry {
    reg := &modelRegistry{entries: make(map[string]observedModel)}
    if configPath == "" {
        return reg
    }
    reg.path = filepath.Join(filepath.Dir(configPath), "data", "observed-models.json")
    raw, err := os.ReadFile(reg.path)
    if err != nil {
        if !os.IsNotExist(err) { log.Printf("observed models: read: %v", err) }
        return reg
    }
    var saved []observedModel
    if err = json.Unmarshal(raw, &saved); err != nil {
        log.Printf("observed models: parse: %v", err)
        return reg
    }
    for _, entry := range saved {
        if !isAgentMode(entry.AgentMode) || !observedModelID.MatchString(entry.ID) || entry.LastSeen.IsZero() {
            continue
        }
        if existing, ok := reg.entries[entry.ID]; !ok || entry.LastSeen.After(existing.LastSeen) {
            reg.entries[entry.ID] = entry
        }
    }
    reg.prune()
    return reg
}

func (r *modelRegistry) prune() {
    if len(r.entries) <= maxObservedModels { return }
    entries := r.snapshotUnlocked()
    for _, e := range entries[maxObservedModels:] { delete(r.entries, e.ID) }
}

func (r *modelRegistry) snapshotUnlocked() []observedModel {
    all := make([]observedModel, 0, len(r.entries))
    for _, entry := range r.entries { all = append(all, entry) }
    sort.Slice(all, func(i, j int) bool {
        if all[i].LastSeen.Equal(all[j].LastSeen) { return all[i].ID < all[j].ID }
        return all[i].LastSeen.After(all[j].LastSeen)
    })
    return all
}

func (r *modelRegistry) list() []observedModel {
    r.mu.RLock()
    defer r.mu.RUnlock()
    return r.snapshotUnlocked()
}

func (r *modelRegistry) lookup(id string) (string, bool) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    entry, ok := r.entries[id]
    return entry.AgentMode, ok
}

func (r *modelRegistry) record(mode string, usage Usage) {
    mode = strings.ToLower(strings.TrimSpace(mode))
    if !isAgentMode(mode) { return }
    id := strings.TrimSpace(usage.ResolvedModelID)
    if id == "" { id = strings.TrimSpace(usage.ServedModelID) }
    if !observedModelID.MatchString(id) || isAgentMode(id) { return }
    now := time.Now().UTC()
    r.mu.Lock()
    defer r.mu.Unlock()
    r.entries[id] = observedModel{ID: id, AgentMode: mode, LastSeen: now}
    r.prune()
    if r.path == "" { return }
    if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
        log.Printf("observed models: mkdir: %v", err)
        return
    }
    body, err := json.MarshalIndent(r.snapshotUnlocked(), "", "  ")
    if err != nil { log.Printf("observed models: marshal: %v", err); return }
    temp, err := os.CreateTemp(filepath.Dir(r.path), ".observed-models-*")
    if err != nil { log.Printf("observed models: temp: %v", err); return }
    defer os.Remove(temp.Name())
    if err = temp.Chmod(0o600); err == nil { _, err = temp.Write(body) }
    if err == nil { err = temp.Close() } else { _ = temp.Close() }
    if err == nil { err = os.Rename(temp.Name(), r.path) }
    if err != nil { log.Printf("observed models: persist: %v", err) }
}

func (r *modelRegistry) listForOpenAI(existing []string) []map[string]any {
    observed := r.list()
    seen := make(map[string]bool)
    out := make([]map[string]any, 0, len(existing)+len(observed))
    for _, id := range existing {
        if seen[id] { continue }
        seen[id] = true
        out = append(out, map[string]any{
            "id": id, "object": "model", "created": int64(0),
            "owned_by": "quickwork", "agent_mode": id,
            "routing": "agent_mode", "direct_selection": false,
        })
    }
    for _, model := range observed {
        if seen[model.ID] { continue }
        seen[model.ID] = true
        out = append(out, map[string]any{
            "id": model.ID, "object": "model", "created": model.LastSeen.Unix(),
            "owned_by": "quickwork-observed", "agent_mode": model.AgentMode,
            "observed_at": model.LastSeen.Format(time.RFC3339),
            "routing": "observed_agent_mode", "direct_selection": false,
        })
    }
    return out
}
