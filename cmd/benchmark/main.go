// benchmark is an explicit paid integration utility. go test never invokes it.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	imageproc "example.com/sync/internal/image"
	"example.com/sync/internal/model"
)

type Profile struct {
	ID             string `json:"id"`
	Model          string `json:"model"`
	Thinking       string `json:"thinking"`
	Dimension      int    `json:"dimension"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Attempts       int    `json:"attempts"`
	RetryMode      string `json:"retry_mode"`
}
type DatasetImage struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}
type Settings struct {
	Profiles    []Profile `json:"profiles"`
	Repetitions int       `json:"repetitions"`
	Workers     int       `json:"workers"`
}
type Record struct {
	Profile       Profile              `json:"profile"`
	ImageID       string               `json:"image_id"`
	Repetition    int                  `json:"repetition"`
	StartedAt     time.Time            `json:"started_at"`
	TotalMS       float64              `json:"total_request_ms"`
	DecodeMS      float64              `json:"image_decode_ms"`
	ResizeMS      float64              `json:"image_resize_ms"`
	EncodeMS      float64              `json:"image_encode_ms"`
	Width         int                  `json:"image_width"`
	Height        int                  `json:"image_height"`
	ImageBytes    int                  `json:"image_bytes"`
	Metrics       client.CallSnapshot  `json:"vertex"`
	Success       bool                 `json:"success"`
	ErrorCategory string               `json:"error_category,omitempty"`
	Analysis      *model.ImageAnalysis `json:"analysis,omitempty"`
	PromptSHA256  string               `json:"prompt_sha256"`
}
type job struct {
	profile    Profile
	image      DatasetImage
	repetition int
}

func errorCategory(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, client.ErrAIRateLimited):
		return "429"
	case errors.Is(err, client.ErrAIUnavailable):
		return "5xx"
	case errors.Is(err, client.ErrInvalidAIResponse):
		return "invalid_response"
	case errors.Is(err, client.ErrAIConfiguration):
		return "configuration"
	default:
		return "service_error"
	}
}
func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	live := flag.Bool("live", false, "explicitly enable paid Vertex image calls")
	settingsPath := flag.String("config", "", "benchmark settings JSON")
	datasetPath := flag.String("dataset", "", "image manifest JSON (local paths; images never saved in result)")
	out := flag.String("out", "", "result directory")
	profileFilter := flag.String("profiles", "", "comma-separated IDs; empty=all")
	limit := flag.Int("images", 0, "first N dataset images; 0=all")
	flag.Parse()
	if !*live {
		return fmt.Errorf("--live is required to call Vertex; use profile-benchmark for offline evaluation")
	}
	if *settingsPath == "" || *datasetPath == "" || *out == "" {
		return fmt.Errorf("config, dataset and out are required")
	}
	var settings Settings
	var images []DatasetImage
	if err := readJSON(*settingsPath, &settings); err != nil {
		return err
	}
	if err := readJSON(*datasetPath, &images); err != nil {
		return err
	}
	if settings.Workers < 1 || settings.Workers > 4 || settings.Repetitions < 1 || settings.Repetitions > 10 {
		return fmt.Errorf("invalid bounded benchmark workers/repetitions")
	}
	if *limit > 0 && *limit < len(images) {
		images = images[:*limit]
	}
	if len(images) == 0 {
		return fmt.Errorf("empty dataset")
	}
	filter := map[string]bool{}
	for _, id := range splitIDs(*profileFilter) {
		filter[id] = true
	}
	fmt.Printf("Planned Vertex logical calls: <= %d (before configured retries); YouTube calls: 0\n", len(images)*settings.Repetitions*len(settings.Profiles))
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0700); err != nil {
		return err
	}
	ctx := context.Background()
	analyzers := map[string]*client.VertexImageAnalyzer{}
	processors := map[string]*imageproc.Processor{}
	profiles := []Profile{}
	for _, p := range settings.Profiles {
		if len(filter) > 0 && !filter[p.ID] {
			continue
		}
		if p.ID == "" || filepath.Base(p.ID) != p.ID || p.Model == "" || p.TimeoutSeconds < 1 || p.TimeoutSeconds > 120 || p.Attempts < 1 || p.Attempts > 3 || (p.RetryMode != "sdk" && p.RetryMode != "budget") || (p.RetryMode == "budget" && p.Attempts > 2) {
			return fmt.Errorf("invalid benchmark profile")
		}
		if err := config.ValidateThinking(p.Model, p.Thinking); err != nil {
			return err
		}
		local := cfg
		local.VertexModel = p.Model
		local.VertexThinkingLevel = p.Thinking
		local.VertexTimeout = time.Duration(p.TimeoutSeconds) * time.Second
		local.VertexRetryAttempts = p.Attempts
		local.VertexRetryMode = p.RetryMode
		analyzer, err := client.NewVertexImageAnalyzer(ctx, local)
		if err != nil {
			return fmt.Errorf("benchmark client unavailable: %s", p.ID)
		}
		processor, err := imageproc.NewProcessor(p.Dimension, cfg.ImageHardMaxBytes)
		if err != nil {
			return err
		}
		analyzers[p.ID] = analyzer
		processors[p.ID] = processor
		profiles = append(profiles, p)
	}
	if len(profiles) == 0 {
		return fmt.Errorf("no selected profiles")
	}
	schema, err := client.AnalysisSchema()
	if err != nil {
		return err
	}
	fingerprint, _ := json.Marshal(struct {
		Prompt string
		Schema map[string]any
	}{client.EnhancedAnalysisPrompt(), schema})
	hash := sha256.Sum256(fingerprint)
	promptHash := hex.EncodeToString(hash[:])
	// Resume only exact profile + image + repetition records, avoiding repeat costs.
	files := map[string]*os.File{}
	done := map[string]bool{}
	for _, p := range profiles {
		path := filepath.Join(*out, p.ID+".jsonl")
		old, _ := os.ReadFile(path)
		for _, line := range lines(old) {
			var r Record
			if json.Unmarshal(line, &r) == nil && r.Profile == p {
				if r.PromptSHA256 != promptHash {
					return fmt.Errorf("existing benchmark uses a different prompt/schema; choose a new output directory")
				}
				done[key(p.ID, r.ImageID, r.Repetition)] = true
			}
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		files[p.ID] = f
		defer f.Close()
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for worker := 0; worker < settings.Workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				started := time.Now()
				r := Record{Profile: j.profile, ImageID: j.image.ID, Repetition: j.repetition, StartedAt: started.UTC(), PromptSHA256: promptHash}
				raw, err := os.ReadFile(j.image.Path)
				if err == nil && len(raw) > 10*1024*1024 {
					err = imageproc.ErrTooLarge
				}
				if err == nil {
					callCtx, cancel := context.WithTimeout(ctx, time.Duration(j.profile.TimeoutSeconds)*time.Second)
					processed, e := processors[j.profile.ID].Process(callCtx, raw, http.DetectContentType(raw))
					err = e
					if err == nil {
						r.DecodeMS = processed.DecodeMS
						r.ResizeMS = processed.ResizeMS
						r.EncodeMS = processed.EncodeMS
						r.Width = processed.Width
						r.Height = processed.Height
						r.ImageBytes = len(processed.Data)
						measured, m := client.MeasureCall(callCtx)
						r.Analysis, err = analyzers[j.profile.ID].AnalyzeImage(measured, processed.Data, processed.MIMEType)
						r.Metrics = m.Snapshot()
					}
					cancel()
				}
				r.TotalMS = float64(time.Since(started)) / float64(time.Millisecond)
				r.Success = err == nil
				if err != nil {
					r.ErrorCategory = errorCategory(err)
				}
				encoded, _ := json.Marshal(r)
				mu.Lock()
				files[j.profile.ID].Write(append(encoded, '\n'))
				files[j.profile.ID].Sync()
				fmt.Printf("benchmark profile=%s image=%s repetition=%d success=%t error=%s total_ms=%.0f attempts=%d\n", j.profile.ID, j.image.ID, j.repetition, r.Success, r.ErrorCategory, r.TotalMS, r.Metrics.Attempts)
				mu.Unlock()
			}
		}()
	}
	// Interleave repetitions and images for less temporal bias; selected profiles
	// are explicit phases so thinking/resolution changes are staged by the operator.
	for repetition := 1; repetition <= settings.Repetitions; repetition++ {
		for _, image := range images {
			for _, p := range profiles {
				if !done[key(p.ID, image.ID, repetition)] {
					jobs <- job{p, image, repetition}
				}
			}
		}
	}
	close(jobs)
	wg.Wait()
	for _, p := range profiles {
		raw, _ := os.ReadFile(filepath.Join(*out, p.ID+".jsonl"))
		records := []Record{}
		for _, line := range lines(raw) {
			var r Record
			if json.Unmarshal(line, &r) == nil && r.Profile == p {
				if r.PromptSHA256 != promptHash {
					return fmt.Errorf("existing benchmark uses a different prompt/schema; choose a new output directory")
				}
				records = append(records, r)
			}
		}
		sort.Slice(records, func(i, j int) bool {
			if records[i].ImageID != records[j].ImageID {
				return records[i].ImageID < records[j].ImageID
			}
			return records[i].Repetition < records[j].Repetition
		})
		encoded, _ := json.MarshalIndent(records, "", "  ")
		if err := os.WriteFile(filepath.Join(*out, p.ID+".json"), encoded, 0600); err != nil {
			return err
		}
	}
	return nil
}
