package directmusic

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
)

const MVPCandidateCount = 12
const MVPFinalCount = 5
const MVPRequiredKoreanTracks = 2
const MVPMaxSearchCalls = 10

type VerifiedTrack struct {
	Gemini             model.DirectTrack `json:"gemini"`
	VideoID            string            `json:"video_id"`
	YouTubeURL         string            `json:"youtube_url"`
	VideoTitle         string            `json:"video_title"`
	Channel            string            `json:"channel"`
	DurationSeconds    int64             `json:"duration_seconds"`
	ResolutionEvidence Evidence          `json:"resolution_evidence"`
}
type Diagnostics struct {
	PromptVersion           string              `json:"prompt_version"`
	SchemaVersion           string              `json:"schema_version"`
	ResolverVersion         string              `json:"resolver_version"`
	Generated               int                 `json:"generated_count"`
	Normalized              int                 `json:"normalized_count"`
	Duplicates              int                 `json:"duplicate_count"`
	Blank                   int                 `json:"blank_count"`
	Attempted               int                 `json:"resolver_attempted_count"`
	Verified                int                 `json:"resolver_verified_count"`
	Unresolved              int                 `json:"unresolved_count"`
	Unattempted             int                 `json:"unattempted_count"`
	GeneratedNotVerified    int                 `json:"generated_not_verified_count"`
	SuccessRate             *float64            `json:"verification_success_rate"`
	UnresolvedRate          *float64            `json:"unresolved_rate_attempted"`
	AverageCallsPerVerified *float64            `json:"average_youtube_calls_per_verified"`
	FinalCount              int                 `json:"final_count"`
	UniqueArtists           int                 `json:"unique_artists"`
	SameArtistMax           int                 `json:"same_artist_max"`
	AverageFitScore         *float64            `json:"average_fit_score,omitempty"`
	Partial                 bool                `json:"partial"`
	Warnings                []string            `json:"warnings"`
	ErrorCode               string              `json:"error_code,omitempty"`
	GeminiMS                float64             `json:"gemini_ms"`
	ResolverMS              float64             `json:"resolver_ms"`
	TotalMS                 float64             `json:"total_ms"`
	LastFMCalls             int                 `json:"lastfm_calls"`
	PlaylistWrites          int                 `json:"playlist_writes"`
	Calls                   CallStats           `json:"youtube_calls"`
	Vertex                  client.CallSnapshot `json:"vertex_call"`
	APIFailures             int                 `json:"api_failure_count"`
}
type Result struct {
	Generated   *model.DirectMusicRecommendation `json:"generated"`
	Normalized  []model.DirectTrack              `json:"normalized"`
	Resolutions []Resolution                     `json:"resolutions"`
	Final       []VerifiedTrack                  `json:"final"`
	Diagnostics Diagnostics                      `json:"diagnostics"`
}
type Service struct {
	Generator client.DirectTrackRecommender
	Resolver  *Resolver
	Config    Config
}

