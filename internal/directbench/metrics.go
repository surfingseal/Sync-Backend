package directbench

import (
	"math"
	"sort"

	"example.com/sync/internal/directmusic"
)

type Sample struct {
	ID           string                  `json:"image_id"`
	Diagnostics  directmusic.Diagnostics `json:"diagnostics"`
	PreprocessMS float64                 `json:"preprocess_ms"`
	PipelineMS   float64                 `json:"pipeline_ms"`
	Outcomes     map[string]int          `json:"outcomes"`
	Safety       Safety                  `json:"safety"`
}
type Safety struct {
	FalsePositives  int `json:"wrong_identity_false_positives"`
	Diversity       int `json:"diversity_violations"`
	Broad           int `json:"broad_search_violations"`
	Substitute      int `json:"substitute_violations"`
	LastFM          int `json:"lastfm_calls"`
	APIValid        int `json:"api_failure_valid_track_violations"`
	Bounds          int `json:"resource_bound_violations"`
	FixtureFailures int `json:"fixture_failures"`
}
type Aggregate struct {
	Images                int         `json:"executed_images"`
	CompleteRate          *float64    `json:"final_10_rate"`
	EightRate             *float64    `json:"final_ge8_rate"`
	BelowEightRate        *float64    `json:"final_lt8_rate"`
	AvgFinal              *float64    `json:"average_final_count"`
	MedianFinal           *float64    `json:"median_final_count"`
	Verification          *float64    `json:"attempted_verification_success_rate"`
	Unresolved            *float64    `json:"unresolved_rate"`
	APIError              *float64    `json:"api_failure_rate"`
	AvgGenerated          *float64    `json:"average_gemini_candidates"`
	AvgAttempted          *float64    `json:"average_attempted_candidates"`
	AvgArtists            *float64    `json:"average_unique_artists"`
	ArtistMaxDistribution map[int]int `json:"same_artist_max_distribution"`
	AvgSearch             *float64    `json:"search_calls_per_image"`
	AvgVideos             *float64    `json:"videos_calls_per_image"`
	AvgHTTP               *float64    `json:"youtube_http_calls_per_image"`
	SearchPerTrack        *float64    `json:"search_calls_per_final_track"`
	CallsPerTrack         *float64    `json:"youtube_calls_per_final_track"`
	FinalTotal            int         `json:"final_total"`
	SearchCalls           int         `json:"search_calls"`
	VideosCalls           int         `json:"videos_calls"`
	PrimaryCalls          int         `json:"primary_calls"`
	FallbackCalls         int         `json:"fallback_calls"`
	CacheHits             int         `json:"cache_hits"`
	CacheMisses           int         `json:"cache_misses"`
	P50                   *float64    `json:"pipeline_p50_ms"`
	P90                   *float64    `json:"pipeline_p90_ms"`
	P95                   *float64    `json:"pipeline_p95_ms"`
	Max                   *float64    `json:"pipeline_max_ms"`
	GeminiP50             *float64    `json:"gemini_p50_ms"`
	GeminiP95             *float64    `json:"gemini_p95_ms"`
	ResolverP50           *float64    `json:"resolver_p50_ms"`
	ResolverP95           *float64    `json:"resolver_p95_ms"`
	Safety                Safety      `json:"safety"`
}

func fraction(n, d int) *float64 {
	if d == 0 {
		return nil
	}
	v := float64(n) / float64(d)
	return &v
}

