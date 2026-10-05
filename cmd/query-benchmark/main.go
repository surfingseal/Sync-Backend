// Explicit Vertex TEXT-only benchmark. Never calls Recommend or YouTube clients.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	"example.com/sync/internal/model"
	"example.com/sync/internal/service"
)

type input struct {
	ID       string              `json:"id"`
	Analysis model.ImageAnalysis `json:"analysis"`
}
type record struct {
	PhotoID          string              `json:"photo_id"`
	Generator        string              `json:"generator"`
	Profile          string              `json:"profile"`
	TimeoutSeconds   float64             `json:"timeout_seconds"`
	InputSHA256      string              `json:"input_sha256"`
	Success          bool                `json:"success"`
	LatencyMS        float64             `json:"latency_ms"`
	Timeout          bool                `json:"timeout"`
	ErrorClass       string              `json:"error_class,omitempty"`
	Queries          []string            `json:"queries"`
	QueryCount       int                 `json:"query_count"`
	FallbackUsed     bool                `json:"fallback_used"`
	SchemaSuccess    bool                `json:"schema_success"`
	MetricsAvailable bool                `json:"response_metrics_available"`
	Metrics          client.CallSnapshot `json:"metrics"`
}

type queryGenerator interface {
	GenerateMusicQueries(context.Context, model.ImageAnalysis, model.MusicPreferences, int) ([]string, error)
}

