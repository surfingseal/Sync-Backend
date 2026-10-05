// One explicit real-photo HTTP E2E. Never invoked by go test; never creates playlists.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	stdimage "image"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	imageproc "example.com/sync/internal/image"
	"example.com/sync/internal/model"
	"example.com/sync/internal/recommendation"
	"example.com/sync/internal/router"
	"example.com/sync/internal/service"
)

type processorObservation struct {
	Core imageproc.ImageProcessor
	Info map[string]any
}

func (p *processorObservation) Process(ctx context.Context, data []byte, mime string) (*imageproc.ProcessedImage, error) {
	start := time.Now()
	out, err := p.Core.Process(ctx, data, mime)
	p.Info = map[string]any{"total_ms": ms(start)}
	if out != nil {
		p.Info["processed_dimensions"] = []int{out.Width, out.Height}
		p.Info["processed_bytes"] = len(out.Data)
		p.Info["processed_mime_type"] = out.MIMEType
		p.Info["resized"] = out.Resized
		p.Info["reencoded"] = out.Reencoded
		p.Info["decode_ms"] = out.DecodeMS
		p.Info["resize_ms"] = out.ResizeMS
		p.Info["encode_ms"] = out.EncodeMS
	}
	return out, err
}

type analyzerObservation struct {
	Core    client.ImageAnalyzer
	Metrics client.CallSnapshot
	TotalMS float64
	Calls   int
}

func (a *analyzerObservation) AnalyzeImage(ctx context.Context, data []byte, mime string) (*model.ImageAnalysis, error) {
	a.Calls++
	ctx, m := client.MeasureCall(ctx)
	start := time.Now()
	out, err := a.Core.AnalyzeImage(ctx, data, mime)
	a.TotalMS = ms(start)
	a.Metrics = m.Snapshot()
	return out, err
}

type searchRecord struct {
	Query   model.MusicSearchQuery        `json:"parameters"`
	Raw     []client.YouTubeSearchSnippet `json:"raw_unverified_candidates"`
	MS      float64                       `json:"search_ms"`
	Success bool                          `json:"success"`
}
type metadataRecord struct {
	IDs     []string             `json:"requested_ids"`
	Videos  []model.YouTubeVideo `json:"videos"`
	MS      float64              `json:"metadata_ms"`
	Success bool                 `json:"success"`
}
type musicObservation struct {
	Core     client.MusicSearchClient
	Searches []searchRecord
	Metadata []metadataRecord
	Snippets map[string][]client.YouTubeSearchSnippet
}

func (m *musicObservation) SearchMusic(ctx context.Context, q model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
	if len(m.Searches) >= 2 {
		return nil, client.ErrSearchBudget
	}
	start := time.Now()
	out, err := m.Core.SearchMusic(ctx, q)
	m.Searches = append(m.Searches, searchRecord{q, append([]client.YouTubeSearchSnippet{}, m.Snippets[q.Text]...), ms(start), err == nil})
	return out, err
}
func (m *musicObservation) GetVideos(ctx context.Context, ids []string) ([]model.YouTubeVideo, error) {
	if len(ids) > 50 {
		return nil, client.ErrMusicSearch
	}
	start := time.Now()
	out, err := m.Core.GetVideos(ctx, ids)
	m.Metadata = append(m.Metadata, metadataRecord{append([]string{}, ids...), append([]model.YouTubeVideo{}, out...), ms(start), err == nil})
	return out, err
}

type rankObservation struct {
	Audit      recommendation.RankingAudit
	Candidates []recommendation.Candidate
	Result     []recommendation.RankedCandidate
}
type runResult struct {
	AnalysisReused    bool                        `json:"analysis_reused"`
	StartedAt         time.Time                   `json:"started_at"`
	Endpoint          string                      `json:"endpoint"`
	Settings          map[string]any              `json:"settings"`
	Image             map[string]any              `json:"image"`
	AnalyzeStatus     int                         `json:"analyze_http_status"`
	RecommendStatus   int                         `json:"recommend_http_status"`
	AnalyzeResponse   json.RawMessage             `json:"analyze_response"`
	RecommendResponse json.RawMessage             `json:"recommend_response"`
	AnalyzeHTTPMS     float64                     `json:"analyze_http_ms"`
	RecommendHTTPMS   float64                     `json:"recommend_http_ms"`
	UploadToReadyMS   float64                     `json:"upload_to_ready_ms"`
	Processor         map[string]any              `json:"preprocessing"`
	Vertex            analyzerObservationJSON     `json:"vertex"`
	Trace             service.RecommendationTrace `json:"trace"`
	Search            []searchRecord              `json:"search"`
	Metadata          []metadataRecord            `json:"metadata"`
	Ranking           []rankObservation           `json:"ranking_evaluations"`
}
type analyzerObservationJSON struct {
	Calls     int                 `json:"logical_image_calls"`
	TextCalls int                 `json:"text_query_calls"`
	MS        float64             `json:"total_ms"`
	Metrics   client.CallSnapshot `json:"metrics"`
}

