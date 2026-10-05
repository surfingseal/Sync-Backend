package main

import (
	"context"
	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	"example.com/sync/internal/directmusic"
	imageproc "example.com/sync/internal/image"
	"example.com/sync/internal/model"
	"example.com/sync/internal/router"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type forbiddenGenerator struct{ t *testing.T }

func (f forbiddenGenerator) RecommendTracks(context.Context, []byte, string, int) (*model.DirectMusicRecommendation, error) {
	f.t.Fatal("unexpected provider call")
	return nil, nil
}

func TestProductionAssembly(t *testing.T) {
	p, err := imageproc.NewProcessor(1920, imageproc.DefaultHardMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, engine := range []config.RecommendationEngine{config.DirectEngine, config.LegacyEngine} {
		t.Run(string(engine), func(t *testing.T) {
			cfg := config.Config{AppEnv: "production", RecommendationEngine: engine, RecommendationDataMode: "live", YouTubeLiveSearchEnabled: true, YouTubeAPIKey: "test-only", YouTubeSearchMaxCalls: 10, YouTubeRegion: "KR", GoogleCloudProject: "test-project", GoogleCloudLocation: "global", VertexModel: config.DefaultVertexModel, VertexThinkingLevel: "MEDIUM", VertexTimeout: time.Minute}
			d, playlists, err := assembleDirect(cfg, p, forbiddenGenerator{t}, client.NewFixtureMusicClient(), client.NewAlbumArtworkResolver(nil, "", "US"), nil)
			if err != nil {
				t.Fatal(err)
			}
			if engine == config.DirectEngine {
				if d == nil || playlists == nil {
					t.Fatal("Direct not wired")
				}
				a := d.NewRunner().(*directmusic.Service)
				b := d.NewRunner().(*directmusic.Service)
				if a.Resolver == b.Resolver || a.Resolver.Cache == b.Resolver.Cache || a.Config.MaxSearchCalls != 10 {
					t.Fatal("request state shared or budget changed")
				}
			} else if d != nil || playlists != nil {
				t.Fatal("legacy activated Direct")
			}
			server := newHTTPServer(cfg, router.NewWithDirect(cfg, nil, nil, nil, d, playlists))
			for _, tc := range []struct {
				method, path, body string
				status             int
			}{{"GET", "/health", "", 200}, {"POST", "/api/v1/recommend", "{}", 400}} {
				req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				server.Handler.ServeHTTP(w, req)
				if w.Code != tc.status {
					t.Fatalf("%s = %d", tc.path, w.Code)
				}
			}
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/api/v1/recommend/direct", strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			server.Handler.ServeHTTP(w, req)
			want := 415
			if engine == config.LegacyEngine {
				want = 503
			}
			if w.Code != want {
				t.Fatalf("Direct status=%d want=%d", w.Code, want)
			}
		})
	}
	cfg := config.Config{RecommendationEngine: config.DirectEngine}
	if _, _, err := assembleDirect(cfg, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("invalid startup accepted")
	}
}
