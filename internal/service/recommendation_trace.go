package service

import (
	"context"
	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
	"example.com/sync/internal/recommendation"
)

// Trace is opt-in per-request diagnostics for explicit integration benchmarks.
// It is never part of HTTP response or a shared mutable service field.
type RetrievalStageTrace struct {
	PlannedQuery                        *recommendation.PlannedQuery
	Strength                            *recommendation.CandidateStrength
	DecisionReasons                     []string
	QueryIndex                          int
	Query                               string
	RawIDs, NewMetadataIDs              []string
	DeduplicatedTotal, MetadataCalls    int
	CacheHits, CacheMisses, SearchCalls int
	SearchMS, MetadataMS, RankingMS     float64
	Qualified                           []recommendation.RankedCandidate
	VocalBalanceOK                      bool
	DiversityBalanceOK                  bool
	DecisionReason                      string
}
type RecommendationTrace struct {
	Plan                                               *recommendation.RetrievalPlan
	CandidateSignals                                   []CandidateSignalTrace
	AdaptiveDecision                                   recommendation.AdaptiveDecision
	FinalStrength                                      *recommendation.CandidateStrength
	Stages                                             []RetrievalStageTrace
	BeforeVocalMix                                     []recommendation.RankedCandidate
	VocalMixMS                                         float64
	Retrieval                                          client.RetrievalMetrics
	QueriesGenerated, QueriesDeduplicated              int
	SecondQuerySkipped, SecondQueryExecuted            bool
	GeneratedQueries, SearchQueries                    []string
	QueryMS, SearchMS, MetadataMS, RankingMS, TotalMS  float64
	RawCount, AITransformFiltered, EligibilityFiltered int
	QueryFallback                                      bool
	Videos                                             []model.YouTubeVideo
	Candidates                                         []recommendation.Candidate
	Ranked                                             []recommendation.RankedCandidate
}
type traceKey struct{}

func WithRecommendationTrace(ctx context.Context, t *RecommendationTrace) context.Context {
	return context.WithValue(ctx, traceKey{}, t)
}

type RankFunc func([]recommendation.Candidate, model.ImageAnalysis, model.MusicPreferences) []recommendation.RankedCandidate
type RecommendationOption func(*RecommendationService)

func WithRanker(f RankFunc) RecommendationOption {
	return func(s *RecommendationService) { s.ranker = f }
}

func WithAdaptiveSafetyMargin(n int) RecommendationOption {
	return func(s *RecommendationService) { s.safetyMargin = max(0, n) }
}

func WithSearchMaxResults(n int) RecommendationOption {
	return func(s *RecommendationService) { s.searchMaxResults = n }
}
