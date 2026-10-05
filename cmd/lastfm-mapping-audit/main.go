package main

import (
	"context"
	"encoding/json"
	"example.com/sync/internal/lastfm"
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

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	prior := flag.String("evidence", "artifacts/lastfm-reviewed-20261004/lastfm_tag_audit.json", "previous reviewed tag audit JSON")
	out := flag.String("out", "artifacts/lastfm-mapping-"+time.Now().UTC().Format("20060102T150405Z"), "new artifact directory")
	genre := flag.String("genre", "", "canonical genre ID filter")
	family := flag.String("family", "", "existing Sync family ID filter")
	sample := flag.Int("sample", 20, "5..50 tracks per new tag probe")
	fresh := flag.Bool("fresh", false, "explicitly force selected tag probes, bypass cache")
	dry := flag.Bool("dry-run", false, "plan/reuse only; no API calls")
	live := flag.Bool("live", false, "explicit controlled live probes, requires existing OS key")
	concurrency := flag.Int("concurrency", 2, "1..3 simultaneous tag probes")
	max := flag.Int("max-probes", 12, "per-run probe cap; at most two API calls/probe")
	cache := flag.String("cache-dir", "artifacts/lastfm-mapping-cache", "dedicated response cache")
	runtimePath := flag.String("runtime-out", "", "optional runtime mapping JSON destination")
	taxonomyPath := flag.String("taxonomy-out", "", "optional derived taxonomy snapshot destination")
	observations := flag.String("unknown-input", "", "optional JSON array of raw labels for unknown aggregation")
	variants := flag.Bool("orthographic-variants", false, "optional independent punctuation/spacing probes; no semantic synonyms")
	flag.Parse()
	if *runtimePath != "" && *runtimePath == *taxonomyPath {
		return fmt.Errorf("runtime and taxonomy output paths must differ")
	}
	if _, e := os.Stat(*out); e == nil {
		return fmt.Errorf("output already exists; use a new directory")
	}
	b, e := os.ReadFile(*prior)
	if e != nil {
		return e
	}
	var evidence lastfm.Audit
	if e = json.Unmarshal(b, &evidence); e != nil {
		return e
	}
	catalog := music.CanonicalTaxonomy()
	opts := lastfmmapping.Options{Genre: *genre, Family: *family, Sample: *sample, Fresh: *fresh, DryRun: *dry, Live: *live, Concurrency: *concurrency, MaxProbes: *max, OrthographicVariants: *variants}
	a, e := lastfmmapping.Plan(catalog, &evidence, *prior, opts)
	if e != nil {
		return e
	}
	key := os.Getenv("LASTFM_API_KEY")
	if *live && !*dry && key == "" {
		a.Options.Live = false
		a.Warnings = append(a.Warnings, "LASTFM_API_KEY absent from OS environment; no live requests attempted")
	}
	if *observations != "" {
		b, e = os.ReadFile(*observations)
		if e != nil {
			return e
		}
		var labels []string
		if e = json.Unmarshal(b, &labels); e != nil {
			return e
		}
		a.Unknown = lastfmmapping.AggregateUnknown(catalog, labels)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	lastfmmapping.Execute(ctx, a, lastfmmapping.Provider{Key: key, CacheDir: *cache, Fresh: *fresh, Timeout: 15 * time.Second})
	if e = lastfmmapping.Write(*out, a, catalog); e != nil {
		return e
	}
	for path, data := range map[string]any{*runtimePath: a.Registry.Runtime(), *taxonomyPath: catalog} {
		if path == "" {
			continue
		}
		if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
			return e
		}
		b, e = json.MarshalIndent(data, "", "  ")
		if e != nil {
			return e
		}
		if e = os.WriteFile(path, append(b, '\n'), 0644); e != nil {
			return e
		}
	}
	fmt.Printf("canonical=%d families=%d reused_tags=%d planned_probes=%d executed_probes=%d actual_calls=%d elapsed_ms=%.1f\nSaved: %s\n", len(catalog.Genres), len(catalog.Families), a.EvidenceReused, len(a.Planned), len(a.Results), a.APICalls, a.ElapsedMS, *out)
	if ctx.Err() != nil {
		return fmt.Errorf("mapping audit canceled; partial artifacts saved")
	}
	if len(a.Results) > 0 {
		allFailed := true
		for _, r := range a.Results {
			if r.Error == "" {
				allFailed = false
			}
		}
		if allFailed {
			return fmt.Errorf("all live probes failed; artifacts saved")
		}
	}
	return nil
}
