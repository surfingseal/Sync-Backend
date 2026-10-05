package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"example.com/sync/internal/lastfm"
	"example.com/sync/internal/lastfmeval"
	"example.com/sync/internal/lastfmmapping"
	"example.com/sync/internal/music"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

func write(dir, name string, value any) error {
	b, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0644)
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func checksum(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func run() error {
	out := flag.String("out", "artifacts/lastfm-freeze-and-ranking-readiness-"+time.Now().UTC().Format("20060102T150405Z"), "new output directory")
	flag.Parse()
	if _, e := os.Stat(*out); !os.IsNotExist(e) {
		return fmt.Errorf("output directory already exists or cannot be checked")
	}
	if e := os.MkdirAll(*out, 0755); e != nil {
		return e
	}
	c := music.CanonicalTaxonomy()
	registryPath := "internal/music/lastfm_retrieval_registry.json"
	taxonomyPath := "internal/music/taxonomy.json"
	evidencePath := "artifacts/lastfm-final-evidence-batch-20261004T144123Z/provider-observations.json"
	r, e := lastfmeval.LoadAutomaticRegistry(registryPath, c)
	if e != nil {
		return e
	}
	registryBytes, e := os.ReadFile(registryPath)
	if e != nil {
		return e
	}
	taxonomyBytes, e := os.ReadFile(taxonomyPath)
	if e != nil {
		return e
	}
	evidenceBytes, e := os.ReadFile(evidencePath)
	if e != nil {
		return e
	}
	var pool []lastfmeval.TagEvidence
	if e = json.Unmarshal(evidenceBytes, &pool); e != nil {
		return e
	}
	legacy, e := lastfmmapping.LoadRegistry("internal/music/lastfm_genre_mapping.json", c)
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(r, lastfmeval.BuildAutomaticRegistry(c, legacy, pool)) {
		return fmt.Errorf("registry does not match referenced evidence; refuse false freeze provenance")
	}
	v, e := lastfmeval.LoadVocabulary("internal/music/lastfm_tag_registry.json", "internal/music/lastfm_tag_mapping.json")
	if e != nil {
		return e
	}
	policy := lastfmeval.DefaultPolicy()
	policy.PerRouteLimit = 20
	fixture, e := lastfmeval.AuditFixtureCoverage("../benchmark-results", c, r, v, policy)
	if e != nil {
		return e
	}
	coverageCounts := map[string]int{"exact": 0, "alias": 0, "family_fallback": 0, "unsupported": 0, "sentinel": 0}
	resolutions := []lastfmeval.GenreResolution{}
	unsupported := []lastfmeval.GenreResolution{}
	for _, g := range c.Genres {
		resolved := r.ResolveGenre(c, g.ID, false)
		resolutions = append(resolutions, resolved)
		coverageCounts[resolved.Status]++
		if resolved.Status == lastfmeval.ResolutionUnsupported {
			unsupported = append(unsupported, resolved)
		}
	}
	metadata := map[string]any{"generated_at": time.Now().UTC().Format(time.RFC3339Nano), "policy_version": r.Policy.Version, "registry_version": r.Version, "registry_sha256": checksum(registryBytes), "canonical_taxonomy_version": c.Version, "canonical_taxonomy_sha256": checksum(taxonomyBytes), "registry_source": registryPath, "canonical_source": taxonomyPath, "evidence_snapshot": evidencePath, "evidence_sha256": checksum(evidenceBytes), "freeze_scope": "reproducible recommendation experiment baseline; not full product coverage certification or permanent immutability", "external_api_calls": 0}
	if e = os.WriteFile(filepath.Join(*out, "frozen-registry.json"), registryBytes, 0644); e != nil {
		return e
	}
	var night lastfmeval.Plan
	for _, f := range fixture.UniqueAnalyses {
		for _, source := range f.SourcePaths {
			if source == "../benchmark-results/live-v3-single/analysis.json" {
				night = f.Plan
			}
		}
	}
	a, _, e := lastfmeval.ReadAnalysis("../benchmark-results/e2e/analyze.json")
	if e != nil {
		return e
	}
	profile, e := lastfmeval.Profile(a)
	if e != nil {
		return e
	}
	plan, e := lastfmeval.BuildAutomaticPlan(profile, v, policy, c, r, false)
	if e != nil {
		return e
	}
	captured := &lastfmeval.CapturedRetriever{Tracks: map[string][]lastfm.Track{}}
	for _, t := range pool {
		if t.TracksSuccess {
			captured.Tracks[lastfm.Normalize(t.Tag)] = t.Tracks
		}
	}
	result, e := lastfmeval.Evaluate(context.Background(), plan, policy, captured)
	if e != nil {
		return e
	}
	result.DataMode = "saved captured still-life regression; no live API"
	oldBytes, e := os.ReadFile("artifacts/lastfm-final-evidence-batch-20261004T144123Z/regression-after.json")
	if e != nil {
		return e
	}
	var old lastfmeval.Evaluation
	if e = json.Unmarshal(oldBytes, &old); e != nil {
		return e
	}
	regressionStable := reflect.DeepEqual(old.Plan.Routes, result.Plan.Routes) && sameCandidates(old.Merged, result.Merged) && sameCandidates(old.Reranked, result.Reranked) && reflect.DeepEqual(old.Raw, result.Raw) && reflect.DeepEqual(old.After, result.After) && old.OverlapCount == result.OverlapCount
	if !regressionStable {
		return fmt.Errorf("still-life score/order/data regression changed; no ranking-readiness success claim")
	}
	blockers := []string{}
	if len(fixture.UniqueAnalyses) == 0 {
		blockers = append(blockers, "no valid fixture analyses")
	}
	zeroRoutes := 0
	for _, f := range fixture.UniqueAnalyses {
		if f.Plan.Coverage.UsableGenreRoutes == 0 {
			zeroRoutes++
		}
	}
	if len(fixture.UniqueAnalyses) > 0 && zeroRoutes*2 > len(fixture.UniqueAnalyses) {
		blockers = append(blockers, "most fixtures have zero usable genre routes")
	}
	for _, candidate := range result.Merged {
		if candidate.Retrieval == nil || candidate.Retrieval.UnclassifiedRouteCount > 0 {
			blockers = append(blockers, "candidate route provenance incomplete")
			break
		}
	}
	readiness := "READY_FOR_RANKING_EXPERIMENT"
	if len(blockers) > 0 {
		readiness = "BLOCKED"
	}
	values := map[string]any{
		"frozen-policy.json": r.Policy, "freeze-metadata.json": metadata, "coverage.json": map[string]any{"counts": coverageCounts, "resolutions": resolutions}, "unmapped-genres.json": unsupported,
		"fixture-coverage.json": fixture, "unsupported-observations.json": fixture.Observations, "night-city-regression.json": night,
		"still-life-regression.json":    map[string]any{"evaluation": result, "original_weight_score_order_data_preserved": regressionStable, "reference": "artifacts/lastfm-final-evidence-batch-20261004T144123Z/regression-after.json", "new_metadata_only": "genre_coverage, stable resolver reason codes, candidate retrieval provenance"},
		"ranking-signal-inventory.json": signalInventory(), "ranking-readiness.json": map[string]any{"status": readiness, "blockers": blockers, "unique_fixture_analyses": len(fixture.UniqueAnalyses), "fixtures_without_usable_genre_routes": zeroRoutes, "unsupported_genres_do_not_alone_block_ranking": true, "mapping_engineering": "paused at frozen policy v1; no automatic audits", "product_support": "partial; unsupported input remains visible", "external_api_calls": 0},
		"quota-timing.json": map[string]any{"lastfm_calls": 0, "gemini_calls": 0, "youtube_calls": 0, "captured_still_life_replay_ms": result.TotalMS},
	}
	for name, value := range values {
		if e = write(*out, name, value); e != nil {
			return e
		}
	}
	if e = writeSummary(*out, metadata, coverageCounts, unsupported, fixture, night, result, readiness); e != nil {
		return e
	}
	for name, text := range map[string]string{"ranking-data-model.md": modelNotes, "freeze-summary.md": freezeNotes} {
		if e = os.WriteFile(filepath.Join(*out, name), []byte(text), 0644); e != nil {
			return e
		}
	}
	// Check exact snapshot integrity, and deliberately never rewrite the live registry.
	frozen, e := os.ReadFile(filepath.Join(*out, "frozen-registry.json"))
	if e != nil {
		return e
	}
	current, e := os.ReadFile(registryPath)
	if e != nil {
		return e
	}
	if checksum(frozen) != checksum(current) {
		return fmt.Errorf("registry changed during freeze")
	}
	fmt.Printf("Saved %s\ncoverage=%v; unique offline analyses=%d; readiness=%s; external API calls=0\n", *out, coverageCounts, len(fixture.UniqueAnalyses), readiness)
	return nil
}
func sameCandidates(a, b []lastfmeval.CandidateTrack) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		x.Retrieval = nil
		y.Retrieval = nil
		if !reflect.DeepEqual(x, y) {
			return false
		}
	}
	return true
}
