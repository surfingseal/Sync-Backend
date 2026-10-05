package main

import (
	"context"
	"errors"
	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
	"testing"
)

type abFake struct{ search, metadata int }

func (f *abFake) SearchMusic(context.Context, model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
	f.search++
	return []model.YouTubeSearchResult{{VideoID: "new"}}, nil
}
func (f *abFake) GetVideos(context.Context, []string) ([]model.YouTubeVideo, error) {
	f.metadata++
	return []model.YouTubeVideo{{VideoID: "new"}}, nil
}
func TestReusedQ1ExactlyOneNewSearchAndBatch(t *testing.T) {
	f := &abFake{}
	obs := &musicObservation{Core: f, Snippets: map[string][]client.YouTubeSearchSnippet{}}
	q := model.MusicSearchQuery{Text: "city pop song", Order: "relevance", MaxResults: 50}
	m := &reusedQ1Music{source: obs, q1: searchRecord{Query: q, Raw: []client.YouTubeSearchSnippet{{VideoID: "old"}}}, metadata: metadataRecord{IDs: []string{"old"}, Videos: []model.YouTubeVideo{{VideoID: "old"}}}}
	ctx := context.Background()
	hits, err := m.SearchMusic(ctx, q)
	if err != nil || hits[0].VideoID != "old" || f.search != 0 {
		t.Fatal(hits, err)
	}
	vids, err := m.GetVideos(ctx, []string{"old"})
	if err != nil || vids[0].VideoID != "old" || f.metadata != 0 {
		t.Fatal(vids, err)
	}
	q.Order = "viewCount"
	if _, err = m.SearchMusic(ctx, q); err != nil {
		t.Fatal(err)
	}
	if _, err = m.GetVideos(ctx, []string{"new"}); err != nil {
		t.Fatal(err)
	}
	if _, err = m.SearchMusic(ctx, q); !errors.Is(err, client.ErrSearchBudget) {
		t.Fatal(err)
	}
	if _, err = m.GetVideos(ctx, []string{"new"}); !errors.Is(err, client.ErrSearchBudget) {
		t.Fatal(err)
	}
	if f.search != 1 || f.metadata != 1 {
		t.Fatal(f)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = m.SearchMusic(canceled, q); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