func measure(ctx context.Context, generator queryGenerator, item input, prefs model.MusicPreferences, name, profile string, timeout time.Duration) record {
	payload, _ := json.Marshal(struct {
		Analysis    model.ImageAnalysis    `json:"analysis"`
		Preferences model.MusicPreferences `json:"preferences"`
	}{item.Analysis, prefs})
	digest := sha256.Sum256(payload)
	r := record{PhotoID: item.ID, Generator: name, Profile: profile, TimeoutSeconds: timeout.Seconds(), InputSHA256: hex.EncodeToString(digest[:]), Queries: []string{}}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ctx, metrics := client.MeasureCall(ctx)
	start := time.Now()
	queries, err := generator.GenerateMusicQueries(ctx, item.Analysis, prefs, 2)
	r.LatencyMS = float64(time.Since(start)) / float64(time.Millisecond)
	r.Metrics = metrics.Snapshot()
	r.MetricsAvailable = r.Metrics.OutputBytes > 0
	r.Success = err == nil
	r.SchemaSuccess = r.Success
	if err == nil {
		r.Queries = queries
		r.QueryCount = len(queries)
	} else {
		r.ErrorClass = classify(err)
		r.Timeout = errors.Is(err, context.DeadlineExceeded)
	}
	return r
}
func classify(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, client.ErrInvalidAIResponse):
		return "invalid_response"
	case errors.Is(err, client.ErrAIRateLimited):
		return "rate_limited"
	case errors.Is(err, client.ErrAIUnavailable):
		return "unavailable"
	case errors.Is(err, client.ErrAIConfiguration):
		return "configuration"
	default:
		return "provider_error"
	}
}
func writeJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	dataset := flag.String("dataset", "", "saved analysis JSON")
	out := flag.String("out", "", "NEW result directory; never overwritten")
	live := flag.Bool("live", false, "explicitly enable 36 Vertex text-only calls")
	flag.Parse()
	if *dataset == "" || *out == "" {
		return fmt.Errorf("dataset and out required")
	}
	raw, err := os.ReadFile(*dataset)
	if err != nil {
		return fmt.Errorf("dataset unavailable")
	}
	var items []input
	if json.Unmarshal(raw, &items) != nil || len(items) != 12 {
		return fmt.Errorf("exactly 12 saved analyses required")
	}
	seen := map[string]bool{}
	for _, item := range items {
		if item.ID == "" || seen[item.ID] {
			return fmt.Errorf("invalid/duplicate photo ID")
		}
		seen[item.ID] = true
		b, _ := json.Marshal(item.Analysis)
		if _, err := model.DecodeImageAnalysis(b); err != nil {
			return fmt.Errorf("invalid saved analysis: %s", item.ID)
		}
	}
	if _, err := os.Stat(*out); !os.IsNotExist(err) {
		return fmt.Errorf("refusing existing/unavailable output directory")
	}
	prefs := model.MusicPreferences{Languages: []string{"ko", "en"}, Count: 10}
	profiles := []time.Duration{service.ProductionQueryTimeout, 10 * time.Second, 15 * time.Second}
	cfg := config.Config{GoogleCloudProject: os.Getenv("GOOGLE_CLOUD_PROJECT"), GoogleCloudLocation: os.Getenv("GOOGLE_CLOUD_LOCATION"), VertexModel: os.Getenv("VERTEX_MODEL"), VertexTimeout: 15 * time.Second, VertexRetryAttempts: 1, VertexRetryMode: "sdk", VertexThinkingLevel: "LOW"}
	if cfg.GoogleCloudLocation == "" {
		cfg.GoogleCloudLocation = "global"
	}
	if cfg.VertexModel == "" {
		cfg.VertexModel = config.DefaultVertexModel
	}
	if cfg.VertexModel != config.DefaultVertexModel {
		return fmt.Errorf("this controlled benchmark requires the current default model")
	}
	planned := 0
	if *live {
		planned = len(items) * len(profiles)
	}
	fmt.Printf("PLAN saved_analyses=%d Vertex_TEXT_logical_calls=%d attempts_per_call=1 YouTube=0 image_analysis=0 user_OAuth=0 playlist=0 fallback=false\n", len(items), planned)
	var vertex queryGenerator
	if *live {
		v, err := client.NewVertexImageAnalyzer(context.Background(), cfg)
		if err != nil {
			return fmt.Errorf("Vertex initialization failed (check project/ADC; details suppressed)")
		}
		vertex = v
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(*out, 0700); err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	conf := map[string]any{"dataset": *dataset, "dataset_sha256": hex.EncodeToString(digest[:]), "saved_analysis_count": len(items), "vertex_text_logical_calls_planned": planned, "query_count": 2, "profiles_seconds": []float64{profiles[0].Seconds(), 10, 15}, "model": cfg.VertexModel, "location": cfg.GoogleCloudLocation, "thinking": "LOW", "max_output_tokens": 1024, "prompt_variant": "current_production_prompt", "prompt_bytes": len(client.MusicQueryPrompt), "prompt_runes": len([]rune(client.MusicQueryPrompt)), "full_analysis_including_scene_description": true, "preferences": prefs, "structured_output": true, "attempts_per_logical_call": 1, "fallback_used": false, "order": "dataset order; timeout profiles rotated per photo to reduce fixed-order bias", "go_sdk": "google.golang.org/genai v1.72.0", "internal_targets": map[string]any{"success_rate": 0.98, "p50_ms": 2000, "p95_ms": 4000, "schema_success_rate": 0.99, "fallback_reliance": 0.02}, "prohibited_api_calls": map[string]int{"youtube_search_list": 0, "youtube_videos_list": 0, "image_analysis": 0, "user_oauth": 0, "playlist_write": 0}}
	if err := writeJSON(filepath.Join(*out, "config.json"), conf); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(*out, "raw.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	failures, err := os.OpenFile(filepath.Join(*out, "failures.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer failures.Close()
	enc := json.NewEncoder(f)
	failEnc := json.NewEncoder(failures)
	for i, item := range items {
		baseline := measure(context.Background(), client.DeterministicQueryBuilder{}, item, prefs, "deterministic", "local", time.Second)
		if err := enc.Encode(baseline); err != nil {
			return err
		}
		if !baseline.Success {
			if err := failEnc.Encode(baseline); err != nil {
				return err
			}
		}
		if !*live {
			continue
		}
		for j := range profiles {
			timeout := profiles[(i+j)%len(profiles)]
			r := measure(context.Background(), vertex, item, prefs, "gemini", fmt.Sprintf("%.0fs", timeout.Seconds()), timeout)
			if err := enc.Encode(r); err != nil {
				return err
			}
			if !r.Success {
				if err := failEnc.Encode(r); err != nil {
					return err
				}
			}
			if err := f.Sync(); err != nil {
				return err
			}
			fmt.Printf("RESULT photo=%s profile=%s success=%t error_class=%s latency_ms=%.2f attempts=%d query_count=%d\n", item.ID, r.Profile, r.Success, r.ErrorClass, r.LatencyMS, r.Metrics.Attempts, r.QueryCount)
		}
	}
	return nil
}
