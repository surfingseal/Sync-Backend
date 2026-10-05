package service

import (
	"context"
	"errors"
	"example.com/sync/internal/recommendation"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"

	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
)

const (
	MinTrackDurationSeconds int64 = 90
	MaxTrackDurationSeconds int64 = 720
	// ProductionQueryTimeout bounds text query generation within the 15s
	// recommendation budget. Benchmarks reference it without calling Recommend.
	ProductionQueryTimeout     = 6 * time.Second
	recommendationQueryTimeout = ProductionQueryTimeout
)

type RecommendationService struct {
	neutralPolicy             *recommendation.NeutralPolicy
	mixedKeyword              string
	ranker                    RankFunc
	generator                 client.MusicQueryGenerator
	music                     client.MusicSearchClient
	region, relevanceLanguage string
	queryCount                int
	timeout                   time.Duration
	safetyMargin              int
	searchMaxResults          int
}

func NewRecommendationService(generator client.MusicQueryGenerator, music client.MusicSearchClient, region, language string, queries int, timeout time.Duration, options ...RecommendationOption) *RecommendationService {
	s := &RecommendationService{searchMaxResults: 50, safetyMargin: 3, ranker: recommendation.Rank, generator: generator, music: music, region: region, relevanceLanguage: language, queryCount: queries, timeout: timeout}
	for _, option := range options {
		option(s)
	}
	return s
}
func (s *RecommendationService) Recommend(ctx context.Context, req model.RecommendationRequest) (*model.RecommendationResponse, error) {
	if s.neutralPolicy != nil {
		return s.recommendNeutral(ctx, req)
	}
	if err := req.Analysis.Validate(); err != nil {
		return nil, fmt.Errorf("invalid analysis")
	}
	if err := req.Preferences.Validate(); err != nil {
		return nil, err
	}
	if s.queryCount < 1 || s.queryCount > 3 || s.timeout <= 0 || s.searchMaxResults < 1 || s.searchMaxResults > 50 {
		return nil, client.ErrMusicSearch
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	metrics := &client.RetrievalMetrics{}
	ctx = client.WithRetrievalMetrics(ctx, metrics)
	start := time.Now()
	trace, _ := ctx.Value(traceKey{}).(*RecommendationTrace)
	if trace == nil {
		trace = &RecommendationTrace{}
	}
	defer func() {
		trace.Retrieval = *metrics
		log.Printf("retrieval queries_generated=%d queries_deduplicated=%d search_cache_hits=%d search_cache_misses=%d search_list_calls=%d second_query_skipped=%t second_query_executed=%t raw_candidates=%d eligible_candidates=%d ranked_candidates=%d quota_errors=%d fixture_requests=%d replay_requests=%d", trace.QueriesGenerated, trace.QueriesDeduplicated, metrics.CacheHits, metrics.CacheMisses, metrics.SearchCalls, trace.SecondQuerySkipped, trace.SecondQueryExecuted, trace.RawCount, len(trace.Candidates), len(trace.Ranked), metrics.QuotaErrors, metrics.FixtureRequests, metrics.ReplayRequests)
		log.Printf("recommendation stages query_generation_ms=%.2f youtube_search_ms=%.2f metadata_ms=%.2f ranking_ms=%.2f candidates=%d returned=%d", trace.QueryMS, trace.SearchMS, trace.MetadataMS, trace.RankingMS, len(trace.Candidates), len(trace.Ranked))
	}()
	if trace != nil {
		defer func() { trace.TotalMS = float64(time.Since(start)) / float64(time.Millisecond) }()
	}
	queryStart := time.Now()
	queryCtx, stopQuery := context.WithTimeout(ctx, recommendationQueryTimeout)
	queries, err := s.generator.GenerateMusicQueries(queryCtx, req.Analysis, req.Preferences, s.queryCount)
	stopQuery()
	if trace != nil {
		trace.QueryMS = float64(time.Since(queryStart)) / float64(time.Millisecond)
		trace.GeneratedQueries = append([]string(nil), queries...)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		// Do not log injected/raw provider errors which may contain credentials.
		reason := "provider_error"
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			reason = "timeout"
		case errors.Is(err, context.Canceled):
			reason = "canceled"
		case errors.Is(err, client.ErrInvalidAIResponse):
			reason = "invalid_response"
		case errors.Is(err, client.ErrAIRateLimited):
			reason = "rate_limited"
		case errors.Is(err, client.ErrAIUnavailable):
			reason = "unavailable"
		case errors.Is(err, client.ErrAIConfiguration):
			reason = "configuration"
		}
		log.Printf("recommendation query_generation=fallback reason=%s cause_type=%T", reason, err)
		queries = nil
		if trace != nil {
			trace.QueryFallback = true
		}
	}
	prepared := trace.QueryFallback
	if g, ok := s.generator.(interface{ UsesPreparedQueries() bool }); ok {
		prepared = prepared || g.UsesPreparedQueries()
	}
	if !prepared {
		queries = prepareQueries(queries, req, s.queryCount)
	}
	if len(queries) == 0 {
		log.Print("recommendation query_generation=fallback reason=empty_or_invalid_queries")
		queries, _ = (client.DeterministicQueryBuilder{}).GenerateMusicQueries(ctx, req.Analysis, req.Preferences, s.queryCount)
		prepared = true
		trace.QueryFallback = true
	}
	// One broad genre/language query supplies established candidates; remaining
	// mood-specific queries retain discovery. No extra searches are introduced.
	if !prepared && len(queries) > 1 {
		queries[0] = popularProfileQuery(req)
	}
	trace.QueriesGenerated = len(queries)
	uniqueQueries := []string{}
	seenQueries := map[string]bool{}
	for _, q := range queries {
		key := strings.ToLower(strings.Join(strings.Fields(q), " "))
		if !seenQueries[key] {
			seenQueries[key] = true
			uniqueQueries = append(uniqueQueries, q)
		}
	}
	trace.QueriesDeduplicated = len(queries) - len(uniqueQueries)
	queries = uniqueQueries[:min(len(uniqueQueries), s.queryCount)]
	if trace != nil {
		trace.SearchQueries = append([]string(nil), queries...)
	}
	rawCount := 0
	ordered := []string{}
	rank := map[string]int{}
	candidates := []recommendation.Candidate{}
	response := &model.RecommendationResponse{Tracks: []model.RecommendedTrack{}, RequestedCount: req.Preferences.Count}
	if source, ok := s.music.(interface{ DataMode() string }); ok {
		response.DataMode = source.DataMode()
	}
	for queryIndex, query := range queries {
		trace.Stages = append(trace.Stages, RetrievalStageTrace{QueryIndex: queryIndex + 1, Query: query, DecisionReason: "search_not_completed"})
		stage := &trace.Stages[len(trace.Stages)-1]
		beforeMetrics := *metrics
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if queryIndex == 1 {
			trace.SecondQueryExecuted = true
		}
		order := "relevance"
		if queryIndex == 0 && len(queries) > 1 {
			order = "viewCount"
		}
		t := time.Now()
		hits, err := s.music.SearchMusic(ctx, model.MusicSearchQuery{Order: order, Text: query, Region: s.region, RelevanceLanguage: s.relevanceLanguage, MaxResults: int64(s.searchMaxResults)})
		stage.SearchMS = float64(time.Since(t)) / float64(time.Millisecond)
		stage.CacheHits = metrics.CacheHits - beforeMetrics.CacheHits
		stage.CacheMisses = metrics.CacheMisses - beforeMetrics.CacheMisses
		stage.SearchCalls = metrics.SearchCalls - beforeMetrics.SearchCalls
		trace.SearchMS += stage.SearchMS
		if err != nil {
			return nil, err
		}
		newIDs := []string{}
		for i, hit := range hits[:min(len(hits), s.searchMaxResults)] {
			stage.RawIDs = append(stage.RawIDs, hit.VideoID)
			rawCount++
			trace.RawCount = rawCount
			if hit.VideoID == "" {
				continue
			}
			old, exists := rank[hit.VideoID]
			if !exists {
				ordered = append(ordered, hit.VideoID)
				rank[hit.VideoID] = i
				newIDs = append(newIDs, hit.VideoID)
			} else if i < old {
				rank[hit.VideoID] = i
			}
		}
		stage.NewMetadataIDs = append([]string{}, newIDs...)
		stage.DeduplicatedTotal = len(ordered)
		for offset := 0; offset < len(newIDs); offset += 50 {
			ids := newIDs[offset:min(offset+50, len(newIDs))]
			wanted := map[string]bool{}
			for _, id := range ids {
				wanted[id] = true
			}
			t = time.Now()
			stage.MetadataCalls++
			videos, err := s.music.GetVideos(ctx, ids)
			elapsedMetadata := float64(time.Since(t)) / float64(time.Millisecond)
			stage.MetadataMS += elapsedMetadata
			trace.MetadataMS += elapsedMetadata
			if err != nil {
				return nil, err
			}
			for _, v := range videos {
				if !wanted[v.VideoID] {
					continue
				}
				delete(wanted, v.VideoID)
				trace.Videos = append(trace.Videos, v)
				if !eligibleVideo(v, req.Preferences, s.region) {
					trace.EligibilityFiltered++
					if recommendation.ExclusionReason(v) != "" {
						trace.AITransformFiltered++
					}
					continue
				}
				if req.Preferences.EffectiveVocalMode() == "instrumental-only" && recommendation.VocalKind(v) != "instrumental" {
					trace.EligibilityFiltered++
					continue
				}
				candidates = append(candidates, recommendation.Candidate{Video: v, SearchRank: rank[v.VideoID]})
			}
		}
		for i := range candidates {
			candidates[i].SearchRank = rank[candidates[i].Video.VideoID]
		}
		poolPreferences := req.Preferences
		poolPreferences.CandidatePoolLimit = len(candidates)
		rankingStart := time.Now()
		qualified := s.ranker(candidates, req.Analysis, poolPreferences)
		trace.Candidates = candidates
		stage.RankingMS = float64(time.Since(rankingStart)) / float64(time.Millisecond)
		stage.Qualified = append([]recommendation.RankedCandidate{}, qualified...)
		trace.RankingMS += stage.RankingMS
		// Do not stop with an explicitly instrumental-dominated pool when the user
		// requests a mixed/vocal direction. Unknown metadata is not inferred as vocal.
		balanceOK := true
		mode := req.Preferences.EffectiveVocalMode()
		if mode == "mixed" || mode == "vocal-first" {
			instrumental := 0
			for _, item := range qualified[:min(len(qualified), req.Preferences.Count)] {
				if recommendation.VocalKind(item.Video) == "instrumental" {
					instrumental++
				}
			}
			balanceOK = instrumental <= int(float64(req.Preferences.Count)*.4)
		}
		// A pool from a single channel has insufficient source coverage. This
		// metadata heuristic affects retrieval only, never V2 ranking weights.
		channels := map[string]bool{}
		for _, item := range qualified {
			key := item.Video.ChannelID
			if key == "" {
				key = item.Video.ChannelTitle
			}
			if key != "" {
				channels[key] = true
			}
		}
		diversityOK := len(channels) >= 2
		stage.DiversityBalanceOK = diversityOK
		stage.VocalBalanceOK = balanceOK
		stage.DecisionReason = "insufficient_qualified_candidates"
		if len(qualified) >= req.Preferences.Count+s.safetyMargin && !balanceOK {
			stage.DecisionReason = "instrumental_dominated_pool"
		}
		if len(qualified) >= req.Preferences.Count+s.safetyMargin && balanceOK && !diversityOK {
			stage.DecisionReason = "insufficient_channel_diversity"
		}
		if len(qualified) >= req.Preferences.Count+s.safetyMargin && balanceOK && diversityOK {
			stage.DecisionReason = "qualified_candidates_sufficient"
			if queryIndex == 0 && len(queries) > 1 {
				trace.SecondQuerySkipped = true
			}
			break
		}
	}
	trace.RawCount = rawCount
	trace.Candidates = candidates
	t := time.Now()
	poolPreferences := req.Preferences
	poolPreferences.CandidatePoolLimit = len(candidates)
	pool := s.ranker(candidates, req.Analysis, poolPreferences)
	trace.BeforeVocalMix = append([]recommendation.RankedCandidate{}, pool...)
	mixStart := time.Now()
	trace.Ranked = recommendation.SelectVocalMix(pool, req.Preferences)
	trace.VocalMixMS = float64(time.Since(mixStart)) / float64(time.Millisecond)
	trace.RankingMS += float64(time.Since(t)) / float64(time.Millisecond)
	for _, item := range trace.Ranked {
		if response.DataMode == "fixture" {
			item.Track.YouTubeURL = ""
		}
		response.Tracks = append(response.Tracks, item.Track)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	filtered := len(response.Tracks)

	response.ReturnedCount = len(response.Tracks)
	response.Partial = response.ReturnedCount < response.RequestedCount
	log.Printf("recommendation queries=%d raw_candidates=%d deduplicated=%d filtered=%d returned=%d recommendation_ms=%.2f latency=%s", len(queries), rawCount, len(ordered), filtered, response.ReturnedCount, float64(time.Since(start))/float64(time.Millisecond), time.Since(start))
	return response, nil
}
func prepareQueries(input []string, req model.RecommendationRequest, limit int) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, raw := range input {
		core := truncate(words(raw), 120)
		if core == "" {
			continue
		}
		// Deduplicate before preference suffixes so an identical AI query never
		// becomes multiple API calls merely because language suffixes differ.
		if seen[core] {
			continue
		}
		seen[core] = true
		query := core + " " + languageName(req.Preferences.Languages[len(result)%len(req.Preferences.Languages)])
		if req.Preferences.EffectiveVocalMode() == "instrumental-only" {
			query += " instrumental"
		}
		query = trackSearchQuery(query)
		result = append(result, query)
		if len(result) == limit {
			break
		}
	}
	return result
}
func popularProfileQuery(req model.RecommendationRequest) string {
	genres := req.Preferences.PreferredGenres
	if len(genres) == 0 {
		genres = req.Analysis.MusicProfile.Genres
	}
	genre := "music"
	if len(genres) > 0 {
		genre = words(genres[0])
	}
	query := languageName(req.Preferences.Languages[0]) + " " + truncate(genre, 60) + " music"
	if req.Preferences.EffectiveVocalMode() == "instrumental-only" {
		query += " instrumental"
	}
	return trackSearchQuery(query)
}

