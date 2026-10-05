package lastfmeval

import (
	"context"
	"example.com/sync/internal/lastfm"
	"fmt"
	"golang.org/x/text/unicode/norm"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

type FetchResult struct {
	Tracks []lastfm.Track
	Events []lastfm.Event
}
type Retriever interface {
	Fetch(context.Context, string, int) (FetchResult, error)
}

// Each call owns a Last.fm client/event slice; shared http.Client is concurrency-safe.
// The existing audit client remains unchanged and is never concurrently reused.
type Provider struct {
	APIKey, CacheDir string
	Fresh            bool
	Timeout          time.Duration
	HTTP             *http.Client
}

func (p Provider) Fetch(ctx context.Context, tag string, limit int) (FetchResult, error) {
	c := lastfm.New(p.APIKey)
	c.CacheDir = p.CacheDir
	c.Fresh = p.Fresh
	if p.Timeout > 0 {
		c.Timeout = p.Timeout
	}
	if p.HTTP != nil {
		c.HTTP = p.HTTP
	}
	tracks, e := c.GetTopTracks(ctx, tag, limit)
	return FetchResult{tracks, c.Events}, e
}

type TagRouteEvidence struct {
	CanonicalConcept string  `json:"canonical_concept"`
	LastFMTag        string  `json:"lastfm_tag"`
	Category         string  `json:"category"`
	Rank             int     `json:"rank"`
	RouteWeight      float64 `json:"route_weight"`
}
type RawCandidate struct {
	Track    lastfm.Track     `json:"provider_track"`
	Evidence TagRouteEvidence `json:"evidence"`
}
type Contribution struct {
	Tag           string  `json:"lastfm_tag"`
	Rank          int     `json:"rank"`
	RankScore     float64 `json:"rank_score"`
	WeightedScore float64 `json:"weighted_score"`
}
type CandidateTrack struct {
	Retrieval        *CandidateRetrievalEvidence `json:"retrieval_provenance,omitempty"`
	Artist           string                      `json:"artist"`
	Title            string                      `json:"title"`
	MBID             string                      `json:"mbid,omitempty"`
	URL              string                      `json:"url"`
	Evidences        []TagRouteEvidence          `json:"evidences"`
	RetrievalScore   float64                     `json:"retrieval_score"`
	NormalizedArtist string                      `json:"normalized_artist"`
	NormalizedTitle  string                      `json:"normalized_title"`
	ProviderRecords  []lastfm.Track              `json:"provider_records"`
	Contributions    []Contribution              `json:"score_contributions"`
	OverlapBonus     float64                     `json:"overlap_bonus"`
	RawRank          int                         `json:"raw_rank"`
	DiversityRank    int                         `json:"diversity_rank,omitempty"`
}

// Typography only: preserve punctuation boundaries, parentheses, featuring,
// remaster and version labels instead of destructively stripping them.
func NormalizeTrack(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		switch r {
		case '’', '‘', 'ʼ':
			return '\''
		case '“', '”':
			return '"'
		case '–', '—', '−':
			return '-'
		}
		if unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, norm.NFKC.String(s))), " "))
}
func Merge(raw []RawCandidate, policy Policy) ([]CandidateTrack, []string, int) {
	parent := make([]int, len(raw))
	for i := range parent {
		parent[i] = i
	}
	var root func(int) int
	root = func(i int) int {
		if parent[i] != i {
			parent[i] = root(parent[i])
		}
		return parent[i]
	}
	union := func(i, j int) {
		a, b := root(i), root(j)
		if a != b {
			if a < b {
				parent[b] = a
			} else {
				parent[a] = b
			}
		}
	}
	keys, mbids := map[string]int{}, map[string]int{}
	valid := make([]bool, len(raw))
	warnings := []string{}
	invalid := 0
	for i, r := range raw {
		artist, title := NormalizeTrack(r.Track.Artist.Name), NormalizeTrack(r.Track.Name)
		if artist == "" || title == "" {
			invalid++
			continue
		}
		valid[i] = true
		key := artist + "\x00" + title
		if j, ok := keys[key]; ok {
			union(i, j)
		} else {
			keys[key] = i
		}
		id := strings.ToLower(strings.TrimSpace(r.Track.MBID))
		if id != "" {
			if j, ok := mbids[id]; ok {
				union(i, j)
			} else {
				mbids[id] = i
			}
		}
	}
	out := []CandidateTrack{}
	groups := map[int]int{}
	for i, r := range raw {
		if !valid[i] {
			continue
		}
		id := root(i)
		idx, ok := groups[id]
		if !ok {
			idx = len(out)
			groups[id] = idx
			out = append(out, CandidateTrack{Artist: r.Track.Artist.Name, Title: r.Track.Name, MBID: r.Track.MBID, URL: r.Track.URL, NormalizedArtist: NormalizeTrack(r.Track.Artist.Name), NormalizedTitle: NormalizeTrack(r.Track.Name), Evidences: []TagRouteEvidence{}, ProviderRecords: []lastfm.Track{}})
		}
		c := &out[idx]
		c.ProviderRecords = append(c.ProviderRecords, r.Track)
		c.Evidences = append(c.Evidences, r.Evidence)
		if c.MBID == "" {
			c.MBID = r.Track.MBID
		}
	}
	for i := range out {
		c := &out[i]
		pairs := map[string]bool{}
		for _, r := range c.ProviderRecords {
			pairs[NormalizeTrack(r.Artist.Name)+"\x00"+NormalizeTrack(r.Name)] = true
		}
		if len(pairs) > 1 {
			warnings = append(warnings, "same-MBID merge joined differing artist/title labels; provider records retained: "+c.Artist+" — "+c.Title)
		}
		Score(c, policy)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RetrievalScore != out[j].RetrievalScore {
			return out[i].RetrievalScore > out[j].RetrievalScore
		}
		a, b := out[i].NormalizedArtist+"\x00"+out[i].NormalizedTitle, out[j].NormalizedArtist+"\x00"+out[j].NormalizedTitle
		return a < b
	})
	for i := range out {
		out[i].RawRank = i + 1
	}
	if invalid > 0 {
		warnings = append(warnings, fmt.Sprintf("%d raw records missing artist/title omitted from merge", invalid))
	}
	return out, warnings, invalid
}

