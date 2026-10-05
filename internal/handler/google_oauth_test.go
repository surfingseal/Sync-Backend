package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"example.com/sync/internal/auth"
	"example.com/sync/internal/config"
	"example.com/sync/internal/router"
	"golang.org/x/oauth2"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// All SDK requests are redirected to this test's local fake Google provider.
type providerTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (p providerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.URL.Scheme = p.target.Scheme
	copy.URL.Host = p.target.Host
	copy.Host = p.target.Host
	return p.base.RoundTrip(copy)
}

type oauthFixture struct {
	mu                                                               sync.Mutex
	exchangeCount, youtubeCount, refreshCount                        int
	exchangeFail, youtubeFail, noChannel, omitRefresh, switchAccount bool
	client                                                           *http.Client
	router                                                           http.Handler
	store                                                            *auth.InMemoryTokenStore
}

func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	f := &oauthFixture{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			f.exchangeCount++
			if f.exchangeFail {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":"invalid_grant","error_description":"test-secret test-refresh"}`)
				return
			}
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			if r.Form.Get("grant_type") == "refresh_token" {
				f.refreshCount++
			} else if r.Form.Get("code_verifier") == "" {
				t.Error("PKCE verifier missing")
			}
			refresh := `,"refresh_token":"test-refresh"`
			if f.omitRefresh || r.Form.Get("grant_type") == "refresh_token" {
				refresh = ""
			}
			access := "test-access"
			if f.switchAccount {
				access = "new-access"
			}
			io.WriteString(w, `{"access_token":"`+access+`","token_type":"Bearer","expires_in":3600`+refresh+`}`)
		case "/youtube/v3/channels":
			f.youtubeCount++
			if r.Header.Get("Authorization") != "Bearer test-access" && r.Header.Get("Authorization") != "Bearer new-access" {
				t.Error("Bearer authentication missing")
			}
			if r.URL.Query().Get("mine") != "true" || r.URL.Query().Get("part") != "snippet" || r.URL.Query().Get("key") != "" {
				t.Error("wrong channels.list request")
			}
			if f.youtubeFail {
				w.WriteHeader(403)
				io.WriteString(w, `{"error":{"message":"test-access test-secret","code":403}}`)
				return
			}
			if f.noChannel {
				io.WriteString(w, `{"items":[]}`)
				return
			}
			id := "UC-test"
			if r.Header.Get("Authorization") == "Bearer new-access" {
				id = "UC-new"
			}
			json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"id": id, "snippet": map[string]string{"title": "Test Channel"}}}})
		case "/youtube/v3/playlists":
			w.WriteHeader(403)
			io.WriteString(w, `{"error":{"code":403,"message":"test-secret"}}`)
		default:
			t.Errorf("unexpected fake provider path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(provider.Close)
	target, _ := url.Parse(provider.URL)
	f.client = &http.Client{Transport: providerTransport{http.DefaultTransport, target}, Timeout: time.Second}
	f.store = auth.NewInMemoryTokenStore()
	service := auth.NewGoogleOAuthService(auth.Settings{ClientID: "test-client", ClientSecret: "test-secret", RedirectURL: "http://localhost:8080/api/v1/auth/google/callback", Scope: auth.YouTubeScope, ForceConsent: true}, f.store, auth.YouTubeChannelVerifier{})
	f.router = router.New(config.Config{AppEnv: "production"}, nil, nil, service)
	return f
}
func (f *oauthFixture) change(fn func()) { f.mu.Lock(); defer f.mu.Unlock(); fn() }
func (f *oauthFixture) request(method, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r = r.WithContext(context.WithValue(r.Context(), oauth2.HTTPClient, f.client))
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}
func responseCookie(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("cookie missing: %s", name)
	return nil
}
func (f *oauthFixture) start(t *testing.T, previous ...*http.Cookie) *http.Cookie {
	t.Helper()
	w := f.request("GET", "/api/v1/auth/google", previous...)
	if w.Code != 302 {
		t.Fatalf("start status=%d", w.Code)
	}
	return responseCookie(t, w, "sync_oauth_state")
}
func (f *oauthFixture) connect(t *testing.T, previous ...*http.Cookie) *http.Cookie {
	t.Helper()
	state := f.start(t, previous...)
	w := f.request("GET", "/api/v1/auth/google/callback?state="+state.Value+"&code=test-code", state)
	if w.Code != 200 {
		t.Fatalf("callback status=%d body=%s", w.Code, w.Body.String())
	}
	for _, secret := range []string{"test-access", "new-access", "test-refresh", "test-secret", "test-code", "access_token", "refresh_token"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("confidential information leaked in response")
		}
	}
	return responseCookie(t, w, "sync_session")
}
func TestOAuthStart(t *testing.T) {
	f := newOAuthFixture(t)
	w := f.request("GET", "/api/v1/auth/google")
	if w.Code != 302 {
		t.Fatal("not redirected")
	}
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := location.Query()
	for key, want := range map[string]string{"scope": auth.YouTubeScope, "access_type": "offline", "include_granted_scopes": "true", "prompt": "consent", "redirect_uri": "http://localhost:8080/api/v1/auth/google/callback", "code_challenge_method": "S256"} {
		if q.Get(key) != want {
			t.Errorf("wrong %s", key)
		}
	}
	cookie := responseCookie(t, w, "sync_oauth_state")
	if len(cookie.Value) != 43 || q.Get("state") != cookie.Value || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.MaxAge != 600 || cookie.Secure {
		t.Fatal("invalid state cookie")
	}
	if f.start(t).Value == cookie.Value {
		t.Fatal("state reused")
	}
}
func TestOAuthRejectedCallbacks(t *testing.T) {
	for _, tc := range []struct {
		name, path, code string
		hasCookie        bool
	}{{"mismatch", "?state=wrong&code=test-code", "OAUTH_STATE_INVALID", true}, {"missing cookie", "?state=STATE&code=test-code", "OAUTH_STATE_INVALID", false}, {"missing state", "?code=test-code", "OAUTH_STATE_INVALID", true}, {"missing code", "?state=STATE", "OAUTH_CODE_MISSING", true}, {"denied", "?state=STATE&error=access_denied", "OAUTH_ACCESS_DENIED", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOAuthFixture(t)
			state := f.start(t)
			cookies := []*http.Cookie{}
			if tc.hasCookie {
				cookies = append(cookies, state)
			}
			w := f.request("GET", "/api/v1/auth/google/callback"+strings.ReplaceAll(tc.path, "STATE", state.Value), cookies...)
			if w.Code != 400 || !strings.Contains(w.Body.String(), tc.code) {
				t.Fatalf("wrong error: %d %s", w.Code, w.Body.String())
			}
			f.mu.Lock()
			calls := f.exchangeCount
			f.mu.Unlock()
			if calls != 0 {
				t.Fatal("invalid callback exchanged code")
			}
			if responseCookie(t, w, "sync_oauth_state").MaxAge != -1 {
				t.Fatal("state cookie not cleared")
			}
		})
	}
}
func TestOAuthSuccessStatusAndDisconnect(t *testing.T) {
	f := newOAuthFixture(t)
	if w := f.request("GET", "/api/v1/auth/google/status"); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"connected":false}` {
		t.Fatal("wrong disconnected status")
	}
	session := f.connect(t)
	if len(session.Value) != 43 || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode || session.Path != "/" {
		t.Fatal("invalid session cookie")
	}
	if _, err := f.store.Get(context.Background(), session.Value); err != nil {
		t.Fatal("token not stored")
	}
	w := f.request("GET", "/api/v1/auth/google/status", session)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"channel_id":"UC-test"`) {
		t.Fatalf("wrong status: %d %s", w.Code, w.Body.String())
	}
	w = f.request("DELETE", "/api/v1/auth/google", session)
	if w.Code != 200 || responseCookie(t, w, "sync_session").MaxAge != -1 {
		t.Fatal("disconnect failed")
	}
	if _, err := f.store.Get(context.Background(), session.Value); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatal("token survived logout")
	}
	if w := f.request("GET", "/api/v1/auth/google/status", session); strings.TrimSpace(w.Body.String()) != `{"connected":false}` {
		t.Fatal("old session connected")
	}
}
func TestOAuthExternalFailures(t *testing.T) {
	for _, stage := range []string{"exchange", "YouTube"} {
		t.Run(stage, func(t *testing.T) {
			f := newOAuthFixture(t)
			f.change(func() { f.exchangeFail = stage == "exchange"; f.youtubeFail = stage == "YouTube" })
			state := f.start(t)
			w := f.request("GET", "/api/v1/auth/google/callback?state="+state.Value+"&code=test-code", state)
			expected := "OAUTH_TOKEN_EXCHANGE_FAILED"
			if stage == "YouTube" {
				expected = "YOUTUBE_AUTH_FAILED"
			}
			if w.Code != 502 || !strings.Contains(w.Body.String(), expected) {
				t.Fatalf("wrong error: %d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "test-secret") || strings.Contains(w.Body.String(), "test-access") {
				t.Fatal("raw provider error leaked")
			}
		})
	}
	f := newOAuthFixture(t)
	session := f.connect(t)
	f.change(func() { f.youtubeFail = true })
	if w := f.request("GET", "/api/v1/auth/google/status", session); w.Code != 502 || !strings.Contains(w.Body.String(), "YOUTUBE_AUTH_FAILED") {
		t.Fatal("YouTube failure ignored")
	}
}
func TestOAuthNoChannel(t *testing.T) {
	f := newOAuthFixture(t)
	f.change(func() { f.noChannel = true })
	session := f.connect(t)
	w := f.request("GET", "/api/v1/auth/google/status", session)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"channel_id":null`) || !strings.Contains(w.Body.String(), `"connected":true`) {
		t.Fatal("channel absence treated as OAuth failure")
	}
}
func TestOAuthRefreshPersistence(t *testing.T) {
	f := newOAuthFixture(t)
	session := f.connect(t)
	token, _ := f.store.Get(context.Background(), session.Value)
	token.Expiry = time.Now().Add(-time.Hour)
	if err := f.store.Update(context.Background(), session.Value, token); err != nil {
		t.Fatal(err)
	}
	w := f.request("GET", "/api/v1/auth/google/status", session)
	f.mu.Lock()
	refreshes := f.refreshCount
	f.mu.Unlock()
	if w.Code != 200 || refreshes != 1 {
		t.Fatal("refresh not performed")
	}
	token, _ = f.store.Get(context.Background(), session.Value)
	if token.RefreshToken != "test-refresh" || !token.Valid() {
		t.Fatal("refreshed token not persisted")
	}
}
func TestOAuthReauthorization(t *testing.T) {
	for _, sameChannel := range []bool{true, false} {
		t.Run(map[bool]string{true: "same channel", false: "different channel"}[sameChannel], func(t *testing.T) {
			f := newOAuthFixture(t)
			old := f.connect(t)
			f.change(func() { f.omitRefresh = true; f.switchAccount = !sameChannel })
			session := f.connect(t, old)
			if session.Value == old.Value {
				t.Fatal("session not rotated")
			}
			token, _ := f.store.Get(context.Background(), session.Value)
			if sameChannel && token.RefreshToken == "" {
				t.Fatal("same-account refresh lost")
			}
			if !sameChannel && token.RefreshToken != "" {
				t.Fatal("different-account refresh reused")
			}
			if _, err := f.store.Get(context.Background(), old.Value); !errors.Is(err, auth.ErrSessionNotFound) {
				t.Fatal("old session not invalidated")
			}
		})
	}
}
func TestOAuthCancellation(t *testing.T) {
	f := newOAuthFixture(t)
	state := f.start(t)
	req := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state.Value+"&code=test-code", nil)
	req.AddCookie(state)
	ctx, cancel := context.WithCancel(context.WithValue(req.Context(), oauth2.HTTPClient, f.client))
	cancel()
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req.WithContext(ctx))
	f.mu.Lock()
	calls := f.exchangeCount
	f.mu.Unlock()
	if w.Code != 502 || calls != 0 {
		t.Fatal("canceled exchange not stopped")
	}
}
