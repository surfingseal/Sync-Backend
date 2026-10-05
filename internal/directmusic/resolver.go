package directmusic

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
)

const ResolverVersion = "exact_metadata_v2.2"
const LegacyResolverVersion = "exact_metadata_v1"

var ErrSearchBudget = errors.New("resolver search budget exhausted")

type Config struct {
	CandidateCount int           `json:"candidate_count"`
	FinalCount     int           `json:"final_count"`
	MaxPerArtist   int           `json:"max_per_artist"`
	MaxSearchCalls int           `json:"max_search_calls_per_photo"`
	SearchResults  int64         `json:"search_results_per_candidate"`
	Region         string        `json:"region"`
	MinDuration    int64         `json:"min_duration_seconds"`
	MaxDuration    int64         `json:"max_duration_seconds"`
	EarlyStop      bool          `json:"early_stop"`
	PositiveTTL    time.Duration `json:"positive_cache_ttl_ns"`
	NegativeTTL    time.Duration `json:"negative_cache_ttl_ns"`
	RequestTimeout time.Duration `json:"youtube_request_timeout_ns"`
}

func DefaultConfig() Config {
	return Config{20, 10, 2, 20, 5, "KR", 90, 720, true, 7 * 24 * time.Hour, 10 * time.Minute, 15 * time.Second}
}
func (c Config) Validate() error {
	if c.CandidateCount < 1 || c.CandidateCount > 20 || c.FinalCount < 1 || c.FinalCount > 10 || c.MaxPerArtist < 1 || c.MaxPerArtist > 2 || c.MaxSearchCalls < 1 || c.MaxSearchCalls > 20 || c.SearchResults < 1 || c.SearchResults > 10 || len(c.Region) != 2 || c.MinDuration < 1 || c.MaxDuration < c.MinDuration || c.PositiveTTL <= 0 || c.NegativeTTL <= 0 || c.RequestTimeout <= 0 {
		return fmt.Errorf("invalid direct experiment configuration")
	}
	return nil
}

type CallStats struct {
	SearchCalls        int     `json:"search_list_calls"`
	VideosCalls        int     `json:"videos_list_calls"`
	CacheHits          int     `json:"cache_hits"`
	CacheMisses        int     `json:"cache_misses"`
	SearchMS           float64 `json:"search_ms"`
	MetadataMS         float64 `json:"metadata_ms"`
	PrimaryCalls       int     `json:"primary_search_calls"`
	FallbackCalls      int     `json:"fallback_search_calls"`
	PrimaryMS          float64 `json:"primary_search_ms"`
	FallbackMS         float64 `json:"fallback_search_ms"`
	PositiveMigrations int     `json:"positive_cache_migrations"`
}
type Evidence struct {
	IdentityScore         float64  `json:"identity_score"`
	IdentityConfidence    float64  `json:"identity_confidence"`
	ArtistMatch           string   `json:"artist_match"`
	TitleMatch            string   `json:"title_match"`
	VersionMatch          string   `json:"version_match"`
	Officiality           string   `json:"officiality"`
	OfficialityConfidence string   `json:"officiality_confidence"`
	Playable              bool     `json:"playable"`
	Signals               []string `json:"signals"`
	Caveat                string   `json:"caveat"`
}
type VideoCheck struct {
	VideoID  string              `json:"video_id"`
	Title    string              `json:"title"`
	Channel  string              `json:"channel"`
	Accepted bool                `json:"accepted"`
	Reason   string              `json:"reason,omitempty"`
	Evidence Evidence            `json:"evidence"`
	Metadata *model.YouTubeVideo `json:"metadata,omitempty"`
}
type Resolution struct {
	Candidate model.DirectTrack   `json:"candidate"`
	Status    string              `json:"status"`
	Reason    string              `json:"reason,omitempty"`
	Attempted bool                `json:"attempted"`
	Query     string              `json:"query,omitempty"`
	CacheHit  bool                `json:"cache_hit"`
	Checks    []VideoCheck        `json:"video_checks,omitempty"`
	Video     *model.YouTubeVideo `json:"video,omitempty"`
	Evidence  *Evidence           `json:"evidence,omitempty"`
	LatencyMS float64             `json:"latency_ms"`
	Searches  []SearchAttempt     `json:"searches"`
}
type Resolver struct {
	Client  client.MusicSearchClient
	Cache   *Cache
	Config  Config
	Stats   CallStats
	Aliases []TitleAlias
}

