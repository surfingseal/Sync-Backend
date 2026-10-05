// This command is an isolated, bounded experiment. It never writes runtime registries.
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
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func probes() []lastfmmapping.Probe {
	out := []lastfmmapping.Probe{}
	for _, id := range []string{"indie-folk", "chamber-pop", "bossa-nova"} {
		g, _ := music.CanonicalTaxonomy().Lookup(id)
		out = append(out, lastfmmapping.Probe{Canonical: id, Tag: lastfm.Normalize(g.Name), Kind: "exact"})
		for _, spelling := range lastfmmapping.Variants(g.Name) {
			out = append(out, lastfmmapping.Probe{Canonical: id, Tag: spelling, Kind: "orthographic_alias"})
		}
	}
	for _, p := range []lastfmmapping.Probe{{Canonical: "folk", Tag: "folk", Kind: "exact"}, {Canonical: "pop", Tag: "pop", Kind: "exact"}, {Canonical: "jazz", Tag: "jazz", Kind: "exact"}} {
		out = append(out, p)
	}
	return out
}

// Availability and usage select a PROPOSAL, never semantic validation.
func propose(results []lastfmmapping.ProbeResult) map[string]lastfmmapping.ProbeResult {
	out := map[string]lastfmmapping.ProbeResult{}
	usage := func(r lastfmmapping.ProbeResult) int64 {
		if r.Info == nil {
			return 0
		}
		if r.Info.Total != nil {
			return int64(*r.Info.Total)
		}
		if r.Info.Taggings != nil {
			return int64(*r.Info.Taggings)
		}
		return 0
	}
	for _, r := range results {
		if r.Probe.Canonical != "indie-folk" && r.Probe.Canonical != "chamber-pop" && r.Probe.Canonical != "bossa-nova" {
			continue
		}
		if r.Error != "" || len(r.Tracks) < 10 {
			continue
		}
		old, ok := out[r.Probe.Canonical]
		if !ok || usage(r) > usage(old) {
			out[r.Probe.Canonical] = r
		}
	}
	return out
}

func save(path string, value any) error {
	b, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}

