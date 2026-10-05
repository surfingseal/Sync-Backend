package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	analysis := flag.String("analysis", "", "saved Gemini analysis or analyze response; no new Gemini call")
	registry := flag.String("registry", "internal/music/lastfm_tag_registry.json", "audited registry")
	mapping := flag.String("mapping", "internal/music/lastfm_tag_mapping.json", "explicit mappings")
	live := flag.Bool("live", false, "explicitly enable controlled Last.fm retrieval")
	fresh := flag.Bool("fresh", false, "bypass all cached responses")
	dir := flag.String("out", "artifacts/lastfm-retrieval-"+time.Now().UTC().Format("20060102T150405Z"), "new output directory")
	cache := flag.String("cache-dir", "artifacts/lastfm-retrieval-cache", "retrieval-only cache; distinct from audit cache")
	photoID := flag.String("photo-id", "saved-analysis", "human evaluation photo identifier")
	limit := flag.Int("per-route-limit", 50, "1..50, default 50")
	concurrency := flag.Int("concurrency", 2, "1..3 bounded requests")
	maxGenres := flag.Int("max-genre-routes", 3, "1..3")
	maxRoutes := flag.Int("max-routes", 4, "1..4")
	moods := flag.Int("max-mood-routes", 1, "0..1 auxiliary route")
	moodOnly := flag.Bool("allow-mood-only", false, "explicit diagnostic override; disabled by default")
	cap := flag.Int("artist-cap", 2, "same normalized artist cap in Top candidates")
	top := flag.Int("top", 20, "candidate evaluation length; not production recommendation")
	timeout := flag.Duration("timeout", 45*time.Second, "whole retrieval timeout")
	routeTimeout := flag.Duration("route-timeout", 15*time.Second, "individual Last.fm call timeout")
	providerMapping := flag.String("provider-mapping", "internal/music/lastfm_retrieval_registry.json", "automatic eligibility provider registry (legacy file with --legacy-provider-policy)")
	legacyProvider := flag.Bool("legacy-provider-policy", false, "explicit pre-eligibility canonical mapping baseline")
	provisional := flag.Bool("allow-provisional", false, "experiment-only opt-in to provisional provider routes")
	legacy := flag.Bool("legacy-direct-mapping", false, "baseline experiment only: prior direct mapping policy")
	replayAudit := flag.String("replay-audit", "", "offline captured audit tracks; no network/key")
	replayRetrieval := flag.String("replay-retrieval", "", "optional captured evaluation tracks (overrides same-tag audit sample)")
	flag.Parse()
	if *legacyProvider {
		explicit := false
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "provider-mapping" {
				explicit = true
			}
		})
		if !explicit {
			*providerMapping = "internal/music/lastfm_genre_mapping.json"
		}
	}
	if *analysis == "" {
		return fmt.Errorf("--analysis is required")
	}
	if *timeout <= 0 || *routeTimeout <= 0 {
		return fmt.Errorf("timeouts must be positive")
	}
	if _, err := os.Stat(*dir); err == nil {
		return fmt.Errorf("output already exists; choose a new --out")
	}
	p := lastfmeval.DefaultPolicy()
	p.PerRouteLimit = *limit
	p.Concurrency = *concurrency
	p.MaxGenreRoutes = *maxGenres
	p.MaxRoutes = *maxRoutes
	p.MaxMoodRoutes = *moods
	p.MoodOnly = *moodOnly
	p.ArtistCap = *cap
	p.TopN = *top
	if err := p.Validate(); err != nil {
		return err
	}
	a, input, err := lastfmeval.ReadAnalysis(*analysis)
	if err != nil {
		return err
	}
	v, err := lastfmeval.LoadVocabulary(*registry, *mapping)
	if err != nil {
		return err
	}
	start := time.Now()
	profile, err := lastfmeval.Profile(a)
	if err != nil {
		return err
	}
	var plan lastfmeval.Plan
	if *legacy {
		plan, err = lastfmeval.BuildPlan(profile, v, p)
	} else {
		catalog := music.CanonicalTaxonomy()
		if *legacyProvider {
			var providerRegistry lastfmmapping.Registry
			providerRegistry, err = lastfmmapping.LoadRegistry(*providerMapping, catalog)
			if err == nil {
				plan, err = lastfmeval.BuildCanonicalPlan(profile, v, p, catalog, providerRegistry)
			}
		} else {
			var providerRegistry lastfmeval.AutomaticRegistry
			providerRegistry, err = lastfmeval.LoadAutomaticRegistry(*providerMapping, catalog)
			if err == nil {
				plan, err = lastfmeval.BuildAutomaticPlan(profile, v, p, catalog, providerRegistry, *provisional)
			}
		}
	}
	if err != nil {
		return err
	}
	mappingMS := float64(time.Since(start)) / float64(time.Millisecond)
	replay := *replayAudit != "" || *replayRetrieval != ""
	if replay && *live {
		return fmt.Errorf("replay and live are mutually exclusive")
	}
	if len(plan.Routes) > 0 && !*live && !replay {
		return fmt.Errorf("selected routes require --live; no external request was made")
	}
	key := os.Getenv("LASTFM_API_KEY")
	if len(plan.Routes) > 0 && key == "" && !replay {
		return fmt.Errorf("LASTFM_API_KEY is required for live retrieval")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	provider := lastfmeval.Provider{APIKey: key, CacheDir: *cache, Fresh: *fresh, Timeout: *routeTimeout}
	var retriever lastfmeval.Retriever = provider
	if replay {
		captured, loadErr := lastfmeval.LoadCaptured(*replayAudit, *replayRetrieval)
		if loadErr != nil {
			return loadErr
		}
		retriever = captured
		plan.Warnings = append(plan.Warnings, "OFFLINE captured-provider regression; no fresh Last.fm or Gemini calls; provider latency is not measured")
	}
	e, runErr := lastfmeval.Evaluate(ctx, plan, p, retriever)
	if replay {
		e.DataMode = "captured_offline"
	} else {
		e.DataMode = "live_or_response_cache"
	}
	e.MappingMS = mappingMS
	e.TotalMS += mappingMS
	if err = lastfmeval.WriteReport(*dir, input, e, *photoID); err != nil {
		return err
	}
	// Snapshot vocabulary and hashes for reproducibility; no credentials copied.
	inputs := map[string]string{}
	if !*legacy {
		b, readErr := os.ReadFile(*providerMapping)
		if readErr != nil {
			return readErr
		}
		inputs["provider_mapping_sha256"] = fmt.Sprintf("%x", sha256.Sum256(b))
		if err = os.WriteFile(filepath.Join(*dir, "input-provider-mapping.json"), b, 0644); err != nil {
			return err
		}
	}
	for name, path := range map[string]string{"registry": *registry, "mapping": *mapping} {
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		inputs[name+"_sha256"] = fmt.Sprintf("%x", sha256.Sum256(b))
		if err = os.WriteFile(filepath.Join(*dir, "input-"+name+".json"), b, 0644); err != nil {
			return err
		}
	}
	inputs["analysis_sha256"] = fmt.Sprintf("%x", sha256.Sum256(input))
	meta := struct {
		InputPath    string            `json:"input_path"`
		StartedAt    time.Time         `json:"started_at"`
		Fresh        bool              `json:"fresh"`
		Mode         string            `json:"mode"`
		Timeout      string            `json:"timeout"`
		RouteTimeout string            `json:"route_timeout"`
		Hashes       map[string]string `json:"hashes"`
	}{*analysis, start.UTC(), *fresh, "Last.fm only; saved Gemini analysis; no new Gemini call", timeout.String(), routeTimeout.String(), inputs}
	b, _ := json.MarshalIndent(meta, "", "  ")
	if err = os.WriteFile(filepath.Join(*dir, "run-config.json"), append(b, '\n'), 0644); err != nil {
		return err
	}
	fmt.Printf("outcome=%s routes=%d actual_calls=%d raw=%d unique=%d overlap=%d diverse_top=%d total_ms=%.1f\nSaved: %s\n", e.Outcome, len(plan.Routes), e.APICalls, len(e.Raw), len(e.Merged), e.OverlapCount, len(e.Reranked), e.TotalMS, *dir)
	return runErr
}
