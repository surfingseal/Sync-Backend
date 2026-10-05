package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	"example.com/sync/internal/directbench"
	"example.com/sync/internal/directmusic"
	imageproc "example.com/sync/internal/image"
)

func write(dir, name string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0600)
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	manifest := flag.String("manifest", "benchmarks/direct/images.json", "real image manifest, hashed and deduplicated")
	live := flag.Bool("live", false, "explicitly enable live paid providers")
	maxImages := flag.Int("max-images", 30, "maximum unique images")
	output := flag.String("output", "artifacts/direct-production-benchmark-"+time.Now().UTC().Format("20060102T150405Z"), "new directory")
	cold := flag.Bool("cold-cache", true, "isolated empty cache for each image; primary benchmark")
	warm := flag.Bool("warm-cache", false, "secondary warm-cache measurement; never qualifies primary readiness")
	cachePath := flag.String("cache", ".cache/direct-resolver-v1.json", "warm cache only")
	searchBudget := flag.Int("max-search-calls", 50, "hard whole-run HTTP search budget; no Cloud remaining inferred")
	flag.Parse()
	if *maxImages < 1 || *maxImages > 100 || *searchBudget < 1 || *searchBudget > 1000 {
		return fmt.Errorf("invalid benchmark bounds")
	}
	if *warm {
		*cold = false
	}
	if !*warm && !*cold {
		return fmt.Errorf("choose cold-cache or warm-cache")
	}
	dataset, err := directbench.LoadManifest(*manifest)
	if err != nil {
		return err
	}
	if _, err = os.Stat(*output); !os.IsNotExist(err) {
		return fmt.Errorf("output exists or cannot be checked")
	}
	if err = os.MkdirAll(*output, 0700); err != nil {
		return err
	}
	count := len(dataset.Images)
	if count > *maxImages {
		count = *maxImages
	}
	selected := dataset.Images[:count]
	gates := directbench.DefaultGates()
	cfg := directmusic.DefaultConfig()
	vc := config.Config{GoogleCloudProject: os.Getenv("GOOGLE_CLOUD_PROJECT"), GoogleCloudLocation: "global", VertexModel: config.DefaultVertexModel, VertexThinkingLevel: "MEDIUM", VertexTimeout: 60 * time.Second, VertexRetryMode: "budget", VertexRetryAttempts: 1}
	missing := []string{}
	for _, n := range []string{"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "YOUTUBE_API_KEY"} {
		if os.Getenv(n) == "" {
			missing = append(missing, n)
		}
	}
	expected := count * cfg.MaxSearchCalls
	if expected > *searchBudget {
		expected = *searchBudget
	}
	fmt.Printf("preflight unique=%d selected=%d max_gemini_calls=%d max_search_http_calls=%d cache=%s live=%t\n", len(dataset.Images), count, count, expected, map[bool]string{true: "cold_per_image", false: "warm"}[*cold], *live)
	schemaJSON, _ := json.Marshal(client.DirectMusicSchema(cfg.CandidateCount))
	promptHash := fmt.Sprintf("%x", sha256.Sum256([]byte(client.DirectMusicPrompt)))
	frozen := map[string]any{"model": vc.VertexModel, "prompt_version": client.DirectPromptVersion, "prompt_sha256": promptHash, "schema_sha256": fmt.Sprintf("%x", sha256.Sum256(schemaJSON)), "generation_config": map[string]any{"temperature": .7, "max_output_tokens": 8192, "thinking": "MEDIUM", "response_mime_type": "application/json", "sdk_attempts": 1}, "preprocessing": map[string]any{"max_dimension": 1920, "hard_max_bytes": imageproc.DefaultHardMaxBytes, "existing_processor": true}, "resolver_policy": directmusic.ResolverVersion, "resolver_config": cfg, "cold_per_image": *cold, "whole_run_search_budget": *searchBudget, "max_images": count, "live": *live, "missing_environment_variable_names": missing, "human_review_gate": false}
	fixtures, safety := directbench.RunFixtures()
	regression, err := recordedRegression()
	if err != nil {
		return err
	}
	if regression.StructuralFailures > 0 {
		safety.FixtureFailures += regression.StructuralFailures
	}
	for name, v := range map[string]any{"config.json": frozen, "manifest.json": dataset, "image-hashes.json": selected, "adversarial-fixtures.json": directbench.AdversarialFixtures(), "regression-results.json": map[string]any{"adversarial_results": fixtures, "recorded": regression}, "promotion-gates.json": gates} {
		if err = write(*output, name, v); err != nil {
			return err
		}
	}
	samples := []directbench.Sample{}
	notRun := []map[string]string{}
	stopReason := ""
	totalSearch := 0
	if safety.FalsePositives+safety.FixtureFailures > 0 {
		stopReason = "SAFETY_REGRESSION"
	} else if !*live {
		stopReason = "LIVE_NOT_ENABLED"
	} else if len(missing) > 0 {
		stopReason = "MISSING_LIVE_ENVIRONMENT"
	}
	var generator client.DirectTrackRecommender
	var youtube client.MusicSearchClient
	if stopReason == "" {
		generator, err = client.NewVertexDirectRecommender(context.Background(), vc)
		if err == nil {
			youtube, err = client.NewYouTubeClient(context.Background(), os.Getenv("YOUTUBE_API_KEY"))
		}
		if err != nil {
			stopReason = "CLIENT_CONFIGURATION_ERROR"
		}
	}
	var warmCache *directmusic.Cache
	if *warm {
		warmCache, err = directmusic.NewCache(*cachePath)
		if err != nil {
			return err
		}
	}
	processor, _ := imageproc.NewProcessor(1920, imageproc.DefaultHardMaxBytes)
	for _, item := range selected {
		if stopReason == "" && totalSearch >= *searchBudget {
			stopReason = "RUN_SEARCH_BUDGET_EXHAUSTED"
		}
		if stopReason != "" {
			notRun = append(notRun, map[string]string{"image_id": item.ID, "reason": stopReason})
			continue
		}
		dir := filepath.Join(*output, "per-image", item.ID)
		if err = os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		started := time.Now()
		b, readErr := directbench.ReadImage(item.Path)
		if readErr != nil {
			stopReason = "BENCHMARK_IMAGE_READ_FAILED"
			notRun = append(notRun, map[string]string{"image_id": item.ID, "reason": stopReason})
			continue
		}
		if fmt.Sprintf("%x", sha256.Sum256(b)) != item.SHA256 {
			stopReason = "BENCHMARK_IMAGE_CHANGED"
			notRun = append(notRun, map[string]string{"image_id": item.ID, "reason": stopReason})
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		processed, processErr := processor.Process(ctx, b, http.DetectContentType(b))
		preMS := float64(time.Since(started)) / float64(time.Millisecond)
		if processErr != nil {
			cancel()
			stopReason = "BENCHMARK_PREPROCESS_FAILED"
			notRun = append(notRun, map[string]string{"image_id": item.ID, "reason": stopReason})
			continue
		}
		runCfg := cfg
		remaining := *searchBudget - totalSearch
		if runCfg.MaxSearchCalls > remaining {
			runCfg.MaxSearchCalls = remaining
		}
		cache := warmCache
		if *cold {
			cache, _ = directmusic.NewCache("")
		}
		service := directmusic.Service{Generator: generator, Resolver: &directmusic.Resolver{Client: youtube, Cache: cache, Config: runCfg}, Config: runCfg}
		result, runErr := service.Run(ctx, processed.Data, processed.MIMEType)
		cancel()
		if result == nil {
			return fmt.Errorf("invalid benchmark service configuration")
		}
		// Fit-score means are not benchmark or promotion metrics.
		result.Diagnostics.AverageFitScore = nil
		sample := directbench.Sample{ID: item.ID, Diagnostics: result.Diagnostics, PreprocessMS: preMS, PipelineMS: float64(time.Since(started)) / float64(time.Millisecond), Outcomes: map[string]int{}, Safety: directbench.AuditResult(result)}
		for _, r := range result.Resolutions {
			sample.Outcomes[r.Status]++
		}
		samples = append(samples, sample)
		totalSearch += result.Diagnostics.Calls.SearchCalls
		for name, v := range map[string]any{"gemini-candidates.json": result.Generated, "normalized-candidates.json": result.Normalized, "resolver-results.json": result.Resolutions, "final-tracks.json": result.Final, "diagnostics.json": sample, "image.json": map[string]any{"sha256": item.SHA256, "original_bytes": len(b), "processed_bytes": len(processed.Data), "processed_width": processed.Width, "processed_height": processed.Height, "mime_type": processed.MIMEType}} {
			if err = write(dir, name, v); err != nil {
				return err
			}
		}
		fmt.Printf("image=%s final=%d attempted=%d verified=%d primary=%d fallback=%d metadata=%d elapsed_ms=%.0f search_budget_remaining=%d\n", item.ID, len(result.Final), result.Diagnostics.Attempted, result.Diagnostics.Verified, result.Diagnostics.Calls.PrimaryCalls, result.Diagnostics.Calls.FallbackCalls, result.Diagnostics.Calls.VideosCalls, sample.PipelineMS, *searchBudget-totalSearch)
		if runErr != nil && (errors.Is(runErr, client.ErrYouTubeQuota) || errors.Is(runErr, client.ErrAIRateLimited)) {
			stopReason = "PROVIDER_QUOTA_OR_RATE_LIMIT"
		}
		if sample.Safety.Broad+sample.Safety.Substitute+sample.Safety.Bounds+sample.Safety.APIValid > 0 {
			stopReason = "SAFETY_REGRESSION"
		}
	}
	aggregate := directbench.Summarize(samples, safety)
	decision := directbench.Evaluate(aggregate, len(dataset.Images), *cold, gates)
	if len(samples) < 20 {
		decision.Reasons = append(decision.Reasons, "live_validation_incomplete")
	}
	if stopReason != "" {
		decision.Reasons = append(decision.Reasons, stopReason)
	}
	unresolved := map[string]int{}
	for _, s := range samples {
		for k, n := range s.Outcomes {
			unresolved[k] += n
		}
	}
	for name, v := range map[string]any{"aggregate.json": aggregate, "latency.json": map[string]any{"pipeline_p50_ms": aggregate.P50, "pipeline_p90_ms": aggregate.P90, "pipeline_p95_ms": aggregate.P95, "max_ms": aggregate.Max, "gemini_p50_ms": aggregate.GeminiP50, "gemini_p95_ms": aggregate.GeminiP95, "resolver_p50_ms": aggregate.ResolverP50, "resolver_p95_ms": aggregate.ResolverP95, "per_image": samples}, "quota.json": map[string]any{"search_calls": aggregate.SearchCalls, "primary_calls": aggregate.PrimaryCalls, "fallback_calls": aggregate.FallbackCalls, "videos_calls": aggregate.VideosCalls, "http_calls": aggregate.SearchCalls + aggregate.VideosCalls, "max_search_budget": *searchBudget, "search_budget_remaining": *searchBudget - aggregate.SearchCalls, "cloud_quota_remaining": "unknown", "cache_hits": aggregate.CacheHits, "cache_misses": aggregate.CacheMisses, "negative_state": "empty per image in cold mode", "lastfm_calls": 0, "playlist_writes": 0}, "unresolved-reasons.json": unresolved, "promotion-decision.json": decision, "not-run.json": notRun} {
		if err = write(*output, name, v); err != nil {
			return err
		}
	}
	if err = report(*output, dataset, aggregate, decision); err != nil {
		return err
	}
	fmt.Printf("status=%s artifacts=%s\n", decision.Status, *output)
	return nil
}
