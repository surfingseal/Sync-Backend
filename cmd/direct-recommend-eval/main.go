// direct-recommend-eval is an isolated, explicitly enabled live experiment.
// It never starts a public endpoint and never writes to a YouTube account.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	stdimage "image"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	"example.com/sync/internal/directmusic"
	imageproc "example.com/sync/internal/image"
	"example.com/sync/internal/model"
)

func write(dir, name string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0600)
}
func hash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	recheck := flag.String("recheck-recorded", "", "offline recheck a saved v2 run after defensive guard changes; no provider clients")
	live := flag.Bool("live", false, "explicitly enable paid Vertex and quota-limited YouTube reads")
	still := flag.String("still-life", "../../work/gemini-photos/scones.jpg", "actual still-life photo")
	night := flag.String("night-city", "/var/folders/n0/sqbsplq11w73n__bnvmkqhz00000gn/T/codex-clipboard-944761b6-a478-44c6-8279-12b5ee91b067.png", "actual night-city photo")
	out := flag.String("out", "artifacts/gemini-direct-resolver-v2-"+time.Now().UTC().Format("20060102T150405Z"), "new output directory; never overwrite")
	cachePath := flag.String("cache", ".cache/direct-resolver-v1.json", "local resolver cache; positive IDs revalidated")
	aliasPath := flag.String("title-aliases", "", "optional provider-proven, video/channel-bound title equivalence JSON; none by default")
	cfg := directmusic.DefaultConfig()
	flag.IntVar(&cfg.CandidateCount, "candidates", 20, "Gemini candidates 15–20")
	flag.IntVar(&cfg.FinalCount, "final", 10, "final tracks 1–10")
	flag.IntVar(&cfg.MaxSearchCalls, "max-search-calls", 20, "hard search.list cap per photo 1–20")
	flag.BoolVar(&cfg.EarlyStop, "early-stop", true, "stop when final diversity-valid target reached")
	timeout := flag.Duration("gemini-timeout", 60*time.Second, "isolated direct Gemini timeout")
	flag.Parse()
	if *recheck != "" {
		if *live {
			return fmt.Errorf("recheck cannot enable live calls")
		}
		return recheckRecorded(*recheck)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if *timeout <= 0 || *timeout > 2*time.Minute {
		return fmt.Errorf("invalid Gemini timeout")
	}
	if _, err := os.Stat(*out); !os.IsNotExist(err) {
		return fmt.Errorf("output exists or cannot be checked")
	}
	if err := os.MkdirAll(*out, 0700); err != nil {
		return err
	}
	missing := []string{}
	for _, name := range []string{"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "YOUTUBE_API_KEY"} {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			missing = append(missing, name)
		}
	}
	vc := config.Config{GoogleCloudProject: strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_PROJECT")), GoogleCloudLocation: "global", VertexModel: config.DefaultVertexModel, VertexThinkingLevel: "MEDIUM", VertexTimeout: *timeout, VertexRetryAttempts: 1, VertexRetryMode: "budget"}
	for key, dest := range map[string]*string{"GOOGLE_CLOUD_LOCATION": &vc.GoogleCloudLocation, "GEMINI_MODEL": &vc.VertexModel, "VERTEX_THINKING_LEVEL": &vc.VertexThinkingLevel} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			*dest = v
		}
	}
	if err := config.ValidateThinking(vc.VertexModel, vc.VertexThinkingLevel); err != nil {
		return fmt.Errorf("invalid model/thinking configuration")
	}
	schemaBytes, _ := json.Marshal(client.DirectMusicSchema(cfg.CandidateCount))
	frozen := map[string]any{"model": vc.VertexModel, "thinking_level": vc.VertexThinkingLevel, "region": cfg.Region, "direct_config": cfg, "image_max_dimension": 1920, "image_hard_max_bytes": imageproc.DefaultHardMaxBytes, "response_mime_type": "application/json", "max_output_tokens": 8192, "temperature": .7, "sdk_attempts": 1, "gemini_timeout_seconds": timeout.Seconds(), "live_enabled": *live, "max_photos": 2, "max_total_search_calls": 2 * cfg.MaxSearchCalls, "lastfm_live_calls": 0, "playlist_writes": 0}
	if err := write(*out, "config.json", frozen); err != nil {
		return err
	}
	if err := write(*out, "prompt-version.json", map[string]any{"prompt_version": client.DirectPromptVersion, "schema_version": model.DirectSchemaVersion, "resolver_version": directmusic.ResolverVersion, "prompt": client.DirectMusicPrompt, "prompt_sha256": hash([]byte(client.DirectMusicPrompt)), "schema": client.DirectMusicSchema(cfg.CandidateCount), "schema_sha256": hash(schemaBytes)}); err != nil {
		return err
	}
	if err := offlineReplay(*out, cfg); err != nil {
		return err
	}
	aliases, err := directmusic.LoadTitleAliases(*aliasPath)
	if err != nil {
		return err
	}
	if err = write(*out, "title-aliases.json", aliases); err != nil {
		return err
	}
	cache, err := directmusic.NewCache(*cachePath)
	if err != nil {
		return err
	}
	var generator client.DirectTrackRecommender
	var youtube client.MusicSearchClient
	if *live && len(missing) == 0 {
		generator, err = client.NewVertexDirectRecommender(context.Background(), vc)
		if err == nil {
			youtube, err = client.NewYouTubeClient(context.Background(), os.Getenv("YOUTUBE_API_KEY"))
		}
		if err != nil {
			write(*out, "preflight.json", map[string]any{"status": "BLOCKED", "error_code": "CLIENT_CONFIGURATION_ERROR"})
			return fmt.Errorf("direct client initialization failed; see credential configuration")
		}
	}
	processor, err := imageproc.NewProcessor(1920, imageproc.DefaultHardMaxBytes)
	if err != nil {
		return err
	}
	results := map[string]*directmusic.Result{}
	warnings := map[string]string{}
	stopYouTube := false
	for _, photo := range []struct{ id, path string }{{"still-life", *still}, {"night-city", *night}} {
		dir := filepath.Join(*out, "live-run", "per-photo", photo.id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		data, readErr := readImage(photo.path)
		if readErr != nil {
			warnings[photo.id] = "IMAGE_READ_FAILED"
			continue
		}
		dimensions, _, decodeErr := stdimage.DecodeConfig(bytes.NewReader(data))
		if decodeErr != nil {
			warnings[photo.id] = "IMAGE_DECODE_FAILED"
			continue
		}
		start := time.Now()
		processed, processErr := processor.Process(context.Background(), data, http.DetectContentType(data))
		if processErr != nil {
			warnings[photo.id] = "PREPROCESS_FAILED"
			continue
		}
		preprocessMS := float64(time.Since(start)) / float64(time.Millisecond)
		if err := write(dir, "image.json", map[string]any{"filename": filepath.Base(photo.path), "image_sha256": hash(data), "original_dimensions": []int{dimensions.Width, dimensions.Height}, "original_bytes": len(data), "processed_dimensions": []int{processed.Width, processed.Height}, "processed_bytes": processed.ProcessedSize, "processed_mime_type": processed.MIMEType, "preprocess_ms": preprocessMS, "image_persisted": false}); err != nil {
			return err
		}
		if !*live || len(missing) > 0 || stopYouTube {
			why := "LIVE_NOT_ENABLED"
			if len(missing) > 0 {
				why = "MISSING_CREDENTIALS"
			}
			if stopYouTube {
				why = "STOPPED_AFTER_QUOTA_ERROR"
			}
			warnings[photo.id] = why
			if err := write(dir, "diagnostics.json", map[string]any{"status": "NOT_RUN", "reason": why, "missing_environment_variable_names": missing, "external_calls": 0}); err != nil {
				return err
			}
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		service := directmusic.Service{Generator: generator, Resolver: &directmusic.Resolver{Client: youtube, Cache: cache, Config: cfg, Aliases: aliases}, Config: cfg}
		result, runErr := service.Run(ctx, processed.Data, processed.MIMEType)
		cancel()
		if result == nil {
			return fmt.Errorf("invalid direct service configuration")
		}
		results[photo.id] = result
		for name, v := range map[string]any{"gemini-candidates.json": result.Generated, "normalized-candidates.json": result.Normalized, "resolver-results.json": result.Resolutions, "final-tracks.json": result.Final, "diagnostics.json": result.Diagnostics, "timings.json": map[string]any{"preprocess_ms": preprocessMS, "upload_to_final_ms": preprocessMS + result.Diagnostics.TotalMS}} {
			if err := write(dir, name, v); err != nil {
				return err
			}
		}
		if runErr != nil {
			warnings[photo.id] = result.Diagnostics.ErrorCode
			if result.Diagnostics.ErrorCode == "YOUTUBE_QUOTA_EXCEEDED" {
				stopYouTube = true
			}
		}
		fmt.Printf("photo=%s generated=%d attempted=%d verified=%d final=%d search_calls=%d videos_calls=%d error=%s\n", photo.id, result.Diagnostics.Generated, result.Diagnostics.Attempted, result.Diagnostics.Verified, len(result.Final), result.Diagnostics.Calls.SearchCalls, result.Diagnostics.Calls.VideosCalls, result.Diagnostics.ErrorCode)
	}
	if err := comparison(*out, results); err != nil {
		return err
	}
	status := "NEEDS_MORE_EVALUATION"
	if len(results) < 2 || len(warnings) > 0 {
		status = "BLOCKED"
	}
	if err := write(*out, "run-status.json", map[string]any{"status": status, "live_photos_completed": len(results), "warnings": warnings, "human_review": "PENDING", "lastfm_calls": 0, "playlist_writes": 0}); err != nil {
		return err
	}
	if err := summary(*out, status, results, warnings); err != nil {
		return err
	}
	if err := v2Report(*out, status, results); err != nil {
		return err
	}
	fmt.Printf("status=%s artifacts=%s\n", status, *out)
	return nil
}
func readImage(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read image")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 10*1024*1024+1))
	if err != nil || len(b) > 10*1024*1024 {
		return nil, fmt.Errorf("invalid image size")
	}
	return b, nil
}
