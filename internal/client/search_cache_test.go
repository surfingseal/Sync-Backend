package client

import (
	"context"
	"encoding/json"
	"errors"
	"example.com/sync/internal/model"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type retrievalFake struct {
	calls int
	err   error
}

func (f *retrievalFake) SearchMusic(context.Context, model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
	f.calls++
	return []model.YouTubeSearchResult{{VideoID: "actual-id"}}, f.err
}
func (f *retrievalFake) GetVideos(context.Context, []string) ([]model.YouTubeVideo, error) {
	return nil, nil
}
func TestCacheAndBudget(t *testing.T) {
	ctx := context.Background()
	source := &retrievalFake{}
	cache := NewInMemorySearchCache()
	c := &CachedMusicClient{Source: source, Cache: cache, TTL: time.Minute, Budget: NewSearchBudget(1), LiveEnabled: true}
	q := model.MusicSearchQuery{Text: "calm music", Region: "KR", RelevanceLanguage: "ko", MaxResults: 20}
	m := &RetrievalMetrics{}
	ctx = WithRetrievalMetrics(ctx, m)
	if _, err := c.SearchMusic(ctx, q); err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 || m.CacheMisses != 1 || m.SearchCalls != 1 {
		t.Fatal(source, m)
	}
	ids, err := c.SearchMusic(ctx, q)
	if err != nil || source.calls != 1 || m.CacheHits != 1 {
		t.Fatal(err, m)
	}
	ids[0].VideoID = "tampered"
	ids, _, _ = cache.Get(ctx, SearchCacheKey(q))
	if ids[0].VideoID != "actual-id" {
		t.Fatal("mutable cache")
	}
	q.Text = "another query"
	if _, err = c.SearchMusic(ctx, q); !errors.Is(err, ErrSearchBudget) || source.calls != 1 {
		t.Fatal(err)
	}
}
func TestCacheKeyAllParameters(t *testing.T) {
	q := model.MusicSearchQuery{Text: " Calm   music ", Region: "KR", RelevanceLanguage: "ko", Order: "relevance", MaxResults: 20}
	key := SearchCacheKey(q)
	normalized := q
	normalized.Text = "calm music"
	if key != SearchCacheKey(normalized) {
		t.Fatal("normalization")
	}
	for _, change := range []func(*model.MusicSearchQuery){func(q *model.MusicSearchQuery) { q.Region = "US" }, func(q *model.MusicSearchQuery) { q.RelevanceLanguage = "en" }, func(q *model.MusicSearchQuery) { q.Order = "viewCount" }, func(q *model.MusicSearchQuery) { q.MaxResults = 10 }, func(q *model.MusicSearchQuery) { q.Text = "calm -mix music" }} {
		copy := q
		change(&copy)
		if SearchCacheKey(copy) == key {
			t.Fatal("collision")
		}
	}
}
func TestCacheTTLAndCancellation(t *testing.T) {
	c := NewInMemorySearchCache()
	now := time.Now()
	c.now = func() time.Time { return now }
	ctx := context.Background()
	_ = c.Set(ctx, "k", []model.YouTubeSearchResult{{VideoID: "id"}}, time.Second)
	now = now.Add(time.Second)
	if _, hit, _ := c.Get(ctx, "k"); hit {
		t.Fatal("expired")
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := c.Get(ctx, "k"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestQuotaHasNoRetryAndNoCache(t *testing.T) {
	f := &retrievalFake{err: ErrYouTubeQuota}
	c := &CachedMusicClient{Source: f, Cache: NewInMemorySearchCache(), TTL: time.Minute, Budget: NewSearchBudget(10), LiveEnabled: true}
	m := &RetrievalMetrics{}
	_, err := c.SearchMusic(WithRetrievalMetrics(context.Background(), m), model.MusicSearchQuery{})
	if !errors.Is(err, ErrYouTubeQuota) || f.calls != 1 || m.QuotaErrors != 1 {
		t.Fatal(err, f.calls, m)
	}
}
func TestLiveOptIn(t *testing.T) {
	f := &retrievalFake{}
	c := &CachedMusicClient{Source: f, Cache: NewInMemorySearchCache(), TTL: time.Minute, Budget: NewSearchBudget(10)}
	if _, err := c.SearchMusic(context.Background(), model.MusicSearchQuery{}); !errors.Is(err, ErrLiveSearchDisabled) || f.calls != 0 {
		t.Fatal(err)
	}
}
func TestConcurrentBudget(t *testing.T) {
	b := NewSearchBudget(10)
	var calls atomic.Int32
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Take() {
				calls.Add(1)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 10 {
		t.Fatal(calls.Load())
	}
}
func TestFixtureAndReplayOffline(t *testing.T) {
	fixture := NewFixtureMusicClient()
	if len(fixture.Pools) != 4 {
		t.Fatal("four scenarios")
	}
	doc := ReplayDocument{RecordedAt: time.Now(), Pools: fixture.Pools}
	data, _ := json.Marshal(doc)
	path := filepath.Join(t.TempDir(), "replay.json")
	_ = os.WriteFile(path, data, 0600)
	replay, err := NewReplayMusicClient(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*OfflineMusicClient{fixture, replay} {
		m := &RetrievalMetrics{}
		ctx := WithRetrievalMetrics(context.Background(), m)
		ids, err := c.SearchMusic(ctx, model.MusicSearchQuery{Text: "ocean calm", MaxResults: 20})
		if err != nil || len(ids) != 20 || m.SearchCalls != 0 {
			t.Fatal(err, m)
		}
		wanted := []string{ids[0].VideoID, ids[1].VideoID}
		videos, err := c.GetVideos(ctx, wanted)
		if err != nil || len(videos) != 2 {
			t.Fatal(err)
		}
	}
	doc.RecordedAt = time.Now().Add(-31 * 24 * time.Hour)
	data, _ = json.Marshal(doc)
	_ = os.WriteFile(path, data, 0600)
	if _, err = NewReplayMusicClient(path); err == nil {
		t.Fatal("stale replay")
	}
}
