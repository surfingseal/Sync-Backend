package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/sync/internal/model"
	"google.golang.org/api/googleapi"
)

func TestYouTubeSDKParametersAndMetadata(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/nonexistent/credentials.json")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if q.Get("key") != "unit-test-key" || r.Header.Get("Authorization") != "" {
			t.Error("API-key/ADC authentication separation failed")
		}
		w.Header().Set("Content-Type", "application/json")
		switch strings.TrimPrefix(r.URL.Path, "/youtube/v3") {
		case "/search":
			for key, want := range map[string]string{"part": "snippet", "order": "relevance", "type": "video", "videoCategoryId": "10", "regionCode": "KR", "relevanceLanguage": "ko", "videoEmbeddable": "true", "videoSyndicated": "true", "safeSearch": "moderate", "maxResults": "50", "q": "calm acoustic song -mix -playlist -compilation -backing -karaoke"} {
				if q.Get(key) != want {
					t.Errorf("%s mismatch", key)
				}
			}
			io.WriteString(w, `{"items":[{"id":{"kind":"youtube#video","videoId":"12345678901"}},{"id":{"kind":"youtube#channel","channelId":"channel"}}]}`)
		case "/videos":
			if strings.Join(q["part"], ",") != "snippet,contentDetails,status,statistics" || strings.Join(q["id"], ",") != "12345678901,12345678902" {
				t.Error("wrong batch/parts")
			}
			io.WriteString(w, `{"items":[{"id":"12345678901","snippet":{"description":"Real description","channelId":"channel-id","publishedAt":"2024-01-01T00:00:00Z","title":"Actual source title","channelTitle":"Actual channel","categoryId":"10","defaultAudioLanguage":"ko","thumbnails":{"high":{"url":"https://example.com/high.jpg"}}},"contentDetails":{"duration":"PT4M2S","licensedContent":true,"regionRestriction":{"blocked":["US"]}},"statistics":{"viewCount":"987654321","likeCount":"12345","commentCount":"54"},"status":{"embeddable":true,"privacyStatus":"public"}}]}`)
		default:
			t.Error("unexpected SDK method")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	y, err := NewYouTubeClient(context.Background(), "unit-test-key")
	if err != nil {
		t.Fatal(err)
	}
	y.service.BasePath = server.URL + "/"
	hits, err := y.SearchMusic(context.Background(), model.MusicSearchQuery{Text: "calm acoustic song -mix -playlist -compilation -backing -karaoke", Region: "KR", RelevanceLanguage: "ko", MaxResults: 50})
	if err != nil || len(hits) != 1 {
		t.Fatal(hits, err)
	}
	videos, err := y.GetVideos(context.Background(), []string{hits[0].VideoID, "12345678902"})
	if err != nil || len(videos) != 1 {
		t.Fatal(videos, err)
	}
	v := videos[0]
	if v.ViewCount != 987654321 || v.LikeCount == nil || *v.LikeCount != 12345 || v.CommentCount == nil || *v.CommentCount != 54 || !v.LicensedContent || v.ChannelID != "channel-id" || v.PublishedAt == "" || v.Description != "Real description" || v.Title != "Actual source title" || v.ChannelTitle != "Actual channel" || v.DurationSeconds != 242 || v.CategoryID != "10" || !v.Embeddable || !v.Public || v.ThumbnailURL != "https://example.com/high.jpg" || v.BlockedRegions[0] != "US" {
		t.Fatal(v)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
func TestYouTubeErrorSanitization(t *testing.T) {
	for _, tc := range []struct {
		reason string
		code   int
		want   error
	}{{"quotaExceeded", 403, ErrYouTubeQuota}, {"dailyLimitExceeded", 403, ErrYouTubeQuota}, {"", 429, ErrYouTubeQuota}, {"forbidden", 403, ErrMusicSearch}, {"backendError", 500, ErrMusicSearch}} {
		err := mapYouTubeError(context.Background(), &googleapi.Error{Code: tc.code, Message: "secret-key", Body: "secret-key", Errors: []googleapi.ErrorItem{{Reason: tc.reason}}})
		if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "secret-key") {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(mapYouTubeError(ctx, errors.New("secret")), context.Canceled) {
		t.Fatal("context not propagated")
	}
	y, err := NewYouTubeClient(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.SearchMusic(context.Background(), model.MusicSearchQuery{}); !errors.Is(err, ErrMusicNotConfigured) {
		t.Fatal(err)
	}
}
func TestYouTubeDuration(t *testing.T) {
	for _, tc := range []struct {
		s       string
		seconds int64
	}{{"PT1M30S", 90}, {"PT12M", 720}, {"PT1H2M3S", 3723}, {"P1DT1S", 86401}, {"PT0S", 0}} {
		n, err := parseYouTubeDuration(tc.s)
		if err != nil || n != tc.seconds {
			t.Fatal(tc, n, err)
		}
	}
	for _, s := range []string{"", "P", "PT", "4:32", "PT999999999999999999999999999S", "P9999999999999999999D"} {
		if _, err := parseYouTubeDuration(s); err == nil {
			t.Fatal("invalid duration accepted")
		}
	}
}

func TestYouTubeSearchPoolBounds(t *testing.T) {
	y, _ := NewYouTubeClient(context.Background(), "test-key")
	for _, n := range []int64{0, 51, 100} {
		if _, err := y.SearchMusic(context.Background(), model.MusicSearchQuery{Text: "music", MaxResults: n}); !errors.Is(err, ErrMusicSearch) {
			t.Fatal(n, err)
		}
	}
}
