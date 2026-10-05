package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"example.com/sync/internal/model"
	"golang.org/x/text/unicode/norm"
)

const (
	ArtworkPositiveTTL    = 30 * 24 * time.Hour
	ArtworkNegativeTTL    = 24 * time.Hour
	ArtworkCacheCapacity  = 1024
	ArtworkRequestTimeout = 1500 * time.Millisecond
	DefaultArtworkCountry = "US" // catalog context, never lyric language evidence
)

var ErrArtworkProvider = errors.New("artwork provider unavailable")

type artworkEntry struct {
	value   *model.AlbumArtwork
	expires time.Time
}
type artworkFlight struct{ done chan struct{} }
type ArtworkStats struct{ Lookups, CacheHits, CacheMisses, HTTPCalls, Failures int64 }

// AlbumArtworkResolver is shared across requests; metadata-only bounded cache,
// same-key single flight and a conservative global ~20/minute HTTP budget.
type AlbumArtworkResolver struct {
	http             *http.Client
	baseURL, country string
	mu               sync.Mutex
	cache            map[string]artworkEntry
	flights          map[string]*artworkFlight
	stats            ArtworkStats
	now              func() time.Time
	window           time.Time
	calls            int
}

func NewAlbumArtworkResolver(httpClient *http.Client, baseURL, country string) *AlbumArtworkResolver {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: ArtworkRequestTimeout}
	}
	if baseURL == "" {
		baseURL = "https://itunes.apple.com/search"
	}
	if country == "" {
		country = DefaultArtworkCountry
	}
	return &AlbumArtworkResolver{http: httpClient, baseURL: baseURL, country: country, cache: map[string]artworkEntry{}, flights: map[string]*artworkFlight{}, now: time.Now}
}
func artworkKey(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, norm.NFKC.String(s))), " ")
}
func artworkPair(artist, title string) string {
	b, _ := json.Marshal([]string{artworkKey(artist), artworkKey(title)})
	return string(b)
}
func (a *AlbumArtworkResolver) Snapshot() ArtworkStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stats
}
func cloneArtwork(v *model.AlbumArtwork) *model.AlbumArtwork {
	if v == nil {
		return nil
	}
	copy := *v
	return &copy
}
func (a *AlbumArtworkResolver) Resolve(ctx context.Context, artist, title string) (*model.AlbumArtwork, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.resolve(ctx, artist, title, "")
}

// ResolveWithArtistEvidence accepts only an already identity-verified YouTube title.
// Evidence is request-scoped, not a learned/global artist alias registry.
func (a *AlbumArtworkResolver) ResolveWithArtistEvidence(ctx context.Context, artist, title, verifiedTitle string) (*model.AlbumArtwork, error) {
	return a.resolve(ctx, artist, title, verifiedTitle)
}

