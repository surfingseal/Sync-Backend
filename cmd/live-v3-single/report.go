package main

import (
	"encoding/json"
	"example.com/sync/internal/model"
	"example.com/sync/internal/music"
	"example.com/sync/internal/recommendation"
	"example.com/sync/internal/service"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

type filterStep struct {
	Stage      string   `json:"stage"`
	Remaining  int      `json:"remaining"`
	RemovedIDs []string `json:"removed_video_ids"`
}
type queryDetail struct {
	Index            int      `json:"index"`
	Text             string   `json:"text"`
	Mood             string   `json:"mood_used"`
	MoodSource       string   `json:"mood_source"`
	Genre            string   `json:"genre_used"`
	ModelScore       *float64 `json:"original_model_score"`
	RetrievalWeight  *float64 `json:"retrieval_weight"`
	AdjustedPriority *float64 `json:"adjusted_retrieval_priority"`
	GenreConfidence  *float64 `json:"genre_confidence"`
	Language         string   `json:"language"`
	VocalMode        string   `json:"vocal_mode"`
}

func buildDiagnostics(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		return err
	}
	var run runResult
	if err = json.Unmarshal(raw, &run); err != nil {
		return err
	}
	if run.Trace.Plan != nil {
		return buildNeutralDiagnostics(dir, run)
	}
	checks := map[string]bool{}
	queries := []queryDetail{}
	priorities := []map[string]any{}
	var a *model.ImageAnalysis
	if run.AnalyzeStatus == 200 || run.AnalysisReused {
		var envelope struct {
			Analysis json.RawMessage `json:"analysis"`
		}
		if json.Unmarshal(run.AnalyzeResponse, &envelope) != nil {
			return fmt.Errorf("invalid saved response")
		}
		a, err = model.DecodeImageAnalysis(envelope.Analysis)
		checks["structured_output_valid"] = err == nil
		if err != nil {
			return err
		}
		checks["schema_version_3"] = a.SchemaVersion == "3"
		checks["exactly_one_primary_mood"] = music.IsMood(a.Mood.Primary)
		checks["secondary_max_three"] = len(a.Mood.Secondary) <= 3
		checks["genre_top_max_three"] = len(a.MusicProfile.GenreCandidates) <= 3
		checks["canonical_enums_categories_mirrors_and_unit_ranges"] = a.Validate() == nil
		sorted := true
		for i := 1; i < len(a.MusicProfile.GenreCandidates); i++ {
			if a.MusicProfile.GenreCandidates[i-1].Score < a.MusicProfile.GenreCandidates[i].Score {
				sorted = false
			}
		}
		checks["model_genre_scores_sorted"] = sorted
		checks["no_image_vocal_preference"] = a.MusicProfile.VocalPreference == "" && a.MusicProfile.InstrumentalPreference == nil
		intent, err := model.AdaptQueryIntent(*a, model.MusicPreferences{Languages: []string{"ko", "en"}, VocalMode: "mixed", Count: 10})
		if err != nil {
			return err
		}
		for i, g := range intent.Genres {
			w := music.RetrievalWeight(g.Name)
			row := map[string]any{"adjusted_rank": i + 1, "genre": g.Name, "category": g.Category, "model_score": g.Score, "retrieval_weight": w, "vocal_friendly_direction": music.VocalFriendly(g.Name)}
			if g.Score != nil {
				row["adjusted_priority"] = *g.Score * w
			}
			priorities = append(priorities, row)
		}
		moods := append([]string{intent.PrimaryMood}, intent.SecondaryMoods...)
		langs := []string{"ko", "en"}
		rawLabelsSafe := true
		noInstrumental := true
		for i, q := range run.Trace.SearchQueries {
			d := queryDetail{Index: i + 1, Text: q, Mood: moods[i%len(moods)], MoodSource: "secondary", GenreConfidence: intent.GenreConfidence, Language: langs[i%len(langs)], VocalMode: "mixed"}
			if i == 0 || i%len(moods) == 0 {
				d.MoodSource = "primary"
			}
			longest := 0
			for _, g := range intent.Genres {
				if g.QueryTokens != "" && strings.Contains(" "+q+" ", " "+g.QueryTokens+" ") && len(g.QueryTokens) > longest {
					longest = len(g.QueryTokens)
					d.Genre = g.Name
					d.ModelScore = g.Score
					w := music.RetrievalWeight(g.Name)
					d.RetrievalWeight = &w
					if g.Score != nil {
						p := *g.Score * w
						d.AdjustedPriority = &p
					}
				}
				if g.RawLabel != "" && strings.Contains(q, music.Normalize(g.RawLabel)) {
					rawLabelsSafe = false
				}
			}
			if strings.Contains(q, "instrumental") {
				noInstrumental = false
			}
			queries = append(queries, d)
		}
		checks["unknown_raw_labels_not_searched"] = rawLabelsSafe
		checks["mixed_does_not_force_instrumental_query"] = noInstrumental
	}
	videos := map[string]model.YouTubeVideo{}
	for _, batch := range run.Metadata {
		for _, v := range batch.Videos {
			videos[v.VideoID] = v
		}
	}
	cumulative := []string{}
	seen := map[string]bool{}
	stages := []map[string]any{}
	prefs := model.MusicPreferences{Languages: []string{"ko", "en"}, VocalMode: "mixed", Count: 10}
	for i, stage := range run.Trace.Stages {
		for _, id := range stage.RawIDs {
			if id != "" && !seen[id] {
				seen[id] = true
				cumulative = append(cumulative, id)
			}
		}
		remaining := append([]string{}, cumulative...)
		funnel := []filterStep{}
		labels := []string{"metadata_present", "music_category", "public_and_not_live", "embeddable", "song_form", "duration", "region", "ai_generated", "transformed_audio", "excluded_artist"}
		for _, label := range labels {
			keep, removed := []string{}, []string{}
			for _, id := range remaining {
				v := videos[id]
				passed := false
				for _, check := range service.ExplainVideoEligibility(v, prefs, "KR") {
					if check.Stage == label {
						passed = check.Passed
						break
					}
				}
				if passed {
					keep = append(keep, id)
				} else {
					removed = append(removed, id)
				}
			}
			remaining = keep
			funnel = append(funnel, filterStep{label, len(remaining), removed})
		}
		keep, removed := []string{}, []string{}
		for _, id := range remaining {
			if recommendation.FailsQualityGate(videos[id]) {
				removed = append(removed, id)
			} else {
				keep = append(keep, id)
			}
		}
		remaining = keep
		funnel = append(funnel, filterStep{"quality_gate", len(remaining), removed})
		scorePassed := map[string]bool{}
		if i < len(run.Ranking) {
			for _, item := range run.Ranking[i].Audit.InitialScores {
				if item.Scores.Final >= run.Ranking[i].Audit.MinimumScore {
					scorePassed[item.Video.VideoID] = true
				}
			}
		}
		keep, removed = []string{}, []string{}
		for _, id := range remaining {
			if scorePassed[id] {
				keep = append(keep, id)
			} else {
				removed = append(removed, id)
			}
		}
		remaining = keep
		funnel = append(funnel, filterStep{"initial_minimum_score", len(remaining), removed})
		vocal, inst, unknown := 0, 0, 0
		for _, item := range stage.Qualified[:min(len(stage.Qualified), 10)] {
			switch recommendation.VocalKind(item.Video) {
			case "vocal":
				vocal++
			case "instrumental":
				inst++
			default:
				unknown++
			}
		}
		stages = append(stages, map[string]any{"query_index": stage.QueryIndex, "query": stage.Query, "raw_count": len(stage.RawIDs), "cumulative_deduplicated_count": len(cumulative), "new_metadata_ids": stage.NewMetadataIDs, "metadata_calls": stage.MetadataCalls, "cumulative_filter_funnel": funnel, "qualified_after_selection_constraints": len(stage.Qualified), "requested_count": 10, "safety_margin": 3, "required_qualified": 13, "diversity_balance_ok": stage.DiversityBalanceOK, "vocal_balance_ok": stage.VocalBalanceOK, "known_vocal_top_count": vocal, "instrumental_top_count": inst, "unknown_top_count": unknown, "decision_reason": stage.DecisionReason})
	}
	before := run.Trace.BeforeVocalMix
	positions := map[string]int{}
	for i, item := range before {
		positions[item.Video.VideoID] = i + 1
	}
	remaining := append([]recommendation.RankedCandidate{}, before...)
	moves := []map[string]any{}
	tolerance := true
	vocal, inst, unknown := 0, 0, 0
	for i, item := range run.Trace.Ranked {
		switch recommendation.VocalKind(item.Video) {
		case "vocal":
			vocal++
		case "instrumental":
			inst++
		default:
			unknown++
		}
		index := -1
		for j, c := range remaining {
			if c.Video.VideoID == item.Video.VideoID {
				index = j
				break
			}
		}
		if index >= 0 {
			if index > 0 {
				gap := math.Max(0, remaining[0].Track.MatchScore-item.Track.MatchScore)
				if gap > recommendation.VocalMixScoreTolerance+1e-9 {
					tolerance = false
				}
				moves = append(moves, map[string]any{"video_id": item.Video.VideoID, "original_rank": positions[item.Video.VideoID], "final_rank": i + 1, "displaced_video_id": remaining[0].Video.VideoID, "score_difference": gap})
			}
			remaining = append(remaining[:index], remaining[index+1:]...)
		} else {
			tolerance = false
		}
	}
	checks["vocal_promotion_within_008"] = tolerance
	checks["search_calls_not_above_two"] = len(run.Search) <= 2
	checks["metadata_batches_max_50"] = true
	checks["no_metadata_id_refetch"] = true
	metadataSeen := map[string]bool{}
	for _, batch := range run.Metadata {
		if len(batch.IDs) > 50 {
			checks["metadata_batches_max_50"] = false
		}
		for _, id := range batch.IDs {
			if metadataSeen[id] {
				checks["no_metadata_id_refetch"] = false
			}
			metadataSeen[id] = true
		}
	}
	mix := map[string]any{"classification_method": "metadata heuristic; unknown is not actual audio classification", "vocal_friendly_count": vocal, "instrumental_count": inst, "unknown_count": unknown, "soft_promotions": moves, "tolerance": recommendation.VocalMixScoreTolerance, "tolerance_check_passed": tolerance, "before": before, "after": run.Trace.Ranked}
	if err = save(filepath.Join(dir, "queries.json"), map[string]any{"queries": queries, "genre_priorities": priorities, "legacy_vocal_fields_ignored": true, "gemini_text_query_calls": 0}); err != nil {
		return err
	}
	// Multi-reason audit evaluates all predicates independently, even when an
	// earlier stage already rejected the candidate. Counts therefore overlap.
	excluded := []map[string]any{}
	reasonCounts := map[string]int{}
	for _, id := range cumulative {
		v := videos[id]
		reasons := recommendation.NonSongReasons(v)
		for _, check := range service.ExplainVideoEligibility(v, prefs, "KR") {
			if !check.Passed && check.Stage != "song_form" {
				reasons = append(reasons, check.Stage)
			}
		}
		if recommendation.FailsQualityGate(v) {
			reasons = append(reasons, "quality_gate")
		}
		if len(run.Ranking) > 0 {
			for _, item := range run.Ranking[len(run.Ranking)-1].Audit.InitialScores {
				if item.Video.VideoID == id && item.Scores.Final < run.Ranking[len(run.Ranking)-1].Audit.MinimumScore {
					reasons = append(reasons, "minimum_score")
				}
			}
		}
		if len(reasons) > 0 {
			excluded = append(excluded, map[string]any{"video_id": id, "excluded": true, "reasons": reasons})
			for _, reason := range reasons {
				reasonCounts[reason]++
			}
		}
	}
	if err = save(filepath.Join(dir, "retrieval.json"), map[string]any{"excluded_candidates": excluded, "exclusion_reason_counts": reasonCounts, "reason_counts_overlap": true, "search": run.Search, "metadata_batches": run.Metadata, "stages": stages, "second_query_skipped": run.Trace.SecondQuerySkipped, "second_query_executed": run.Trace.SecondQueryExecuted, "second_query_reason": func() string {
		if len(run.Trace.Stages) > 0 {
			return run.Trace.Stages[0].DecisionReason
		}
		return "analyze_failed_not_searched"
	}()}); err != nil {
		return err
	}
	if err = save(filepath.Join(dir, "ranking.json"), map[string]any{"evaluations": run.Ranking, "before_vocal_mix": before, "after_vocal_mix": run.Trace.Ranked, "v2_weights": map[string]float64{"mood": .45, "popularity": .25, "trust": .15, "engagement": .05, "diversity": .10}, "initial_scores_scope": "first marginal evaluation with no repeated selected channel; actual final scores retain diversity effects"}); err != nil {
		return err
	}
	if err = save(filepath.Join(dir, "vocal-mix.json"), mix); err != nil {
		return err
	}
	if err = save(filepath.Join(dir, "sanity.json"), checks); err != nil {
		return err
	}
	quota := map[string]any{"queries_generated": run.Trace.QueriesGenerated, "queries_actually_searched": len(run.Search), "search_list_actual_calls": len(run.Search), "videos_list_actual_calls": len(run.Metadata), "cache_hits": run.Trace.Retrieval.CacheHits, "cache_misses": run.Trace.Retrieval.CacheMisses, "search_budget_remaining": 2 - len(run.Search), "second_query_executed": run.Trace.SecondQueryExecuted, "quota_errors": run.Trace.Retrieval.QuotaErrors, "vertex_image_logical_calls": run.Vertex.Calls, "vertex_generate_content_transport_attempts": run.Vertex.Metrics.Attempts, "vertex_text_query_calls": 0, "user_oauth_calls": 0, "playlist_writes": 0}
	if err = save(filepath.Join(dir, "quota.json"), quota); err != nil {
		return err
	}
	timings := map[string]any{"units": "milliseconds", "image_preprocess": run.Processor, "vertex_analysis_ms": run.Vertex.MS, "gemini_text_query_generation_ms": 0, "deterministic_query_generation_ms": run.Trace.QueryMS, "query_stages": run.Trace.Stages, "youtube_search_ms": run.Trace.SearchMS, "youtube_metadata_ms": run.Trace.MetadataMS, "ranking_including_vocal_mix_ms": run.Trace.RankingMS, "vocal_mix_ms": run.Trace.VocalMixMS, "recommendation_server_total_ms": run.Trace.TotalMS, "recommendation_http_total_ms": run.RecommendHTTPMS, "analyze_http_total_ms": run.AnalyzeHTTPMS, "upload_to_recommendations_ms": run.UploadToReadyMS, "filter_separate_ms": nil, "filter_ranking_and_orchestration_ms": run.Trace.TotalMS - run.Trace.QueryMS - run.Trace.SearchMS - run.Trace.MetadataMS, "filter_timing_note": "filtering is not separately timed in this baseline; residual includes ranking, filtering and local orchestration, not pure filtering"}
	if err = save(filepath.Join(dir, "timings.json"), timings); err != nil {
		return err
	}
	return save(filepath.Join(dir, "image.json"), map[string]any{"original": run.Image, "processed": run.Processor})
}
