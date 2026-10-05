package main

import (
	"context"
	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
	"io"
	"net/http"
	"strings"
	"testing"
)

type orderFakeTransport struct{ calls int }

func (f *orderFakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls++
	body := `{"items":[{"id":{"kind":"youtube#video","videoId":"b"},"snippet":{"title":"B","channelId":"chan","channelTitle":"Channel","publishedAt":"2020-01-01T00:00:00Z"}},{"id":{"kind":"youtube#video","videoId":"a"},"snippet":{"title":"A","channelTitle":"Channel"}}]}`
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}
func TestFreshBypassRankSourcesAndBudgets(t *testing.T) {
	core := &orderFakeTransport{}
	rt := &boundedAuditTransport{Core: core}
	raws := [][]client.YouTubeSearchSnippet{}
	c, err := client.NewDiagnosticYouTubeClient(context.Background(), &http.Client{Transport: rt}, func(_ model.MusicSearchQuery, s []client.YouTubeSearchSnippet) { raws = append(raws, s) })
	if err != nil {
		t.Fatal(err)
	}
	q := model.MusicSearchQuery{Text: freshQuery, Region: "KR", Order: "relevance", MaxResults: 50}
	for _, order := range []string{"relevance", "viewCount"} {
		q.Order = order
		if _, err = c.SearchMusic(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	if core.calls != 2 || rt.Search != 2 || len(raws) != 2 || raws[0][0].VideoID != "b" || raws[0][1].VideoID != "a" || raws[0][0].ChannelID != "chan" {
		t.Fatal(core, raws)
	}
	if rt.Calls[0].CacheHit || !rt.Calls[0].Network || rt.Calls[1].Parameters["order"][0] != "viewCount" {
		t.Fatal(rt.Calls)
	}
	if _, err = c.SearchMusic(context.Background(), q); err == nil || core.calls != 2 {
		t.Fatal("network budget exceeded", err)
	}
	ids := unionFresh(raws)
	if len(ids) != 2 || ids[0] != "b" || ids[1] != "a" {
		t.Fatal(ids)
	}
	q.Order = "relevance"
	key := client.SearchCacheKey(q)
	q.Order = "viewCount"
	if client.SearchCacheKey(q) == key {
		t.Fatal("cache identity collision")
	}
	hundred := make([]string, 100)
	batches := batchesFresh(hundred)
	if len(batches) != 2 || len(batches[0]) != 50 || len(batches[1]) != 50 {
		t.Fatal(batches)
	}
}

func TestFreshEvaluationKeepsRanksAndSourceTags(t *testing.T) {
	videos := map[string]model.YouTubeVideo{}
	for _, id := range []string{"b", "a"} {
		videos[id] = model.YouTubeVideo{VideoID: id, Title: "Official Audio", ChannelTitle: "Channel " + id, ChannelID: id, CategoryID: "10", Public: true, Embeddable: true, DurationSeconds: 180, LicensedContent: true, ViewCount: 1000000}
	}
	ranks := map[string]int{"b": 0, "a": 1}
	for _, tag := range []string{"q1_relevance", "q2_viewcount"} {
		sources := map[string][]string{"b": {tag}, "a": {tag}}
		out := evaluateFresh([]string{"b", "a"}, ranks, sources, videos, model.ImageAnalysis{})
		if out.Candidates[0]["video_id"] != "b" || out.Candidates[0]["search_rank"] != 1 || out.Candidates[1]["search_rank"] != 2 || out.Candidates[0]["sources"].([]string)[0] != tag {
			t.Fatal(out.Candidates)
		}
	}
	out := evaluateFresh([]string{"b"}, ranks, map[string][]string{"b": {"q1_relevance", "q2_viewcount"}}, videos, model.ImageAnalysis{})
	if len(out.Candidates[0]["sources"].([]string)) != 2 {
		t.Fatal("overlap source lost")
	}
}
