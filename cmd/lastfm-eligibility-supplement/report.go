package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"example.com/sync/internal/lastfm"
	"example.com/sync/internal/lastfmeval"
	"example.com/sync/internal/lastfmmapping"
	"example.com/sync/internal/music"
)

func mappedCounts(r lastfmeval.AutomaticRegistry) map[string]int {
	out := map[string]int{"eligible": 0, "provisional": 0, "insufficient_evidence": 0, "rejected": 0}
	seen := map[string]bool{}
	for _, g := range r.Genres {
		for _, t := range g.Tags {
			if !seen[t.Tag] {
				seen[t.Tag] = true
				out[t.Status]++
			}
		}
	}
	return out
}
func observations(pool []lastfmeval.TagEvidence, tags ...string) []lastfmeval.TagEvidence {
	out := []lastfmeval.TagEvidence{}
	for _, tag := range tags {
		for _, t := range pool {
			if lastfm.Normalize(tag) == lastfm.Normalize(t.Tag) {
				out = append(out, t)
			}
		}
	}
	return out
}
func supplementReport(dir string, c music.CanonicalCatalog, before, after lastfmeval.AutomaticRegistry, old, pool []lastfmeval.TagEvidence, results map[string]*lastfmeval.Evaluation, planned int, events []lastfm.Event, attempts int64, live bool, elapsed time.Duration) error {
	targets := []string{"Classical", "Hip-Hop", "hip hop", "electronic"}
	config := map[string]any{"policy": lastfmeval.RetrievalPolicyV1(), "targets": targets, "sample_limit": 20, "http_attempt_hard_cap": 12, "retry": 0, "scope": "evidence supplementation only; no schema/family/scoring changes", "info_reuse": "existing successful info with reach; mixed-vintage timestamps retained", "top_tracks_documentation": "https://www.last.fm/api/show/tag.getTopTracks", "info_documentation": "https://www.last.fm/api/show/tag.getInfo"}
	status := func(r lastfmeval.AutomaticRegistry) map[string]any {
		return map[string]any{"provider_counts": mappedCounts(r), "coverage": coverage(c, func(id string) lastfmmapping.ResolvedProviderRoute { return r.Resolve(c, id, false) })}
	}
	familyChanges := []map[string]any{}
	for _, family := range []string{"classical", "hip-hop", "electronic"} {
		tags := map[string]any{}
		transitions := []string{}
		usableBefore, usableAfter := 0, 0
		for idx, r := range []lastfmeval.AutomaticRegistry{before, after} {
			for _, f := range r.Families {
				if f.Family == family {
					label := "after"
					if idx == 0 {
						label = "before"
					}
					tags[label] = f.Primary
				}
			}
		}
		for _, g := range c.Genres {
			if g.Family != family {
				continue
			}
			b, a := before.Resolve(c, g.ID, false), after.Resolve(c, g.ID, false)
			if b.ResolutionType == "family_fallback" {
				usableBefore++
			}
			if a.ResolutionType == "family_fallback" {
				usableAfter++
			}
			if b.ResolutionType == "unmapped" && a.ResolutionType == "family_fallback" {
				transitions = append(transitions, g.ID)
			}
		}
		familyChanges = append(familyChanges, map[string]any{"family": family, "primary": tags, "before_fallback_routes": usableBefore, "after_fallback_routes": usableAfter, "unmapped_to_family_fallback": transitions})
	}
	synthetic := []map[string]any{}
	for _, g := range c.Genres {
		if g.Family == "classical" || g.Family == "hip-hop" || g.Family == "electronic" {
			synthetic = append(synthetic, map[string]any{"input_type": "synthetic existing canonical ID; resolver only, no retrieval", "canonical": g.ID, "before": before.Resolve(c, g.ID, false), "after": after.Resolve(c, g.ID, false)})
		}
	}
	hipRoute := after.Resolve(c, "hip-hop", false)
	hipCandidates := []lastfmeval.AutomaticTag{}
	for _, g := range after.Genres {
		if g.Canonical == "hip-hop" {
			hipCandidates = g.Tags
		}
	}
	comparator := map[string]any{"canonical": "hip-hop", "candidates_independent": hipCandidates, "selected_route": hipRoute, "order": []string{"status", "min(unique_tracks,20)", "unique_artists", "severe_top1_above_0.60", "reach", "exact_spelling", "lexical"}, "implementation": "AutomaticRegistry.Resolve; unchanged v1"}
	endpoint := map[string]int{}
	cacheHits := 0
	for _, e := range events {
		if e.Cached {
			cacheHits++
		} else if e.HTTPAttempted {
			endpoint[e.Method]++
		}
	}
	unchanged := []string{}
	changed := []string{}
	for i, t := range old {
		if !reflect.DeepEqual(lastfmeval.DecideEligibility(t), lastfmeval.DecideEligibility(pool[i])) {
			changed = append(changed, t.Tag)
		} else {
			unchanged = append(unchanged, t.Tag)
		}
	}
	values := map[string]any{
		"config.json": config, "before-status.json": status(before), "after-status.json": status(after),
		"existing-evidence.json": observations(old, targets...), "api-call-plan.json": map[string]any{"estimated_http_attempts": planned, "cap": 12, "targets": targets, "get_info_reused": 4, "top_tracks_limit": 20},
		"live-calls.json": events, "classical-evidence.json": observations(pool, "Classical"), "hip-hop-variants.json": observations(pool, "Hip-Hop", "hip hop"), "electronic-evidence.json": observations(pool, "electronic"),
		"equivalent-selection.json": comparator, "family-coverage-before-after.json": familyChanges,
		"regression.json":            map[string]any{"same_photo_before": results["before"], "same_photo_after": results["after"], "synthetic_family_resolver": synthetic, "changed_evidence_tags": changed, "unchanged_observations": len(unchanged)},
		"quota-timing.json":          map[string]any{"actual_http_attempts": attempts, "calls_per_endpoint": endpoint, "cache_hits": cacheHits, "reused_info_count": 4, "reused_unchanged_observations": len(unchanged), "retry_count": 0, "live_performed": live, "total_elapsed_ms": float64(elapsed) / float64(time.Millisecond), "gemini_calls": 0, "youtube_calls": 0},
		"provider-observations.json": pool,
	}
	for name, v := range values {
		if err := write(dir, name, v); err != nil {
			return err
		}
	}
	var summary strings.Builder
	fmt.Fprintf(&summary, "# Last.fm policy v1 — scoped Top20 evidence supplement\n\nLive performed: %t. HTTP attempts: %d / 12; endpoints: %v; retries: 0; cache hits: %d. Existing getInfo reused for four targets. Reused reach is not a fresh usage measurement.\n\nPolicy, canonical taxonomy, family graph, production endpoints and ranking are unchanged. No semantic/audio quality certification.\n\n## Target evidence\n\n|Provider tag|Reach (reused)|Unique tracks|Artists|Top1|Top3|Status|Warnings|\n|---|---:|---:|---:|---:|---:|---|---|\n", live, attempts, endpoint, cacheHits)
	for _, t := range observations(pool, targets...) {
		d := lastfmeval.DecideEligibility(t)
		reach := "missing"
		if d.Evidence.Reach != nil {
			reach = fmt.Sprint(*d.Evidence.Reach)
		}
		fmt.Fprintf(&summary, "|%s|%s|%d|%d|%.1f%%|%.1f%%|%s|%s|\n", t.Tag, reach, d.Evidence.ValidUniqueTracks, d.Evidence.UniqueArtists, 100*d.Evidence.Top1Share, 100*d.Evidence.Top3Share, d.Status, strings.Join(d.Warnings, ", "))
	}
	fmt.Fprintf(&summary, "\n## Coverage\n\nMapped provider counts before: %v\n\nAfter: %v\n\nRoutes before: %v\n\nAfter: %v\n\nHip-hop comparator selected: %s (%s). See equivalent-selection.json for independent evidence and comparator inputs.\n", mappedCounts(before), mappedCounts(after), status(before)["coverage"], status(after)["coverage"], hipRoute.ProviderTag, hipRoute.ResolutionType)
	// Avoid dumping full coverage routes in prose: detailed JSON preserves them.
	text := summary.String()
	start := strings.Index(text, "Routes before:")
	end := strings.Index(text, "Hip-hop comparator selected:")
	text = text[:start] + fmt.Sprintf("Route counts before: %v\n\nAfter: %v\n\n", coverage(c, func(id string) lastfmmapping.ResolvedProviderRoute { return before.Resolve(c, id, false) })["counts"], coverage(c, func(id string) lastfmmapping.ResolvedProviderRoute { return after.Resolve(c, id, false) })["counts"]) + text[end:]
	text += "\n## Family fallback changes\n\n"
	for _, f := range familyChanges {
		text += fmt.Sprintf("- %s: fallback routes %v → %v; newly recovered: %v\n", f["family"], f["before_fallback_routes"], f["after_fallback_routes"], f["unmapped_to_family_fallback"])
	}
	text += "\n## Saved still-life regression\n\nCaptured same-photo replay, not fresh E2E; no Gemini or provider retrieval. Info/track timestamps are mixed-vintage and retained independently. New Top20 replaces the old Top10, not concatenation.\n\n"
	for _, label := range []string{"before", "after"} {
		r := results[label]
		text += fmt.Sprintf("- %s: raw=%d unique=%d artists=%d overlap=%d Top1=%.3f Top3=%.3f\n", label, len(r.Raw), len(r.Merged), r.After.UniqueArtists, r.OverlapCount, r.After.Top1Share, r.After.Top3Share)
	}
	text += "\n## Interpretation\n\nCoverage recovery is not recommendation quality improvement. Family fallbacks retain their explicit old representative and do not automatically adopt an equivalent spelling chosen for the exact route. Eligibility certifies evidence sufficiency, not genre precision or listening quality. Thresholds were not tuned. Missing fields remain missing. Next batch can use the same bounded supplementation procedure for remaining Top10-only families, without assuming they will pass.\n\n## Sources\n\n[Official tag.getTopTracks](https://www.last.fm/api/show/tag.getTopTracks), [official tag.getInfo](https://www.last.fm/api/show/tag.getInfo).\n"
	return os.WriteFile(filepath.Join(dir, "summary.md"), []byte(text), 0644)
}

func coreNeeds(missing []lastfmeval.EvidenceNeed) []lastfmeval.EvidenceNeed {
	out := []lastfmeval.EvidenceNeed{}
	for _, tag := range []string{"Classical", "Hip-Hop", "hip hop", "electronic"} {
		for _, n := range missing {
			if lastfm.Normalize(tag) == lastfm.Normalize(n.Tag) {
				out = append(out, n)
			}
		}
	}
	return out
}