// Frozen experiment policy: maximum route contribution + small distinct-route
// overlap bonus, capped at 1.0. Do not mistake this for popularity or final fit.
func Score(c *CandidateTrack, policy Policy) {
	best := map[string]TagRouteEvidence{}
	for _, e := range c.Evidences {
		key := lastfm.Normalize(e.LastFMTag)
		if old, ok := best[key]; !ok || e.Rank < old.Rank {
			best[key] = e
		}
	}
	c.Contributions = []Contribution{}
	max := 0.0
	keys := []string{}
	for k := range best {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		e := best[key]
		rank := e.Rank
		if rank < 1 {
			rank = 1
		}
		rs := 1 / math.Log2(float64(rank)+1)
		s := e.RouteWeight * rs
		if s > max {
			max = s
		}
		c.Contributions = append(c.Contributions, Contribution{e.LastFMTag, rank, rs, s})
	}
	c.OverlapBonus = math.Min(.15, policy.OverlapBonus*float64(maxInt(0, len(best)-1)))
	c.RetrievalScore = math.Min(1, max+c.OverlapBonus)
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func Diversity(ranked []CandidateTrack, top, cap int) ([]CandidateTrack, []CandidateTrack) {
	selected, deferred := []CandidateTrack{}, []CandidateTrack{}
	counts := map[string]int{}
	for _, c := range ranked {
		if len(selected) < top && counts[c.NormalizedArtist] < cap {
			c.DiversityRank = len(selected) + 1
			selected = append(selected, c)
			counts[c.NormalizedArtist]++
		} else {
			deferred = append(deferred, c)
		}
	}
	return selected, deferred
}

type ArtistStats struct {
	TrackCount    int            `json:"track_count"`
	UniqueArtists int            `json:"unique_artist_count"`
	Top1Share     float64        `json:"top1_artist_share"`
	Top3Share     float64        `json:"top3_artist_share"`
	MaxTracks     int            `json:"max_tracks_per_artist"`
	Counts        map[string]int `json:"artist_counts"`
}

func Concentration(tracks []CandidateTrack) ArtistStats {
	s := ArtistStats{TrackCount: len(tracks), Counts: map[string]int{}}
	for _, c := range tracks {
		s.Counts[c.NormalizedArtist]++
	}
	s.UniqueArtists = len(s.Counts)
	counts := []int{}
	for _, n := range s.Counts {
		counts = append(counts, n)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(counts)))
	if len(counts) > 0 {
		s.MaxTracks = counts[0]
		s.Top1Share = float64(counts[0]) / float64(len(tracks))
		total := 0
		for i, n := range counts {
			if i < 3 {
				total += n
			}
		}
		s.Top3Share = float64(total) / float64(len(tracks))
	}
	return s
}