// RunMVP uses twelve candidates and at most ten searches; final selection happens at the API boundary.
// Historical experiment Run keeps its configuration and diversity behavior.
func (s *Service) RunMVP(ctx context.Context, image []byte, mime string) (*Result, error) {
	copy := *s
	copy.Config.CandidateCount = MVPCandidateCount
	copy.Config.FinalCount = MVPFinalCount
	copy.Config.EarlyStop = true
	copy.Config.MaxSearchCalls = min(copy.Config.MaxSearchCalls, MVPMaxSearchCalls)
	if s.Resolver != nil {
		resolver := *s.Resolver
		resolver.Config.MaxSearchCalls = min(resolver.Config.MaxSearchCalls, copy.Config.MaxSearchCalls)
		resolver.Stats = CallStats{}
		copy.Resolver = &resolver
	}
	return copy.run(ctx, image, mime, true)
}
func (s *Service) Run(ctx context.Context, image []byte, mime string) (*Result, error) {
	return s.run(ctx, image, mime, false)
}
func (s *Service) run(ctx context.Context, image []byte, mime string, mvp bool) (result *Result, resultErr error) {
	if err := s.Config.Validate(); err != nil {
		return nil, err
	}
	result = &Result{Normalized: []model.DirectTrack{}, Resolutions: []Resolution{}, Final: []VerifiedTrack{}, Diagnostics: Diagnostics{PromptVersion: client.DirectPromptVersion, SchemaVersion: model.DirectSchemaVersion, ResolverVersion: ResolverVersion, Warnings: []string{}}}
	start := time.Now()
	d := &result.Diagnostics
	defer func() {
		d.TotalMS = float64(time.Since(start)) / float64(time.Millisecond)
		d.Calls = s.Resolver.Stats
		d.GeneratedNotVerified = d.Generated - d.Verified
		d.FinalCount = len(result.Final)
		d.Partial = d.FinalCount < s.Config.FinalCount
		if d.Partial {
			d.Warnings = append(d.Warnings, "insufficient_verified_tracks")
		}
		if d.Attempted > 0 {
			success := float64(d.Verified) / float64(d.Attempted)
			failure := float64(d.Unresolved) / float64(d.Attempted)
			d.SuccessRate = &success
			d.UnresolvedRate = &failure
		}
		if d.Verified > 0 {
			calls := float64(d.Calls.SearchCalls+d.Calls.VideosCalls) / float64(d.Verified)
			d.AverageCallsPerVerified = &calls
		}
		artists := map[string]int{}
		sum := 0.0
		for _, t := range result.Final {
			artists[NormalizeKey(t.Gemini.Artist)]++
			sum += t.Gemini.FitScore
		}
		d.UniqueArtists = len(artists)
		for _, n := range artists {
			if n > d.SameArtistMax {
				d.SameArtistMax = n
			}
		}
		if len(result.Final) > 0 {
			avg := sum / float64(len(result.Final))
			d.AverageFitScore = &avg
		}
	}()
	callCtx, m := client.MeasureCall(ctx)
	gstart := time.Now()
	generated, err := s.Generator.RecommendTracks(callCtx, image, mime, s.Config.CandidateCount)
	d.GeminiMS = float64(time.Since(gstart)) / float64(time.Millisecond)
	d.Vertex = m.Snapshot()
	if err != nil {
		d.ErrorCode = "GEMINI_DIRECT_FAILED"
		return result, err
	}
	// Revalidate injected providers too; they cannot bypass structured field rules.
	if generated == nil || len(generated.Tracks) == 0 || len(generated.Tracks) > s.Config.CandidateCount {
		d.ErrorCode = "INVALID_GEMINI_RESULT"
		return result, client.ErrInvalidAIResponse
	}
	for _, t := range generated.Tracks {
		if math.IsNaN(t.FitScore) || math.IsInf(t.FitScore, 0) || t.FitScore < 0 || t.FitScore > 1 {
			d.ErrorCode = "INVALID_GEMINI_RESULT"
			return result, client.ErrInvalidAIResponse
		}
	}
	result.Generated = generated
	d.Generated = len(generated.Tracks)
	result.Normalized, d.Duplicates, d.Blank = NormalizeCandidates(generated.Tracks)
	d.Normalized = len(result.Normalized)
	artists := map[string]int{}
	videos := map[string]bool{}
	stopReason := ""
	rstart := time.Now()
	defer func() { d.ResolverMS = float64(time.Since(rstart)) / float64(time.Millisecond) }()
	for _, candidate := range result.Normalized {
		if stopReason != "" {
			result.Resolutions = append(result.Resolutions, Resolution{Candidate: candidate, Status: "NOT_ATTEMPTED", Reason: stopReason})
			d.Unattempted++
			continue
		}
		if s.Config.EarlyStop && ((!mvp && len(result.Final) >= s.Config.FinalCount) || (mvp && mvpSelectionReady(result.Final, s.Config.MaxPerArtist))) {
			stopReason = "FINAL_TARGET_REACHED"
			result.Resolutions = append(result.Resolutions, Resolution{Candidate: candidate, Status: "NOT_ATTEMPTED", Reason: stopReason})
			d.Unattempted++
			continue
		}
		if mvp && !candidate.LyricLanguage.Allowed() {
			result.Resolutions = append(result.Resolutions, Resolution{Candidate: candidate, Status: "NOT_ATTEMPTED", Reason: "MVP_LANGUAGE_INELIGIBLE"})
			d.Unattempted++
			continue
		}
		artist := NormalizeKey(candidate.Artist)
		if !mvp && artists[artist] >= s.Config.MaxPerArtist {
			result.Resolutions = append(result.Resolutions, Resolution{Candidate: candidate, Status: "NOT_ATTEMPTED", Reason: "ARTIST_DIVERSITY_LIMIT"})
			d.Unattempted++
			continue
		}
		resolution, resolveErr := s.Resolver.Resolve(ctx, candidate)
		result.Resolutions = append(result.Resolutions, resolution)
		if resolution.Attempted {
			d.Attempted++
			if IsResolved(resolution.Status) {
				d.Verified++
			} else if resolution.Status == "API_FAILURE" {
				d.APIFailures++
			} else {
				d.Unresolved++
			}
		} else {
			d.Unattempted++
		}
		if resolveErr != nil {
			d.Warnings = append(d.Warnings, errorCode(resolveErr))
			if errors.Is(resolveErr, client.ErrYouTubeQuota) || errors.Is(resolveErr, context.DeadlineExceeded) || errors.Is(resolveErr, context.Canceled) || errors.Is(resolveErr, ErrSearchBudget) {
				stopReason = errorCode(resolveErr)
				d.ErrorCode = stopReason
				resultErr = resolveErr
			}
			continue
		}
		if resolution.Video != nil && IsResolved(resolution.Status) {
			if videos[resolution.Video.VideoID] {
				d.Warnings = append(d.Warnings, "duplicate_resolved_video")
				continue
			}
			if !mvp && len(result.Final) >= s.Config.FinalCount {
				continue
			}
			v := resolution.Video
			videos[v.VideoID] = true
			artists[artist]++
			result.Final = append(result.Final, VerifiedTrack{candidate, v.VideoID, "https://www.youtube.com/watch?v=" + v.VideoID, v.Title, v.ChannelTitle, v.DurationSeconds, *resolution.Evidence})
		}
	}
	if len(result.Normalized) == 0 {
		d.ErrorCode = "EMPTY_NORMALIZED_CANDIDATES"
		return result, fmt.Errorf("no nonblank candidates")
	}
	return result, resultErr
}

// Respect artist capacity when deciding whether the final policy can select five
// unique verified tracks including two Korean-eligible tracks. The pool already
// excludes duplicate video IDs and ineligible languages.
func mvpSelectionReady(pool []VerifiedTrack, maxPerArtist int) bool {
	counts, korean := map[string]int{}, map[string]int{}
	for _, track := range pool {
		if !track.Gemini.LyricLanguage.Allowed() {
			continue
		}
		artist := NormalizeKey(track.Gemini.Artist)
		counts[artist]++
		if track.Gemini.LyricLanguage.KoreanEligible() {
			korean[artist]++
		}
	}
	total, ko := 0, 0
	for artist, count := range counts {
		total += min(count, maxPerArtist)
		ko += min(korean[artist], maxPerArtist)
	}
	return total >= MVPFinalCount && ko >= MVPRequiredKoreanTracks
}