func fallbackQueries(req model.RecommendationRequest, limit int) []string {
	genres := req.Preferences.PreferredGenres
	if len(genres) == 0 {
		genres = req.Analysis.MusicProfile.Genres
	}
	// Keep fallback broad: requiring all profile keywords starves the pool.
	if len(genres) == 0 {
		genres = []string{"music"}
	}
	tags := req.Analysis.Mood.Tags
	if len(tags) == 0 {
		tags = []string{""}
	}
	result := []string{}
	seen := map[string]bool{}
	for i := 0; i < limit; i++ {
		core := truncate(words(genres[i%len(genres)]+" "+tags[i%len(tags)]), 80)
		query := core + " " + languageName(req.Preferences.Languages[i%len(req.Preferences.Languages)]) + " song"
		if req.Preferences.EffectiveVocalMode() == "instrumental-only" {
			query += " instrumental"
		}
		if seen[query] {
			continue
		}
		seen[query] = true
		result = append(result, trackSearchQuery(query))
	}
	return result
}

// Broad cafe/mood searches often return hours-long mixes. Use YouTube's
// documented NOT operator to target individual tracks before duration validation.
// Reserve suffix space so long AI queries cannot truncate the exclusions.
func trackSearchQuery(query string) string {
	const exclusions = " -mix -playlist -compilation"
	return truncate(query, 160-len([]rune(exclusions))) + exclusions
}

