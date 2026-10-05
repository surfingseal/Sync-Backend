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

// Fixed captured metadata pool. No provider client, HTTP request or new query
// execution is constructed; source queries are historical, not a counterfactual.
func eligibilityReplay(source, dir string) error {
	if dir == "" {
		return fmt.Errorf("new output required")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		return fmt.Errorf("refusing existing output")
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	var old runResult
	if err = json.Unmarshal(raw, &old); err != nil {
		return err
	}
	var envelope struct {
		Analysis json.RawMessage `json:"analysis"`
	}
	if json.Unmarshal(old.AnalyzeResponse, &envelope) != nil {
		return fmt.Errorf("saved analysis missing")
	}
	analysis, err := model.DecodeImageAnalysis(envelope.Analysis)
	if err != nil {
		return err
	}
	prefs := model.MusicPreferences{Languages: []string{"ko", "en"}, Count: 10, VocalMode: "mixed"}
	ranks := map[string]int{}
	sources := map[string]recommendation.PlannedQuery{}
	for i, search := range old.Search {
		for rank, hit := range search.Raw {
			if previous, ok := ranks[hit.VideoID]; !ok || rank < previous {
				ranks[hit.VideoID] = rank
			}
			if _, exists := sources[hit.VideoID]; !exists && i < len(old.Trace.Stages) && old.Trace.Stages[i].PlannedQuery != nil {
				sources[hit.VideoID] = *old.Trace.Stages[i].PlannedQuery
			}
		}
	}
	videos := map[string]model.YouTubeVideo{}
	for _, batch := range old.Metadata {
		for _, v := range batch.Videos {
			videos[v.VideoID] = v
		}
	}
	audit := []service.CandidateSignalTrace{}
	candidates := []recommendation.Candidate{}
	hard, release := 0, 0
	newCoverage := map[string]int{"ko": 0, "en": 0, "ja": 0, "unknown": 0}
	textOnly, audio, instExcluded := 0, 0, 0
	for _, v := range old.Trace.Videos {
		q := sources[v.VideoID]
		lang := ""
		if q.Language != nil {
			lang = *q.Language
		}
		reasons := recommendation.SongFormReasons(v)
		eligible := true
		for _, check := range service.ExplainVideoEligibility(v, prefs, "KR") {
			if !check.Passed {
				eligible = false
				if check.Stage != "song_form" {
					reasons = append(reasons, check.Stage)
				}
			}
		}
		signal := service.CandidateSignalTrace{VideoID: v.VideoID, Source: q, HardEligible: eligible, HardReasons: reasons, Release: recommendation.ReleaseConfidence(v), QualityAllowed: !recommendation.FailsQualityGate(v), VocalAllowed: true, Language: recommendation.LanguageAffinity(v, q.Genre, lang), PopularityBucket: recommendation.PopularityBucket(v.ViewCount), Mood: recommendation.AuditMood(v, ranks[v.VideoID], *analysis, prefs)}
		signal.ReleaseAllowed = recommendation.DefaultNeutralPolicy().AllowsRelease(signal.Release.Confidence)
		audit = append(audit, signal)
		if !eligible {
			continue
		}
		hard++
		if !signal.ReleaseAllowed || !signal.QualityAllowed {
			continue
		}
		release++
		language := signal.Language.CoverageLanguage
		if !signal.Language.CoverageEligible {
			language = "unknown"
		}
		newCoverage[language]++
		if signal.Language.TextLanguage.Value != "unknown" && signal.Language.AudioLanguage.Value == "unknown" {
			textOnly++
		}
		if signal.Language.AudioLanguage.Value != "unknown" {
			audio++
		}
		if recommendation.VocalKind(v) == "instrumental" {
			instExcluded++
		}
		candidates = append(candidates, recommendation.Candidate{Video: v, SearchRank: ranks[v.VideoID]})
	}
	poolPrefs := prefs
	poolPrefs.CandidatePoolLimit = len(candidates)
	pool, rankAudit := recommendation.RankV2WithAudit(candidates, *analysis, poolPrefs)
	final := recommendation.SelectVocalMix(pool, prefs)
	oldCoverage := map[string]int{}
	oldTextOnly, oldInstCounted, oldPreferredInstCounted := 0, 0, 0
	for _, signal := range old.Trace.CandidateSignals {
		if signal.HardEligible && signal.ReleaseAllowed && signal.QualityAllowed && signal.VocalAllowed {
			oldCoverage[signal.Language.Affinity]++
			v := videos[signal.VideoID]
			if v.DefaultLanguage != "" && v.DefaultAudioLanguage == "" {
				oldTextOnly++
			}
			if recommendation.VocalKind(v) == "instrumental" && signal.Language.Affinity != "unknown" {
				oldInstCounted++
				if signal.Language.Affinity == "ko" || signal.Language.Affinity == "en" {
					oldPreferredInstCounted++
				}
			}
		}
	}
	removed := []map[string]any{}
	selected := map[string]bool{}
	for _, x := range final {
		selected[x.Video.VideoID] = true
	}
	for _, x := range old.Trace.Ranked {
		if !selected[x.Video.VideoID] {
			removed = append(removed, map[string]any{"video_id": x.Video.VideoID, "title": x.Video.Title, "song_form_reasons": recommendation.SongFormReasons(x.Video)})
		}
	}
	observedFix := map[string]bool{}
	for _, id := range []string{"NhxKRixsKa4", "MWCIWVxzbmo"} {
		if v, exists := videos[id]; exists {
			observedFix[id] = len(recommendation.SongFormReasons(v)) > 0 && !selected[id]
			if !observedFix[id] {
				return fmt.Errorf("observed song-form defect not fixed")
			}
		}
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	files := map[string]any{
		"offline-replay.json": map[string]any{"mode": "fixed captured metadata pool, not retrieval simulation", "external_calls": 0, "captured_candidates": len(videos), "old_hard_eligible": func() int {
			n := 0
			for _, s := range old.Trace.CandidateSignals {
				if s.HardEligible {
					n++
				}
			}
			return n
		}(), "new_hard_eligible": hard, "old_release_quality_eligible": old.Trace.FinalStrength.PoolCount, "new_release_quality_eligible": release, "old_final_count": len(old.Trace.Ranked), "new_final_count": len(final), "removed_candidates": removed, "observed_defects_excluded": observedFix, "old_language_coverage": oldCoverage, "new_vocal_language_coverage": newCoverage, "old_coverage_sufficient": oldCoverage["ko"] >= 2 && oldCoverage["en"] >= 2, "new_coverage_sufficient": newCoverage["ko"] >= 2 && newCoverage["en"] >= 2, "weak_upload_text_only_old_pool": oldTextOnly, "weak_upload_text_only_new_pool": textOnly, "reported_audio_new_pool": audio, "instrumental_counted_before_all_languages": oldInstCounted, "instrumental_counted_before_preferred_languages": oldPreferredInstCounted, "instrumental_coverage_excluded_new_pool": instExcluded, "first_live_results_not_modified": true},
		"song-form-audit.json": audit, "language-evidence.json": audit, "ranking.json": map[string]any{"initial_score_audit": rankAudit, "before_vocal": pool, "after_vocal": final}, "recommendations.json": final, "analysis.json": analysis,
	}
	for name, value := range files {
		if err = save(filepath.Join(dir, name), value); err != nil {
			return err
		}
	}
	fmt.Printf("offline fixed-pool audit: candidates=%d old_final=%d new_final=%d external_calls=0\n", len(videos), len(old.Trace.Ranked), len(final))
	return nil
}
