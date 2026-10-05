package service

import (
	"context"
	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
	"example.com/sync/internal/recommendation"
	"fmt"
	"log"
	"sort"
	"time"
)

type CandidateSignalTrace struct {
	Sources          []recommendation.PlannedQuery    `json:"sources"`
	Content          recommendation.ContentDiagnostic `json:"content_diagnostic"`
	VideoID          string                           `json:"video_id"`
	Source           recommendation.PlannedQuery      `json:"source"`
	HardEligible     bool                             `json:"hard_eligible"`
	HardReasons      []string                         `json:"hard_filter_reasons"`
	Release          recommendation.ReleaseEvidence   `json:"release"`
	ReleaseAllowed   bool                             `json:"release_allowed"`
	QualityAllowed   bool                             `json:"quality_allowed"`
	VocalAllowed     bool                             `json:"vocal_allowed"`
	Language         recommendation.LanguageEvidence  `json:"language"`
	PopularityBucket string                           `json:"popularity_bucket"`
	Mood             recommendation.MoodAudit         `json:"mood_audit"`
}

func WithNeutralRetrieval(policy recommendation.NeutralPolicy, mixedKeyword string) RecommendationOption {
	return func(s *RecommendationService) { s.neutralPolicy = &policy; s.mixedKeyword = mixedKeyword }
}
func median(values []float64) float64 {
	sort.Float64s(values)
	if len(values) == 0 {
		return 0
	}
	i := len(values) / 2
	if len(values)%2 == 1 {
		return values[i]
	}
	return (values[i-1] + values[i]) / 2
}
func evaluateStrength(raw int, signals []CandidateSignalTrace, videos map[string]model.YouTubeVideo, p model.MusicPreferences) recommendation.CandidateStrength {
	out := recommendation.CandidateStrength{Raw: raw, ReleaseCounts: map[string]int{"HIGH": 0, "MEDIUM": 0, "LOW": 0, "UNKNOWN": 0}, AffinityCounts: map[string]int{}, LanguageCounts: map[string]int{"ko": 0, "en": 0, "ja": 0, "other": 0, "unknown": 0}, PreferredLanguages: append([]string{}, p.Languages...)}
	channels := map[string]bool{}
	views, likes := []float64{}, []float64{}
	for _, signal := range signals {
		if !signal.HardEligible {
			continue
		}
		out.HardEligible++
		out.ReleaseCounts[signal.Release.Confidence]++
		if signal.Release.Confidence == "HIGH" || signal.Release.Confidence == "MEDIUM" {
			out.ReleaseTrusted++
		}
		if !signal.ReleaseAllowed || !signal.QualityAllowed || !signal.VocalAllowed {
			continue
		}
		out.PoolCount++
		v := videos[signal.VideoID]
		views = append(views, float64(v.ViewCount))
		if v.LikeCount != nil {
			likes = append(likes, float64(*v.LikeCount))
		}
		switch signal.PopularityBucket {
		case "discovery":
			out.Discovery++
		case "medium":
			out.Medium++
		case "established":
			out.Established++
		}
		key := v.ChannelID
		if key == "" {
			key = v.ChannelTitle
		}
		if key != "" {
			channels[key] = true
		}
		out.AffinityCounts[signal.Language.Affinity]++
		coverage := signal.Language.CoverageLanguage
		if !signal.Language.CoverageEligible {
			coverage = "unknown"
		}
		out.LanguageCounts[coverage]++
		if signal.Language.TextLanguage.Value != "unknown" && signal.Language.AudioLanguage.Value == "unknown" {
			out.TextOnlyCount++
		}
		if signal.Language.AudioLanguage.Value != "unknown" {
			out.AudioEvidenceCount++
		}
		if recommendation.VocalKind(v) == "instrumental" {
			out.InstrumentalCoverageExcluded++
		}
		if coverage != "ko" && coverage != "en" && coverage != "ja" && coverage != "unknown" {
			out.LanguageCounts["other"]++
		}
		switch recommendation.VocalKind(v) {
		case "vocal":
			out.VocalFriendly++
		case "instrumental":
			out.Instrumental++
		default:
			out.VocalUnknown++
		}
	}
	out.UniqueChannels = len(channels)
	out.MedianViews = median(views)
	if len(likes) > 0 {
		value := median(likes)
		out.MedianLikes = &value
	}
	return out
}

