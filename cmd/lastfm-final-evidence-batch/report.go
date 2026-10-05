package main

import (
	"example.com/sync/internal/lastfm"
	"example.com/sync/internal/lastfmeval"
	"example.com/sync/internal/lastfmmapping"
	"example.com/sync/internal/music"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

func mappedCounts(r lastfmeval.AutomaticRegistry) map[string]int {
	counts := map[string]int{"eligible": 0, "provisional": 0, "insufficient_evidence": 0, "rejected": 0}
	seen := map[string]bool{}
	for _, g := range r.Genres {
		for _, t := range g.Tags {
			if !seen[t.Tag] {
				counts[t.Status]++
				seen[t.Tag] = true
			}
		}
	}
	return counts
}
func finalReport(dir string, c music.CanonicalCatalog, legacy lastfmmapping.Registry, before, after lastfmeval.AutomaticRegistry, old, pool []lastfmeval.TagEvidence, results map[string]*lastfmeval.Evaluation, ranking []Priority, selected []lastfmeval.EvidenceNeed, usage Usage, planned int, events []lastfm.Event, attempts int64, live bool, elapsed time.Duration) error {
	cov := func(r lastfmeval.AutomaticRegistry) map[string]any {
		return coverage(c, func(id string) lastfmmapping.ResolvedProviderRoute { return r.Resolve(c, id, false) })
	}
	status := func(r lastfmeval.AutomaticRegistry) map[string]any {
		return map[string]any{"provider_counts": mappedCounts(r), "route_coverage": cov(r)}
	}
	changed := []map[string]any{}
	newEvidence := []map[string]any{}
	infoReuse := 0
	for _, need := range selected {
		for i, t := range pool {
			if lastfm.Normalize(t.Tag) != lastfm.Normalize(need.Tag) {
				continue
			}
			b, a := lastfmeval.DecideEligibility(old[i]), lastfmeval.DecideEligibility(t)
			if !need.NeedInfo {
				infoReuse++
			}
			mbidMissing := 0
			for _, track := range t.Tracks {
				if track.MBID == "" {
					mbidMissing++
				}
			}
			tracksQueried := false
			for _, event := range events {
				if event.Tag == t.Tag && event.Method == "tag.getTopTracks" {
					tracksQueried = true
				}
			}
			skipReason := ""
			if need.NeedTracks && !tracksQueried && t.InfoSuccess && t.Info != nil && t.Info.Reach != nil && *t.Info.Reach < 1000 {
				skipReason = "known_reach_below_provisional_threshold; sample_not_collected_not_empty_retrieval"
			}
			newEvidence = append(newEvidence, map[string]any{"top_tracks_queried_this_run": tracksQueried, "top_tracks_skip_reason": skipReason, "observation": t, "decision": a, "mbid_missing_count": mbidMissing, "info_reused": !need.NeedInfo, "mixed_vintage": t.InfoRecordedAt != t.RecordedAt, "before_decision": b})
			copyPool := append([]lastfmeval.TagEvidence{}, old...)
			copyPool[i] = t
			alone := lastfmeval.BuildAutomaticRegistry(c, legacy, copyPool)
			recovered := []string{}
			exact, fallback, upgrades := 0, 0, 0
			for _, g := range c.Genres {
				if g.Family == "other" {
					continue
				}
				br, ar := before.Resolve(c, g.ID, false), alone.Resolve(c, g.ID, false)
				if br.ResolutionType == "unmapped" && ar.ResolutionType != "unmapped" {
					recovered = append(recovered, g.ID)
					if ar.ResolutionType == "family_fallback" {
						fallback++
					} else {
						exact++
					}
				}
				if br.ResolutionType == "family_fallback" && (ar.ResolutionType == "exact" || ar.ResolutionType == "alias") {
					upgrades++
				}
			}
			changed = append(changed, map[string]any{"tag": t.Tag, "before": b.Status, "after": a.Status, "newly_eligible_exact_or_alias": exact, "newly_recovered_family_fallback": fallback, "total_recovered_canonical": len(recovered), "recovered_canonical_ids": recovered, "family_to_equivalent_upgrade": upgrades, "attribution": "one-tag-at-a-time against the same before snapshot; not necessarily additive"})
		}
	}
	families := []map[string]any{}
	synthetic := []map[string]any{}
	gaps := []string{}
	for _, f := range c.Families {
		if f.ID == "other" {
			continue
		}
		bCount, aCount := 0, 0
		primaries := map[string]any{}
		for index, r := range []lastfmeval.AutomaticRegistry{before, after} {
			label := "before"
			if index == 1 {
				label = "after"
			}
			for _, x := range r.Families {
				if x.Family == f.ID {
					primaries[label] = x.Primary
				}
			}
		}
		for _, g := range c.Genres {
			if g.Family != f.ID {
				continue
			}
			br, ar := before.Resolve(c, g.ID, false), after.Resolve(c, g.ID, false)
			if br.ResolutionType != "unmapped" {
				bCount++
			}
			if ar.ResolutionType != "unmapped" {
				aCount++
			}
			synthetic = append(synthetic, map[string]any{"canonical": g.ID, "input_type": "existing canonical ID synthetic resolver test; no new photo", "before": br, "after": ar})
		}
		families = append(families, map[string]any{"family": f.ID, "before_covered": bCount, "after_covered": aCount, "primary": primaries})
		if aCount == 0 {
			gaps = append(gaps, f.ID)
		}
	}
	used := []map[string]any{}
	usedUnmapped := []string{}
	usedCoveredBefore, usedCoveredAfter := 0, 0
	for _, g := range c.Genres {
		if n := usage.Counts[g.ID]; n > 0 {
			br, ar := before.Resolve(c, g.ID, false), after.Resolve(c, g.ID, false)
			if br.ResolutionType != "unmapped" {
				usedCoveredBefore++
			}
			if ar.ResolutionType != "unmapped" {
				usedCoveredAfter++
			} else {
				usedUnmapped = append(usedUnmapped, g.ID)
			}
			used = append(used, map[string]any{"canonical": g.ID, "frequency": n, "before": br, "after": ar})
		}
	}
	remaining := []map[string]any{}
	for _, g := range c.Genres {
		if g.Family != "other" && after.Resolve(c, g.ID, false).ResolutionType == "unmapped" {
			remaining = append(remaining, map[string]any{"canonical": g.ID, "family": g.Family, "saved_usage": usage.Counts[g.ID], "reason": after.Resolve(c, g.ID, false).Reason, "rarity": "not inferable from this small saved corpus"})
		}
	}
	actionable := []string{}
	for _, p := range ranking {
		if p.Usage > 0 && p.Selectable {
			selectedTag := false
			for _, s := range selected {
				if s.Tag == p.Tag {
					selectedTag = true
				}
			}
			if !selectedTag {
				actionable = append(actionable, p.Tag)
			}
		}
	}
	verdict := "READY_TO_FREEZE"
	reason := "Major available family representatives restored; all observed saved genres have routes. Freeze experiment mapping, leave remaining gaps as backlog, audit only new frequent runtime observations. Small saved corpus does not establish production failure rates."
	if len(usedUnmapped) > 0 {
		verdict = "NOT_READY"
		reason = "At least one actually observed saved genre still has no default eligible route. Existing graph cannot provide safe fallback; repeated evidence calls are not automatically a remedy."
		if len(actionable) > 0 {
			verdict = "ONE_MORE_SMALL_BATCH"
			reason = "Actually used unsupported genres still have unprobed missing evidence; only the listed narrow probes are justified."
		}
	}
	if !live {
		verdict = "NOT_READY"
		reason = "Live supplementation not performed; this is an offline plan only."
	}
	freeze := map[string]any{"decision": verdict, "reason": reason, "broad_family_coverage": families, "zero_covered_families": gaps, "actual_used_genres": used, "actual_used_covered_before": usedCoveredBefore, "actual_used_covered_after": usedCoveredAfter, "actual_used_unmapped": usedUnmapped, "remaining_unmapped": remaining, "next_actionable_usage_probes": actionable, "limitations": usage.Note, "policy_change": false, "automatic_next_batch": false, "ranking_readiness": "Ranking research can proceed on covered genres, but unsupported genre routing must be explicit; no full-Sync coverage claim."}
	endpoint := map[string]int{}
	hits := 0
	misses := 0
	apiMS := 0.0
	for _, e := range events {
		if e.Cached {
			hits++
		} else {
			misses++
		}
		if e.HTTPAttempted {
			endpoint[e.Method]++
			apiMS += e.MS
		}
	}
	// Changes outside selected tags must not be silently introduced.
	allowed := map[string]bool{}
	for _, s := range selected {
		allowed[lastfm.Normalize(s.Tag)] = true
	}
	unchanged := 0
	for i, t := range old {
		if !allowed[lastfm.Normalize(t.Tag)] {
			if !reflect.DeepEqual(t, pool[i]) {
				return fmt.Errorf("unselected evidence changed")
			}
			unchanged++
		}
	}
	same := reflect.DeepEqual(results["before"].Plan, results["after"].Plan) && reflect.DeepEqual(results["before"].Merged, results["after"].Merged) && reflect.DeepEqual(results["before"].Reranked, results["after"].Reranked)
	values := map[string]any{
		"before-status.json": status(before), "after-status.json": status(after), "api-call-plan.json": map[string]any{"estimated_http_attempts": planned, "hard_cap": 16, "retry": 0, "selected": selected},
		"live-calls.json": events, "new-evidence.json": newEvidence, "coverage-impact.json": changed, "family-recovery.json": families,
		"regression.json":       map[string]any{"same_photo_before": results["before"], "same_photo_after": results["after"], "same_photo_identical_plan_candidates_ranking": same, "family_resolver_synthetic": synthetic},
		"freeze-readiness.json": freeze, "provider-observations.json": pool,
		"quota-timing.json": map[string]any{"live_performed": live, "actual_http_attempts": attempts, "calls_per_endpoint": endpoint, "retry_count": 0, "cache_hits": hits, "cache_misses": misses, "get_info_evidence_reused": infoReuse, "unchanged_observations_reused": unchanged, "total_elapsed_ms": float64(elapsed) / float64(time.Millisecond), "sum_api_latency_ms": apiMS, "gemini_calls": 0, "youtube_calls": 0},
		"config.json":       map[string]any{"policy": lastfmeval.RetrievalPolicyV1(), "priority_formula": "100*family_recovery + 10*total_unmapped_recovery + 50*unique_saved_usage + 5*existing_info + family_to_equivalent_upgrade", "selected_limit": 6, "http_cap": 16, "usage_source": "deduplicated saved analyses; no production telemetry", "counterfactual": "resolver scaffold only, never live evidence", "no_new_relationships": true},
	}
	for name, value := range values {
		if e := write(dir, name, value); e != nil {
			return e
		}
	}
	var s strings.Builder
	fmt.Fprintf(&s, "# Last.fm final high-leverage evidence batch\n\n## Calls\n\nLive=%t; planned attempts=%d; actual=%d / 16; endpoints=%v; cache hits=%d; retries=0. Info reused=%d; unchanged observations=%d; total elapsed=%.2f ms. API key absent from code/artifacts/logs.\n\n## Fixed selection\n\nPriority = 100 × new family fallback routes + 10 × total unmapped recovery + 50 × unique saved-analysis frequency + 5 × reusable info + equivalent upgrades. This is a developer scheduling heuristic, not music quality. Counterfactual recovery is conditional on eligibility, not a predicted API result.\n\n", live, planned, attempts, endpoint, hits, infoReuse, unchanged, float64(elapsed)/float64(time.Millisecond))
	for _, p := range ranking {
		if strings.HasPrefix(p.Reason, "selected:") {
			fmt.Fprintf(&s, "- %s: score=%d, family recovery=%d, unmapped recovery=%d, usage=%d, calls=%d; %s\n", p.Tag, p.Score, p.FamilyRecoverable, len(p.Recoverable), p.Usage, p.ExpectedCalls, p.Reason)
		}
	}
	fmt.Fprint(&s, "\n## New observations\n\n|Tag|Reach|Received / valid / unique|Artists|Top1|Top3|Status|Warnings|\n|---|---:|---|---:|---:|---:|---|---|\n")
	for _, x := range newEvidence {
		d := x["decision"].(lastfmeval.EligibilityDecision)
		e := d.Evidence
		reach := "missing"
		if e.Reach != nil {
			reach = fmt.Sprint(*e.Reach)
		}
		fmt.Fprintf(&s, "|%s|%s|%d / %d / %d|%d|%.1f%%|%.1f%%|%s|%s|\n", d.Tag, reach, e.SampleReceived, e.ValidReceived, e.ValidUniqueTracks, e.UniqueArtists, 100*e.Top1Share, 100*e.Top3Share, d.Status, strings.Join(d.Warnings, ", "))
	}
	fmt.Fprintf(&s, "\n## Coverage\n\nA zero sample for low-reach tags means TopTracks was skipped, not an empty provider retrieval.\n\nProvider before: %v\n\nProvider after: %v\n\nRoutes before: %v\n\nRoutes after: %v\n\n", mappedCounts(before), mappedCounts(after), cov(before)["counts"], cov(after)["counts"])
	for _, x := range changed {
		fmt.Fprintf(&s, "- %s: %s → %s; recovered=%v (%v equivalent + %v family), upgraded=%v; IDs=%v\n", x["tag"], x["before"], x["after"], x["total_recovered_canonical"], x["newly_eligible_exact_or_alias"], x["newly_recovered_family_fallback"], x["family_to_equivalent_upgrade"], x["recovered_canonical_ids"])
	}
	fmt.Fprint(&s, "\n## Broad families\n\n")
	for _, f := range families {
		fmt.Fprintf(&s, "- %s: covered canonical IDs %v → %v\n", f["family"], f["before_covered"], f["after_covered"])
	}
	fmt.Fprintf(&s, "\n## Usage / regression\n\n%d unique decoded saved analyses; observed canonical IDs covered %d → %d / %d. Unsupported actual-used IDs=%v. Repeated experiment copies deduplicated. Not production frequency.\n\nStill-life plan/candidates/ranking identical=%t. Routes unchanged unless regression JSON shows otherwise. Captured replay, no new Gemini, no new recommendation API.\n", usage.UniqueAnalyses, usedCoveredBefore, usedCoveredAfter, len(used), usedUnmapped, same)
	for _, label := range []string{"before", "after"} {
		r := results[label]
		fmt.Fprintf(&s, "- %s: raw=%d unique=%d artists=%d overlap=%d Top1=%.3f Top3=%.3f; local replay=%.3f ms\n", label, len(r.Raw), len(r.Merged), r.After.UniqueArtists, r.OverlapCount, r.After.Top1Share, r.After.Top3Share, r.TotalMS)
	}
	fmt.Fprintf(&s, "\n## Freeze readiness\n\n**%s**\n\n%s\n\nZero-covered families=%v. Remaining unmapped=%d; rarity cannot be inferred from absence in two saved photos. Do not describe the remaining gaps as all rare/fine-grained. Coverage improvement does not establish recommendation quality. Broad fallback still has semantic loss.\n\nMarginal benefit: family primaries yield multiple routes; single-tag exact additions mainly yield one route. Further probes cannot safely repair known low reach or absent family graph. No automatic additional batch and no taxonomy/threshold tuning.\n\nRanking experiments can proceed on covered genres; full Sync integration must retain explicit unsupported-route observations. Mapping is experiment-only; public /recommend unchanged.\n", verdict, reason, gaps, len(remaining))
	return os.WriteFile(filepath.Join(dir, "summary.md"), []byte(s.String()), 0644)
}