func run() error {
	out := flag.String("out", "artifacts/lastfm-specificity-"+time.Now().UTC().Format("20060102T150405Z"), "new directory; never overwrite")
	live := flag.Bool("live", false, "explicit external API authorization; requires OS LASTFM_API_KEY")
	flag.Parse()
	if !*live || os.Getenv("LASTFM_API_KEY") == "" {
		return fmt.Errorf("--live and LASTFM_API_KEY environment variable required")
	}
	if _, e := os.Stat(*out); !os.IsNotExist(e) {
		return fmt.Errorf("output exists or cannot be checked")
	}
	priorBytes, e := os.ReadFile("artifacts/lastfm-reviewed-20261004/lastfm_tag_audit.json")
	if e != nil {
		return e
	}
	var prior lastfm.Audit
	if e = json.Unmarshal(priorBytes, &prior); e != nil {
		return e
	}
	catalog := music.CanonicalTaxonomy()
	a, e := lastfmmapping.Plan(catalog, &prior, "prior reviewed audit", lastfmmapping.Options{Sample: 20, Fresh: true, Live: true, Concurrency: 2, MaxProbes: 12})
	if e != nil {
		return e
	}
	a.Planned = probes()
	if len(a.Planned) > 12 {
		return fmt.Errorf("hard cap: maximum 12 probes / 24 HTTP calls")
	}
	fmt.Printf("Planned %d tags; maximum %d HTTP calls; sample=20; no retries; runtime registry unchanged\n", len(a.Planned), len(a.Planned)*2)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	lastfmmapping.Execute(ctx, a, lastfmmapping.Provider{Key: os.Getenv("LASTFM_API_KEY"), Fresh: true, Timeout: 15 * time.Second})
	if e = lastfmmapping.Write(*out, a, catalog); e != nil {
		return e
	}
	if a.APICalls > 24 {
		return fmt.Errorf("hard cap violated")
	}
	selected := propose(a.Results)
	if e = save(filepath.Join(*out, "proposed-mapping.json"), map[string]any{"status": "candidate", "human_review": "PENDING", "selection": "retrieval availability then reported usage; NOT genre purity", "proposals": selected}); e != nil {
		return e
	}
	// Fixed saved photo; fresh candidate captures + older acoustic/dreamy captures.
	captured, e := lastfmeval.LoadCaptured("artifacts/lastfm-reviewed-20261004/lastfm_tag_audit.json", "artifacts/lastfm-retrieval-live-20261004/evaluation.json")
	if e != nil {
		return e
	}
	for _, r := range a.Results {
		if r.Error == "" && len(r.Tracks) > 0 {
			captured.Tracks[lastfm.Normalize(r.Probe.Tag)] = r.Tracks
		}
	}
	analysis, input, e := lastfmeval.ReadAnalysis("../benchmark-results/e2e/analyze.json")
	if e != nil {
		return e
	}
	profile, e := lastfmeval.Profile(analysis)
	if e != nil {
		return e
	}
	v, e := lastfmeval.LoadVocabulary("internal/music/lastfm_tag_registry.json", "internal/music/lastfm_tag_mapping.json")
	if e != nil {
		return e
	}
	policy := lastfmeval.DefaultPolicy()
	policy.PerRouteLimit = 10
	for _, proposed := range []bool{false, true} {
		registry, e := lastfmmapping.LoadRegistry("internal/music/lastfm_genre_mapping.json", catalog)
		if e != nil {
			return e
		}
		name := "before"
		if proposed {
			name = "proposed-experiment"
			// Local counterfactual adapter ONLY: never serialize as validated registry.
			// It allows existing planning/weights to be measured without changing them.
			for i := range registry.Genres {
				if r, ok := selected[registry.Genres[i].CanonicalGenreID]; ok {
					p := lastfmmapping.ProviderTag{Tag: r.Probe.Tag, MappingType: r.Probe.Kind, Status: "validated", Category: "subgenre"}
					registry.Genres[i].Exact = nil
					registry.Genres[i].Aliases = nil
					if r.Probe.Kind == "exact" {
						registry.Genres[i].Exact = &p
					} else {
						registry.Genres[i].Aliases = []lastfmmapping.ProviderTag{p}
					}
				}
			}
		}
		plan, e := lastfmeval.BuildCanonicalPlan(profile, v, policy, catalog, registry)
		if e != nil {
			return e
		}
		plan.Warnings = append(plan.Warnings, "Mixed-vintage captured data: fresh specified tags, historical acoustic/dreamy; limit 10 matched; NOT fresh whole-pipeline E2E")
		if proposed {
			// Artifacts must retain candidate status even though the local adapter
			// passed the existing validated-only planner's gate for a what-if.
			for i := range plan.Mapped {
				if plan.Mapped[i].InputType == "genre" {
					if g, ok := catalog.Resolve(plan.Mapped[i].Input.Name); ok {
						if _, proposed := selected[g.ID]; proposed {
							plan.Mapped[i].Status = "candidate"
							plan.Mapped[i].Reason = "experimental candidate allowance; NOT semantic validation"
						}
					}
				}
			}
			for i := range plan.TaxonomyResolution.Genres {
				g := &plan.TaxonomyResolution.Genres[i]
				if g.Canonical != nil {
					if _, ok := selected[g.Canonical.ID]; ok {
						g.Provider.Reason = "experimental candidate route; human review PENDING; runtime unchanged"
					}
				}
			}
			for tag, sources := range plan.TaxonomyResolution.RouteSources {
				for i := range sources {
					if sources[i].Canonical != nil {
						if _, ok := selected[sources[i].Canonical.ID]; ok {
							sources[i].Provider.Reason = "experimental candidate route; human review PENDING; runtime unchanged"
						}
					}
				}
				plan.TaxonomyResolution.RouteSources[tag] = sources
			}
			plan.Warnings = append(plan.Warnings, "UNVALIDATED CANDIDATE WHAT-IF ONLY; planner allow adapter does not promote runtime evidence; human review pending")
		}
		result, evalErr := lastfmeval.Evaluate(ctx, plan, policy, captured)
		result.DataMode = "mixed_vintage_captured_counterfactual"
		if e = lastfmeval.WriteReport(filepath.Join(*out, name), input, result, "saved-still-life"); e != nil {
			return e
		}
		if evalErr != nil {
			fmt.Printf("%s: partial diagnostic results saved\n", name)
		}
	}
	fmt.Printf("actual_calls=%d endpoint_calls=%v elapsed_ms=%.1f; Saved: %s\n", a.APICalls, a.PerEndpoint, a.ElapsedMS, *out)
	return nil
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