// Neutral retrieval never invokes the injected text generator. It consumes the
// existing structured analysis and performs at most two bounded searches.
func (s *RecommendationService) recommendNeutral(ctx context.Context, req model.RecommendationRequest) (*model.RecommendationResponse, error) {
	if err := req.Analysis.Validate(); err != nil {
		return nil, fmt.Errorf("invalid analysis")
	}
	if err := req.Preferences.Validate(); err != nil {
		return nil, err
	}
	if s.queryCount < 1 || s.timeout <= 0 || s.neutralPolicy.Validate() != nil {
		return nil, client.ErrMusicSearch
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	metrics := &client.RetrievalMetrics{}
	ctx = client.WithRetrievalMetrics(ctx, metrics)
	trace, _ := ctx.Value(traceKey{}).(*RecommendationTrace)
	if trace == nil {
		trace = &RecommendationTrace{}
	}
	start := time.Now()
	defer func() {
		trace.Retrieval = *metrics
		trace.TotalMS = float64(time.Since(start)) / float64(time.Millisecond)
		metadataCalls, hardEligible := 0, 0
		for _, stage := range trace.Stages {
			metadataCalls += stage.MetadataCalls
		}
		for _, signal := range trace.CandidateSignals {
			if signal.HardEligible {
				hardEligible++
			}
		}
		log.Printf("neutral_retrieval searches=%d metadata_batches=%d raw=%d hard_eligible=%d release_candidates=%d returned=%d q2_role=%s latency_ms=%.2f", metrics.SearchCalls, metadataCalls, trace.RawCount, hardEligible, len(trace.Candidates), len(trace.Ranked), trace.AdaptiveDecision.SelectedRole, trace.TotalMS)
	}()
	t := time.Now()
	plan, err := recommendation.PlanNeutral(req.Analysis, req.Preferences, int64(s.searchMaxResults), s.mixedKeyword)
	trace.QueryMS = float64(time.Since(t)) / float64(time.Millisecond)
	if err != nil {
		return nil, err
	}
	trace.Plan = &plan
	next := &plan.Q1
	sourcesByID := map[string][]recommendation.PlannedQuery{}
	seen := map[string]bool{}
	ranks := map[string]int{}
	videosByID := map[string]model.YouTubeVideo{}
	candidates := []recommendation.Candidate{}
	for index := 0; index < min(2, s.queryCount) && next != nil; index++ {
		q := *next
		trace.SearchQueries = append(trace.SearchQueries, q.Query)
		trace.GeneratedQueries = append(trace.GeneratedQueries, q.Query)
		trace.QueriesGenerated++
		trace.Stages = append(trace.Stages, RetrievalStageTrace{QueryIndex: index + 1, Query: q.Query, PlannedQuery: &q})
		stage := &trace.Stages[len(trace.Stages)-1]
		before := *metrics
		if index == 1 {
			trace.SecondQueryExecuted = true
		}
		searchLanguage := ""
		if q.Language != nil {
			searchLanguage = *q.Language
		}
		t = time.Now()
		hits, err := s.music.SearchMusic(ctx, model.MusicSearchQuery{Text: q.Query, Region: s.region, RelevanceLanguage: searchLanguage, Order: q.Order, MaxResults: q.MaxResults})
		stage.SearchMS = float64(time.Since(t)) / float64(time.Millisecond)
		trace.SearchMS += stage.SearchMS
		stage.CacheHits = metrics.CacheHits - before.CacheHits
		stage.CacheMisses = metrics.CacheMisses - before.CacheMisses
		stage.SearchCalls = metrics.SearchCalls - before.SearchCalls
		if err != nil {
			return nil, err
		}
		ids := []string{}
		for i, hit := range hits[:min(len(hits), s.searchMaxResults)] {
			stage.RawIDs = append(stage.RawIDs, hit.VideoID)
			trace.RawCount++
			if hit.VideoID == "" {
				continue
			}
			if rank, ok := ranks[hit.VideoID]; !ok || i < rank {
				ranks[hit.VideoID] = i
			}
			present := false
			for _, source := range sourcesByID[hit.VideoID] {
				if source.Query == q.Query && source.Order == q.Order {
					present = true
					break
				}
			}
			if !present {
				sourcesByID[hit.VideoID] = append(sourcesByID[hit.VideoID], q)
			}
			if seen[hit.VideoID] {
				continue
			}
			seen[hit.VideoID] = true
			ids = append(ids, hit.VideoID)
		}
		stage.NewMetadataIDs = append([]string{}, ids...)
		stage.DeduplicatedTotal = len(seen)
		for offset := 0; offset < len(ids); offset += 50 {
			batch := ids[offset:min(offset+50, len(ids))]
			wanted := map[string]bool{}
			for _, id := range batch {
				wanted[id] = true
			}
			t = time.Now()
			stage.MetadataCalls++
			metadata, err := s.music.GetVideos(ctx, batch)
			elapsed := float64(time.Since(t)) / float64(time.Millisecond)
			trace.MetadataMS += elapsed
			stage.MetadataMS += elapsed
			if err != nil {
				return nil, err
			}
			for _, v := range metadata {
				if !wanted[v.VideoID] {
					continue
				}
				delete(wanted, v.VideoID)
				videosByID[v.VideoID] = v
				trace.Videos = append(trace.Videos, v)
				language := ""
				if q.Language != nil {
					language = *q.Language
				}
				signal := CandidateSignalTrace{Sources: append([]recommendation.PlannedQuery{}, sourcesByID[v.VideoID]...), Content: recommendation.DiagnoseContent(v), VideoID: v.VideoID, Source: q, HardEligible: eligibleVideo(v, req.Preferences, s.region), HardReasons: recommendation.SongFormReasons(v), Release: recommendation.ReleaseConfidence(v), Language: recommendation.LanguageAffinity(v, q.Genre, language), PopularityBucket: recommendation.PopularityBucket(v.ViewCount), Mood: recommendation.AuditMood(v, ranks[v.VideoID], req.Analysis, req.Preferences)}
				for _, check := range ExplainVideoEligibility(v, req.Preferences, s.region) {
					if !check.Passed && check.Stage != "song_form" {
						signal.HardReasons = append(signal.HardReasons, check.Stage)
					}
				}
				signal.ReleaseAllowed = s.neutralPolicy.AllowsRelease(signal.Release.Confidence)
				signal.QualityAllowed = !recommendation.FailsQualityGate(v)
				signal.VocalAllowed = req.Preferences.EffectiveVocalMode() != "instrumental-only" || recommendation.VocalKind(v) == "instrumental"
				trace.CandidateSignals = append(trace.CandidateSignals, signal)
				if !signal.HardEligible {
					trace.EligibilityFiltered++
					continue
				}
				if !signal.ReleaseAllowed || !signal.VocalAllowed {
					continue
				}
				// Quality gate remains inside V2 too; this same predicate is used for
				// retrieval strength, without altering its thresholds or scoring.
				if !signal.QualityAllowed {
					continue
				}
				candidates = append(candidates, recommendation.Candidate{Video: v, SearchRank: ranks[v.VideoID]})
			}
		}
		for i := range candidates {
			candidates[i].SearchRank = ranks[candidates[i].Video.VideoID]
		}
		for i := range trace.CandidateSignals {
			v := videosByID[trace.CandidateSignals[i].VideoID]
			trace.CandidateSignals[i].Sources = append([]recommendation.PlannedQuery{}, sourcesByID[v.VideoID]...)
			trace.CandidateSignals[i].Mood = recommendation.AuditMood(v, ranks[v.VideoID], req.Analysis, req.Preferences)
		}
		strength := evaluateStrength(trace.RawCount, trace.CandidateSignals, videosByID, req.Preferences)
		stage.Strength = &strength
		decisionStart := time.Now()
		decision, follow := recommendation.DecideNeutral(plan, strength, req.Preferences, *s.neutralPolicy, s.safetyMargin, s.mixedKeyword)
		trace.QueryMS += float64(time.Since(decisionStart)) / float64(time.Millisecond)
		stage.DecisionReasons = append([]string{}, decision.Reasons...)
		stage.DecisionReason = "all_candidate_strength_axes_sufficient"
		if len(decision.Reasons) > 0 {
			stage.DecisionReason = decision.Reasons[0]
		}
		if index == 0 {
			trace.AdaptiveDecision = decision
			if s.queryCount < 2 && follow != nil {
				trace.AdaptiveDecision.Q2Executed = false
				trace.AdaptiveDecision.Explanation += "; query count budget exhausted"
				follow = nil
			}
			plan.Q2 = follow
			if follow == nil {
				trace.SecondQuerySkipped = true
			}
			next = follow
		} else {
			next = nil
			trace.FinalStrength = &strength
		}
		trace.Candidates = candidates
	}
	if len(trace.Stages) > 0 && trace.FinalStrength == nil {
		trace.FinalStrength = trace.Stages[len(trace.Stages)-1].Strength
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	t = time.Now()
	poolPrefs := req.Preferences
	poolPrefs.CandidatePoolLimit = len(candidates)
	pool := s.ranker(candidates, req.Analysis, poolPrefs)
	trace.BeforeVocalMix = append([]recommendation.RankedCandidate{}, pool...)
	mixStart := time.Now()
	trace.Ranked = recommendation.SelectVocalMix(pool, req.Preferences)
	trace.VocalMixMS = float64(time.Since(mixStart)) / float64(time.Millisecond)
	trace.RankingMS = float64(time.Since(t)) / float64(time.Millisecond)
	response := &model.RecommendationResponse{Tracks: []model.RecommendedTrack{}, RequestedCount: req.Preferences.Count}
	if source, ok := s.music.(interface{ DataMode() string }); ok {
		response.DataMode = source.DataMode()
	}
	for _, item := range trace.Ranked {
		track := item.Track
		if response.DataMode == "fixture" {
			track.YouTubeURL = ""
		}
		response.Tracks = append(response.Tracks, track)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	response.ReturnedCount = len(response.Tracks)
	response.Partial = response.ReturnedCount < response.RequestedCount
	return response, nil
}
