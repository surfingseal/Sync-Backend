package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"example.com/sync/internal/auth"
	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	"example.com/sync/internal/directapi"
	"example.com/sync/internal/handler"
	"example.com/sync/internal/mobileauth"
	"example.com/sync/internal/model"
	"example.com/sync/internal/router"
	"example.com/sync/internal/service"
	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

type mobileFixture struct {
	engine http.Handler
	oauth  *auth.GoogleOAuthService
	store  *auth.InMemoryTokenStore
	client *http.Client
	calls  atomic.Int32
	writes atomic.Int32
}

func newMobileFixture(t *testing.T) *mobileFixture {
	t.Helper()
	f := &mobileFixture{store: auth.NewInMemoryTokenStore()}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			io.WriteString(w, `{"access_token":"fake-google-access","refresh_token":"fake-google-refresh","token_type":"Bearer","expires_in":3600}`)
		case "/youtube/v3/channels":
			io.WriteString(w, `{"items":[{"id":"UC-test","snippet":{"title":"Test Channel"}}]}`)
		case "/youtube/v3/playlists":
			f.writes.Add(1)
			io.WriteString(w, `{"id":"PL-test","snippet":{"title":"Test"},"status":{"privacyStatus":"private"}}`)
		case "/youtube/v3/playlistItems":
			f.writes.Add(1)
			io.WriteString(w, `{"id":"item-test"}`)
		default:
			t.Errorf("unexpected fake path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(fake.Close)
	target, _ := url.Parse(fake.URL)
	f.client = &http.Client{Transport: providerTransport{http.DefaultTransport, target}, Timeout: time.Second}
	f.oauth = auth.NewGoogleOAuthService(auth.Settings{ClientID: "fake-client", ClientSecret: "fake-client-secret", RedirectURL: "https://sync.example/api/v1/auth/google/callback", Scope: auth.YouTubeScope, CookieSecure: true}, f.store, auth.YouTubeChannelVerifier{})
	f.engine = router.New(config.Config{AppEnv: "development", GoogleOAuthRedirectURL: "https://sync.example/api/v1/auth/google/callback", MobileAppLinkBaseURL: "https://sync.example", AndroidPackageName: "org.test.sync", AndroidAppSigningSHA256: strings.TrimSuffix(strings.Repeat("AB:", 32), ":")}, nil, nil, f.oauth)
	return f
}
func (f *mobileFixture) request(method, path, body, bearer string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	r = r.WithContext(context.WithValue(r.Context(), oauth2.HTTPClient, f.client))
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, r)
	return w
}
func (f *mobileFixture) start(t *testing.T) (string, *http.Cookie) {
	t.Helper()
	v := strings.Repeat("a", 43)
	body, _ := json.Marshal(map[string]string{"code_challenge": mobileauth.Challenge(v), "code_challenge_method": "S256"})
	w := f.request("POST", "/api/v1/auth/google/mobile/start", string(body), "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var res mobileauth.StartResponse
	json.Unmarshal(w.Body.Bytes(), &res)
	if res.ExpiresIn != 300 {
		t.Fatal(res)
	}
	u, _ := url.Parse(res.AuthorizationURL)
	w = f.request("GET", u.RequestURI(), "", "")
	if w.Code != 302 {
		t.Fatal(w.Code, w.Body.String())
	}
	return v, responseCookie(t, w, "sync_oauth_state")
}
func (f *mobileFixture) connect(t *testing.T) string {
	t.Helper()
	v, cookie := f.start(t)
	w := f.request("GET", "/api/v1/auth/google/callback?state="+cookie.Value+"&code=fake-authorization-code", "", "", cookie)
	if w.Code != 302 {
		t.Fatal(w.Code, w.Body.String())
	}
	u, _ := url.Parse(w.Header().Get("Location"))
	if u.Host != "sync.example" || u.Path != "/auth/android/complete" || len(u.Query()) != 1 {
		t.Fatal("bad redirect")
	}
	body, _ := json.Marshal(map[string]string{"code": u.Query().Get("code"), "code_verifier": v})
	w = f.request("POST", "/api/v1/auth/mobile/exchange", string(body), "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var res mobileauth.ExchangeResponse
	json.Unmarshal(w.Body.Bytes(), &res)
	if res.SessionToken == "" {
		t.Fatal("no session")
	}
	for _, secret := range []string{"fake-google-access", "fake-google-refresh", "fake-client-secret", "fake-authorization-code", "access_token", "refresh_token"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("leaked Google credential")
		}
	}
	return res.SessionToken
}
func TestMobileHTTPFlowStatusAndPlaylist(t *testing.T) {
	f := newMobileFixture(t)
	token := f.connect(t)
	w := f.request("GET", "/api/v1/auth/google/status", "", token)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"connected":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	body := `{"title":"Test","tracks":[{"video_id":"fake-video"}]}`
	w = f.request("POST", "/api/v1/playlists", body, token)
	if w.Code != 201 || f.writes.Load() != 2 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, secret := range []string{token, "fake-google-access", "fake-google-refresh"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("secret in playlist")
		}
	}
	w = f.request("POST", "/api/v1/playlists", body, "")
	if w.Code != 401 {
		t.Fatal("anonymous playlist", w.Code)
	}
	w = f.request("POST", "/api/v1/playlists", `{"title":"Test","recommendation_id":"rec_`+strings.Repeat("a", 43)+`"}`, "")
	if w.Code != 401 {
		t.Fatal("anonymous checkpoint", w.Code)
	}
}
func TestMobileBearerCookieConflicts(t *testing.T) {
	f := newMobileFixture(t)
	token := f.connect(t) // Obtain the internal reference through middleware solely in this test.
	// The fixture's callback-generated Google session is intentionally not exposed.
	// A separate valid browser login must be rejected conservatively as distinct.
	state := f.request("GET", "/api/v1/auth/google", "", "")
	cookie := responseCookie(t, state, "sync_oauth_state")
	w := f.request("GET", "/api/v1/auth/google/callback?state="+cookie.Value+"&code=fake-web-code", "", "", cookie)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	browser := responseCookie(t, w, "sync_session")
	w = f.request("GET", "/api/v1/auth/google/status", "", token, browser)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "AUTH_IDENTITY_CONFLICT") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = f.request("GET", "/api/v1/auth/google/status", "", "invalid", browser)
	if w.Code != 401 {
		t.Fatal("invalid bearer fell back to cookie", w.Code)
	}
	w = f.request("GET", "/api/v1/auth/google/status", "", "", browser)
	if w.Code != 200 {
		t.Fatal("browser regression", w.Code)
	}
	w = f.request("GET", "/api/v1/auth/google/status", "", token, &http.Cookie{Name: "sync_session", Value: "stale"})
	if w.Code != 200 {
		t.Fatal("stale cookie shouldn't claim identity", w.Code)
	}
}
func TestMobileLogoutAndDisconnect(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		f := newMobileFixture(t)
		token := f.connect(t)
		path := "/api/v1/auth/mobile/session"
		if disconnect {
			path = "/api/v1/auth/google"
		}
		w := f.request("DELETE", path, "", token)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		w = f.request("GET", "/api/v1/auth/google/status", "", token)
		if w.Code != 401 {
			t.Fatal("revoked token usable", w.Code)
		}
	}
}
func TestMobileBadCallbackDoesNotExchange(t *testing.T) {
	for _, kind := range []string{"wrong-cookie", "missing-cookie", "missing-code", "denied"} {
		t.Run(kind, func(t *testing.T) {
			f := newMobileFixture(t)
			_, cookie := f.start(t)
			path := "/api/v1/auth/google/callback?state=" + cookie.Value
			cookies := []*http.Cookie{cookie}
			switch kind {
			case "wrong-cookie":
				cookies = []*http.Cookie{{Name: "sync_oauth_state", Value: strings.Repeat("z", 43)}}
				path += "&code=fake-code"
			case "missing-cookie":
				cookies = nil
				path += "&code=fake-code"
			case "denied":
				path += "&error=access_denied"
			}
			w := f.request("GET", path, "", "", cookies...)
			if w.Code != 400 || f.calls.Load() != 0 {
				t.Fatal(w.Code, f.calls.Load())
			}
		})
	}
}
func TestMobileFeatureUnavailableAndAssetLinks(t *testing.T) {
	r := router.New(config.Config{AppEnv: "production"}, nil, nil)
	for _, path := range []string{"/api/v1/auth/google/mobile/start", "/api/v1/auth/mobile/exchange", "/.well-known/assetlinks.json", "/auth/android/complete"} {
		method := "GET"
		if strings.Contains(path, "start") || strings.Contains(path, "exchange") {
			method = "POST"
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != 503 {
			t.Fatal(path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if w.Code != 200 {
		t.Fatal("feature broke server")
	}
	f := newMobileFixture(t)
	w = f.request("GET", "/.well-known/assetlinks.json", "", "")
	if w.Code != 200 || w.Header().Get("Location") != "" || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Fatal(w.Code)
	}
	if !strings.Contains(w.Body.String(), "org.test.sync") {
		t.Fatal(w.Body.String())
	}
	w = f.request("GET", "/auth/android/complete?code=sensitive-handoff", "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "sensitive-handoff") || w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("unsafe completion")
	}
}

// synchronized capture also tolerates concurrent logger calls in unrelated tests.
type safeLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *safeLog) Write(p []byte) (int, error) { b.mu.Lock(); defer b.mu.Unlock(); return b.b.Write(p) }
func (b *safeLog) String() string              { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }
func TestMobileSecretLoggingAbsent(t *testing.T) {
	capture := &safeLog{}
	old := log.Writer()
	oldGin := gin.DefaultWriter
	log.SetOutput(capture)
	gin.DefaultWriter = capture
	t.Cleanup(func() { log.SetOutput(old); gin.DefaultWriter = oldGin })
	f := newMobileFixture(t)
	token := f.connect(t)
	f.request("GET", "/api/v1/auth/google/status", "", token)
	for _, secret := range []string{token, "fake-google-access", "fake-google-refresh", "fake-client-secret", "fake-authorization-code", strings.Repeat("a", 43), "?state=", "?transaction=", "?code="} {
		if strings.Contains(capture.String(), secret) {
			t.Fatal("confidential data logged")
		}
	}
}

// Standalone middleware verifies the same-reference policy and service injection
// without any network, including successful playlist execution with fake clients.
type middlewareProvider struct{}

func (middlewareProvider) Configured() bool { return true }
func (middlewareProvider) Start(context.Context, string) (string, string, error) {
	s, _ := auth.RandomID()
	return s, "https://fake.example", nil
}
func (middlewareProvider) BindBrowser(context.Context, string, string) error { return nil }
func (middlewareProvider) Complete(context.Context, string, string, string) (string, *auth.Connection, error) {
	return "same-ref", &auth.Connection{Connected: true}, nil
}
func (middlewareProvider) Cancel(string, string) {}
func (middlewareProvider) SessionExists(_ context.Context, id string) (bool, error) {
	return id == "same-ref", nil
}
func TestMobileSameCredentialCookieAndFakePlaylist(t *testing.T) {
	cfg := mobileauth.Settings{BaseURL: "https://sync.example", PackageName: "org.test.sync", SigningSHA256: strings.TrimSuffix(strings.Repeat("AB:", 32), ":"), OAuthRedirectURL: "https://sync.example/api/v1/auth/google/callback"}
	p := middlewareProvider{}
	mobile := mobileauth.New(cfg, p, mobileauth.NewMemoryStore(16))
	v := strings.Repeat("a", 43)
	start, _ := mobile.Start(context.Background(), mobileauth.Challenge(v), "S256")
	u, _ := url.Parse(start.AuthorizationURL)
	tx, _ := mobile.Begin(context.Background(), u.Query().Get("transaction"), "")
	location, _ := mobile.Complete(context.Background(), tx.OAuthState, tx.OAuthState, "fake")
	u, _ = url.Parse(location)
	session, _ := mobile.Exchange(context.Background(), u.Query().Get("code"), v)
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(handler.Authenticate(mobile, p))
	svc := service.NewPlaylistService(playlistTestSessions{}, func(context.Context, *http.Client) (client.PlaylistClient, error) { return playlistTestClient{}, nil }, time.Second)
	r.POST("/playlists", handler.NewPlaylistHandler(svc, cfg.OAuthRedirectURL).Create)
	req := httptest.NewRequest("POST", "/playlists", strings.NewReader(`{"title":"Test","tracks":[{"video_id":"A"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+session.SessionToken)
	req.AddCookie(&http.Cookie{Name: "sync_session", Value: "same-ref"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestMobileCheckpointUsesResolvedCredentialWithoutRerunning(t *testing.T) {
	cfg := mobileauth.Settings{BaseURL: "https://sync.example", PackageName: "org.test.sync", SigningSHA256: strings.TrimSuffix(strings.Repeat("AB:", 32), ":"), OAuthRedirectURL: "https://sync.example/api/v1/auth/google/callback"}
	provider := middlewareProvider{}
	mobile := mobileauth.New(cfg, provider, mobileauth.NewMemoryStore(16))
	verifier := strings.Repeat("a", 43)
	start, _ := mobile.Start(context.Background(), mobileauth.Challenge(verifier), "S256")
	u, _ := url.Parse(start.AuthorizationURL)
	tx, _ := mobile.Begin(context.Background(), u.Query().Get("transaction"), "")
	location, _ := mobile.Complete(context.Background(), tx.OAuthState, tx.OAuthState, "fake")
	u, _ = url.Parse(location)
	session, _ := mobile.Exchange(context.Background(), u.Query().Get("code"), verifier)
	recommender, calls := directFixture(t, 3, nil)
	data := validPNG(t)
	response, err := recommender.Recommend(context.Background(), model.UploadedImage{ImageInfo: model.ImageInfo{Filename: "test.png", ContentType: "image/png", Size: int64(len(data))}, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	sessions := playlistTestSessions{}
	creator := service.NewPlaylistService(sessions, func(context.Context, *http.Client) (client.PlaylistClient, error) { return playlistTestClient{}, nil }, time.Second)
	checkpoints := directapi.NewPlaylists(recommender.Store, sessions, creator)
	route := gin.New()
	route.Use(handler.Authenticate(mobile, provider))
	route.POST("/playlists", handler.NewPlaylistHandler(creator, cfg.OAuthRedirectURL, checkpoints).Create)
	body, _ := json.Marshal(map[string]string{"title": "Test", "recommendation_id": response.RecommendationID})
	for _, bearer := range []string{"", session.SessionToken, session.SessionToken} {
		req := httptest.NewRequest("POST", "/playlists", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		route.ServeHTTP(w, req)
		expected := 201
		if bearer == "" {
			expected = 401
		}
		if w.Code != expected {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatal("checkpoint creation reran recommendation")
	}
}