func (r *Resolver) metadata(ctx context.Context, ids []string) ([]model.YouTubeVideo, error) {
	ctx, cancel := context.WithTimeout(ctx, r.Config.RequestTimeout)
	defer cancel()
	start := time.Now()
	r.Stats.VideosCalls++
	defer func() { r.Stats.MetadataMS += float64(time.Since(start)) / float64(time.Millisecond) }()
	return r.Client.GetVideos(ctx, ids)
}

type SearchAttempt struct {
	Kind          string  `json:"kind"`
	Query         string  `json:"query"`
	Calls         int     `json:"calls"`
	RawCount      int     `json:"raw_count"`
	LatencyMS     float64 `json:"latency_ms"`
	AcceptedCount int     `json:"accepted_count"`
}

// Resolve tries one primary identity query and only then one token query.
// API errors never trigger a fallback or negative-cache entry.
func (r *Resolver) Resolve(ctx context.Context, t model.DirectTrack) (out Resolution, err error) {
	start := time.Now()
	out = Resolution{Candidate: t, Status: "UNRESOLVED_IDENTITY", Attempted: true, Searches: []SearchAttempt{}}
	defer func() {
		out.LatencyMS = float64(time.Since(start)) / float64(time.Millisecond)
		if err != nil && !errors.Is(err, ErrSearchBudget) {
			out.Status = "API_FAILURE"
			out.Reason = errorCode(err)
		}
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	key := r.candidateCacheKey(t.Artist, t.Title)
	entry, hit := r.Cache.Get(key)
	legacyHit := false
	if !hit {
		for _, version := range []string{"exact_metadata_v2.1", "exact_metadata_v2", LegacyResolverVersion} {
			if old, ok := r.Cache.Get(policyCacheKey(t.Artist, t.Title, r.Config, version)); ok && !old.Negative {
				entry = old
				hit = true
				legacyHit = true
				break
			}
		}
	}
	if hit {
		r.Stats.CacheHits++
		out.CacheHit = true
		if entry.Negative {
			out.Status = entry.Status
			if out.Status == "" {
				out.Status = "UNRESOLVED_NO_CANDIDATE"
			}
			out.Reason = "NEGATIVE_CACHE"
			return
		}
		var videos []model.YouTubeVideo
		videos, err = r.metadata(ctx, []string{entry.VideoID})
		if err != nil {
			return
		}
		for _, v := range videos {
			e, reason := CheckIdentityWithAliases(t, v, r.Config, r.Aliases)
			out.Checks = append(out.Checks, videoCheck(v, e, reason))
			if reason == "" {
				if legacyHit {
					r.Stats.PositiveMigrations++
				}
				out.Status = ResolvedStatus(e)
				out.Video = &v
				out.Evidence = &e
				_ = r.Cache.Put(key, CacheEntry{VideoID: v.VideoID, Expires: time.Now().Add(r.Config.PositiveTTL)})
				return
			}
		}
	} else {
		r.Stats.CacheMisses++
	}
	primary := IdentityPrimaryQuery(t)
	fallback := IdentityTokenQuery(t)
	known := map[string]model.YouTubeVideo{}
	queried := map[string]bool{}
	rawFound := false
	versionFailure := false
	for i, query := range []string{primary, fallback} {
		if r.Stats.SearchCalls >= r.Config.MaxSearchCalls {
			if i == 0 {
				out.Attempted = false
				out.Status = "NOT_ATTEMPTED"
			}
			out.Reason = "SEARCH_BUDGET_EXHAUSTED"
			return out, ErrSearchBudget
		}
		ctxSearch, cancel := context.WithTimeout(ctx, r.Config.RequestTimeout)
		s := time.Now()
		r.Stats.SearchCalls++
		kind := "primary"
		if i == 0 {
			r.Stats.PrimaryCalls++
		} else {
			kind = "fallback"
			r.Stats.FallbackCalls++
		}
		results, searchErr := r.Client.SearchMusic(ctxSearch, model.MusicSearchQuery{Text: query, Region: r.Config.Region, Order: "relevance", MaxResults: r.Config.SearchResults})
		cancel()
		ms := float64(time.Since(s)) / float64(time.Millisecond)
		r.Stats.SearchMS += ms
		if i == 0 {
			r.Stats.PrimaryMS += ms
		} else {
			r.Stats.FallbackMS += ms
		}
		out.Searches = append(out.Searches, SearchAttempt{Kind: kind, Query: query, Calls: 1, RawCount: len(results), LatencyMS: ms})
		if i == 0 {
			out.Query = query
		}
		if searchErr != nil {
			return out, searchErr
		}
		rawFound = rawFound || len(results) > 0
		ids := []string{}
		seen := map[string]bool{}
		for _, v := range results {
			if v.VideoID != "" && !seen[v.VideoID] {
				seen[v.VideoID] = true
				if !queried[v.VideoID] {
					ids = append(ids, v.VideoID)
				}
			}
		}
		if len(ids) > 0 {
			for _, id := range ids {
				queried[id] = true
			}
			var videos []model.YouTubeVideo
			videos, err = r.metadata(ctx, ids)
			if err != nil {
				return
			}
			for _, v := range videos {
				if seen[v.VideoID] {
					known[v.VideoID] = v
				}
			}
		}
		// Preserve provider rank when identity evidence ties, independently of map order.
		type accepted struct {
			v model.YouTubeVideo
			e Evidence
		}
		matches := []accepted{}
		for _, result := range results {
			v, ok := known[result.VideoID]
			if !ok {
				continue
			}
			e, reason := CheckIdentityWithAliases(t, v, r.Config, r.Aliases)
			out.Checks = append(out.Checks, videoCheck(v, e, reason))
			versionFailure = versionFailure || reason == "UNREQUESTED_VERSION"
			if reason == "" {
				matches = append(matches, accepted{v, e})
			}
		}
		out.Searches[len(out.Searches)-1].AcceptedCount = len(matches)
		if len(matches) > 0 {
			sort.SliceStable(matches, func(i, j int) bool {
				if matches[i].e.IdentityConfidence != matches[j].e.IdentityConfidence {
					return matches[i].e.IdentityConfidence > matches[j].e.IdentityConfidence
				}
				return officialPriority(matches[i].e) > officialPriority(matches[j].e)
			})
			out.Status = ResolvedStatus(matches[0].e)
			out.Video = &matches[0].v
			out.Evidence = &matches[0].e
			if cacheErr := r.Cache.Put(key, CacheEntry{VideoID: out.Video.VideoID, Expires: time.Now().Add(r.Config.PositiveTTL)}); cacheErr != nil {
				out.Reason = "CACHE_WRITE_FAILED"
			}
			return
		}
	}
	out.Reason = "NO_IDENTITY_MATCH"
	if !rawFound {
		out.Status = "UNRESOLVED_NO_CANDIDATE"
		out.Reason = "NO_SEARCH_CANDIDATES"
	} else if versionFailure {
		out.Status = "UNRESOLVED_VERSION"
	} else {
		out.Status = "UNRESOLVED_IDENTITY"
	}
	if cacheErr := r.Cache.Put(key, CacheEntry{Negative: true, Status: out.Status, Expires: time.Now().Add(r.Config.NegativeTTL)}); cacheErr != nil {
		out.Reason = "CACHE_WRITE_FAILED"
	}
	return
}
func videoCheck(v model.YouTubeVideo, e Evidence, reason string) VideoCheck {
	return VideoCheck{VideoID: v.VideoID, Title: v.Title, Channel: v.ChannelTitle, Accepted: reason == "", Reason: reason, Evidence: e, Metadata: &v}
}
func officialPriority(e Evidence) int {
	switch e.Officiality {
	case "topic":
		return 5
	case "vevo":
		return 4
	case "licensed":
		return 3
	case "official_artist":
		return 2
	case "ordinary_channel":
		return 1
	default:
		return 0
	}
}

func errorCode(e error) string {
	switch {
	case errors.Is(e, client.ErrYouTubeQuota):
		return "YOUTUBE_QUOTA_EXCEEDED"
	case errors.Is(e, context.DeadlineExceeded):
		return "TIMEOUT"
	case errors.Is(e, context.Canceled):
		return "CONTEXT_CANCELLED"
	case errors.Is(e, ErrSearchBudget):
		return "SEARCH_BUDGET_EXHAUSTED"
	default:
		return "YOUTUBE_RESOLUTION_ERROR"
	}
}

func IdentityPrimaryQuery(t model.DirectTrack) string {
	quote := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, " ") + `"` }
	return quote(t.Artist) + " " + quote(t.Title)
}
func IdentityTokenQuery(t model.DirectTrack) string {
	return matchKey(t.Artist) + " " + matchKey(t.Title)
}