func languageName(code string) string {
	if name, ok := map[string]string{"ko": "korean", "en": "english language", "ja": "japanese", "es": "spanish", "fr": "french", "de": "german", "zh": "chinese", "pt": "portuguese"}[code]; ok {
		return name
	}
	return code + " language"
}
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return strings.TrimSpace(string(r[:n]))
	}
	return s
}
func words(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, s)), " ")
}
func hasPhrase(text, phrase string) bool {
	normalized := words(phrase)
	return normalized != "" && strings.Contains(" "+words(text)+" ", " "+normalized+" ")
}
func eligibleVideo(v model.YouTubeVideo, p model.MusicPreferences, region string) bool {
	if v.VideoID == "" || v.Title == "" || v.ChannelTitle == "" || v.CategoryID != model.MusicCategoryID || !v.Embeddable || !v.Public || v.Live || v.DurationSeconds < MinTrackDurationSeconds || v.DurationSeconds > MaxTrackDurationSeconds {
		return false
	}
	if v.AllowedRegions != nil && !contains(v.AllowedRegions, region) {
		return false
	}
	if contains(v.BlockedRegions, region) {
		return false
	}
	if recommendation.ExclusionReason(v) != "" || len(recommendation.NonSongReasons(v)) > 0 {
		return false
	}
	for _, artist := range p.ExcludedArtists {
		// Exact, normalized phrase boundaries only. No artist parsing or fuzzy matching.
		if hasPhrase(v.Title, artist) || hasPhrase(v.ChannelTitle, artist) {
			return false
		}
	}
	return true
}
func contains(items []string, value string) bool {
	for _, item := range items {
		if strings.EqualFold(item, value) {
			return true
		}
	}
	return false
}
