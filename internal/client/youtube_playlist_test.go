package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"encoding/json"
	"example.com/sync/internal/auth"
	"example.com/sync/internal/model"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

func TestYouTubePlaylistSDK(t *testing.T) {
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-token" || r.URL.Query().Get("key") != "" {
			t.Error("wrong write authentication")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		snippet := body["snippet"].(map[string]any)
		switch r.URL.Path {
		case "/youtube/v3/playlists":
			if len(r.URL.Query()["part"]) != 2 || snippet["title"] != "Title" || body["status"].(map[string]any)["privacyStatus"] != "private" {
				t.Error("wrong playlist body")
			}
			io.WriteString(w, `{"id":"PL-actual","snippet":{"title":"Title"},"status":{"privacyStatus":"private"}}`)
		case "/youtube/v3/playlistItems":
			resource := snippet["resourceId"].(map[string]any)
			if snippet["playlistId"] != "PL-actual" || snippet["position"] != float64(0) || resource["kind"] != "youtube#video" || resource["videoId"] != "video" {
				t.Error("wrong playlist item body or zero position omitted")
			}
			io.WriteString(w, `{"id":"actual-item"}`)
		default:
			t.Error("unexpected request path")
		}
	}))
	defer provider.Close()
	api, err := youtube.NewService(context.Background(), option.WithHTTPClient(&http.Client{Transport: bearerTransport{http.DefaultTransport}}), option.WithEndpoint(provider.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	client := &YouTubePlaylistClient{api}
	playlist, err := client.CreatePlaylist(context.Background(), model.CreatePlaylistRequest{Title: "Title", PrivacyStatus: "private"})
	if err != nil || playlist.ID != "PL-actual" {
		t.Fatal("playlist response not used")
	}
	id, err := client.AddVideo(context.Background(), playlist.ID, "video", 0)
	if err != nil || id != "actual-item" || calls != 2 {
		t.Fatal("item response not used")
	}
}

type bearerTransport struct{ base http.RoundTripper }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("Authorization", "Bearer test-token")
	return b.base.RoundTrip(copy)
}
func TestPlaylistWriteNeverRetries(t *testing.T) {
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		io.WriteString(w, `{"error":{"code":503,"message":"private provider details"}}`)
	}))
	defer provider.Close()
	api, err := youtube.NewService(context.Background(), option.WithHTTPClient(provider.Client()), option.WithEndpoint(provider.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	c := &YouTubePlaylistClient{api}
	_, err = c.CreatePlaylist(context.Background(), model.CreatePlaylistRequest{Title: "T", PrivacyStatus: "private"})
	if PlaylistErrorCode(err) != "PLAYLIST_CREATE_RESULT_UNKNOWN" || calls != 1 {
		t.Fatal("create retried or ambiguity lost")
	}
	_, err = c.AddVideo(context.Background(), "PL", "video", 0)
	if PlaylistErrorCode(err) != "VIDEO_ADD_RESULT_UNKNOWN" || calls != 2 {
		t.Fatal("add retried or ambiguity lost")
	}
}
func TestPlaylistErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{&googleapi.Error{Code: 403, Errors: []googleapi.ErrorItem{{Reason: "quotaExceeded"}}}, "YOUTUBE_QUOTA_EXCEEDED"},
		{&googleapi.Error{Code: 404, Errors: []googleapi.ErrorItem{{Reason: "videoNotFound"}}}, "VIDEO_NOT_FOUND"},
		{&googleapi.Error{Code: 403}, "VIDEO_ADD_FORBIDDEN"},
		{&googleapi.Error{Code: 401}, "YOUTUBE_AUTH_EXPIRED"},
		{auth.ErrAuthExpired, "YOUTUBE_AUTH_EXPIRED"},
		{&googleapi.Error{Code: 429}, "YOUTUBE_SERVICE_UNAVAILABLE"},
		{errors.New("private token body"), "VIDEO_ADD_RESULT_UNKNOWN"},
	} {
		if code := PlaylistErrorCode(mapPlaylistError(tc.err, false)); code != tc.code {
			t.Fatalf("wrong safe code: %s", code)
		}
	}
}
