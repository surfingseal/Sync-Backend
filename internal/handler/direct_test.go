package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	"example.com/sync/internal/directapi"
	"example.com/sync/internal/directmusic"
	"example.com/sync/internal/handler"
	imageprocessing "example.com/sync/internal/image"
	"example.com/sync/internal/model"
	"example.com/sync/internal/router"
	"example.com/sync/internal/service"
	"github.com/gin-gonic/gin"
)

type directRunner struct {
	result *directmusic.Result
	err    error
	calls  *atomic.Int64
}

func (r directRunner) Run(context.Context, []byte, string) (*directmusic.Result, error) {
	r.calls.Add(1)
	return r.result, r.err
}
func directFixture(t *testing.T, n int, err error) (*directapi.Recommender, *atomic.Int64) {
	t.Helper()
	base, e := directapi.LoadFixture("../directapi/testdata/completed-e2e")
	if e != nil {
		t.Fatal(e)
	}
	// Synthetic metadata clones are contract fixtures, never live evidence.
	result := &directmusic.Result{Generated: &model.DirectMusicRecommendation{Scene: model.DirectScene{Description: "가을 밤 불꽃놀이 중인 공원"}, Playlist: model.DirectPlaylist{Title: "Autumn Fireworks"}}, Normalized: []model.DirectTrack{}, Resolutions: []directmusic.Resolution{}, Final: []directmusic.VerifiedTrack{}}
	for i := 0; i < n; i++ {
		track := base.Final[0]
		track.Gemini.Artist = fmt.Sprint("mock artist ", i)
		track.Gemini.Title = fmt.Sprint("mock song ", i)
		track.Gemini.LyricLanguage = model.LyricEN
		if i < 2 {
			track.Gemini.LyricLanguage = model.LyricKO
		}
		track.VideoID = fmt.Sprint("mock-video-", i)
		track.VideoTitle = fmt.Sprint("mock YouTube title ", i)
		rr := base.Resolutions[0]
		v := *rr.Video
		v.VideoID = track.VideoID
		v.Title = track.VideoTitle
		rr.Video = &v
		rr.Candidate = track.Gemini
		result.Normalized = append(result.Normalized, track.Gemini)
		result.Resolutions = append(result.Resolutions, rr)
		result.Final = append(result.Final, track)
	}
	processor, e := imageprocessing.NewProcessor(1920, imageprocessing.DefaultHardMaxBytes)
	if e != nil {
		t.Fatal(e)
	}
	count := new(atomic.Int64)
	return &directapi.Recommender{Store: directapi.NewStore(), Processor: processor, NewRunner: func() directapi.Runner { return directRunner{result, err, count} }}, count
}
func imageRequest(t *testing.T, data []byte, field string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if field != "" {
		p, e := w.CreateFormFile(field, "test.png")
		if e != nil {
			t.Fatal(e)
		}
		_, _ = p.Write(data)
	}
	_ = w.Close()
	req := httptest.NewRequest("POST", "/api/v1/recommend/direct", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}
func validPNG(t *testing.T) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestDirectVariableContract(t *testing.T) {
	for _, n := range []int{0, 3, 5, 8, 10} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s, calls := directFixture(t, n, nil)
			r := router.NewWithDirect(config.Config{AppEnv: "development", RecommendationEngine: config.DirectEngine}, nil, nil, nil, s, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, imageRequest(t, validPNG(t), "image"))
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			var response directapi.Response
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.RecommendationID == "" || response.TargetTrackCount != 5 || len(response.Tracks) != min(n, 5) || response.Partial != (n < 5) || calls.Load() != 1 {
				t.Fatal(response, calls.Load())
			}
			if n > 0 && (response.Tracks[0].TrackTitle == response.Tracks[0].YouTubeTitle || response.Tracks[0].Rank != 1) {
				t.Fatal(response)
			}
			if response.Playlist.Title == "" || response.Scene.Description == "" || strings.Contains(w.Body.String(), "access_token") {
				t.Fatal("field meaning or secret")
			}
			cp, err := s.Store.Get(context.Background(), response.RecommendationID)
			if err != nil || len(cp.Response.Tracks) != min(n, 5) {
				t.Fatal(cp, err)
			}
		})
	}
}
func TestDirectUploadValidation(t *testing.T) {
	good := validPNG(t)
	badPNG := append([]byte(nil), good[:24]...)
	for _, tc := range []struct {
		name   string
		data   []byte
		field  string
		status int
		code   string
	}{
		{"missing", nil, "", 400, "IMAGE_REQUIRED"}, {"empty", nil, "image", 400, "INVALID_IMAGE"}, {"text", []byte("text"), "image", 415, "UNSUPPORTED_IMAGE_TYPE"}, {"malformed image", badPNG, "image", 400, "INVALID_IMAGE"}, {"oversize", bytes.Repeat([]byte{'x'}, int(service.MaxImageSize)+1), "image", 413, "IMAGE_TOO_LARGE"}, {"unknown field", good, "other", 400, "INVALID_REQUEST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, calls := directFixture(t, 3, nil)
			r := gin.New()
			r.POST("/api/v1/recommend/direct", handler.NewDirectHandler(s).Recommend)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, imageRequest(t, tc.data, tc.field))
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || calls.Load() != 0 {
				t.Fatal(w.Code, w.Body.String(), calls.Load())
			}
		})
	}
	s, _ := directFixture(t, 3, nil)
	r := gin.New()
	r.POST("/api/v1/recommend/direct", handler.NewDirectHandler(s).Recommend)
	req := httptest.NewRequest("POST", "/api/v1/recommend/direct", strings.NewReader("broken"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestDirectErrorsAndReadiness(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
	}{{context.DeadlineExceeded, 504}, {errors.New("secret-provider-body"), 502}} {
		s, calls := directFixture(t, 0, tc.err)
		r := gin.New()
		r.POST("/api/v1/recommend/direct", handler.NewDirectHandler(s).Recommend)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, imageRequest(t, validPNG(t), "image"))
		if w.Code != tc.code || calls.Load() != 1 || strings.Contains(w.Body.String(), "secret-provider-body") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, cfg := range []config.Config{{AppEnv: "production", RecommendationEngine: config.DirectEngine}, {AppEnv: "development", RecommendationEngine: config.LegacyEngine}} {
		s, calls := directFixture(t, 5, nil)
		r := router.NewWithDirect(cfg, nil, nil, nil, s, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, imageRequest(t, validPNG(t), "image"))
		if w.Code != 503 || calls.Load() != 0 {
			t.Fatal(w.Code, calls.Load())
		}
	}
	engine, err := config.ParseRecommendationEngine("")
	if err != nil || engine != config.LegacyEngine || config.ValidateServerEngine(config.DirectEngine) == nil {
		t.Fatal("production guard changed")
	}
}

func TestCheckpointPlaylistHTTP(t *testing.T) {
	s, calls := directFixture(t, 3, nil)
	directRoute := gin.New()
	directRoute.POST("/api/v1/recommend/direct", handler.NewDirectHandler(s).Recommend)
	w := httptest.NewRecorder()
	directRoute.ServeHTTP(w, imageRequest(t, validPNG(t), "image"))
	var response directapi.Response
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	sessions := playlistTestSessions{}
	creator := service.NewPlaylistService(sessions, func(context.Context, *http.Client) (client.PlaylistClient, error) { return playlistTestClient{}, nil }, service.DefaultPlaylistTimeout)
	checkpoints := directapi.NewPlaylists(s.Store, sessions, creator)
	route := gin.New()
	route.POST("/api/v1/playlists", handler.NewPlaylistHandler(creator, "http://localhost:8080/api/v1/auth/google/callback", checkpoints).Create)
	good := fmt.Sprintf(`{"recommendation_id":%q,"title":"Test"}`, response.RecommendationID)
	for _, tc := range []struct {
		body     string
		cookie   bool
		code     int
		contains string
	}{
		{good, false, 401, "YOUTUBE_NOT_CONNECTED"},
		{good, true, 201, `"added_count":3`},
		{good, true, 201, `"added_count":3`},
		{fmt.Sprintf(`{"recommendation_id":%q,"title":"Test","tracks":[{"video_id":"modified"}]}`, response.RecommendationID), true, 400, "INVALID_REQUEST"},
		{`{"recommendation_id":"invalid","title":"Test"}`, true, 400, "INVALID_RECOMMENDATION_ID"},
		{fmt.Sprintf(`{"recommendation_id":%q,"title":"Changed"}`, response.RecommendationID), true, 409, "PLAYLIST_REQUEST_CONFLICT"},
	} {
		req := httptest.NewRequest("POST", "/api/v1/playlists", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		if tc.cookie {
			req.AddCookie(&http.Cookie{Name: "sync_session", Value: "mock-session"})
		}
		w := httptest.NewRecorder()
		route.ServeHTTP(w, req)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.contains) {
			t.Fatal(w.Code, w.Body.String())
		}
		for _, secret := range []string{"access_token", "refresh_token", "mock-session"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("secret exposed")
			}
		}
	}
	if calls.Load() != 1 {
		t.Fatal("playlist retry regenerated recommendation")
	}
}
func TestDirectRejectsUnverifiedProviderTracks(t *testing.T) {
	s, _ := directFixture(t, 3, nil)
	old := s.NewRunner
	s.NewRunner = func() directapi.Runner {
		r := old().(directRunner)
		r.result.Final[0].VideoID = "unverified-client-id"
		return r
	}
	_, err := s.Recommend(context.Background(), model.UploadedImage{ImageInfo: model.ImageInfo{ContentType: "image/png"}, Data: validPNG(t)})
	if !errors.Is(err, directapi.ErrInvalidResult) {
		t.Fatal(err)
	}
}

