package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quickwork-openai/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Error(err)
	}
}

func TestEnterpriseLoginCallbackFlow(t *testing.T) {
	st := testStore(t)
	var tokenForm url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		tokenForm = r.PostForm
		writeJSON(t, w, map[string]any{
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
			"id_token":      "id-token",
			"expires_in":    3600,
		})
	})
	mux.HandleFunc("/integration/quick-work/user-info", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("user-info authorization = %q", got)
		}
		writeJSON(t, w, map[string]any{"userInfo": map[string]any{
			"email":        "user@example.com",
			"displayName":  "Test User",
			"userArn":      "arn:aws:quicksight:us-east-1:123:user/default/user",
			"awsAccountId": "123",
			"accountName":  "example",
		}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := &loginFlow{
		opts:        LoginOptions{Logf: t.Logf},
		store:       st,
		provider:    "enterprise",
		stage:       "prod",
		redirectURI: "http://localhost:18080",
		state:       "state-1",
		nonce:       "nonce-1",
		verifier:    "verifier-1",
		challenge:   "challenge-1",
		done:        make(chan struct{}),
	}
	discoveryQuery := url.Values{
		"nonce":         {"nonce-1"},
		"authEndpoint":  {srv.URL + "/authorize"},
		"tokenEndpoint": {srv.URL + "/token"},
		"clientId":      {"client-id"},
		"tenantUrl":     {srv.URL},
		"region":        {"us-east-1"},
	}
	rec := httptest.NewRecorder()
	f.handleDiscovery(rec, httptest.NewRequest(http.MethodGet, "/callback?"+discoveryQuery.Encode(), nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("discovery status = %d body=%s", rec.Code, rec.Body.String())
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := location.Query().Get("state"); got != "state-1" {
		t.Fatalf("authorize state = %q", got)
	}
	if got := location.Query().Get("code_challenge"); got != "challenge-1" {
		t.Fatalf("authorize challenge = %q", got)
	}

	codeQuery := url.Values{"state": {"state-1"}, "code": {"auth-code"}}
	rec = httptest.NewRecorder()
	f.handleCode(rec, httptest.NewRequest(http.MethodGet, "/?"+codeQuery.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-f.done:
	default:
		t.Fatal("login flow did not finish")
	}
	if f.fail != nil {
		t.Fatal(f.fail)
	}
	if tokenForm.Get("grant_type") != "authorization_code" ||
		tokenForm.Get("code") != "auth-code" ||
		tokenForm.Get("code_verifier") != "verifier-1" {
		t.Fatalf("unexpected token form: %v", tokenForm)
	}

	saved, err := st.Get(context.Background(), f.acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Email != "user@example.com" || saved.AccessToken != "access-token" || saved.RefreshToken != "refresh-token" {
		t.Fatalf("unexpected saved account: %+v", saved)
	}
	if saved.ClientID != "client-id" || saved.TokenEndpoint != srv.URL+"/token" {
		t.Fatalf("unexpected oauth metadata: %+v", saved)
	}
	if saved.WSHost != strings.TrimPrefix(srv.URL, "http://") {
		t.Fatalf("ws_host = %q", saved.WSHost)
	}
}

func TestSocialLoginCallbackFlow(t *testing.T) {
	st := testStore(t)
	var registration map[string]any
	var tokenRequest map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/client/register", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
			t.Error(err)
		}
		writeJSON(t, w, map[string]any{"clientId": "social-client", "clientSecret": "social-secret"})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&tokenRequest); err != nil {
			t.Error(err)
		}
		writeJSON(t, w, map[string]any{
			"accessToken":  "social-access",
			"refreshToken": "social-refresh",
			"idToken":      "social-id",
			"expiresIn":    3600,
		})
	})
	mux.HandleFunc("/dp.appintegrations/integration/quick-work/user-info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"userInfo": map[string]any{
			"email":        "social@example.com",
			"userName":     "social",
			"userArn":      "arn:aws:quicksight:us-east-1:456:user/default/social",
			"awsAccountId": "456",
		}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tenant := srv.URL + "/dp.appintegrations"
	f := &loginFlow{
		opts:        LoginOptions{Logf: t.Logf},
		store:       st,
		provider:    "social",
		stage:       "prod",
		redirectURI: "http://127.0.0.1:18080",
		state:       "state-2",
		nonce:       "nonce-2",
		verifier:    "verifier-2",
		challenge:   "challenge-2",
		oidcBase:    srv.URL,
		done:        make(chan struct{}),
	}
	discoveryQuery := url.Values{
		"nonce":     {"nonce-2"},
		"issuerUrl": {"https://identitycenter.example/ssoins-test"},
		"tenantUrl": {tenant},
		"idcRegion": {"us-east-1"},
	}
	rec := httptest.NewRecorder()
	f.handleDiscovery(rec, httptest.NewRequest(http.MethodGet, "/callback?"+discoveryQuery.Encode(), nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("discovery status = %d body=%s", rec.Code, rec.Body.String())
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(location.String(), srv.URL+"/authorize?") {
		t.Fatalf("authorize location = %s", location)
	}
	if location.Query().Get("client_id") != "social-client" {
		t.Fatalf("authorize client_id = %q", location.Query().Get("client_id"))
	}
	if registration["issuerUrl"] != "https://identitycenter.example/ssoins-test" {
		t.Fatalf("registration issuerUrl = %v", registration["issuerUrl"])
	}

	codeQuery := url.Values{"state": {"state-2"}, "code": {"social-code"}}
	rec = httptest.NewRecorder()
	f.handleCode(rec, httptest.NewRequest(http.MethodGet, "/?"+codeQuery.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code status = %d body=%s", rec.Code, rec.Body.String())
	}
	if f.fail != nil {
		t.Fatal(f.fail)
	}
	if tokenRequest["clientId"] != "social-client" ||
		tokenRequest["grantType"] != "authorization_code" ||
		tokenRequest["codeVerifier"] != "verifier-2" {
		t.Fatalf("unexpected token request: %v", tokenRequest)
	}

	saved, err := st.Get(context.Background(), f.acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Email != "social@example.com" || saved.AccessToken != "social-access" {
		t.Fatalf("unexpected saved account: %+v", saved)
	}
	if saved.ClientID != "social-client" || saved.TokenEndpoint != srv.URL+"/token" {
		t.Fatalf("unexpected oauth metadata: %+v", saved)
	}
}

func TestRefreshUsesStoredEndpointAndClearsError(t *testing.T) {
	var form url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		writeJSON(t, w, map[string]any{
			"access_token": "new-access",
			"id_token":     "new-id",
			"expires_in":   1800,
		})
	})
	mux.HandleFunc("/integration/quick-work/user-info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"userInfo": map[string]any{
			"email":   "refresh@example.com",
			"userArn": "arn:aws:quicksight:us-east-1:789:user/default/refresh",
		}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a := &store.Account{
		Provider:      "enterprise",
		BuildStage:    "prod",
		Region:        "us-east-1",
		TenantURL:     srv.URL,
		AccessToken:   "old-access",
		RefreshToken:  "old-refresh",
		IDToken:       "old-id",
		ClientID:      "client-id",
		TokenEndpoint: srv.URL + "/token",
		LastError:     "previous failure",
		ExpiresAt:     time.Now().Add(-time.Minute).Unix(),
	}
	got, err := Refresh(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "new-access" || got.RefreshToken != "old-refresh" {
		t.Fatalf("unexpected tokens: access=%q refresh=%q", got.AccessToken, got.RefreshToken)
	}
	if got.LastError != "" {
		t.Fatalf("last_error not cleared: %q", got.LastError)
	}
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "old-refresh" {
		t.Fatalf("unexpected refresh form: %v", form)
	}
	if got.ExpiresAt <= time.Now().Unix() {
		t.Fatalf("expires_at not updated: %d", got.ExpiresAt)
	}
}
