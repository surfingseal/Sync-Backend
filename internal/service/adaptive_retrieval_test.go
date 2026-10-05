package service

import (
	"context"
	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
	"example.com/sync/internal/recommendation"
	"testing"
	"time"
)

func TestAdaptiveQualifiedPool(t *testing.T) {
	for _, tc := range []struct {
		name        string
		size, calls int
	}{{"enough", 15, 1}, {"insufficient", 4, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			req := recommendationInput(t)
			calls := 0
			f := musicFake{search: func(context.Context, model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
				calls++
				out := []model.YouTubeSearchResult{}
				for i := 0; i < tc.size; i++ {
					out = append(out, model.YouTubeSearchResult{VideoID: track(calls*100 + i).VideoID})
				}
				return out, nil
			}, videos: func(_ context.Context, ids []string) ([]model.YouTubeVideo, error) {
				out := []model.YouTubeVideo{}
				for i, id := range ids {
					v := track(i)
					v.VideoID = id
					v.ChannelID = id
					v.ViewCount = 2000000
					v.LicensedContent = true
					v.Title = "calm acoustic official lyrics"
					out = append(out, v)
				}
				return out, nil
			}}
			trace := &RecommendationTrace{}
			s := NewRecommendationService(client.DeterministicQueryBuilder{}, f, "KR", "ko", 2, time.Second, WithRanker(recommendation.RankV2))
			_, err := s.Recommend(WithRecommendationTrace(context.Background(), trace), req)
			if err != nil || calls != tc.calls || trace.SecondQuerySkipped != (tc.calls == 1) {
				t.Fatal(err, calls, trace)
			}
		})
	}
}
func TestFixtureFullPipeline(t *testing.T) {
	req := recommendationInput(t)
	m := client.NewFixtureMusicClient()
	trace := &RecommendationTrace{}
	s := NewRecommendationService(client.DeterministicQueryBuilder{}, m, "KR", "ko", 2, time.Second, WithRanker(recommendation.RankV2))
	result, err := s.Recommend(WithRecommendationTrace(context.Background(), trace), req)
	if err != nil || result.DataMode != "fixture" || len(result.Tracks) == 0 || trace.Retrieval.SearchCalls != 0 || trace.Retrieval.FixtureRequests == 0 {
		t.Fatal(result, err, trace)
	}
	for _, track := range result.Tracks {
		if track.YouTubeURL != "" {
			t.Fatal("synthetic link")
		}
	}
}

func TestRetrievalV2BatchDedupAndBudget(t *testing.T) {
	req := recommendationInput(t)
	searches, batches := 0, 0
	seen := map[string]bool{}
	source := musicFake{search: func(_ context.Context, q model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
		searches++
		if q.MaxResults != 50 {
			t.Fatal(q)
		}
		out := []model.YouTubeSearchResult{}
		for i := 0; i < 50; i++ {
			out = append(out, model.YouTubeSearchResult{VideoID: track(i).VideoID})
		}
		return out, nil
	}, videos: func(_ context.Context, ids []string) ([]model.YouTubeVideo, error) {
		batches++
		if len(ids) > 50 {
			t.Fatal("batch too large")
		}
		out := []model.YouTubeVideo{}
		for i, id := range ids {
			if seen[id] {
				t.Fatal("metadata repeated", id)
			}
			seen[id] = true
			v := track(i)
			v.VideoID = id
			v.DurationSeconds = 800
			out = append(out, v)
		}
		return out, nil
	}}
	cached := &client.CachedMusicClient{Source: source, Cache: client.NewInMemorySearchCache(), TTL: time.Minute, Budget: client.NewSearchBudget(2), LiveEnabled: true}
	svc := NewRecommendationService(client.DeterministicQueryBuilder{}, cached, "KR", "ko", 2, time.Second, WithRanker(recommendation.RankV2))
	result, err := svc.Recommend(context.Background(), req)
	if err != nil || result.ReturnedCount != 0 || searches != 2 || batches != 1 {
		t.Fatal(result, err, searches, batches)
	}
	if _, err := cached.SearchMusic(context.Background(), model.MusicSearchQuery{Text: "third distinct query", MaxResults: 50}); err != client.ErrSearchBudget || searches != 2 {
		t.Fatal("third search permitted", err, searches)
	}
}
