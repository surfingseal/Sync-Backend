package lastfm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fake(t *testing.T, body string) *Client {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" || r.Header.Get("User-Agent") == "" {
			t.Error("missing JSON or identifying user agent")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	c := New("test-secret-not-real")
	c.BaseURL = s.URL
	return c
}
func TestParsing(t *testing.T) {
	c := fake(t, `{"toptags":{"tag":[{"name":"rock","count":123,"reach":"45"},{"name":"mood"}]}}`)
	tags, e := c.GetTopTags(context.Background())
	if e != nil || len(tags) != 2 || *tags[0].Count != 123 || *tags[0].Reach != 45 || tags[1].Count != nil {
		t.Fatalf("tags=%+v err=%v", tags, e)
	}
	c = fake(t, `{"tag":{"name":"rock","reach":"100","taggings":300,"wiki":{"summary":"Rock music"}}}`)
	info, e := c.GetInfo(context.Background(), "rock")
	if e != nil || *info.Reach != 100 || *info.Taggings != 300 || info.Wiki.Summary != "Rock music" {
		t.Fatalf("info=%+v err=%v", info, e)
	}
	c = fake(t, `{"tracks":{"track":[{"name":"Song","mbid":"","url":"https://last.fm/track","artist":{"name":"Artist","mbid":"id"},"@attr":{"rank":"1"}}]}}`)
	tracks, e := c.GetTopTracks(context.Background(), "rock", 10)
	if e != nil || len(tracks) != 1 || tracks[0].Artist.Name != "Artist" || tracks[0].Attr.Rank != "1" {
		t.Fatalf("tracks=%+v err=%v", tracks, e)
	}
	c = fake(t, `{"tag":{"name":"x","total":100}}`)
	info, e = c.GetInfo(context.Background(), "x")
	if e != nil || info.Taggings != nil || info.Reach != nil || *info.Total != 100 {
		t.Fatal("must not synthesize taggings/reach")
	}
}
func TestErrors(t *testing.T) {
	c := fake(t, `{"error":10,"message":"private internal detail"}`)
	_, e := c.GetTopTags(context.Background())
	if !errors.Is(e, ErrAPI) || strings.Contains(e.Error(), "private") || strings.Contains(e.Error(), c.Key) {
		t.Fatalf("unsafe error %v", e)
	}
	c = fake(t, `{"bad":"schema"}`)
	_, e = c.GetTopTags(context.Background())
	if !errors.Is(e, ErrResponse) {
		t.Fatal(e)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }))
	defer s.Close()
	c.BaseURL = s.URL
	_, e = c.GetInfo(context.Background(), "x")
	var api *Error
	if !errors.As(e, &api) || api.Status != 429 || !errors.Is(e, ErrHTTP) {
		t.Fatal(e)
	}
	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s2.Close()
	c.BaseURL = s2.URL
	c.Timeout = 20 * time.Millisecond
	_, e = c.GetTopTags(context.Background())
	if !errors.Is(e, ErrTimeout) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e = c.GetTopTags(ctx)
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
func TestCache(t *testing.T) {
	n := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		_, _ = w.Write([]byte(`{"tag":{"name":"dream pop","reach":42}}`))
	}))
	defer s.Close()
	c := New("test-secret-not-real")
	c.BaseURL = s.URL
	c.CacheDir = t.TempDir()
	for i := 0; i < 2; i++ {
		if _, e := c.GetInfo(context.Background(), "dream pop"); e != nil {
			t.Fatal(e)
		}
	}
	if n != 1 || !c.Events[1].Cached {
		t.Fatal("cache miss")
	}
	c.Fresh = true
	_, _ = c.GetInfo(context.Background(), "dream pop")
	if n != 2 {
		t.Fatal("fresh must bypass")
	}
	key := CacheKey(s.URL, "tag.getInfo", "Dream  Pop", 0)
	if key != CacheKey(s.URL, "tag.getInfo", "dream pop", 0) || key == CacheKey(s.URL, "tag.getTopTracks", "dream pop", 10) || key == CacheKey("other", "tag.getInfo", "dream pop", 0) || CacheKey(s.URL, "tag.getTopTracks", "dream pop", 10) == CacheKey(s.URL, "tag.getTopTracks", "dream pop", 20) {
		t.Fatal("cache identity")
	}
	b, e := os.ReadFile(filepath.Join(c.CacheDir, key+".json"))
	if e != nil || strings.Contains(string(b), c.Key) {
		t.Fatal("unsafe cache")
	}
}
func TestVocabulary(t *testing.T) {
	if Normalize("  ＲＯＣＫ \t") != "rock" || Canonical("Dream Pop") != "dream-pop" {
		t.Fatal("normalization")
	}
	tags := AuditGlobal([]Tag{{Name: " Rock "}, {Name: "rock"}, {Name: "hip-hop"}, {Name: "hip hop"}, {Name: "seen live"}, {Name: "mystery"}})
	if len(tags) != 5 || tags[1].Canonical == tags[2].Canonical || *tags[0].Rank != 1 {
		t.Fatal("dedup/collision")
	}
	if tags[3].Status != "rejected" || tags[3].Category != "personal/noisy" || tags[4].Status != "unknown" {
		t.Fatal("classification")
	}
	s, status := Classify("dreamy")
	if s.Category != "mood" || status != "candidate" {
		t.Fatal("must not prevalidate mood")
	}
	r := GenerateRegistry(tags)
	if len(r.Tags) != 3 {
		t.Fatal("registry excludes unknown/noisy")
	}
	a := &Audit{Tags: tags}
	if ApplyReviews(a, map[string]Review{"rock": {Status: "validated", Notes: "guess"}}) == nil {
		t.Fatal("validation requires evidence")
	}
	tracks := []Track{{Name: "S"}, {Name: "s"}}
	tracks[0].Artist.Name = "A"
	tracks[1].Artist.Name = "a"
	samples := Samples(tracks)
	if samples[0].Duplicate || !samples[1].Duplicate {
		t.Fatal("sample duplicate")
	}
}
func TestAuditAndReview(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("method") {
		case "tag.getTopTags":
			_, _ = w.Write([]byte(`{"toptags":{"tag":[{"name":"rock","count":100},{"name":"seen live"}]}}`))
		case "tag.getInfo":
			_, _ = w.Write([]byte(`{"tag":{"name":"rock","reach":20}}`))
		case "tag.getTopTracks":
			_, _ = w.Write([]byte(`{"tracks":{"track":[{"name":"a"},{"name":"b"},{"name":"c"},{"name":"d"},{"name":"e"}]}}`))
		}
	}))
	defer s.Close()
	c := New("test-secret-not-real")
	c.BaseURL = s.URL
	a, e := Run(context.Background(), c, AuditConfig{MaxEnrich: 1, SampleLimit: 10}, t.TempDir())
	if e != nil || !a.Complete || len(c.Events) != 3 || len(a.Tags[0].Samples) != 5 {
		t.Fatalf("%+v %v", a, e)
	}
	if e = ApplyReviews(a, map[string]Review{"rock": {Status: "validated", Notes: "Known rock repertoire; metadata reviewed"}}); e != nil {
		t.Fatal(e)
	}
	if GenerateRegistry(a.Tags).Tags[0].Status != "validated" {
		t.Fatal("review not reflected")
	}
	if !strings.Contains(Report(a), "Genre coverage matrix") {
		t.Fatal("report incomplete")
	}
}

func TestValidationCannotBeInferredFromStatusOnly(t *testing.T) {
	t1 := AuditedTag{Name: "rock", Canonical: "rock", Category: "genre", Status: "validated"}
	if got := GenerateRegistry([]AuditedTag{t1}); len(got.Tags) != 1 || got.Tags[0].Status != "candidate" {
		t.Fatal("unreviewed status must not become validated")
	}
	a := &Audit{Tags: []AuditedTag{t1}}
	a.Tags[0].Info = &TagInfo{}
	a.Tags[0].Samples = make([]Sample, 5)
	if ApplyReviews(a, map[string]Review{"rock": {Status: "validated", Notes: "review"}}) == nil {
		t.Fatal("positive usage required")
	}
}