type RouteResult struct {
	Route       RetrievalRoute `json:"route"`
	RawCount    int            `json:"raw_count"`
	LatencyMS   float64        `json:"latency_ms"`
	Events      []lastfm.Event `json:"requests"`
	Error       string         `json:"error,omitempty"`
	ArtistStats ArtistStats    `json:"artist_concentration"`
}
type Evaluation struct {
	CoverageState   string           `json:"coverage_state,omitempty"`
	DataMode        string           `json:"data_mode,omitempty"`
	Plan            Plan             `json:"plan"`
	Policy          Policy           `json:"policy"`
	RouteResults    []RouteResult    `json:"route_results"`
	Raw             []RawCandidate   `json:"raw_candidates"`
	Merged          []CandidateTrack `json:"merged_candidates"`
	Reranked        []CandidateTrack `json:"diversity_top_candidates"`
	Deferred        []CandidateTrack `json:"deferred_candidates"`
	Warnings        []string         `json:"warnings"`
	TotalMS         float64          `json:"total_ms"`
	MappingMS       float64          `json:"mapping_ms"`
	RetrievalMS     float64          `json:"retrieval_ms"`
	MergeRankMS     float64          `json:"merge_rank_ms"`
	DiversityMS     float64          `json:"diversity_ms"`
	APICalls        int              `json:"api_calls"`
	CacheHits       int              `json:"cache_hits"`
	CacheMisses     int              `json:"cache_misses"`
	DuplicateMerges int              `json:"duplicate_merges"`
	InvalidRaw      int              `json:"invalid_raw_records"`
	OverlapCount    int              `json:"multi_route_candidates"`
	MBIDMissing     int              `json:"mbid_missing_candidates"`
	Before          ArtistStats      `json:"raw_top_artist_stats"`
	After           ArtistStats      `json:"diversity_top_artist_stats"`
	Partial         bool             `json:"partial"`
	Outcome         string           `json:"outcome"`
	HumanQuality    string           `json:"human_quality"`
}

