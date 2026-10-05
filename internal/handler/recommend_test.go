package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	"example.com/sync/internal/handler"
	"example.com/sync/internal/model"
	"example.com/sync/internal/router"
	"example.com/sync/internal/service"
)

type fakeQueryGenerator struct{}

func (fakeQueryGenerator) GenerateMusicQueries(context.Context, model.ImageAnalysis, model.MusicPreferences, int) ([]string, error) {
	return []string{"calm acoustic music", "warm dream pop music"}, nil
}

type handlerMusic struct{ err error }

func (f handlerMusic) SearchMusic(ctx context.Context, q model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
	if errors.Is(f.err, context.DeadlineExceeded) {
		return nil, context.DeadlineExceeded
	}
	if f.err != nil {
		return nil, f.err
	}
	return []model.YouTubeSearchResult{{VideoID: "12345678901"}}, nil
}
func (f handlerMusic) GetVideos(context.Context, []string) ([]model.YouTubeVideo, error) {
	return []model.YouTubeVideo{{VideoID: "12345678901", Title: "Actual source title", ChannelTitle: "Actual source channel", CategoryID: "10", DurationSeconds: 240, Embeddable: true, Public: true}}, nil
}
func recommendBody(t *testing.T) []byte {
	t.Helper()
	analysis, err := os.ReadFile("../model/testdata/analysis.json")
	if err != nil {
		t.Fatal(err)
	}
	return []byte(`{"analysis":` + string(analysis) + `}`)
}
func TestRecommendHandler(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{{nil, 200, ""}, {client.ErrMusicSearch, 502, "MUSIC_SEARCH_ERROR"}, {client.ErrYouTubeQuota, 503, "YOUTUBE_QUOTA_EXCEEDED"}, {client.ErrSearchBudget, 503, "YOUTUBE_SEARCH_BUDGET_EXCEEDED"}, {client.ErrLiveSearchDisabled, 503, "YOUTUBE_LIVE_SEARCH_DISABLED"}, {client.ErrMusicNotConfigured, 503, "MUSIC_SEARCH_UNAVAILABLE"}, {context.DeadlineExceeded, 504, "RECOMMENDATION_TIMEOUT"}, {errors.New("private key must not leak"), 502, "MUSIC_SEARCH_ERROR"}} {
		recommendations := service.NewRecommendationService(fakeQueryGenerator{}, handlerMusic{tc.err}, "KR", "ko", 2, time.Second)
		r := router.New(config.Config{AppEnv: "production", RecommendationCount: 10}, nil, recommendations)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/recommend", bytes.NewReader(recommendBody(t)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if tc.status == 200 {
			var result model.RecommendationResponse
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.RequestedCount != 10 || result.ReturnedCount != 1 || result.Tracks[0].VideoID != "12345678901" {
				t.Fatal(result)
			}
		} else {
			assertError(t, w, tc.code)
		}
		if strings.Contains(w.Body.String(), "private key") {
			t.Fatal("provider error leaked")
		}
	}
}
func TestRecommendInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		body, contentType string
		status            int
		code              string
	}{{`{}`, "application/json", 400, "INVALID_REQUEST"}, {`{"analysis":null}`, "application/json", 400, "INVALID_REQUEST"}, {`{}`, "text/plain", 415, "UNSUPPORTED_CONTENT_TYPE"}, {strings.Repeat("x", int(handler.MaxRecommendRequestSize)+1), "application/json", 413, "REQUEST_TOO_LARGE"}} {
		req := httptest.NewRequest("POST", "/api/v1/recommend", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", tc.contentType)
		w := httptest.NewRecorder()
		router.New(config.Config{AppEnv: "production", RecommendationCount: 10}, nil, nil).ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatal(w.Code, w.Body.String())
		}
		assertError(t, w, tc.code)
	}
	// The unknown-length body is bounded too.
	req := httptest.NewRequest("POST", "/api/v1/recommend", io.LimitReader(zeroReader{}, handler.MaxRecommendRequestSize+1))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.New(config.Config{AppEnv: "production", RecommendationCount: 10}, nil, nil).ServeHTTP(w, req)
	if w.Code != 413 {
		t.Fatal(w.Code)
	}
}
