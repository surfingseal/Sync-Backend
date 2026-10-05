package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func itunesItem(artist, title string) map[string]any {
	return map[string]any{"wrapperType": "track", "kind": "song", "artistName": artist, "trackName": title, "collectionName": "Album", "artworkUrl100": "https://is1-ssl.mzstatic.com/image/test.jpg"}
}
func TestITunesIdentityAndFailures(t *testing.T) {
	for _, name := range []string{"exact", "unicode-case", "punctuation", "wrong-artist", "wrong-title", "tribute", "cover", "live", "remix", "empty", "missing-artwork", "bad-url", "ambiguous", "429", "500", "malformed", "oversized"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				q := r.URL.Query()
				if q.Get("media") != "music" || q.Get("entity") != "song" || q.Get("country") != "KR" || q.Get("limit") != "10" || r.Header.Get("User-Agent") == "" {
					t.Error("invalid Apple request")
				}
				item := itunesItem("Artist", "My Song")
				results := []map[string]any{item}
				switch name {
				case "unicode-case":
					item["artistName"] = "ＡＲＴＩＳＴ"
					item["trackName"] = "my song"
				case "punctuation":
					item["trackName"] = "My-Song"
				case "wrong-artist":
					item["artistName"] = "Other"
				case "wrong-title":
					item["trackName"] = "Other"
				case "tribute":
					item["artistName"] = "Tribute to Artist"
				case "cover":
					item["trackName"] = "My Song (Cover)"
				case "live":
					item["trackName"] = "My Song (Live)"
				case "remix":
					item["trackName"] = "My Song (Remix)"
				case "empty":
					results = nil
				case "missing-artwork":
					delete(item, "artworkUrl100")
				case "bad-url":
					item["artworkUrl100"] = "https://evil.example/cover.jpg"
				case "ambiguous":
					second := itunesItem("Artist", "My Song")
					second["collectionName"] = "Other Release"
					results = append(results, second)
				case "429":
					w.WriteHeader(429)
					return
				case "500":
					w.WriteHeader(500)
					return
				case "malformed":
					w.Write([]byte("not-json"))
					return
				case "oversized":
					w.Write([]byte(strings.Repeat("x", 1024*1024+1)))
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"resultCount": len(results), "results": results})
			}))
			defer server.Close()
			a := NewAlbumArtworkResolver(server.Client(), server.URL, "KR")
			got, err := a.Resolve(context.Background(), "Artist", "My Song")
			accept := name == "exact" || name == "unicode-case" || name == "punctuation"
			fail := name == "429" || name == "500" || name == "malformed" || name == "oversized"
			if (got != nil) != accept || (err != nil) != fail {
				t.Fatalf("got=%v err=%v", got, err)
			}
			if calls != 1 {
				t.Fatal(calls)
			}
			if accept {
				got.AlbumTitle = "tamper"
				again, _ := a.Resolve(context.Background(), "artist", "MY SONG")
				if calls != 1 || again.AlbumTitle != "Album" {
					t.Fatal("cache mutated")
				}
			}
		})
	}
}
func TestArtworkCacheExpiryCapacityAndNegative(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"resultCount": 0, "results": []any{}})
	}))
	defer server.Close()
	a := NewAlbumArtworkResolver(server.Client(), server.URL, "KR")
	clock := time.Now()
	a.now = func() time.Time { return clock }
	a.Resolve(context.Background(), "A", "T")
	a.Resolve(context.Background(), "A", "T")
	if calls.Load() != 1 {
		t.Fatal("negative cache missed")
	}
	clock = clock.Add(ArtworkNegativeTTL)
	a.Resolve(context.Background(), "A", "T")
	if calls.Load() != 2 {
		t.Fatal("negative cache did not expire")
	}
	a.cache = map[string]artworkEntry{}
	for i := 0; i < ArtworkCacheCapacity; i++ {
		a.cache[string(rune(i))] = artworkEntry{expires: clock.Add(time.Hour)}
	}
	a.Resolve(context.Background(), "B", "T")
	if len(a.cache) > ArtworkCacheCapacity {
		t.Fatal("unbounded cache")
	}
	snapshot := a.Snapshot()
	if snapshot.CacheHits != 1 || snapshot.CacheMisses != 3 {
		t.Fatal(snapshot)
	}
}
func TestArtworkTimeoutCancellationAndSingleFlight(t *testing.T) {
	var calls atomic.Int32
	wait := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-wait:
			json.NewEncoder(w).Encode(map[string]any{"resultCount": 0, "results": []any{}})
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	a := NewAlbumArtworkResolver(server.Client(), server.URL, "KR")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Resolve(ctx, "A", "T"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("canceled call sent HTTP")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := a.Resolve(ctx, "A", "T"); err == nil {
		t.Fatal("timeout ignored")
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a.Resolve(context.Background(), "B", "T") }()
	}
	time.Sleep(20 * time.Millisecond)
	close(wait)
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatal("same-key duplicate HTTP", calls.Load())
	}
}
func TestArtworkPositiveTTLAndRateBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"resultCount": 1, "results": []any{itunesItem("A", "T")}})
	}))
	defer server.Close()
	a := NewAlbumArtworkResolver(server.Client(), server.URL, "KR")
	clock := time.Now()
	a.now = func() time.Time { return clock }
	a.Resolve(context.Background(), "A", "T")
	clock = clock.Add(ArtworkPositiveTTL)
	a.Resolve(context.Background(), "A", "T")
	if a.Snapshot().HTTPCalls != 2 {
		t.Fatal("positive TTL")
	}
	a.mu.Lock()
	a.calls = 20
	a.window = clock
	a.mu.Unlock()
	if _, err := a.Resolve(context.Background(), "B", "T"); !errors.Is(err, ErrArtworkProvider) {
		t.Fatal("unbounded provider rate", err)
	}
}
