package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"example.com/sync/internal/model"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var ErrSearchBudget = errors.New("YouTube search budget exceeded")
var ErrLiveSearchDisabled = errors.New("live YouTube search disabled")

type SearchCache interface {
	Get(context.Context, string) ([]model.YouTubeSearchResult, bool, error)
	Set(context.Context, string, []model.YouTubeSearchResult, time.Duration) error
}
type cacheEntry struct {
	ids     []model.YouTubeSearchResult
	expires time.Time
}
type InMemorySearchCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	now     func() time.Time
}

func NewInMemorySearchCache() *InMemorySearchCache {
	return &InMemorySearchCache{entries: map[string]cacheEntry{}, now: time.Now}
}
func (c *InMemorySearchCache) Get(ctx context.Context, key string) ([]model.YouTubeSearchResult, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false, nil
	}
	if !c.now().Before(e.expires) {
		delete(c.entries, key)
		return nil, false, nil
	}
	return append([]model.YouTubeSearchResult{}, e.ids...), true, nil
}
func (c *InMemorySearchCache) Set(ctx context.Context, key string, ids []model.YouTubeSearchResult, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ttl <= 0 || ttl > time.Hour {
		return fmt.Errorf("invalid cache TTL")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if !c.now().Before(e.expires) {
			delete(c.entries, k)
		}
	}
	if len(c.entries) >= 1000 {
		clear(c.entries)
	}
	c.entries[key] = cacheEntry{append([]model.YouTubeSearchResult{}, ids...), c.now().Add(ttl)}
	return nil
}
func SearchCacheKey(q model.MusicSearchQuery) string {
	if q.Order == "" {
		q.Order = "relevance"
	}
	q.Text = strings.ToLower(strings.Join(strings.Fields(q.Text), " "))
	// Include fixed SDK filters as well as all variable result-affecting parameters.
	data, _ := json.Marshal(struct {
		Query                                                    model.MusicSearchQuery
		Part, Type, Category, Embeddable, Syndicated, SafeSearch string
	}{q, "snippet", "video", "10", "true", "true", "moderate"})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

type RetrievalMetrics struct{ CacheHits, CacheMisses, SearchCalls, QuotaErrors, FixtureRequests, ReplayRequests int }
type retrievalKey struct{}

func WithRetrievalMetrics(ctx context.Context, m *RetrievalMetrics) context.Context {
	return context.WithValue(ctx, retrievalKey{}, m)
}
func retrievalMetrics(ctx context.Context) *RetrievalMetrics {
	m, _ := ctx.Value(retrievalKey{}).(*RetrievalMetrics)
	return m
}

type SearchBudget struct {
	limit int64
	used  atomic.Int64
}

func NewSearchBudget(limit int) *SearchBudget { return &SearchBudget{limit: int64(limit)} }
func (b *SearchBudget) Take() bool {
	for {
		n := b.used.Load()
		if n >= b.limit {
			return false
		}
		if b.used.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

// The process shares one budget across requests. Cached IDs still receive fresh metadata.
type CachedMusicClient struct {
	Source      MusicSearchClient
	Cache       SearchCache
	TTL         time.Duration
	Budget      *SearchBudget
	LiveEnabled bool
}

func (c *CachedMusicClient) SearchMusic(ctx context.Context, q model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m := retrievalMetrics(ctx)
	key := SearchCacheKey(q)
	ids, hit, err := c.Cache.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if hit {
		if m != nil {
			m.CacheHits++
		}
		return ids, nil
	}
	if m != nil {
		m.CacheMisses++
	}
	if !c.LiveEnabled {
		return nil, ErrLiveSearchDisabled
	}
	if c.Budget == nil || !c.Budget.Take() {
		return nil, ErrSearchBudget
	}
	if m != nil {
		m.SearchCalls++
	}
	ids, err = c.Source.SearchMusic(ctx, q)
	if err != nil {
		if m != nil && errors.Is(err, ErrYouTubeQuota) {
			m.QuotaErrors++
		}
		return nil, err
	}
	if err = c.Cache.Set(ctx, key, ids, c.TTL); err != nil {
		return nil, err
	}
	return ids, nil
}
func (c *CachedMusicClient) GetVideos(ctx context.Context, ids []string) ([]model.YouTubeVideo, error) {
	if !c.LiveEnabled {
		return nil, ErrLiveSearchDisabled
	}
	videos, err := c.Source.GetVideos(ctx, ids)
	if errors.Is(err, ErrYouTubeQuota) {
		if m := retrievalMetrics(ctx); m != nil {
			m.QuotaErrors++
		}
	}
	return videos, err
}
func (*CachedMusicClient) DataMode() string { return "live" }
