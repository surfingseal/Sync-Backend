package directmusic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
)

type fakeGenerator struct {
	tracks []model.DirectTrack
	err    error
	calls  int
}

func (f *fakeGenerator) RecommendTracks(ctx context.Context, _ []byte, _ string, _ int) (*model.DirectMusicRecommendation, error) {
	f.calls++
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	return &model.DirectMusicRecommendation{AnalysisSummary: "image atmosphere", Tracks: f.tracks}, f.err
}

type fakeSearch struct {
	queries     []model.MusicSearchQuery
	ids         [][]string
	searchErr   error
	videos      map[string]model.YouTubeVideo
	omit        bool
	metadataErr error
}

func (f *fakeSearch) SearchMusic(ctx context.Context, q model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
	f.queries = append(f.queries, q)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	if f.omit {
		return []model.YouTubeSearchResult{}, nil
	}
	id := q.Text
	v := validVideo()
	parts := strings.Split(q.Text, `"`)
	if len(parts) >= 4 {
		v.Title = parts[1] + " - " + parts[3] + " (Official Audio)"
		v.ChannelTitle = parts[1] + " - Topic"
	}
	v.VideoID = id
	if f.videos == nil {
		f.videos = map[string]model.YouTubeVideo{}
	}
	f.videos[id] = v
	return []model.YouTubeSearchResult{{VideoID: id}, {VideoID: id}}, nil
}
func (f *fakeSearch) GetVideos(ctx context.Context, ids []string) ([]model.YouTubeVideo, error) {
	f.ids = append(f.ids, append([]string{}, ids...))
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if f.metadataErr != nil {
		return nil, f.metadataErr
	}
	out := []model.YouTubeVideo{}
	for _, id := range ids {
		if v, ok := f.videos[id]; ok {
			out = append(out, v)
		}
	}
	return out, nil
}
func validVideo() model.YouTubeVideo {
	return model.YouTubeVideo{VideoID: "id", Title: "Beach House - Space Song (Official Audio)", ChannelTitle: "Beach House - Topic", CategoryID: "10", DurationSeconds: 300, Embeddable: true, Public: true, LicensedContent: true}
}
func candidate() model.DirectTrack {
	return model.DirectTrack{Artist: "Beach House", Title: "Space Song", FitScore: .9, Reason: "night atmosphere"}
}
func TestNormalization(t *testing.T) {
	a := candidate()
	b := a
	b.Artist = " Ｂｅａｃｈ\tHouse "
	b.Title = "Space  Song"
	version := a
	version.Title += " (Remaster)"
	blank := a
	blank.Artist = " "
	out, dupes, blanks := NormalizeCandidates([]model.DirectTrack{a, b, version, blank})
	if len(out) != 2 || dupes != 1 || blanks != 1 || out[1].GeminiRank != 3 {
		t.Fatal(out, dupes, blanks)
	}
	if Normalize(Normalize(b.Artist)) != Normalize(b.Artist) {
		t.Fatal("unstable")
	}
}
func TestIdentity(t *testing.T) {
	cfg := DefaultConfig()
	v := validVideo()
	if _, why := CheckIdentity(candidate(), v, cfg); why != "" {
		t.Fatal(why)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*model.YouTubeVideo)
	}{
		{"cover", func(v *model.YouTubeVideo) { v.Title += " cover" }}, {"karaoke", func(v *model.YouTubeVideo) { v.Title += " karaoke" }}, {"live", func(v *model.YouTubeVideo) { v.Title += " live" }}, {"remix", func(v *model.YouTubeVideo) { v.Title += " remix" }}, {"region", func(v *model.YouTubeVideo) { v.BlockedRegions = []string{"KR"} }}, {"allowed", func(v *model.YouTubeVideo) { v.AllowedRegions = []string{"US"} }}, {"short", func(v *model.YouTubeVideo) { v.DurationSeconds = 20 }}, {"long", func(v *model.YouTubeVideo) { v.DurationSeconds = 800 }}, {"private", func(v *model.YouTubeVideo) { v.Public = false }}, {"embed", func(v *model.YouTubeVideo) { v.Embeddable = false }}, {"category", func(v *model.YouTubeVideo) { v.CategoryID = "22" }}, {"substring", func(v *model.YouTubeVideo) { v.Title = "Beach House - Space Songbird" }}, {"mention", func(v *model.YouTubeVideo) { v.Title = "Songs inspired by Beach House Space Song" }}, {"wrong artist", func(v *model.YouTubeVideo) { v.Title = "Someone - Space Song"; v.ChannelTitle = "Someone" }}, {"AI", func(v *model.YouTubeVideo) { v.Description = "AI generated music" }}} {
		t.Run(tc.name, func(t *testing.T) {
			bad := v
			tc.mutate(&bad)
			if _, why := CheckIdentity(candidate(), bad, cfg); why == "" {
				t.Fatal("invalid video accepted")
			}
		})
	}
}
func newService(t *testing.T, tracks []model.DirectTrack, f *fakeSearch) *Service {
	t.Helper()
	cache, e := NewCache("")
	if e != nil {
		t.Fatal(e)
	}
	cfg := DefaultConfig()
	return &Service{Generator: &fakeGenerator{tracks: tracks}, Resolver: &Resolver{Client: f, Cache: cache, Config: cfg}, Config: cfg}
}
func TestServiceOrderingDiversityAndEarlyStop(t *testing.T) {
	tracks := []model.DirectTrack{}
	for i := 0; i < 20; i++ {
		a := fmt.Sprintf("Artist %d", i)
		if i < 3 {
			a = "Same Artist"
		}
		tracks = append(tracks, model.DirectTrack{Artist: a, Title: fmt.Sprintf("Song %d", i), FitScore: float64(20-i) / 20})
	}
	f := &fakeSearch{}
	s := newService(t, tracks, f)
	r, e := s.Run(context.Background(), []byte("fake"), "image/png")
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Final) != 10 || r.Final[2].Gemini.GeminiRank != 4 || r.Diagnostics.SameArtistMax != 2 || r.Diagnostics.Attempted != 10 || len(f.queries) != 10 || r.Diagnostics.Unattempted != 10 {
		t.Fatalf("diagnostics %+v", r.Diagnostics)
	}
	if r.Diagnostics.PromptVersion != client.DirectPromptVersion || r.Diagnostics.LastFMCalls != 0 || r.Diagnostics.PlaylistWrites != 0 || r.Diagnostics.Unresolved != 0 {
		t.Fatal(r.Diagnostics)
	}
	for _, q := range f.queries {
		if !strings.HasPrefix(q.Text, `"`) || q.RelevanceLanguage != "" || q.Order != "relevance" {
			t.Fatal("broad or biased search", q)
		}
	}
	for _, ids := range f.ids {
		if len(ids) != 1 {
			t.Fatal("batch must dedupe", ids)
		}
	}
	b, e := json.Marshal(r.Diagnostics)
	if e != nil || strings.Contains(string(b), "api_key") {
		t.Fatal(e, string(b))
	}
}
func TestPartialAndUnresolved(t *testing.T) {
	f := &fakeSearch{omit: true}
	s := newService(t, []model.DirectTrack{candidate()}, f)
	r, e := s.Run(context.Background(), nil, "image/png")
	if e != nil || !r.Diagnostics.Partial || r.Diagnostics.Unresolved != 1 || *r.Diagnostics.UnresolvedRate != 1 || len(r.Final) != 0 {
		t.Fatal(r, e)
	}
	// No-match entries use a short negative TTL, and perform no API call on reuse.
	s.Resolver.Stats = CallStats{}
	again, e := s.Run(context.Background(), nil, "image/png")
	if e != nil || again.Diagnostics.Calls.CacheHits != 1 || again.Diagnostics.Calls.SearchCalls != 0 {
		t.Fatal(again, e)
	}
}
func TestQuotaBudgetAndCancellation(t *testing.T) {
	for _, err := range []error{client.ErrYouTubeQuota, context.DeadlineExceeded, context.Canceled} {
		f := &fakeSearch{searchErr: err}
		s := newService(t, []model.DirectTrack{candidate(), {Artist: "Other", Title: "Song", FitScore: .8}}, f)
		r, e := s.Run(context.Background(), nil, "image/png")
		if !errors.Is(e, err) || r.Diagnostics.Attempted != 1 || r.Diagnostics.Unattempted != 1 || len(f.queries) != 1 {
			t.Fatal(r, e)
		}
		if _, hit := s.Resolver.Cache.Get(cacheKey(candidate().Artist, candidate().Title, s.Config)); hit {
			t.Fatal("API failure must not be negative cached")
		}
	}
	f := &fakeSearch{omit: true}
	s := newService(t, []model.DirectTrack{candidate(), {Artist: "Other", Title: "Song", FitScore: .8}}, f)
	s.Config.MaxSearchCalls = 1
	s.Resolver.Config = s.Config
	r, e := s.Run(context.Background(), nil, "image/png")
	if !errors.Is(e, ErrSearchBudget) || r.Diagnostics.Calls.SearchCalls != 1 || r.Diagnostics.Unattempted != 1 {
		t.Fatal(r, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s = newService(t, []model.DirectTrack{candidate()}, &fakeSearch{})
	_, e = s.Run(ctx, nil, "image/png")
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
func TestCacheRevalidationPersistenceExpiryAndKeys(t *testing.T) {
	cfg := DefaultConfig()
	path := filepath.Join(t.TempDir(), "cache.json")
	cache, e := NewCache(path)
	if e != nil {
		t.Fatal(e)
	}
	f := &fakeSearch{}
	r := &Resolver{Client: f, Cache: cache, Config: cfg}
	out, e := r.Resolve(context.Background(), candidate())
	if e != nil || !IsResolved(out.Status) {
		t.Fatal(out, e)
	}
	saved, e := NewCache(path)
	if e != nil {
		t.Fatal(e)
	}
	r = &Resolver{Client: f, Cache: saved, Config: cfg}
	out, e = r.Resolve(context.Background(), candidate())
	if e != nil || !out.CacheHit || r.Stats.SearchCalls != 0 || r.Stats.VideosCalls != 1 {
		t.Fatal(out, e)
	}
	saved.now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	if _, ok := saved.Get(cacheKey("Beach House", "Space Song", cfg)); ok {
		t.Fatal("expired cache")
	}
	other := cfg
	other.Region = "US"
	if cacheKey("a|b", "c", cfg) == cacheKey("a", "b|c", cfg) || cacheKey("a", "b", cfg) == cacheKey("a", "b", other) {
		t.Fatal("cache collision")
	}
	// Current unplayability invalidates the positive mapping before reuse.
	f.videos[out.Video.VideoID] = model.YouTubeVideo{}
	f.omit = true
	r = &Resolver{Client: f, Cache: cache, Config: cfg}
	out, e = r.Resolve(context.Background(), candidate())
	if e != nil || IsResolved(out.Status) || r.Stats.SearchCalls != 2 {
		t.Fatal(out, e)
	}
}
func TestGeneratorFailureAndStableScores(t *testing.T) {
	s := newService(t, []model.DirectTrack{candidate()}, &fakeSearch{})
	g := s.Generator.(*fakeGenerator)
	g.err = client.ErrInvalidAIResponse
	r, e := s.Run(context.Background(), nil, "image/png")
	if e == nil || r.Diagnostics.Calls.SearchCalls != 0 {
		t.Fatal(r, e)
	}
	g.err = nil
	r, e = s.Run(context.Background(), nil, "image/png")
	if e != nil || !reflect.DeepEqual(r.Final[0].Gemini.FitScore, g.tracks[0].FitScore) {
		t.Fatal(r, e)
	}
}
