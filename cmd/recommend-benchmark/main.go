// Explicit paid query/search integration; never run by go test.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	"example.com/sync/internal/model"
	"example.com/sync/internal/recommendation"
	"example.com/sync/internal/service"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type input struct {
	ID       string              `json:"id"`
	Analysis model.ImageAnalysis `json:"analysis"`
}
type result struct {
	ImageID, Mode          string
	Analysis               model.ImageAnalysis
	Success                bool
	ErrorCategory          string `json:",omitempty"`
	Trace                  service.RecommendationTrace
	OldRanking, NewRanking []recommendation.RankedCandidate
	NewRankingMS           float64
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	dataset := flag.String("dataset", "", "saved analysis manifest")
	out := flag.String("out", "", "new output directory")
	limit := flag.Int("images", 12, "bounded image count")
	rerankSource := flag.String("rerank-source", "", "offline re-score saved candidate pools; no API calls")
	retryFailed := flag.Bool("retry-failed", false, "explicit one-time read-only retry; preserve failed outcome")
	flag.Parse()
	if *rerankSource != "" {
		if *out == "" {
			return fmt.Errorf("out required")
		}
		if os.MkdirAll(*out, 0700) != nil {
			return fmt.Errorf("output unavailable")
		}
		paths, _ := filepath.Glob(filepath.Join(*rerankSource, "*.json"))
		for _, path := range paths {
			bytes, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("saved pool unavailable")
			}
			var r result
			if json.Unmarshal(bytes, &r) != nil || !r.Success {
				continue
			}
			start := time.Now()
			r.NewRanking = recommendation.RankV2(r.Trace.Candidates, r.Analysis, model.MusicPreferences{Languages: []string{"ko", "en"}, Count: 10})
			r.NewRankingMS = float64(time.Since(start)) / float64(time.Millisecond)
			bytes, _ = json.MarshalIndent(r, "", "  ")
			if os.WriteFile(filepath.Join(*out, filepath.Base(path)), bytes, 0600) != nil {
				return fmt.Errorf("output unavailable")
			}
		}
		return nil
	}
	if *dataset == "" || *out == "" || *limit < 1 || *limit > 30 {
		return fmt.Errorf("dataset/out and images 1..30 required")
	}
	raw, err := os.ReadFile(*dataset)
	if err != nil {
		return fmt.Errorf("dataset unavailable")
	}
	var images []input
	if json.Unmarshal(raw, &images) != nil {
		return fmt.Errorf("invalid dataset")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.YouTubeLiveSearchEnabled {
		return fmt.Errorf("explicit YOUTUBE_LIVE_SEARCH_ENABLED=true required for live benchmark")
	}
	fmt.Printf("Planned search.list calls: <= %d (process budget)\n", cfg.YouTubeSearchMaxCalls)
	if cfg.YouTubeAPIKey == "" {
		return fmt.Errorf("YOUTUBE_API_KEY required")
	}
	ctx := context.Background()
	vertex, err := client.NewVertexImageAnalyzer(ctx, cfg)
	if err != nil {
		return fmt.Errorf("Vertex client unavailable")
	}
	source, err := client.NewYouTubeClient(ctx, cfg.YouTubeAPIKey)
	if err != nil {
		return fmt.Errorf("YouTube client unavailable")
	}
	yt := &client.CachedMusicClient{Source: source, Cache: client.NewInMemorySearchCache(), TTL: cfg.YouTubeSearchCacheTTL, Budget: client.NewSearchBudget(cfg.YouTubeSearchMaxCalls), LiveEnabled: true}
	if err = os.MkdirAll(*out, 0700); err != nil {
		return fmt.Errorf("output unavailable")
	}
	modes := []string{"gemini", "deterministic"}
	for index, image := range images[:min(len(images), *limit)] {
		if image.ID == "" || filepath.Base(image.ID) != image.ID || image.Analysis.Validate() != nil {
			return fmt.Errorf("invalid saved analysis entry")
		}
		if index%2 == 1 {
			modes = []string{"deterministic", "gemini"}
		} else {
			modes = []string{"gemini", "deterministic"}
		}
		for _, mode := range modes {
			path := filepath.Join(*out, image.ID+"-"+mode+".json")
			if _, err := os.Stat(path); err == nil {
				priorRaw, _ := os.ReadFile(path)
				var prior result
				_ = json.Unmarshal(priorRaw, &prior)
				failurePath := filepath.Join(*out, "failures", image.ID+"-"+mode+".json")
				_, alreadyRetried := os.Stat(failurePath)
				if !*retryFailed || prior.Success || alreadyRetried == nil {
					fmt.Println("resume", image.ID, mode)
					continue
				}
				if os.MkdirAll(filepath.Dir(failurePath), 0700) != nil || os.WriteFile(failurePath, priorRaw, 0600) != nil {
					return fmt.Errorf("failed outcome preservation failed")
				}
			}
			var generator client.MusicQueryGenerator = vertex
			if mode == "deterministic" {
				generator = client.DeterministicQueryBuilder{}
			}
			s := service.NewRecommendationService(generator, yt, cfg.YouTubeRegion, cfg.YouTubeRelevanceLanguage, 2, cfg.RecommendationTimeout)
			trace := service.RecommendationTrace{}
			callCtx := service.WithRecommendationTrace(ctx, &trace)
			req := model.RecommendationRequest{Analysis: image.Analysis, Preferences: model.MusicPreferences{Languages: []string{"ko", "en"}, Count: 10}}
			_, err := s.Recommend(callCtx, req)
			r := result{ImageID: image.ID, Mode: mode, Analysis: image.Analysis, Success: err == nil, Trace: trace}
			if err != nil {
				r.ErrorCategory = "external_request_failed"
				if errors.Is(err, context.DeadlineExceeded) {
					r.ErrorCategory = "timeout"
				}
				if errors.Is(err, client.ErrYouTubeQuota) {
					r.ErrorCategory = "quota"
				}
			} else {
				r.OldRanking = trace.Ranked
				start := time.Now()
				r.NewRanking = recommendation.RankV2(trace.Candidates, req.Analysis, req.Preferences)
				r.NewRankingMS = float64(time.Since(start)) / float64(time.Millisecond)
			}
			bytes, _ := json.MarshalIndent(r, "", "  ")
			if os.WriteFile(path, bytes, 0600) != nil {
				return fmt.Errorf("output write failed")
			}
			fmt.Printf("%s %s success=%t old=%d new=%d query_ms=%.2f total_ms=%.2f\n", image.ID, mode, r.Success, len(r.OldRanking), len(r.NewRanking), trace.QueryMS, trace.TotalMS)
			if errors.Is(err, client.ErrYouTubeQuota) {
				return fmt.Errorf("benchmark stopped on quota error; saved outcome, no quota refill/retry")
			}
		}
	}
	return nil
}