func Evaluate(ctx context.Context, plan Plan, p Policy, retriever Retriever) (*Evaluation, error) {
	start := time.Now()
	e := &Evaluation{Plan: plan, Policy: p, Raw: []RawCandidate{}, Merged: []CandidateTrack{}, Reranked: []CandidateTrack{}, Deferred: []CandidateTrack{}, Warnings: append([]string{}, plan.Warnings...), Outcome: "pending", HumanQuality: "HUMAN REVIEW PENDING"}
	if plan.Coverage != nil {
		e.CoverageState = plan.Coverage.State
	}
	if err := p.Validate(); err != nil {
		return e, err
	}
	if len(plan.Routes) > p.MaxRoutes {
		return e, fmt.Errorf("route budget exceeded")
	}
	if len(plan.Routes) == 0 {
		e.Outcome = "insufficient_validated_mapping"
		e.TotalMS = float64(time.Since(start)) / float64(time.Millisecond)
		return e, nil
	}
	// Recheck externally supplied plans before making any API request.
	seen := map[string]bool{}
	for _, r := range plan.Routes {
		key := lastfm.Normalize(r.LastFMTag)
		if key == "" || seen[key] || !finiteWeight(r.ProfileWeight) {
			return e, fmt.Errorf("invalid/duplicate route")
		}
		seen[key] = true
	}
	e.RouteResults = make([]RouteResult, len(plan.Routes))
	fetched := make([]FetchResult, len(plan.Routes))
	failures := make([]error, len(plan.Routes))
	sem := make(chan struct{}, p.Concurrency)
	var wg sync.WaitGroup
	for i, r := range plan.Routes {
		wg.Add(1)
		go func(i int, r RetrievalRoute) {
			defer wg.Done()
			rr := RouteResult{Route: r}
			t := time.Now()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				failures[i] = ctx.Err()
				rr.Error = "context canceled or deadline exceeded"
				e.RouteResults[i] = rr
				return
			}
			result, err := retriever.Fetch(ctx, r.LastFMTag, p.PerRouteLimit)
			fetched[i] = result
			failures[i] = err
			rr.LatencyMS = float64(time.Since(t)) / float64(time.Millisecond)
			rr.Events = result.Events
			rr.RawCount = len(result.Tracks)
			if err != nil {
				rr.Error = safeError(err)
			}
			e.RouteResults[i] = rr
		}(i, r)
	}
	wg.Wait()
	e.RetrievalMS = float64(time.Since(start)) / float64(time.Millisecond)
	successful := 0
	for i, r := range plan.Routes {
		for _, event := range fetched[i].Events {
			if event.Cached {
				e.CacheHits++
			} else if event.HTTPAttempted {
				e.APICalls++
				e.CacheMisses++
			}
		}
		if failures[i] != nil {
			e.Partial = true
			e.Warnings = append(e.Warnings, "route failed: "+r.LastFMTag+"; "+e.RouteResults[i].Error)
			continue
		}
		successful++
		routeCandidates := []CandidateTrack{}
		for j, track := range fetched[i].Tracks {
			rank := j + 1
			if n, err := strconv.Atoi(track.Attr.Rank); err == nil && n > 0 {
				rank = n
			}
			e.Raw = append(e.Raw, RawCandidate{track, TagRouteEvidence{r.CanonicalConcept, r.LastFMTag, r.Category, rank, r.ProfileWeight}})
			routeCandidates = append(routeCandidates, CandidateTrack{NormalizedArtist: NormalizeTrack(track.Artist.Name)})
		}
		e.RouteResults[i].ArtistStats = Concentration(routeCandidates)
		if len(routeCandidates) > 0 && e.RouteResults[i].ArtistStats.Top1Share >= .4 {
			e.Warnings = append(e.Warnings, "route artist concentration >=40%: "+r.LastFMTag)
		}
	}
	if successful == 0 {
		e.Outcome = "all_routes_failed"
		e.TotalMS = float64(time.Since(start)) / float64(time.Millisecond)
		return e, fmt.Errorf("all Last.fm retrieval routes failed")
	}
	t := time.Now()
	var warnings []string
	e.Merged, warnings, e.InvalidRaw = Merge(e.Raw, p)
	attachCandidateProvenance(e.Merged, plan, p)
	e.Warnings = append(e.Warnings, warnings...)
	e.DuplicateMerges = len(e.Raw) - e.InvalidRaw - len(e.Merged)
	for _, c := range e.Merged {
		routes := map[string]bool{}
		for _, v := range c.Evidences {
			routes[lastfm.Normalize(v.LastFMTag)] = true
		}
		if len(routes) > 1 {
			e.OverlapCount++
		}
		if c.MBID == "" {
			e.MBIDMissing++
		}
	}
	e.MergeRankMS = float64(time.Since(t)) / float64(time.Millisecond)
	t = time.Now()
	e.Reranked, e.Deferred = Diversity(e.Merged, p.TopN, p.ArtistCap)
	e.DiversityMS = float64(time.Since(t)) / float64(time.Millisecond)
	top := e.Merged
	if len(top) > p.TopN {
		top = top[:p.TopN]
	}
	e.Before = Concentration(top)
	e.After = Concentration(e.Reranked)
	if e.OverlapCount == 0 {
		e.Warnings = append(e.Warnings, "no multi-route overlap; merged pool does not imply joint genre/mood agreement")
	}
	if len(e.Reranked) < p.TopN {
		e.Warnings = append(e.Warnings, "fewer than requested diverse candidates; artist cap not relaxed")
	}
	e.Outcome = "candidate_pool_collected"
	if e.Partial {
		e.Outcome = "partial_candidate_pool"
	}
	e.TotalMS = float64(time.Since(start)) / float64(time.Millisecond)
	return e, nil
}
func finiteWeight(w float64) bool { return !math.IsNaN(w) && !math.IsInf(w, 0) && w >= 0 && w <= 1 }

// Never include provider URL/API key/raw body in experiment errors.
func safeError(err error) string {
	if e, ok := err.(*lastfm.Error); ok {
		return e.Error()
	}
	return "retrieval failed or context canceled"
}

// Reserved extension boundary; not used by this experiment.
type TrackEnricher interface {
	Enrich(context.Context, []CandidateTrack) ([]CandidateTrack, error)
}