// Nearest-rank quantiles are deterministic; missing samples are null, not zero.
func Quantile(values []float64, q float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	v := append([]float64{}, values...)
	sort.Float64s(v)
	i := int(math.Ceil(q*float64(len(v)))) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(v) {
		i = len(v) - 1
	}
	x := v[i]
	return &x
}
func Summarize(samples []Sample, safety Safety) Aggregate {
	a := Aggregate{Images: len(samples), ArtistMaxDistribution: map[int]int{}, Safety: safety}
	complete, eight, generated, attempted, verified, unresolved, api, artists := 0, 0, 0, 0, 0, 0, 0, 0
	finals, total, gemini, resolver := []float64{}, []float64{}, []float64{}, []float64{}
	for _, s := range samples {
		d := s.Diagnostics
		n := d.FinalCount
		if n == 10 {
			complete++
		}
		if n >= 8 {
			eight++
		}
		a.FinalTotal += n
		generated += d.Generated
		attempted += d.Attempted
		verified += d.Verified
		unresolved += d.Unresolved
		api += d.APIFailures
		artists += d.UniqueArtists
		a.ArtistMaxDistribution[d.SameArtistMax]++
		a.SearchCalls += d.Calls.SearchCalls
		a.VideosCalls += d.Calls.VideosCalls
		a.PrimaryCalls += d.Calls.PrimaryCalls
		a.FallbackCalls += d.Calls.FallbackCalls
		a.CacheHits += d.Calls.CacheHits
		a.CacheMisses += d.Calls.CacheMisses
		a.Safety.Diversity += s.Safety.Diversity
		a.Safety.Broad += s.Safety.Broad
		a.Safety.Substitute += s.Safety.Substitute
		a.Safety.LastFM += d.LastFMCalls
		a.Safety.APIValid += s.Safety.APIValid
		a.Safety.Bounds += s.Safety.Bounds
		a.Safety.FalsePositives += s.Safety.FalsePositives
		a.Safety.FixtureFailures += s.Safety.FixtureFailures
		finals = append(finals, float64(n))
		total = append(total, s.PipelineMS)
		gemini = append(gemini, d.GeminiMS)
		resolver = append(resolver, d.ResolverMS)
	}
	a.CompleteRate = fraction(complete, a.Images)
	a.EightRate = fraction(eight, a.Images)
	a.BelowEightRate = fraction(a.Images-eight, a.Images)
	a.AvgFinal = fraction(a.FinalTotal, a.Images)
	a.MedianFinal = Quantile(finals, .5)
	a.Verification = fraction(verified, attempted)
	a.Unresolved = fraction(unresolved, attempted)
	a.APIError = fraction(api, attempted)
	a.AvgGenerated = fraction(generated, a.Images)
	a.AvgAttempted = fraction(attempted, a.Images)
	a.AvgArtists = fraction(artists, a.Images)
	a.AvgSearch = fraction(a.SearchCalls, a.Images)
	a.AvgVideos = fraction(a.VideosCalls, a.Images)
	a.AvgHTTP = fraction(a.SearchCalls+a.VideosCalls, a.Images)
	a.SearchPerTrack = fraction(a.SearchCalls, a.FinalTotal)
	a.CallsPerTrack = fraction(a.SearchCalls+a.VideosCalls, a.FinalTotal)
	a.P50 = Quantile(total, .5)
	a.P90 = Quantile(total, .9)
	a.P95 = Quantile(total, .95)
	a.Max = Quantile(total, 1)
	a.GeminiP50 = Quantile(gemini, .5)
	a.GeminiP95 = Quantile(gemini, .95)
	a.ResolverP50 = Quantile(resolver, .5)
	a.ResolverP95 = Quantile(resolver, .95)
	return a
}

type Gate struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}
type Decision struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons"`
	Gates   []Gate   `json:"gates"`
}
type Gates struct {
	MinImages    int     `json:"min_unique_executed_images"`
	EightRate    float64 `json:"ge8_min"`
	TenRate      float64 `json:"ten_min"`
	AvgTracks    float64 `json:"average_tracks_min"`
	Verification float64 `json:"verification_min"`
	APIError     float64 `json:"api_failure_max"`
	P50MS        float64 `json:"pipeline_p50_max_ms"`
	P95MS        float64 `json:"pipeline_p95_max_ms"`
}

func DefaultGates() Gates { return Gates{20, .95, .8, 9, .8, .02, 20000, 30000} }
func Evaluate(a Aggregate, dataset int, cold bool, g Gates) Decision {
	d := Decision{Status: "READY_FOR_PRIMARY", Reasons: []string{}, Gates: []Gate{}}
	add := func(name string, pass bool, reason string, hard bool) {
		status := "PASS"
		if !pass {
			status = "FAIL"
			d.Reasons = append(d.Reasons, reason)
			if hard {
				d.Status = "BLOCKED"
			} else if d.Status != "BLOCKED" {
				d.Status = "NEEDS_MORE_TECHNICAL_VALIDATION"
			}
		}
		d.Gates = append(d.Gates, Gate{name, status, reason})
	}
	add("safety", a.Safety.FalsePositives+a.Safety.Broad+a.Safety.Substitute+a.Safety.LastFM+a.Safety.APIValid+a.Safety.FixtureFailures == 0, "safety_regression", true)
	add("dataset", dataset >= g.MinImages && a.Images >= g.MinImages, "insufficient_unique_live_benchmark_images", false)
	add("cold_cache", cold, "primary_benchmark_requires_cold_cache", false)
	add("coverage", a.EightRate != nil && a.CompleteRate != nil && a.AvgFinal != nil && *a.EightRate >= g.EightRate && *a.CompleteRate >= g.TenRate && *a.AvgFinal >= g.AvgTracks, "coverage_insufficient", false)
	add("resolver", a.Verification != nil && a.APIError != nil && *a.Verification >= g.Verification && *a.APIError <= g.APIError, "resolver_or_api_reliability_insufficient", false)
	add("diversity", a.Safety.Diversity == 0, "diversity_invariant_failed", true)
	add("latency", a.P50 != nil && a.P95 != nil && *a.P50 <= g.P50MS && *a.P95 <= g.P95MS, "latency_insufficient", false)
	add("bounded_resources", a.Safety.Bounds == 0, "resource_bounds_failed", true)
	return d
}
