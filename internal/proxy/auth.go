package proxy

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type tokenEntry struct {
	Email     string `json:"email"`
	CreatedAt int64  `json:"created_at"`
}

type tokenFile struct {
	Tokens map[string]tokenEntry `json:"tokens"`
}

// TokenStore validates proxy API keys minted by the Python auth helper.
// When no token file exists (or it contains no tokens) authentication is
// disabled, which keeps a purely local deployment frictionless.
type TokenStore struct {
	path string

	mu     sync.Mutex
	mod    time.Time
	tokens map[string]tokenEntry
	bad    bool
}

func NewTokenStore(path string) *TokenStore {
	return &TokenStore{path: path}
}

func (t *TokenStore) reload() {
	if t.path == "" {
		return
	}
	st, err := os.Stat(t.path)
	if err != nil {
		t.tokens = nil
		t.mod = time.Time{}
		t.bad = false
		return
	}
	if st.ModTime().Equal(t.mod) {
		return
	}
	body, err := os.ReadFile(t.path)
	if err != nil {
		t.bad = true
		return
	}
	var tf tokenFile
	if err := json.Unmarshal(body, &tf); err != nil {
		t.bad = true
		t.tokens = nil
		t.mod = st.ModTime()
		return
	}
	t.tokens = tf.Tokens
	t.bad = false
	t.mod = st.ModTime()
}

func (t *TokenStore) tokenFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
			return strings.TrimSpace(h[7:])
		}
	}
	if h := r.Header.Get("X-Api-Key"); h != "" {
		return strings.TrimSpace(h)
	}
	return ""
}

// Allowed reports whether the request may use the proxy.
func (t *TokenStore) Allowed(r *http.Request) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reload()
	if t.bad {
		return false
	}
	if len(t.tokens) == 0 {
		return true
	}
	token := t.tokenFromRequest(r)
	if token == "" {
		return false
	}
	_, ok := t.tokens[token]
	return ok
}