func (a *AlbumArtworkResolver) resolve(ctx context.Context, artist, title, verifiedTitle string) (*model.AlbumArtwork, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := artworkPair(artist, title)
	if verifiedTitle != "" {
		b, _ := json.Marshal([]string{key, verifiedTitle})
		key = string(b)
	}
	if artworkKey(artist) == "" || artworkKey(title) == "" {
		return nil, nil
	}
	a.mu.Lock()
	a.stats.Lookups++
	a.mu.Unlock()
	for {
		a.mu.Lock()
		now := a.now()
		if e, ok := a.cache[key]; ok && now.Before(e.expires) {
			a.stats.CacheHits++
			a.mu.Unlock()
			return cloneArtwork(e.value), nil
		}
		if flight, ok := a.flights[key]; ok {
			a.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-flight.done:
				continue
			}
		}
		if len(a.flights) >= 4 {
			a.stats.Failures++
			a.mu.Unlock()
			return nil, ErrArtworkProvider
		}
		if a.window.IsZero() || now.Sub(a.window) >= time.Minute {
			a.window = now
			a.calls = 0
		}
		if a.calls >= 20 {
			a.stats.Failures++
			a.mu.Unlock()
			return nil, ErrArtworkProvider
		}
		flight := &artworkFlight{done: make(chan struct{})}
		a.flights[key] = flight
		a.calls++
		a.stats.CacheMisses++
		a.stats.HTTPCalls++
		a.mu.Unlock()
		value, err := a.lookup(ctx, artist, title, verifiedTitle)
		a.mu.Lock()
		// Transient errors/cancellation are not negative cached for an entire day.
		if err == nil {
			for k, e := range a.cache {
				if !a.now().Before(e.expires) {
					delete(a.cache, k)
				}
			}
			if len(a.cache) >= ArtworkCacheCapacity {
				var oldestKey string
				var oldest time.Time
				for k, e := range a.cache {
					if oldestKey == "" || e.expires.Before(oldest) {
						oldest = e.expires
						oldestKey = k
					}
				}
				delete(a.cache, oldestKey)
			}
			ttl := ArtworkPositiveTTL
			if value == nil {
				ttl = ArtworkNegativeTTL
			}
			a.cache[key] = artworkEntry{cloneArtwork(value), a.now().Add(ttl)}
		} else {
			a.stats.Failures++
		}
		delete(a.flights, key)
		close(flight.done)
		a.mu.Unlock()
		return value, err
	}
}
func (a *AlbumArtworkResolver) lookup(ctx context.Context, artist, title, verifiedTitle string) (*model.AlbumArtwork, error) {
	ctx, cancel := context.WithTimeout(ctx, ArtworkRequestTimeout)
	defer cancel()
	u, err := url.Parse(a.baseURL)
	if err != nil || u.Host == "" {
		return nil, ErrArtworkProvider
	}
	q := u.Query()
	q.Set("term", artist+" "+title)
	q.Set("media", "music")
	q.Set("entity", "song")
	q.Set("country", a.country)
	q.Set("limit", "10")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, ErrArtworkProvider
	}
	req.Header.Set("User-Agent", "Sync-Backend/1.0 (album-artwork)")
	req.Header.Set("Accept", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: HTTP %d", ErrArtworkProvider, resp.StatusCode)
	}
	const maxBody = 1024 * 1024
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(b) > maxBody {
		return nil, ErrArtworkProvider
	}
	// Explicit DTO names follow the actual Apple response, not inferred fields.
	var wire struct {
		Count   *int `json:"resultCount"`
		Results []struct {
			Wrapper string `json:"wrapperType"`
			Kind    string `json:"kind"`
			Artist  string `json:"artistName"`
			Title   string `json:"trackName"`
			Album   string `json:"collectionName"`
			URL     string `json:"artworkUrl100"`
		} `json:"results"`
	}
	if json.Unmarshal(b, &wire) != nil || wire.Count == nil || *wire.Count < 0 || *wire.Count != len(wire.Results) {
		return nil, ErrArtworkProvider
	}
	var match *model.AlbumArtwork
	for _, r := range wire.Results {
		if r.Wrapper != "track" || r.Kind != "song" || !artworkArtistCompatible(artist, r.Artist, title, verifiedTitle) || artworkKey(r.Title) != artworkKey(title) || strings.TrimSpace(r.Album) == "" || artworkConflict.MatchString(r.Artist+" "+r.Title+" "+r.Album) {
			continue
		}
		// No version stripping: cover/remix/live qualifiers must match the requested
		// title exactly after only Unicode/case/punctuation normalization.
		parsed, e := url.Parse(r.URL)
		if e != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" || !(strings.HasSuffix(parsed.Hostname(), ".mzstatic.com") || strings.HasSuffix(parsed.Hostname(), ".itunes.apple.com")) {
			continue
		}
		candidate := &model.AlbumArtwork{AlbumTitle: r.Album, URL: r.URL, Source: "itunes"}
		if match != nil && (match.AlbumTitle != candidate.AlbumTitle || match.URL != candidate.URL) {
			return nil, nil
		} // ambiguous releases: no invented canonical album
		match = candidate
	}
	return match, nil
}

// Only explicit bilingual equivalence is admitted. Same-title/rank/album alone
// never establishes equivalence between two unrelated artist names.
var artworkBilingual = regexp.MustCompile(`^([^()]+)\(([^()]+)\)\s*(.*)$`)
var artworkMV = regexp.MustCompile(`(?i)\s*(?:m/v|mv|official music video|official video)\s*$`)
var artworkConflict = regexp.MustCompile(`(?i)\b(?:cover|tribute|karaoke|instrumental|remix|live|demo|nightcore|sped up|slowed)\b`)

func artworkNameScript(s string) int {
	script := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			next := 0
			if unicode.In(r, unicode.Latin) {
				next = 1
			} else if unicode.In(r, unicode.Hangul) {
				next = 2
			} else {
				return 0
			}
			if script != 0 && script != next {
				return 0
			}
			script = next
		} else if !unicode.IsSpace(r) {
			return 0
		}
	}
	return script
}
func artworkArtistCompatible(requested, provider, title, verifiedTitle string) bool {
	if artworkKey(requested) == artworkKey(provider) {
		return true
	}
	// The provider itself may explicitly express both artist names.
	for _, pair := range []struct {
		text         string
		requireTitle bool
	}{{provider, false}, {verifiedTitle, true}} {
		m := artworkBilingual.FindStringSubmatch(strings.TrimSpace(norm.NFKC.String(pair.text)))
		if m == nil {
			continue
		}
		left, right := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
		a, b := artworkNameScript(left), artworkNameScript(right)
		if a == 0 || b == 0 || a == b {
			continue
		}
		if pair.requireTitle {
			remainder := artworkMV.ReplaceAllString(strings.TrimSpace(m[3]), "")
			if artworkKey(remainder) != artworkKey(title) {
				continue
			}
		} else if strings.TrimSpace(m[3]) != "" {
			continue
		}
		req, prov := artworkKey(requested), artworkKey(provider)
		l, rr := artworkKey(left), artworkKey(right)
		if pair.requireTitle && ((req == l && prov == rr) || (req == rr && prov == l)) {
			return true
		}
		if !pair.requireTitle && (req == l || req == rr) {
			return true
		}
	}
	return false
}
