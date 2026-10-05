package main

import (
	"context"
	"encoding/json"
	"example.com/sync/internal/lastfm"
	"example.com/sync/internal/lastfmeval"
	"example.com/sync/internal/lastfmmapping"
	"example.com/sync/internal/music"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

func write(dir, name string, value any) error {
	b, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0644)
}
func coverage(c music.CanonicalCatalog, resolve func(string) lastfmmapping.ResolvedProviderRoute) map[string]any {
	counts := map[string]int{"exact": 0, "alias": 0, "family_fallback": 0, "unmapped": 0}
	routes := []lastfmmapping.ResolvedProviderRoute{}
	for _, g := range c.Genres {
		r := resolve(g.ID)
		routes = append(routes, r)
		if g.Family != "other" {
			counts[r.ResolutionType]++
		}
	}
	return map[string]any{"music_genres": 59, "sentinels_excluded": 2, "counts": counts, "routes": routes}
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	out := flag.String("out", "artifacts/lastfm-eligibility-"+time.Now().UTC().Format("20060102T150405Z"), "new directory")
	live := flag.Bool("live", false, "explicit bounded evidence supplementation; OS key required")
	fresh := flag.Bool("fresh", false, "bypass cache only for selected missing-evidence tags")
	flag.Parse()
	start := time.Now()
	if _, e := os.Stat(*out); !os.IsNotExist(e) {
		return fmt.Errorf("output exists or cannot be checked")
	}
	if e := os.MkdirAll(*out, 0755); e != nil {
		return e
	}
	c := music.CanonicalTaxonomy()
	baseline, e := lastfmmapping.LoadRegistry("internal/music/lastfm_genre_mapping.json", c)
	if e != nil {
		return e
	}
	auditPath := "artifacts/lastfm-reviewed-20261004/lastfm_tag_audit.json"
	specificPath := "artifacts/lastfm-specificity-live-20261004/mappings.json"
	retrievalPath := "artifacts/lastfm-retrieval-live-20261004/evaluation.json"
	pool, e := lastfmeval.LoadEligibilityEvidence(auditPath, specificPath, retrievalPath, c)
	if e != nil {
		return e
	}
	a, input, e := lastfmeval.ReadAnalysis("../benchmark-results/e2e/analyze.json")
	if e != nil {
		return e
	}
	profile, e := lastfmeval.Profile(a)
	if e != nil {
		return e
	}
	r := lastfmeval.BuildAutomaticRegistry(c, baseline, pool)
	providerDecisions := []lastfmeval.EligibilityDecision{}
	for _, t := range pool {
		providerDecisions = append(providerDecisions, lastfmeval.DecideEligibility(t))
	}
	if e = write(*out, "offline-provider-evidence-decisions.json", providerDecisions); e != nil {
		return e
	}
	missing := lastfmeval.MissingEvidencePlan(c, r, pool, profile)
	// Existing baseline exact/alias observations get priority 3, below recent/families.
	oldTags := map[string]bool{}
	for _, m := range baseline.Genres {
		if m.Exact != nil {
			oldTags[lastfm.Normalize(m.Exact.Tag)] = true
		}
		for _, t := range m.Aliases {
			oldTags[lastfm.Normalize(t.Tag)] = true
		}
	}
	for i := range missing {
		if missing[i].Priority == 4 && oldTags[lastfm.Normalize(missing[i].Tag)] {
			missing[i].Priority = 3
		}
	}
	// Stable priority ordering, fixed before any live observations.
	sortNeeds(missing)
	selected := []lastfmeval.EvidenceNeed{}
	planned := 0
	for _, need := range missing {
		if planned+need.ExpectedCalls > 24 {
			break
		}
		selected = append(selected, need)
		planned += need.ExpectedCalls
	}
	for name, value := range map[string]any{"policy.json": lastfmeval.RetrievalPolicyV1(), "before-registry.json": baseline, "offline-eligibility-decisions.json": r, "missing-evidence-plan.json": missing, "live-plan.json": map[string]any{"planned_http_calls": planned, "max_actual_http_attempts": 24, "selected_tags": selected, "live_requested": *live, "fresh_scope_selected_only": *fresh}} {
		if e = write(*out, name, value); e != nil {
			return e
		}
	}
	fmt.Printf("Offline assessment saved. planned_http_calls=%d (cap 24); selected tags=%d\n", planned, len(selected))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	key := os.Getenv("LASTFM_API_KEY")
	budget := &lastfmeval.AuditHTTPBudget{Limit: 24}
	events := []lastfm.Event{}
	performed := false
	if *live && key != "" {
		performed = true
		for _, need := range selected {
			if ctx.Err() != nil {
				break
			}
			idx := -1
			for i, t := range pool {
				if lastfm.Normalize(t.Tag) == lastfm.Normalize(need.Tag) {
					idx = i
					break
				}
			}
			if idx < 0 {
				continue
			}
			t := pool[idx]
			client := lastfm.New(key)
			client.Fresh = *fresh
			client.CacheDir = "artifacts/lastfm-eligibility-cache"
			client.HTTP = &http.Client{Transport: budget, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			if need.NeedInfo {
				t.Info, e = client.GetInfo(ctx, t.Tag)
				t.InfoSuccess = e == nil
				if e == nil {
					t.InfoRecordedAt = time.Now().UTC().Format(time.RFC3339Nano)
				}
			}
			if need.NeedTracks && t.InfoSuccess && t.Info != nil && t.Info.Reach != nil && *t.Info.Reach >= 1000 {
				t.Tracks, e = client.GetTopTracks(ctx, t.Tag, 20)
				t.SampleRequested = 20
				t.TracksSuccess = e == nil
				t.RecordedAt = time.Now().UTC().Format(time.RFC3339Nano)
			}
			t.Source = "current bounded supplementation; prior info provenance preserved when not refreshed: " + t.Source
			t.Reused = false
			pool[idx] = t
			events = append(events, client.Events...)
			// No retry. Stop the controlled run on key/rate-limit failure.
			if err, ok := e.(*lastfm.Error); ok && (err.Code == 10 || err.Code == 26 || err.Code == 29 || err.Status == 429) {
				break
			}
		}
		if e = write(*out, "live-evidence.json", map[string]any{"observations": pool, "requests": events}); e != nil {
			return e
		}
	} else {
		fmt.Println("No live calls: --live and existing OS LASTFM_API_KEY are both required.")
	}
	r = lastfmeval.BuildAutomaticRegistry(c, baseline, pool)
	if e = r.Validate(c); e != nil {
		return e
	}
	for name, value := range map[string]any{"eligibility-decisions.json": r, "updated-registry.json": r, "coverage.json": map[string]any{"before": coverage(c, func(id string) lastfmmapping.ResolvedProviderRoute { return baseline.Resolve(c, id) }), "after": coverage(c, func(id string) lastfmmapping.ResolvedProviderRoute { return r.Resolve(c, id, false) })}} {
		if e = write(*out, name, value); e != nil {
			return e
		}
	}
	v, e := lastfmeval.LoadVocabulary("internal/music/lastfm_tag_registry.json", "internal/music/lastfm_tag_mapping.json")
	if e != nil {
		return e
	}
	policy := lastfmeval.DefaultPolicy()
	policy.PerRouteLimit = 20
	// Matched per-route limit 20. Legacy 10-track samples cause honest partial
	// before results rather than lowering the new policy or filling fixtures.
	captured := &lastfmeval.CapturedRetriever{Tracks: map[string][]lastfm.Track{}}
	for _, t := range pool {
		if t.TracksSuccess {
			captured.Tracks[lastfm.Normalize(t.Tag)] = t.Tracks
		}
	}
	beforePlan, e := lastfmeval.BuildCanonicalPlan(profile, v, policy, c, baseline)
	if e != nil {
		return e
	}
	afterPlan, e := lastfmeval.BuildAutomaticPlan(profile, v, policy, c, r, false)
	if e != nil {
		return e
	}
	results := map[string]*lastfmeval.Evaluation{}
	for name, plan := range map[string]lastfmeval.Plan{"before": beforePlan, "after": afterPlan} {
		result, err := lastfmeval.Evaluate(ctx, plan, policy, captured)
		result.DataMode = "mixed_vintage_saved_provider_regression"
		result.Warnings = append(result.Warnings, "captured comparison, not fresh E2E; identical limit 20; missing samples remain partial")
		if err != nil {
			result.Warnings = append(result.Warnings, "regression incomplete; no fabricated candidates")
		}
		results[name] = result
		if e = write(*out, "regression-"+name+".json", result); e != nil {
			return e
		}
		if e = lastfmeval.WriteReport(filepath.Join(*out, "regression-"+name), input, result, "saved-still-life"); e != nil {
			return e
		}
	}
	counts := map[string]int{"eligible": 0, "provisional": 0, "insufficient_evidence": 0, "rejected": 0}
	seen := map[string]bool{}
	oldNotEligible := []string{}
	familyNot := []string{}
	for _, m := range r.Genres {
		for _, t := range m.Tags {
			if !seen[t.Tag] {
				seen[t.Tag] = true
				counts[t.Status]++
			}
		}
	}
	for _, f := range r.Families {
		if f.Primary != nil && f.Primary.Status != "eligible" {
			familyNot = append(familyNot, f.Family+" → "+f.Primary.Tag+": "+strings.Join(f.Primary.Reasons, ", "))
		}
	}
	for _, m := range baseline.Genres {
		ts := m.Aliases
		if m.Exact != nil {
			ts = append(append([]lastfmmapping.ProviderTag{}, ts...), *m.Exact)
		}
		for _, old := range ts {
			if old.Status == "validated" {
				for _, t := range pool {
					if lastfm.Normalize(t.Tag) == lastfm.Normalize(old.Tag) {
						d := lastfmeval.DecideEligibility(t)
						if d.Status != "eligible" {
							oldNotEligible = append(oldNotEligible, old.Tag+": "+strings.Join(d.Reasons, ", "))
						}
					}
				}
			}
		}
	}
	endpoint := map[string]int{}
	cacheHits := 0
	for _, x := range events {
		if x.Cached {
			cacheHits++
		} else if x.HTTPAttempted {
			endpoint[x.Method]++
		}
	}
	if e = write(*out, "quota-timing.json", map[string]any{"actual_http_attempts": budget.Used.Load(), "calls_per_endpoint": endpoint, "retry_count": 0, "response_cache_hits": cacheHits, "reused_observation_count": reusedCount(pool), "total_elapsed_ms": float64(time.Since(start)) / float64(time.Millisecond), "live_performed": performed, "os_key_present": key != "", "gemini_calls": 0, "youtube_calls": 0}); e != nil {
		return e
	}
	summary := fmt.Sprintf("# Sync Last.fm retrieval eligibility policy v1\n\nHuman review is optional; no human scores generated. Reach threshold is operational policy, not a quality boundary. Production recommend, YouTube, Gemini schema, canonical taxonomy, scoring, diversity and OAuth/playlist unchanged.\n\n## Rules\n\nReach >=5000 eligible; 1000..4999 provisional; <1000/missing insufficient. Successful info + TopTracks, requested >=20, valid unique >=15, duplicate ratio <=10%%. Unique artists <8 limits to provisional if reach >=1000 and basic evidence passes. Top1 >40%% warning, >60%% provisional. Explicit noisy/rejection evidence only is rejected; transport failure is not rejection. Duplicate denominator is valid received records, identity uses existing transitive MBID + normalized artist/title merge. Diversity uses deduplicated tracks.\n\nEquivalent spelling selection: eligible status, completeness, artist diversity, severe concentration, reach, exact spelling tie-break, lexical. No related/fuzzy mapping. Existing factors 1/.95/.65. Existing family primary relationships remain unchanged and are independently re-evaluated.\n\n## Counts\n\nUnique mapped provider candidates: %v\n\nBefore: %v\n\nAfter: %v\n\n## Old validated no longer eligible\n\n%s\n\n## Family representatives not eligible\n\n%s\n\n## Calls\n\nLive performed=%t, actual HTTP attempts=%d, endpoints=%v, retry=0, cache hits=%d. Prior reuse is not an API cache hit. OS key absent => offline + plan only; no conversation key injection.\n\n## Regression\n\nSame saved still-life analysis, route budget 3 genres +1 mood, matched limit20, unchanged weights. Mixed-vintage captured data. Before raw=%d unique=%d artists=%d overlap=%d partial=%t; after raw=%d unique=%d artists=%d overlap=%d partial=%t. Local replay latency is not provider latency. See regression JSON for route counts/concentration.\n\n## Limits / next evidence\n\nEligibility means sufficient provider retrieval data, not genre accuracy, track identity, audio quality or recommendation satisfaction. New thresholds may reduce coverage. Missing plan is fixed before any live call. Next evidence: priorities in missing-evidence-plan.json (maximum 24 HTTP attempts per explicit run).\n", counts, coverage(c, func(id string) lastfmmapping.ResolvedProviderRoute { return baseline.Resolve(c, id) })["counts"], coverage(c, func(id string) lastfmmapping.ResolvedProviderRoute { return r.Resolve(c, id, false) })["counts"], strings.Join(oldNotEligible, "\n\n"), strings.Join(familyNot, "\n\n"), performed, budget.Used.Load(), endpoint, cacheHits, len(results["before"].Raw), len(results["before"].Merged), results["before"].After.UniqueArtists, results["before"].OverlapCount, results["before"].Partial, len(results["after"].Raw), len(results["after"].Merged), results["after"].After.UniqueArtists, results["after"].OverlapCount, results["after"].Partial)
	if e = os.WriteFile(filepath.Join(*out, "summary.md"), []byte(summary), 0644); e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(*out, "regression-comparison.md"), []byte(summary[strings.Index(summary, "## Regression"):]), 0644); e != nil {
		return e
	}
	// Automatic registry is experiment-only; legacy registry remains the baseline.
	if e = write("internal/music", "lastfm_retrieval_registry.json", r); e != nil {
		return e
	}
	fmt.Printf("Saved: %s\nprovider statuses=%v; actual HTTP attempts=%d\n", *out, counts, budget.Used.Load())
	return ctx.Err()
}
func reusedCount(e []lastfmeval.TagEvidence) int {
	n := 0
	for _, x := range e {
		if x.Reused {
			n++
		}
	}
	return n
}
func sortNeeds(needs []lastfmeval.EvidenceNeed) {
	sort.Slice(needs, func(i, j int) bool {
		if needs[i].Priority != needs[j].Priority {
			return needs[i].Priority < needs[j].Priority
		}
		return needs[i].Tag < needs[j].Tag
	})
}
