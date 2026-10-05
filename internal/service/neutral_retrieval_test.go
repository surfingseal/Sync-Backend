package service

import (
	"context"
	"errors"
	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
	"example.com/sync/internal/recommendation"
	"fmt"
	"testing"
	"time"
)

func noTextCalls(t *testing.T) queryFunc {
	return func(context.Context, model.ImageAnalysis, model.MusicPreferences, int) ([]string, error) {
		t.Fatal("text generator called by neutral planner")
		return nil, nil
	}
}
func TestNeutralServiceSkipOrQ2(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  int
		calls int
	}{{"sufficient", 15, 1}, {"insufficient", 3, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			req := recommendationInput(t)
			queries, batches := 0, 0
			seen := map[string]bool{}
			fake := musicFake{search: func(_ context.Context, q model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
				queries++
				if (queries == 1 && q.Order != "relevance") || (queries == 2 && q.Order != "viewCount") || q.MaxResults != 50 || q.RelevanceLanguage != "" {
					t.Fatal(q)
				}
				out := []model.YouTubeSearchResult{}
				for i := 0; i < tc.size; i++ {
					out = append(out, model.YouTubeSearchResult{VideoID: track(i).VideoID})
				}
				return out, nil
			}, videos: func(_ context.Context, ids []string) ([]model.YouTubeVideo, error) {
				batches++
				out := []model.YouTubeVideo{}
				for i, id := range ids {
					if seen[id] {
						t.Fatal("metadata refetched")
					}
					seen[id] = true
					v := track(i)
					v.VideoID = id
					v.ChannelID = fmt.Sprint(i)
					v.Title = "calm warm indie pop official audio"
					v.ViewCount = 2000000
					v.LicensedContent = true
					v.DefaultAudioLanguage = "en"
					if i%2 == 0 {
						v.DefaultAudioLanguage = "ko"
					}
					out = append(out, v)
				}
				return out, nil
			}}
			trace := &RecommendationTrace{}
			svc := NewRecommendationService(noTextCalls(t), fake, "KR", "ko", 3, time.Second, WithRanker(recommendation.RankV2), WithNeutralRetrieval(recommendation.DefaultNeutralPolicy(), "song"))
			_, err := svc.Recommend(WithRecommendationTrace(context.Background(), trace), req)
			if err != nil || queries != tc.calls || batches != 1 || trace.SecondQuerySkipped != (tc.calls == 1) {
				t.Fatal(err, queries, batches, trace)
			}
			for _, signal := range trace.CandidateSignals {
				if len(signal.Sources) != tc.calls {
					t.Fatal("all query sources must be retained without refetch", signal)
				}
				if tc.calls == 2 && signal.Sources[1].Role != "popularity_rescue" {
					t.Fatal(signal.Sources)
				}
			}
			if queries > 2 {
				t.Fatal("search cap")
			}
		})
	}
}
func TestNeutralReleaseGateCannotReenter(t *testing.T) {
	req := recommendationInput(t)
	fake := musicFake{search: func(context.Context, model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
		return []model.YouTubeSearchResult{{VideoID: "licensed"}, {VideoID: "title-only"}, {VideoID: "unknown"}}, nil
	}, videos: func(_ context.Context, ids []string) ([]model.YouTubeVideo, error) {
		out := []model.YouTubeVideo{}
		for i, id := range ids {
			v := track(i)
			v.VideoID = id
			v.ViewCount = 100000000
			v.Title = "calm warm indie pop Official Audio"
			v.LicensedContent = id == "licensed"
			if id == "unknown" {
				v.Title = "calm warm indie pop"
			}
			out = append(out, v)
		}
		return out, nil
	}}
	svc := NewRecommendationService(noTextCalls(t), fake, "KR", "ko", 2, time.Second, WithRanker(recommendation.RankV2), WithNeutralRetrieval(recommendation.DefaultNeutralPolicy(), "song"))
	trace := &RecommendationTrace{}
	out, err := svc.Recommend(WithRecommendationTrace(context.Background(), trace), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Candidates) != 1 {
		t.Fatal(trace.Candidates)
	}
	for _, track := range out.Tracks {
		if track.VideoID != "licensed" {
			t.Fatal("low release reentered", track)
		}
	}
}
func TestNeutralContextAndErrors(t *testing.T) {
	req := recommendationInput(t)
	fake := musicFake{search: func(ctx context.Context, _ model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	svc := NewRecommendationService(noTextCalls(t), fake, "KR", "ko", 2, time.Millisecond, WithNeutralRetrieval(recommendation.DefaultNeutralPolicy(), "song"))
	_, err := svc.Recommend(context.Background(), req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	source := musicFake{search: func(context.Context, model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
		return nil, client.ErrYouTubeQuota
	}}
	svc = NewRecommendationService(noTextCalls(t), source, "KR", "ko", 2, time.Second, WithNeutralRetrieval(recommendation.DefaultNeutralPolicy(), "song"))
	_, err = svc.Recommend(context.Background(), req)
	if !errors.Is(err, client.ErrYouTubeQuota) {
		t.Fatal(err)
	}
}

func TestReleaseDoesNotOverrideSongForm(t *testing.T) {
	req := recommendationInput(t)
	for _, title := range []string{"CITY POP Jam C Major 105bpm All Instruments version BackingTrack", "【カラオケ】真夜中のドア～Stay With Me/松原みき"} {
		v := track(1)
		v.Title = title
		v.LicensedContent = true
		if recommendation.ReleaseConfidence(v).Confidence != "HIGH" || eligibleVideo(v, req.Preferences, "KR") {
			t.Fatal("release bypassed hard filter", v)
		}
	}
}
func TestCoverageCountsOnlyPlausibleAudio(t *testing.T) {
	videos := map[string]model.YouTubeVideo{}
	signals := []CandidateSignalTrace{}
	for i, v := range []model.YouTubeVideo{{DefaultLanguage: "en"}, {DefaultAudioLanguage: "ko"}, {Title: "Instrumental", DefaultAudioLanguage: "en"}, {Title: "plain Latin title"}} {
		id := fmt.Sprint(i)
		v.VideoID = id
		v.ViewCount = 2000000
		videos[id] = v
		signals = append(signals, CandidateSignalTrace{VideoID: id, HardEligible: true, ReleaseAllowed: true, QualityAllowed: true, VocalAllowed: true, Release: recommendation.ReleaseEvidence{Confidence: "HIGH"}, Language: recommendation.LanguageAffinity(v, "city-pop", "en")})
	}
	s := evaluateStrength(4, signals, videos, model.MusicPreferences{Languages: []string{"ko", "en"}})
	if s.LanguageCounts["ko"] != 1 || s.LanguageCounts["en"] != 0 || s.LanguageCounts["unknown"] != 3 || s.TextOnlyCount != 1 || s.InstrumentalCoverageExcluded != 1 {
		t.Fatal(s)
	}
}