func ms(start time.Time) float64 { return float64(time.Since(start)) / float64(time.Millisecond) }
func save(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0600)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "single-photo run stopped (%T); inspect saved safe result if available\n", err)
		os.Exit(1)
	}
}
func run() error {
	q2AfterFailure := flag.Bool("q2-after-failed-q1", false, "one remaining Q2 attempt after failed Q1, never retry Q1")
	freshOrder := flag.Bool("fresh-order-audit", false, "diagnostic: fresh same-query relevance/viewCount searches, no adaptive planner/cache")
	popularitySource := flag.String("popularity-ab", "", "captured eligibility run: reuse Q1, one fresh neutral viewCount Q2")
	replaySource := flag.String("eligibility-replay", "", "offline fixed-pool eligibility audit from captured run.json")
	neutral := flag.Bool("neutral-retrieval", false, "enable v4 neutral planner and release gate for saved-analysis live comparison")
	savedAnalysis := flag.String("saved-analysis", "", "retrieval-only: saved baseline schema v3, no Vertex calls")
	reportOnly := flag.Bool("report-only", false, "build offline diagnostics from an existing run.json")
	live := flag.Bool("live", false, "explicitly enable one image and <=2 YouTube searches")
	photo := flag.String("image", "", "user-provided real photo path")
	out := flag.String("out", "", "new result directory")
	flag.Parse()
	if *freshOrder {
		return runFreshOrderAudit(*live, *savedAnalysis, *out, *q2AfterFailure)
	}
	if *popularitySource != "" {
		return runPopularityAB(*live, *popularitySource, *out)
	}
	if *replaySource != "" {
		return eligibilityReplay(*replaySource, *out)
	}
	if *reportOnly {
		return buildDiagnostics(*out)
	}
	if *savedAnalysis != "" {
		return runSavedRetrieval(*live, *savedAnalysis, *out, *neutral)
	}
	if !*live || *photo == "" || *out == "" {
		return fmt.Errorf("explicit live/image/out required")
	}
	if _, err := os.Stat(*out); !os.IsNotExist(err) {
		return fmt.Errorf("output must not exist")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.RecommendationQueryMode != "deterministic" || cfg.RecommendationDataMode != "live" || !cfg.YouTubeLiveSearchEnabled || cfg.YouTubeSearchMaxCalls != 2 || cfg.YouTubeSearchQueryCount != 2 || cfg.RecommendationSafetyMargin != 3 || cfg.VertexModel != "gemini-3.8-flash" || cfg.VertexThinkingLevel != "MEDIUM" || cfg.ImageMaxDimension != 1920 || cfg.YouTubeAPIKey == "" {
		return fmt.Errorf("single-run settings do not match requested baseline")
	}
	data, err := os.ReadFile(*photo)
	if err != nil {
		return fmt.Errorf("image unavailable")
	}
	if len(data) > int(service.MaxImageSize) {
		return fmt.Errorf("image too large")
	}
	dimensions, _, err := stdimage.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid photo")
	}
	ctx := context.Background()
	vertex, err := client.NewVertexImageAnalyzer(ctx, cfg)
	if err != nil {
		return fmt.Errorf("Vertex initialization failed")
	}
	coreProcessor, err := imageproc.NewProcessor(cfg.ImageMaxDimension, cfg.ImageHardMaxBytes)
	if err != nil {
		return err
	}
	processor := &processorObservation{Core: coreProcessor}
	analyzer := &analyzerObservation{Core: vertex}
	images := service.NewImageService(analyzer, processor, cfg.VertexTimeout)
	observedMusic := &musicObservation{Snippets: map[string][]client.YouTubeSearchSnippet{}}
	youtubeClient, err := client.NewYouTubeClient(ctx, cfg.YouTubeAPIKey, func(q model.MusicSearchQuery, hits []client.YouTubeSearchSnippet) {
		observedMusic.Snippets[q.Text] = hits
	})
	if err != nil {
		return fmt.Errorf("YouTube initialization failed")
	}
	observedMusic.Core = youtubeClient
	cached := &client.CachedMusicClient{Source: observedMusic, Cache: client.NewInMemorySearchCache(), TTL: cfg.YouTubeSearchCacheTTL, Budget: client.NewSearchBudget(2), LiveEnabled: true}
	ranking := []rankObservation{}
	ranker := func(c []recommendation.Candidate, a model.ImageAnalysis, p model.MusicPreferences) []recommendation.RankedCandidate {
		result, audit := recommendation.RankV2WithAudit(c, a, p)
		ranking = append(ranking, rankObservation{audit, append([]recommendation.Candidate{}, c...), result})
		return result
	}
	recommendations := service.NewRecommendationService(client.DeterministicQueryBuilder{OmitLanguageKeywords: !cfg.RecommendationQueryLanguageKeywords}, cached, cfg.YouTubeRegion, cfg.YouTubeRelevanceLanguage, 2, cfg.RecommendationTimeout, service.WithRanker(ranker), service.WithAdaptiveSafetyMargin(3), service.WithSearchMaxResults(20))
	r := router.New(cfg, images, recommendations)
	trace := &service.RecommendationTrace{}
	analyzeDone, recommendDone := make(chan struct{}), make(chan struct{})
	var analyzeOnce, recommendOnce sync.Once
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/analyze":
			defer analyzeOnce.Do(func() { close(analyzeDone) })
		case "/api/v1/recommend":
			request = request.WithContext(service.WithRecommendationTrace(request.Context(), trace))
			defer recommendOnce.Do(func() { close(recommendDone) })
		}
		r.ServeHTTP(w, request)
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdown, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err = os.Mkdir(*out, 0700); err != nil {
		return err
	}
	result := runResult{StartedAt: time.Now().UTC(), Endpoint: "http://" + listener.Addr().String(), Settings: map[string]any{"model": cfg.VertexModel, "thinking": cfg.VertexThinkingLevel, "max_image_dimension": cfg.ImageMaxDimension, "vertex_timeout_seconds": cfg.VertexTimeout.Seconds(), "vertex_retry_mode": cfg.VertexRetryMode, "vertex_retry_attempts": cfg.VertexRetryAttempts, "query_mode": "deterministic", "data_mode": "live", "ranker": "v2", "live_search": true, "search_budget": 2, "query_limit": 2, "safety_margin": 3, "requested_count": 10, "vocal_mode": "mixed", "languages": []string{"ko", "en"}, "region": cfg.YouTubeRegion, "relevance_language": cfg.YouTubeRelevanceLanguage, "cache_initially_empty": true, "cache_ttl_seconds": cfg.YouTubeSearchCacheTTL.Seconds()}, Image: map[string]any{"filename": filepath.Base(*photo), "original_dimensions": []int{dimensions.Width, dimensions.Height}, "original_bytes": len(data)}}
	defer func() {
		result.Processor = processor.Info
		result.Vertex = analyzerObservationJSON{analyzer.Calls, 0, analyzer.TotalMS, analyzer.Metrics}
		result.Trace = *trace
		result.Search = observedMusic.Searches
		result.Metadata = observedMusic.Metadata
		result.Ranking = ranking
		_ = save(filepath.Join(*out, "run.json"), result)
		_ = save(filepath.Join(*out, "recommendations.json"), result.RecommendResponse)
	}()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	field, err := form.CreateFormFile("image", filepath.Base(*photo))
	if err != nil {
		return err
	}
	_, _ = field.Write(data)
	_ = form.Close()
	request, err := http.NewRequest("POST", result.Endpoint+"/api/v1/analyze", &body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", form.FormDataContentType())
	httpClient := &http.Client{Timeout: cfg.VertexTimeout + 10*time.Second}
	uploadStart := time.Now()
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("analyze HTTP failed")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	if err != nil {
		return err
	}
	<-analyzeDone
	result.AnalyzeHTTPMS = ms(uploadStart)
	result.AnalyzeStatus = response.StatusCode
	result.AnalyzeResponse = payload
	_ = save(filepath.Join(*out, "analyze-response.json"), json.RawMessage(payload))
	if response.StatusCode != 200 {
		result.UploadToReadyMS = ms(uploadStart)
		return fmt.Errorf("analyze failed; no replacement or rerun")
	}
	var envelope struct {
		Analysis json.RawMessage `json:"analysis"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return fmt.Errorf("invalid analyze response")
	}
	analysis, err := model.DecodeImageAnalysis(envelope.Analysis)
	if err != nil || analysis.SchemaVersion != "3" {
		return fmt.Errorf("v3 response validation failed")
	}
	_ = save(filepath.Join(*out, "analysis.json"), analysis)
	prefs := model.MusicPreferences{Languages: []string{"ko", "en"}, VocalMode: "mixed", Count: 10}
	recommendPayload, _ := json.Marshal(model.RecommendationRequest{Analysis: *analysis, Preferences: prefs})
	request, err = http.NewRequest("POST", result.Endpoint+"/api/v1/recommend", bytes.NewReader(recommendPayload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	httpClient.Timeout = cfg.RecommendationTimeout + 5*time.Second
	recommendStart := time.Now()
	response, err = httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("recommend HTTP failed")
	}
	payload, err = io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	if err != nil {
		return err
	}
	<-recommendDone
	result.RecommendHTTPMS = ms(recommendStart)
	result.UploadToReadyMS = ms(uploadStart)
	result.RecommendStatus = response.StatusCode
	result.RecommendResponse = payload
	if len(observedMusic.Searches) > 2 {
		return fmt.Errorf("search limit breached")
	}
	if response.StatusCode != 200 {
		return fmt.Errorf("recommend failed; no rerun")
	}
	fmt.Printf("single-photo HTTP run completed: analyze=%d recommend=%d search=%d metadata=%d\n", result.AnalyzeStatus, result.RecommendStatus, len(observedMusic.Searches), len(observedMusic.Metadata))
	return nil
}