func TestDirectRequestIDAndDuplicateImage(t *testing.T) {
	for _, tc := range []struct {
		id     string
		images int
		status int
	}{{"android_request_1", 1, 200}, {"bad id", 1, 400}, {"okay", 2, 400}} {
		s, calls := directFixture(t, 3, nil)
		r := gin.New()
		r.POST("/api/v1/recommend/direct", handler.NewDirectHandler(s).Recommend)
		var b bytes.Buffer
		multipartWriter := multipart.NewWriter(&b)
		_ = multipartWriter.WriteField("request_id", tc.id)
		for i := 0; i < tc.images; i++ {
			p, _ := multipartWriter.CreateFormFile("image", "photo.png")
			_, _ = p.Write(validPNG(t))
		}
		_ = multipartWriter.Close()
		req := httptest.NewRequest("POST", "/api/v1/recommend/direct", &b)
		req.Header.Set("Content-Type", multipartWriter.FormDataContentType())
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatal(w.Code, w.Body.String())
		}
		if tc.status == 200 && (w.Header().Get("X-Request-ID") != tc.id || calls.Load() != 1) {
			t.Fatal("request id / runner calls")
		}
		if tc.status != 200 && calls.Load() != 0 {
			t.Fatal("invalid request called runner")
		}
	}
}
