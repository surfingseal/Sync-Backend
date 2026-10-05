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
	out := flag.String("out", "artifacts/lastfm-eligibility-supplement-"+time.Now().UTC().Format("20060102T150405Z"), "new directory")
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
	// This command probes only representations already observed in the prior audit.
	missing = coreNeeds(missing)
	beforeAutomatic := r
	beforePool := append([]lastfmeval.TagEvidence{}, pool...)
	selected := []lastfmeval.EvidenceNeed{}
	planned := 0
	for _, need := range missing {
		if planned+need.ExpectedCalls > 12 {
			break
		}
		selected = append(selected, need)
		planned += need.ExpectedCalls
	}
	for name, value := range map[string]any{"policy.json": lastfmeval.RetrievalPolicyV1(), "before-registry.json": beforeAutomatic, "offline-eligibility-decisions.json": r, "missing-evidence-plan.json": missing, "live-plan.json": map[string]any{"planned_http_calls": planned, "max_actual_http_attempts": 12, "selected_tags": selected, "live_requested": *live, "fresh_scope_selected_only": *fresh}} {
		if e = write(*out, name, value); e != nil {
			return e
		}
	}
	fmt.Printf("Offline assessment saved. planned_http_calls=%d (cap 12); selected tags=%d\n", planned, len(selected))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	key := os.Getenv("LASTFM_API_KEY")
	budget := &lastfmeval.AuditHTTPBudget{Limit: 12}
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
			t.Source = "current scoped supplement tracks; reused info: " + t.Source
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
	for name, value := range map[string]any{"eligibility-decisions.json": r, "updated-registry.json": r, "coverage.json": map[string]any{"before": coverage(c, func(id string) lastfmmapping.ResolvedProviderRoute { return beforeAutomatic.Resolve(c, id, false) }), "after": coverage(c, func(id string) lastfmmapping.ResolvedProviderRoute { return r.Resolve(c, id, false) })}} {
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
	beforePlan, e := lastfmeval.BuildAutomaticPlan(profile, v, policy, c, beforeAutomatic, false)
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
	// Automatic registry is experiment-only; legacy registry remains the baseline.
	if e = supplementReport(*out, c, beforeAutomatic, r, beforePool, pool, results, planned, events, budget.Used.Load(), performed, time.Since(start)); e != nil {
		return e
	}
	if e = write("internal/music", "lastfm_retrieval_registry.json", r); e != nil {
		return e
	}
	fmt.Printf("Saved: %s\nprovider statuses=%v; actual HTTP attempts=%d\n", *out, mappedCounts(r), budget.Used.Load())
	return ctx.Err()
}
func sortNeeds(needs []lastfmeval.EvidenceNeed) {
	sort.Slice(needs, func(i, j int) bool {
		if needs[i].Priority != needs[j].Priority {
			return needs[i].Priority < needs[j].Priority
		}
		return needs[i].Tag < needs[j].Tag
	})
}
