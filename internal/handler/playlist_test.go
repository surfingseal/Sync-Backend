package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/sync/internal/auth"
	"example.com/sync/internal/client"
	"example.com/sync/internal/handler"
	"example.com/sync/internal/model"
	"example.com/sync/internal/service"
	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

type playlistTestSessions struct{ failure error }

func (s playlistTestSessions) WithAuthenticatedClient(ctx context.Context, id string, fn func(*http.Client) error) error {
	if s.failure != nil {
		return s.failure
	}
	return fn(&http.Client{})
}

type playlistTestClient struct {
	createErr error
	failID    string
	failCode  string
}

func (p playlistTestClient) CreatePlaylist(ctx context.Context, r model.CreatePlaylistRequest) (*model.PlaylistResult, error) {
	if p.createErr != nil {
		return nil, p.createErr
	}
	return &model.PlaylistResult{ID: "PL-created", Title: r.Title, PrivacyStatus: r.PrivacyStatus, URL: client.PlaylistURL("PL-created")}, nil
}
func (p playlistTestClient) AddVideo(ctx context.Context, playlistID, videoID string, position int64) (string, error) {
	if videoID == p.failID {
		return "", &client.PlaylistError{Code: p.failCode}
	}
	return "actual-item-" + videoID, nil
}
func playlistHandlerTest(method, body string, cookie bool, sessionFailure error, api playlistTestClient, origin string) *httptest.ResponseRecorder {
	svc := service.NewPlaylistService(playlistTestSessions{sessionFailure}, func(context.Context, *http.Client) (client.PlaylistClient, error) { return api, nil }, time.Second)
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	h := handler.NewPlaylistHandler(svc, "http://localhost:8080/api/v1/auth/google/callback")
	r.POST("/api/v1/playlists", h.Create)
	request := httptest.NewRequest(method, "/api/v1/playlists", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if cookie {
		request.AddCookie(&http.Cookie{Name: "sync_session", Value: "fake-session"})
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, request)
	return w
}
func TestPlaylistHandlerSuccessAndPartial(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "partial"}[partial], func(t *testing.T) {
			api := playlistTestClient{}
			if partial {
				api.failID = "B"
				api.failCode = "VIDEO_NOT_FOUND"
			}
			w := playlistHandlerTest("POST", `{"title":" Test ","tracks":[{"video_id":"A"},{"video_id":"B"}]}`, true, nil, api, "http://localhost:8080")
			if w.Code != 201 {
				t.Fatalf("status=%d %s", w.Code, w.Body.String())
			}
			var body model.CreatePlaylistResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Playlist.PrivacyStatus != "private" || body.Partial != partial || body.RequestedCount != 2 {
				t.Fatal("wrong result")
			}
			for _, secret := range []string{"access_token", "refresh_token", "client_secret", "authorization_code", "fake-session"} {
				if strings.Contains(w.Body.String(), secret) {
					t.Fatal("confidential information in response")
				}
			}
		})
	}
}
func TestPlaylistHandlerErrors(t *testing.T) {
	valid := `{"title":"Test","tracks":[{"video_id":"A"}]}`
	for _, tc := range []struct {
		name, body string
		cookie     bool
		sessionErr error
		api        playlistTestClient
		status     int
		code       string
	}{
		{name: "no session", body: valid, status: 401, code: "YOUTUBE_NOT_CONNECTED"},
		{name: "no token", body: valid, cookie: true, sessionErr: auth.ErrSessionNotFound, status: 401, code: "YOUTUBE_NOT_CONNECTED"},
		{name: "expired auth", body: valid, cookie: true, sessionErr: auth.ErrAuthExpired, status: 401, code: "YOUTUBE_AUTH_EXPIRED"},
		{name: "invalid", body: `{"title":"Test","tracks":[]}`, cookie: true, status: 400, code: "INVALID_REQUEST"},
		{name: "create failed", body: valid, cookie: true, api: playlistTestClient{createErr: &client.PlaylistError{Code: "PLAYLIST_CREATE_FAILED"}}, status: 502, code: "PLAYLIST_CREATE_FAILED"},
		{name: "quota before creation", body: valid, cookie: true, api: playlistTestClient{createErr: &client.PlaylistError{Code: "YOUTUBE_QUOTA_EXCEEDED"}}, status: 503, code: "YOUTUBE_QUOTA_EXCEEDED"},
		{name: "unknown creation", body: valid, cookie: true, api: playlistTestClient{createErr: &client.PlaylistError{Code: "PLAYLIST_CREATE_RESULT_UNKNOWN"}}, status: 502, code: "PLAYLIST_CREATE_RESULT_UNKNOWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := playlistHandlerTest("POST", tc.body, tc.cookie, tc.sessionErr, tc.api, "")
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
				t.Fatalf("wrong error: %d %s", w.Code, w.Body.String())
			}
		})
	}
	r := model.CreatePlaylistRequest{Title: "Test", Tracks: make([]model.PlaylistTrack, 21)}
	for i := range r.Tracks {
		r.Tracks[i].VideoID = "A"
	}
	body, _ := json.Marshal(r)
	if w := playlistHandlerTest("POST", string(body), true, nil, playlistTestClient{}, ""); w.Code != 400 || !strings.Contains(w.Body.String(), "PLAYLIST_TOO_MANY_TRACKS") {
		t.Fatal("limit not enforced before dedup")
	}
	if w := playlistHandlerTest("POST", valid, true, nil, playlistTestClient{}, "https://attacker.example"); w.Code != 403 {
		t.Fatal("cross-origin cookie write accepted")
	}
}

// Exercise the existing real OAuth TokenSource/store through a local fake SDK provider.
func TestPlaylistAuthenticatedSessionAndRefresh(t *testing.T) {
	f := newOAuthFixture(t)
	session := f.connect(t)
	token, _ := f.store.Get(context.Background(), session.Value)
	token.Expiry = time.Now().Add(-time.Hour)
	if err := f.store.Update(context.Background(), session.Value, token); err != nil {
		t.Fatal(err)
	}
	// /playlists on the actual router recognizes the saved browser session.
	req := httptest.NewRequest("POST", "/api/v1/playlists", strings.NewReader(`{"title":"Test","tracks":[{"video_id":"A"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(session)
	req = req.WithContext(context.WithValue(req.Context(), oauth2.HTTPClient, f.client))
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	// Fake provider intentionally rejects write paths: refresh must occur before write,
	// and an HTTP failure must be surfaced rather than creating fictional IDs.
	if w.Code != 502 {
		t.Fatalf("unexpected status: %d", w.Code)
	}
	f.mu.Lock()
	refreshes := f.refreshCount
	f.mu.Unlock()
	if refreshes != 1 {
		t.Fatal("OAuth refresh not reused")
	}
	token, _ = f.store.Get(context.Background(), session.Value)
	if token.RefreshToken == "" || !token.Valid() {
		t.Fatal("refresh not persisted")
	}
}
