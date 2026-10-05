package lastfmeval

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"example.com/sync/internal/lastfm"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func vocabulary(t *testing.T) *Vocabulary {
	t.Helper()
	r := lastfm.Registry{Version: 1, Tags: []lastfm.RegistryTag{}}
	mappings := []lastfm.TagMapping{}
	for _, x := range []struct{ name, tag, cat, status string }{{"rock", "rock", "genre", "validated"}, {"electronic", "electronic", "genre", "validated"}, {"indie-rock", "indie rock", "subgenre", "validated"}, {"acoustic", "acoustic", "style", "validated"}, {"dreamy", "dreamy", "mood", "validated"}, {"mellow", "mellow", "mood", "validated"}, {"calm", "calm", "mood", "candidate"}, {"ambient", "ambient", "genre", "candidate"}, {"noise", "noise", "genre", "rejected"}, {"unknown", "unknown", "genre", "unknown"}} {
		r.Tags = append(r.Tags, lastfm.RegistryTag{Canonical: x.name, Tag: x.tag, Category: x.cat, Status: x.status})
		mappings = append(mappings, lastfm.TagMapping{Canonical: x.name, Primary: x.tag, Type: x.cat})
	}
	dir := t.TempDir()
	b, _ := json.Marshal(r)
	_ = os.WriteFile(filepath.Join(dir, "registry.json"), b, 0600)
	b, _ = json.Marshal(map[string]any{"version": 1, "mappings": mappings, "unmapped": []map[string]string{{"canonical": "reflective", "reason": "no exact equivalent"}}})
	_ = os.WriteFile(filepath.Join(dir, "mapping.json"), b, 0600)
	v, err := LoadVocabulary(filepath.Join(dir, "registry.json"), filepath.Join(dir, "mapping.json"))
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func concept(name string, w float64) WeightedConcept {
	return WeightedConcept{Name: name, Weight: w, WeightSource: "test signal"}
}
func TestRegistryMappingAndSelection(t *testing.T) {
	v := vocabulary(t)
	for _, name := range []string{"calm", "ambient", "noise", "unknown", "reflective", "chillwave"} {
		if v.Map(concept(name, .8), "mood").Allowed {
			t.Fatalf("must exclude %s", name)
		}
	}
	if !v.Map(concept("indie rock", .8), "genre").Allowed {
		t.Fatal("format-only normalization")
	}
	if v.Map(concept("dreamy", .8), "genre").Allowed {
		t.Fatal("category mismatch")
	}
	p := DefaultPolicy()
	p.MaxGenreRoutes = 2
	plan, e := BuildPlan(MusicRetrievalProfile{Genres: []WeightedConcept{concept("acoustic", .99), concept("indie-rock", .9), concept("electronic", .7), concept("rock", .8), concept("ambient", .9)}, Moods: []WeightedConcept{concept("calm", 1), concept("dreamy", .8), concept("mellow", .7)}}, v, p)
	if e != nil || len(plan.Routes) != 3 || plan.Routes[0].LastFMTag != "rock" || plan.Routes[1].LastFMTag != "electronic" || plan.Routes[2].LastFMTag != "dreamy" {
		t.Fatalf("plan %+v %v", plan, e)
	}
	if plan.Routes[2].ProfileWeight != .8*.6 {
		t.Fatal("weak mood factor")
	}
	p.MaxRoutes = 2
	plan, _ = BuildPlan(plan.Profile, v, p)
	if len(plan.Routes) != 2 {
		t.Fatal("total budget")
	}
}
func TestMoodOnlyAndUnmapped(t *testing.T) {
	v := vocabulary(t)
	profile := MusicRetrievalProfile{Genres: []WeightedConcept{concept("ambient", .9)}, Moods: []WeightedConcept{concept("dreamy", 1)}}
	p := DefaultPolicy()
	plan, e := BuildPlan(profile, v, p)
	if e != nil || len(plan.Routes) != 0 || len(plan.Warnings) == 0 {
		t.Fatal("mood-only must be disabled")
	}
	p.MoodOnly = true
	plan, e = BuildPlan(profile, v, p)
	if e != nil || len(plan.Routes) != 1 || plan.Routes[0].Category != "mood" {
		t.Fatal("explicit override")
	}
	p.MaxRoutes = 5
	if _, e = BuildPlan(profile, v, p); e == nil {
		t.Fatal("budget validation")
	}
}
func track(artist, title, mbid string) lastfm.Track {
	r := lastfm.Track{Name: title, MBID: mbid, URL: "https://last.fm/music/test"}
	r.Artist.Name = artist
	return r
}
func raw(artist, title, id, tag string, rank int, w float64) RawCandidate {
	return RawCandidate{track(artist, title, id), TagRouteEvidence{tag, tag, "genre", rank, w}}
}
func TestNormalizationAndDedupe(t *testing.T) {
	if NormalizeTrack("  Ａrtist\tName  ") != "artist name" || NormalizeTrack("Don’t – Go") != NormalizeTrack("Don't - Go") {
		t.Fatal("typographic normalization")
	}
	if NormalizeTrack("Song (Live)") == NormalizeTrack("Song") || NormalizeTrack("Song feat. X") == NormalizeTrack("Song") || NormalizeTrack("Song (Remastered)") == NormalizeTrack("Song") {
		t.Fatal("version labels must survive")
	}
	rows := []RawCandidate{raw("Ａrtist", "Song", "id1", "rock", 4, .8), raw("artist", " song ", "", "dreamy", 12, .48), raw("Alias", "Different title", "id1", "electronic", 8, .7), raw("Artist", "Song (Live)", "id2", "rock", 5, .8), raw("Other", "No ID", "", "rock", 6, .8)}
	merged, warnings, invalid := Merge(rows, DefaultPolicy())
	if len(merged) != 3 || invalid != 0 || len(warnings) == 0 {
		t.Fatalf("merge=%+v warnings=%v", merged, warnings)
	}
	found := false
	for _, c := range merged {
		if len(c.ProviderRecords) == 3 {
			found = true
			if len(c.Evidences) != 3 || len(c.Contributions) != 3 {
				t.Fatal("evidence lost")
			}
		}
	}
	if !found {
		t.Fatal("transitive MBID/artist-title identity")
	}
}
func TestScoreOrderingAndBonus(t *testing.T) {
	p := DefaultPolicy()
	c := CandidateTrack{Evidences: []TagRouteEvidence{{LastFMTag: "rock", Rank: 1, RouteWeight: .8}, {LastFMTag: "rock", Rank: 2, RouteWeight: .8}}}
	Score(&c, p)
	if c.RetrievalScore != .8 || c.OverlapBonus != 0 {
		t.Fatal("same-route duplicate bonus")
	}
	c.Evidences = append(c.Evidences, TagRouteEvidence{LastFMTag: "dreamy", Rank: 10, RouteWeight: .48})
	Score(&c, p)
	if c.OverlapBonus != .05 || c.RetrievalScore <= .8 {
		t.Fatal("distinct overlap bonus")
	}
	merged, _, _ := Merge([]RawCandidate{raw("A", "Low", "", "rock", 10, 1), raw("B", "High", "", "rock", 1, 1)}, p)
	if merged[0].Title != "High" || merged[0].RawRank != 1 {
		t.Fatal("rank score ordering")
	}
}
func TestDiversityRetainsDeferred(t *testing.T) {
	ranked := []CandidateTrack{}
	for i := 0; i < 6; i++ {
		a := "A"
		if i >= 3 {
			a = string(rune('B' + i - 3))
		}
		ranked = append(ranked, CandidateTrack{Artist: a, NormalizedArtist: NormalizeTrack(a), RawRank: i + 1, RetrievalScore: 1 - float64(i)*.1})
	}
	selected, deferred := Diversity(ranked, 4, 2)
	if len(selected) != 4 || len(deferred) != 2 || selected[2].RawRank != 4 || selected[2].DiversityRank != 3 {
		t.Fatal("stable diversity selection")
	}
	if Concentration(selected).MaxTracks > 2 || len(ranked) != 6 {
		t.Fatal("must retain original ranking")
	}
	selected, _ = Diversity(ranked[:3], 20, 2)
	if len(selected) != 2 {
		t.Fatal("must not relax cap to fill")
	}
}

type fakeRetriever struct {
	fetch func(context.Context, string, int) (FetchResult, error)
}

func (f fakeRetriever) Fetch(ctx context.Context, tag string, limit int) (FetchResult, error) {
	return f.fetch(ctx, tag, limit)
}
func testPlan() Plan {
	return Plan{Routes: []RetrievalRoute{{"rock", "rock", "genre", 1, 1}, {"dreamy", "dreamy", "mood", .8, .48}}, Warnings: []string{}}
}
func TestPartialAndAllFailure(t *testing.T) {
	f := fakeRetriever{func(ctx context.Context, tag string, limit int) (FetchResult, error) {
		if tag == "dreamy" {
			return FetchResult{Events: []lastfm.Event{{Method: "tag.getTopTracks", HTTPAttempted: true}}}, errors.New("api_key=secret raw url")
		}
		return FetchResult{Tracks: []lastfm.Track{track("Artist", "Song", "id")}, Events: []lastfm.Event{{Method: "tag.getTopTracks", HTTPAttempted: true, Success: true}}}, nil
	}}
	e, err := Evaluate(context.Background(), testPlan(), DefaultPolicy(), f)
	if err != nil || !e.Partial || len(e.Merged) != 1 || e.APICalls != 2 || strings.Contains(Report(e, "photo"), "secret") {
		t.Fatalf("partial %+v %v", e, err)
	}
	f.fetch = func(context.Context, string, int) (FetchResult, error) { return FetchResult{}, errors.New("failed") }
	e, err = Evaluate(context.Background(), testPlan(), DefaultPolicy(), f)
	if err == nil || e.Outcome != "all_routes_failed" {
		t.Fatal("all failure")
	}
}
func TestCancellationAndConcurrency(t *testing.T) {
	var active, maxActive atomic.Int32
	f := fakeRetriever{func(ctx context.Context, tag string, limit int) (FetchResult, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := maxActive.Load()
			if n <= old || maxActive.CompareAndSwap(old, n) {
				break
			}
		}
		select {
		case <-ctx.Done():
			return FetchResult{}, ctx.Err()
		case <-time.After(20 * time.Millisecond):
			return FetchResult{Tracks: []lastfm.Track{track(tag, "S", "")}}, nil
		}
	}}
	plan := testPlan()
	plan.Routes = append(plan.Routes, RetrievalRoute{"electronic", "electronic", "genre", .7, .7}, RetrievalRoute{"acoustic", "acoustic", "style", .6, .6})
	_, err := Evaluate(context.Background(), plan, DefaultPolicy(), f)
	if err != nil || maxActive.Load() > 2 {
		t.Fatal("bounded concurrency")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e, err := Evaluate(ctx, plan, DefaultPolicy(), f)
	if err == nil || e.Outcome != "all_routes_failed" {
		t.Fatal("cancellation")
	}
}
func TestHTTPTimeoutCacheAndNoEnrichment(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("method") != "tag.getTopTracks" {
			t.Error("unexpected enrichment")
		}
		_, _ = w.Write([]byte(`{"tracks":{"track":[{"name":"Song","artist":{"name":"Artist"}}]}}`))
	}))
	defer s.Close()
	// Adapter through injected transport maps official base URL to local fake only.
	transport := rewriteTransport{base: s.URL}
	p := Provider{APIKey: "unit-test-not-real", CacheDir: t.TempDir(), HTTP: &http.Client{Transport: transport}}
	for i := 0; i < 2; i++ {
		res, err := p.Fetch(context.Background(), "dreamy", 50)
		if err != nil || len(res.Tracks) != 1 {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("cache hit")
	}
	p.Fresh = true
	_, _ = p.Fetch(context.Background(), "dreamy", 50)
	if calls != 2 {
		t.Fatal("fresh bypass")
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	p.HTTP = &http.Client{Transport: rewriteTransport{base: slow.URL}}
	p.Timeout = 10 * time.Millisecond
	_, err := p.Fetch(context.Background(), "different", 50)
	if !errors.Is(err, lastfm.ErrTimeout) {
		t.Fatal("timeout", err)
	}
	if lastfm.CacheKey("base", "tag.getTopTracks", "x", 30) == lastfm.CacheKey("base", "tag.getTopTracks", "x", 50) {
		t.Fatal("limit identity")
	}
}

type rewriteTransport struct{ base string }

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	u := *r.URL
	target, _ := http.NewRequest(http.MethodGet, rt.base, nil)
	u.Scheme = target.URL.Scheme
	u.Host = target.URL.Host
	copy.URL = &u
	return http.DefaultTransport.RoundTrip(copy)
}
func TestProfileAndReport(t *testing.T) {
	// Existing enhanced fixture validates the current schema; no Gemini call.
	a, input, err := ReadAnalysis("../model/testdata/analysis-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := Profile(a)
	if err != nil || profile.Genres[0].Weight != a.MusicProfile.GenreCandidates[0].Score || profile.Moods[0].Weight != 1 {
		t.Fatal("enhanced signal conversion", err)
	}
	f := fakeRetriever{func(context.Context, string, int) (FetchResult, error) {
		return FetchResult{Tracks: []lastfm.Track{track("Artist", "Song", "id")}}, nil
	}}
	e, err := Evaluate(context.Background(), testPlan(), DefaultPolicy(), f)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = WriteReport(dir, input, e, "photo"); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join(dir, "human-review.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil || len(rows) != 2 {
		t.Fatal("csv", err)
	}
	for i := 6; i <= 10; i++ {
		if rows[1][i] != "" {
			t.Fatal("human scores must stay blank")
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "evaluation.json"))
	var saved Evaluation
	if json.Unmarshal(b, &saved) != nil || len(saved.Raw) != 2 || saved.OverlapCount != 1 {
		t.Fatal("report machine-readable")
	}
	blocked, err := Evaluate(context.Background(), Plan{}, DefaultPolicy(), f)
	if err != nil || blocked.Outcome != "insufficient_validated_mapping" || len(blocked.Raw) != 0 {
		t.Fatal("blocked mapping")
	}
}
