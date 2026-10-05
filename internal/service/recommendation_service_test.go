package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
	"example.com/sync/internal/recommendation"
)

type queryFunc func(context.Context, model.ImageAnalysis, model.MusicPreferences, int) ([]string, error)

func (f queryFunc) GenerateMusicQueries(ctx context.Context, a model.ImageAnalysis, p model.MusicPreferences, n int) ([]string, error) {
	return f(ctx, a, p, n)
}

type musicFake struct {
	search func(context.Context, model.MusicSearchQuery) ([]model.YouTubeSearchResult, error)
	videos func(context.Context, []string) ([]model.YouTubeVideo, error)
}

func (f musicFake) SearchMusic(ctx context.Context, q model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
	return f.search(ctx, q)
}
func (f musicFake) GetVideos(ctx context.Context, ids []string) ([]model.YouTubeVideo, error) {
	return f.videos(ctx, ids)
}
func recommendationInput(t *testing.T) model.RecommendationRequest {
	return model.RecommendationRequest{Analysis: *validAnalysis(t), Preferences: model.MusicPreferences{Languages: []string{"ko", "en"}, Count: 10}}
}
func track(i int) model.YouTubeVideo {
	return model.YouTubeVideo{VideoID: fmt.Sprintf("%011d", i), Title: "Actual YouTube title", ChannelTitle: "Actual YouTube channel", ThumbnailURL: "https://example.com/image.jpg", CategoryID: "10", DurationSeconds: 240, Embeddable: true, Public: true}
}
func twoQueries() queryFunc {
	return queryFunc(func(context.Context, model.ImageAnalysis, model.MusicPreferences, int) ([]string, error) {
		return []string{"calm acoustic music", "dream pop warm music"}, nil
	})
}
func TestRecommendationDedupAndSourceTruth(t *testing.T) {
	req := recommendationInput(t)
	calls := 0
	fake := musicFake{
		search: func(ctx context.Context, q model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
			calls++
			if (calls == 1 && q.Order != "viewCount") || (calls == 2 && q.Order != "relevance") {
				t.Fatal("mixed retrieval order missing", q.Order)
			}
			if q.MaxResults != 50 || q.Region != "KR" || q.RelevanceLanguage != "ko" {
				t.Fatal(q)
			}
			if calls == 1 {
				return []model.YouTubeSearchResult{{VideoID: track(1).VideoID}, {VideoID: track(2).VideoID}}, nil
			}
			return []model.YouTubeSearchResult{{VideoID: track(2).VideoID}, {VideoID: track(3).VideoID}, {VideoID: track(4).VideoID}}, nil
		},
		videos: func(ctx context.Context, ids []string) ([]model.YouTubeVideo, error) {
			if len(ids) != 2 {
				t.Fatal("IDs not deduplicated")
			}
			return []model.YouTubeVideo{track(1), track(2), track(3), track(3), track(9)}, nil
		},
	}
	result, err := NewRecommendationService(twoQueries(), fake, "KR", "ko", 2, time.Second).Recommend(context.Background(), req)
	if err != nil || calls != 2 || result.ReturnedCount != 3 || !result.Partial {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
	}
	seen := map[string]bool{}
	for _, v := range result.Tracks {
		if seen[v.VideoID] || v.VideoID == track(4).VideoID || v.VideoID == track(9).VideoID {
			t.Fatal("unverified/duplicate ID returned")
		}
		seen[v.VideoID] = true
		if v.Title != track(1).Title || v.ChannelTitle != track(1).ChannelTitle || v.MatchScore < 0 || v.MatchScore > 1 {
			t.Fatal(v)
		}
	}
}
func TestRecommendationFilters(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*model.YouTubeVideo)
		keep bool
	}{
		{"category", func(v *model.YouTubeVideo) { v.CategoryID = "22" }, false},
		{"short", func(v *model.YouTubeVideo) { v.DurationSeconds = 89 }, false},
		{"long", func(v *model.YouTubeVideo) { v.DurationSeconds = 721 }, false},
		{"minimum", func(v *model.YouTubeVideo) { v.DurationSeconds = 90 }, true},
		{"maximum", func(v *model.YouTubeVideo) { v.DurationSeconds = 720 }, true},
		{"blocked", func(v *model.YouTubeVideo) { v.BlockedRegions = []string{"KR"} }, false},
		{"allowed US only", func(v *model.YouTubeVideo) { v.AllowedRegions = []string{"US"} }, false},
		{"allowed empty", func(v *model.YouTubeVideo) { v.AllowedRegions = []string{} }, false},
		{"allowed KR", func(v *model.YouTubeVideo) { v.AllowedRegions = []string{"KR"} }, true},
		{"not embeddable", func(v *model.YouTubeVideo) { v.Embeddable = false }, false},
		{"private", func(v *model.YouTubeVideo) { v.Public = false }, false},
		{"live", func(v *model.YouTubeVideo) { v.Live = true }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := track(1)
			tc.edit(&v)
			fake := musicFake{search: func(context.Context, model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
				return []model.YouTubeSearchResult{{VideoID: v.VideoID}}, nil
			}, videos: func(context.Context, []string) ([]model.YouTubeVideo, error) { return []model.YouTubeVideo{v}, nil }}
			result, err := NewRecommendationService(twoQueries(), fake, "KR", "ko", 2, time.Second).Recommend(context.Background(), recommendationInput(t))
			if err != nil {
				t.Fatal(err)
			}
			if (result.ReturnedCount == 1) != tc.keep {
				t.Fatal(result)
			}
		})
	}
}
func TestRequestedCountAndRanking(t *testing.T) {
	req := recommendationInput(t)
	req.Preferences.Count = 5
	req.Preferences.PreferredGenres = []string{"acoustic"}
	videos := make([]model.YouTubeVideo, 10)
	hits := make([]model.YouTubeSearchResult, 10)
	for i := range videos {
		videos[i] = track(i + 1)
		hits[i] = model.YouTubeSearchResult{VideoID: videos[i].VideoID}
	}
	videos[9].Title = "Official acoustic calm music"
	videos[9].DefaultAudioLanguage = "ko"
	fake := musicFake{search: func(context.Context, model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) { return hits, nil }, videos: func(context.Context, []string) ([]model.YouTubeVideo, error) { return videos, nil }}
	result, err := NewRecommendationService(twoQueries(), fake, "KR", "ko", 2, time.Second).Recommend(context.Background(), req)
	if err != nil || result.ReturnedCount != 5 || result.Partial || result.Tracks[0].VideoID != videos[9].VideoID {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for i := 1; i < len(result.Tracks); i++ {
		if result.Tracks[i].MatchScore > result.Tracks[i-1].MatchScore {
			t.Fatal("not sorted")
		}
	}
}
func TestVertexFailureFallbackPreferences(t *testing.T) {
	req := recommendationInput(t)
	req.Preferences.InstrumentalOnly = true
	req.Preferences.Languages = []string{"ko", "en"}
	req.Preferences.PreferredGenres = []string{"jazz"}
	generator := queryFunc(func(context.Context, model.ImageAnalysis, model.MusicPreferences, int) ([]string, error) {
		return nil, errors.New("provider secret must not be logged")
	})
	calls := []string{}
	fake := musicFake{search: func(ctx context.Context, q model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
		calls = append(calls, q.Text)
		return nil, nil
	}, videos: func(context.Context, []string) ([]model.YouTubeVideo, error) {
		t.Fatal("batch called with no candidates")
		return nil, nil
	}}
	result, err := NewRecommendationService(generator, fake, "KR", "ko", 2, time.Second).Recommend(context.Background(), req)
	if err != nil || result.Tracks == nil || result.ReturnedCount != 0 || len(calls) != 2 {
		t.Fatal(result, err)
	}
	if !strings.Contains(calls[0], "korean") || !strings.Contains(calls[1], "english") {
		t.Fatal(calls)
	}
	for _, q := range calls {
		if !strings.Contains(q, "jazz") || !strings.Contains(q, "instrumental") {
			t.Fatal(q)
		}
	}
}
func TestExcludedArtistsExactPhrase(t *testing.T) {
	req := recommendationInput(t)
	req.Preferences.ExcludedArtists = []string{"IU"}
	for _, tc := range []struct {
		title, channel string
		keep           bool
	}{{"IU - real title", "Music", false}, {"Title", "IU Official", false}, {"Quiet song", "MIYU", true}, {"Title", "Guitar studio", true}} {
		v := track(1)
		v.Title = tc.title
		v.ChannelTitle = tc.channel
		if eligibleVideo(v, req.Preferences, "KR") != tc.keep {
			t.Fatal(tc)
		}
	}
}
func TestNoDuplicateQueryCalls(t *testing.T) {
	count := 0
	generator := queryFunc(func(context.Context, model.ImageAnalysis, model.MusicPreferences, int) ([]string, error) {
		return []string{"calm music", "CALM MUSIC", "calm music"}, nil
	})
	fake := musicFake{search: func(context.Context, model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
		count++
		return nil, nil
	}}
	_, err := NewRecommendationService(generator, fake, "KR", "ko", 3, time.Second).Recommend(context.Background(), recommendationInput(t))
	if err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
func TestRecommendationProviderErrorsAndTimeout(t *testing.T) {
	for _, want := range []error{client.ErrMusicSearch, client.ErrYouTubeQuota, client.ErrMusicNotConfigured} {
		fake := musicFake{search: func(context.Context, model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) { return nil, want }}
		_, err := NewRecommendationService(twoQueries(), fake, "KR", "ko", 2, time.Second).Recommend(context.Background(), recommendationInput(t))
		if !errors.Is(err, want) {
			t.Fatal(err)
		}
	}
	fake := musicFake{search: func(ctx context.Context, _ model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	_, err := NewRecommendationService(twoQueries(), fake, "KR", "ko", 2, time.Millisecond).Recommend(context.Background(), recommendationInput(t))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewRecommendationService(twoQueries(), fake, "KR", "ko", 2, time.Second).Recommend(ctx, recommendationInput(t))
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestDetailFailure(t *testing.T) {
	fake := musicFake{search: func(context.Context, model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
		return []model.YouTubeSearchResult{{VideoID: track(1).VideoID}}, nil
	}, videos: func(context.Context, []string) ([]model.YouTubeVideo, error) { return nil, client.ErrMusicSearch }}
	_, err := NewRecommendationService(twoQueries(), fake, "KR", "ko", 2, time.Second).Recommend(context.Background(), recommendationInput(t))
	if !errors.Is(err, client.ErrMusicSearch) {
		t.Fatal(err)
	}
}

func TestMusicSearchExclusionsSurviveLongQueries(t *testing.T) {
	query := trackSearchQuery(strings.Repeat("acoustic calm ", 30))
	if len([]rune(query)) > 160 || !strings.HasSuffix(query, " -mix -playlist -compilation") {
		t.Fatal("individual-track exclusions were lost")
	}
	req := recommendationInput(t)
	for _, query := range fallbackQueries(req, 2) {
		if !strings.HasSuffix(query, " -mix -playlist -compilation") {
			t.Fatal("fallback still targets compilations")
		}
	}
}

func TestFallbackDoesNotRequireAllProfileKeywords(t *testing.T) {
	req := recommendationInput(t)
	req.Analysis.MusicProfile.Genres = []string{"indie folk", "acoustic", "bossa nova", "chamber pop", "coffeehouse pop"}
	req.Analysis.Mood.Tags = []string{"peaceful", "romantic", "cozy", "bright", "delicate"}
	queries := fallbackQueries(req, 2)
	if len(queries) != 2 || !strings.Contains(queries[0], "indie folk peaceful korean song") || !strings.Contains(queries[1], "acoustic romantic english language song") {
		t.Fatal(queries)
	}
	if strings.Contains(queries[0], "coffeehouse") || strings.Contains(queries[0], "delicate") {
		t.Fatal("overconstrained fallback")
	}
}
func TestThirdQueryUsesBoundedVideoBatches(t *testing.T) {
	req := recommendationInput(t)
	calls, batches := 0, 0
	gen := queryFunc(func(context.Context, model.ImageAnalysis, model.MusicPreferences, int) ([]string, error) {
		return []string{"calm acoustic", "warm folk", "dream pop"}, nil
	})
	fake := musicFake{search: func(context.Context, model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
		hits := []model.YouTubeSearchResult{}
		for i := 0; i < 20; i++ {
			hits = append(hits, model.YouTubeSearchResult{VideoID: track(calls*20 + i + 1).VideoID})
		}
		calls++
		return hits, nil
	}, videos: func(_ context.Context, ids []string) ([]model.YouTubeVideo, error) {
		batches++
		if len(ids) > 50 {
			t.Fatal("oversized batch")
		}
		videos := []model.YouTubeVideo{}
		for _, id := range ids {
			v := track(1)
			v.VideoID = id
			videos = append(videos, v)
		}
		return videos, nil
	}}
	result, err := NewRecommendationService(gen, fake, "KR", "ko", 3, time.Second, WithAdaptiveSafetyMargin(100)).Recommend(context.Background(), req)
	if err != nil || calls != 3 || batches != 3 || result.ReturnedCount != 10 {
		t.Fatal(result, err, calls, batches)
	}
}

func TestPreparedQueriesAndV2PartialTrace(t *testing.T) {
	req := recommendationInput(t)
	generator := client.DeterministicQueryBuilder{}
	expected, _ := generator.GenerateMusicQueries(context.Background(), req.Analysis, req.Preferences, 2)
	calls := 0
	fake := musicFake{
		search: func(ctx context.Context, q model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
			if q.Text != expected[calls] {
				t.Fatal("prepared query overwritten", q.Text, expected)
			}
			calls++
			return []model.YouTubeSearchResult{{VideoID: track(1).VideoID}, {VideoID: track(2).VideoID}}, nil
		},
		videos: func(context.Context, []string) ([]model.YouTubeVideo, error) {
			weak := track(1)
			weak.ViewCount = 100
			one := uint64(1)
			weak.LikeCount = &one
			good := track(2)
			good.ViewCount = 100000
			hundred := uint64(100)
			good.LikeCount = &hundred
			good.LicensedContent = true
			return []model.YouTubeVideo{weak, good}, nil
		},
	}
	trace := RecommendationTrace{}
	result, err := NewRecommendationService(generator, fake, "KR", "ko", 2, time.Second, WithRanker(recommendation.RankV2)).Recommend(WithRecommendationTrace(context.Background(), &trace), req)
	if err != nil || calls != 2 || result.ReturnedCount != 1 || !result.Partial || len(trace.Candidates) != 2 || len(trace.Ranked) != 1 || trace.TotalMS <= 0 {
		t.Fatal(result, trace, err)
	}
}
