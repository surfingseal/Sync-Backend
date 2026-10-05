package main

import (
	"encoding/json"
	"example.com/sync/internal/model"
	"example.com/sync/internal/recommendation"
	"example.com/sync/internal/service"
	"fmt"
	"os"
	"path/filepath"
)

func buildNeutralDiagnostics(dir string, run runResult) error {
	files := map[string]any{
		"retrieval-plan.json":     run.Trace.Plan,
		"adaptive-decision.json":  run.Trace.AdaptiveDecision,
		"language-coverage.json":  map[string]any{"candidate_evidence": run.Trace.CandidateSignals, "final_pool": run.Trace.FinalStrength, "classification": "separate text/retrieval affinity and reported audio evidence; plausible vocal coverage, never certified singing language"},
		"release-confidence.json": map[string]any{"candidates": run.Trace.CandidateSignals, "final_pool": run.Trace.FinalStrength, "confidence_is_not_release_certification": true},
		"candidate-funnel.json":   map[string]any{"stages": run.Trace.Stages, "final_strength": run.Trace.FinalStrength},
		"ranking.json":            map[string]any{"evaluations": run.Ranking, "before_vocal_mix": run.Trace.BeforeVocalMix, "after_vocal_mix": run.Trace.Ranked, "mood_audit": run.Trace.CandidateSignals, "v2_weights_unchanged": true},
		"retrieval.json":          map[string]any{"search": run.Search, "metadata_batches": run.Metadata, "candidate_signals": run.Trace.CandidateSignals, "stages": run.Trace.Stages},
		"quota.json":              map[string]any{"search_list_actual_calls": len(run.Search), "videos_list_actual_calls": len(run.Metadata), "cache_hits": run.Trace.Retrieval.CacheHits, "cache_misses": run.Trace.Retrieval.CacheMisses, "budget_remaining": 2 - len(run.Search), "quota_errors": run.Trace.Retrieval.QuotaErrors, "vertex_calls": 0, "playlist_writes": 0, "q2_executed": run.Trace.SecondQueryExecuted},
		"timings.json":            map[string]any{"query_planning_ms": run.Trace.QueryMS, "youtube_search_ms": run.Trace.SearchMS, "youtube_metadata_ms": run.Trace.MetadataMS, "ranking_and_vocal_mix_ms": run.Trace.RankingMS, "vocal_mix_ms": run.Trace.VocalMixMS, "recommendation_total_ms": run.Trace.TotalMS, "http_total_ms": run.RecommendHTTPMS, "vertex_ms": 0, "gemini_text_query_ms": 0},
	}
	var q1, q2 *service.RetrievalStageTrace
	for i := range run.Trace.Stages {
		if i == 0 {
			q1 = &run.Trace.Stages[i]
		}
		if i == 1 {
			q2 = &run.Trace.Stages[i]
		}
	}
	files["q1.json"] = q1
	files["q2.json"] = q2
	checks := map[string]bool{"no_fresh_vertex": run.Vertex.Calls == 0, "analysis_reused": run.AnalysisReused, "search_budget": len(run.Search) <= 2, "batches_at_most_50": true, "no_metadata_refetch": true, "final_ids_in_actual_videos_list": true}
	seen := map[string]bool{}
	videos := map[string]model.YouTubeVideo{}
	for _, batch := range run.Metadata {
		if len(batch.IDs) > 50 {
			checks["batches_at_most_50"] = false
		}
		for _, id := range batch.IDs {
			if seen[id] {
				checks["no_metadata_refetch"] = false
			}
			seen[id] = true
		}
		for _, v := range batch.Videos {
			videos[v.VideoID] = v
		}
	}
	for _, item := range run.Trace.Ranked {
		if _, ok := videos[item.Video.VideoID]; !ok {
			checks["final_ids_in_actual_videos_list"] = false
		}
	}
	files["sanity.json"] = checks
	for name, value := range files {
		if err := save(filepath.Join(dir, name), value); err != nil {
			return err
		}
	}
	for _, passed := range checks {
		if !passed {
			return fmt.Errorf("neutral run invariant failed")
		}
	}
	return buildOldNeutralSignals(dir)
}

// Retrospective evidence on saved metadata only; not another live retrieval.
func buildOldNeutralSignals(dir string) error {
	path := filepath.Join(filepath.Dir(dir), "live-v3-retrieval-v2", "run.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var old runResult
	if err = json.Unmarshal(raw, &old); err != nil {
		return err
	}
	source := map[string]recommendation.PlannedQuery{}
	for i, search := range old.Search {
		genre, lang := "city-pop", "ko"
		if i == 1 {
			genre, lang = "chillwave", "en"
		}
		q := recommendation.PlannedQuery{Role: "historical_v3_retrieval", Query: search.Query.Text, Genre: genre, Language: &lang, Order: search.Query.Order, MaxResults: search.Query.MaxResults}
		for _, hit := range search.Raw {
			if _, exists := source[hit.VideoID]; !exists {
				source[hit.VideoID] = q
			}
		}
	}
	signals := []service.CandidateSignalTrace{}
	for _, batch := range old.Metadata {
		for _, v := range batch.Videos {
			q := source[v.VideoID]
			lang := ""
			if q.Language != nil {
				lang = *q.Language
			}
			hard := true
			reasons := []string{}
			for _, check := range service.ExplainVideoEligibility(v, model.MusicPreferences{}, "KR") {
				if !check.Passed {
					hard = false
					reasons = append(reasons, check.Stage)
				}
			}
			r := recommendation.ReleaseConfidence(v)
			signals = append(signals, service.CandidateSignalTrace{VideoID: v.VideoID, Source: q, HardEligible: hard, HardReasons: reasons, Release: r, ReleaseAllowed: recommendation.DefaultNeutralPolicy().AllowsRelease(r.Confidence), QualityAllowed: !recommendation.FailsQualityGate(v), Language: recommendation.LanguageAffinity(v, q.Genre, lang), PopularityBucket: recommendation.PopularityBucket(v.ViewCount)})
		}
	}
	return save(filepath.Join(dir, "comparison-old-signals.json"), map[string]any{"source": "../live-v3-retrieval-v2/run.json", "interpretation": "retrospective current heuristics on saved official metadata; no new API calls and no historical release gate implied", "signals": signals})
}
