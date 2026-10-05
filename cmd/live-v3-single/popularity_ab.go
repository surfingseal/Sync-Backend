package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	"example.com/sync/internal/model"
	"example.com/sync/internal/recommendation"
	"example.com/sync/internal/router"
	"example.com/sync/internal/service"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Explicit single request with the unchanged saved analysis. No Vertex client
// is constructed, no retry loop, pagination, OAuth or playlist operation exists.
func runPopularityAB(live bool, input, dir string) error {
	if !live || dir == "" {
		return fmt.Errorf("explicit live and new output required")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		return fmt.Errorf("output must not exist")
	}
	raw, err := os.ReadFile(input)
	if err != nil {
		return err
	}
	var captured runResult
	if err = json.Unmarshal(raw, &captured); err != nil {
		return err
	}
	if len(captured.Search) != 2 || len(captured.Metadata) != 2 || captured.Trace.Plan == nil {
		return fmt.Errorf("captured Q1/Q2 baseline required")
	}
	var savedEnvelope struct {
		Analysis json.RawMessage `json:"analysis"`
	}
	if err = json.Unmarshal(captured.AnalyzeResponse, &savedEnvelope); err != nil {
		return err
	}
	raw = savedEnvelope.Analysis
	analysis, err := model.DecodeImageAnalysis(raw)
	if err != nil || analysis.SchemaVersion != "3" {
		return fmt.Errorf("saved v3 analysis invalid")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.RecommendationQueryMode != "deterministic" || cfg.RecommendationDataMode != "live" || !cfg.YouTubeLiveSearchEnabled || cfg.YouTubeSearchMaxCalls != 2 || cfg.YouTubeSearchQueryCount != 2 || cfg.YouTubeSearchMaxResults != 50 || cfg.RecommendationRanker != "v2" || cfg.RecommendationSafetyMargin != 3 || cfg.YouTubeAPIKey == "" {
		return fmt.Errorf("retrieval settings mismatch")
	}
	ctx := context.Background()
	observed := &musicObservation{Snippets: map[string][]client.YouTubeSearchSnippet{}}
	source, err := client.NewYouTubeClient(ctx, cfg.YouTubeAPIKey, func(q model.MusicSearchQuery, hits []client.YouTubeSearchSnippet) { observed.Snippets[q.Text] = hits })
	if err != nil {
		return err
	}
	hybrid := &reusedQ1Music{source: observed, q1: captured.Search[0], metadata: captured.Metadata[0]}
	observed.Core = source
	cached := &client.CachedMusicClient{Source: hybrid, Cache: client.NewInMemorySearchCache(), TTL: cfg.YouTubeSearchCacheTTL, Budget: client.NewSearchBudget(2), LiveEnabled: true}
	ranking := []rankObservation{}
	ranker := func(c []recommendation.Candidate, a model.ImageAnalysis, p model.MusicPreferences) []recommendation.RankedCandidate {
		result, audit := recommendation.RankV2WithAudit(c, a, p)
		ranking = append(ranking, rankObservation{audit, append([]recommendation.Candidate{}, c...), result})
		return result
	}
	options := []service.RecommendationOption{service.WithRanker(ranker), service.WithAdaptiveSafetyMargin(3), service.WithSearchMaxResults(50)}
	{
		policy := recommendation.NeutralPolicy{MinMediumEstablished: cfg.RecommendationMinMediumEstablished, MinLanguageCoverage: cfg.RecommendationMinLanguageCoverage, RequireRelease: cfg.RecommendationRequireRelease, AllowedRelease: cfg.RecommendationAllowedRelease, MinChannels: 2}
		if policy.Validate() != nil || !policy.RequireRelease || len(policy.AllowedRelease) != 2 || !policy.AllowsRelease("HIGH") || !policy.AllowsRelease("MEDIUM") || policy.MinMediumEstablished != 6 || policy.MinLanguageCoverage != 2 {
			return fmt.Errorf("controlled v4 policy mismatch")
		}
		options = append(options, service.WithNeutralRetrieval(policy, cfg.RecommendationMixedKeyword))
	}
	svc := service.NewRecommendationService(client.DeterministicQueryBuilder{MixedKeyword: cfg.RecommendationMixedKeyword, OmitLanguageKeywords: !cfg.RecommendationQueryLanguageKeywords}, cached, cfg.YouTubeRegion, cfg.YouTubeRelevanceLanguage, 2, cfg.RecommendationTimeout, options...)
	r := router.New(cfg, nil, svc)
	trace := &service.RecommendationTrace{}
	done := make(chan struct{})
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer close(done)
		r.ServeHTTP(w, req.WithContext(service.WithRecommendationTrace(req.Context(), trace)))
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdown, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err = os.Mkdir(dir, 0700); err != nil {
		return err
	}
	envelope, _ := json.Marshal(map[string]any{"analysis": analysis})
	baselineDiagnostics := []map[string]any{}
	for _, video := range captured.Trace.Videos {
		baselineDiagnostics = append(baselineDiagnostics, map[string]any{"video_id": video.VideoID, "content": recommendation.DiagnoseContent(video)})
	}
	if err = save(filepath.Join(dir, "baseline-content.json"), baselineDiagnostics); err != nil {
		return err
	}
	result := runResult{AnalysisReused: true, StartedAt: time.Now().UTC(), Endpoint: "http://" + listener.Addr().String(), AnalyzeResponse: envelope, Settings: map[string]any{"neutral_retrieval_v4": true, "minimum_medium_established": cfg.RecommendationMinMediumEstablished, "minimum_language_coverage": cfg.RecommendationMinLanguageCoverage, "require_release": cfg.RecommendationRequireRelease, "allowed_release": cfg.RecommendationAllowedRelease, "analysis_source": "captured Q1 + one fresh Q2; no fresh image analysis", "baseline_source": input, "analysis_sha256": fmt.Sprintf("%x", sha256.Sum256(raw)), "query_mode": "deterministic", "data_mode": "live", "ranker": "v2", "max_results": 50, "search_budget": 2, "query_limit": 2, "safety_margin": 3, "mixed_keyword": cfg.RecommendationMixedKeyword, "cache_initially_empty": true, "region": cfg.YouTubeRegion, "languages": []string{"ko", "en"}, "vocal_mode": "mixed"}}
	if err = os.WriteFile(filepath.Join(dir, "analysis.json"), raw, 0600); err != nil {
		return err
	}
	defer func() {
		result.Trace = *trace
		result.Search = append([]searchRecord{captured.Search[0]}, observed.Searches...)
		result.Metadata = append([]metadataRecord{captured.Metadata[0]}, observed.Metadata...)
		result.Ranking = ranking
		_ = save(filepath.Join(dir, "run.json"), result)
		_ = save(filepath.Join(dir, "quota.json"), map[string]any{"new_search_list_actual_calls": len(observed.Searches), "new_videos_list_actual_calls": len(observed.Metadata), "captured_q1_reused": true, "cumulative_strategies": 1 + len(observed.Searches), "gemini_calls": 0, "playlist_writes": 0, "no_pagination": true})
		_ = save(filepath.Join(dir, "new-search.json"), observed.Searches)
		_ = save(filepath.Join(dir, "new-metadata.json"), observed.Metadata)
		if len(result.RecommendResponse) > 0 {
			_ = save(filepath.Join(dir, "recommendations.json"), result.RecommendResponse)
		}
	}()
	payload, _ := json.Marshal(model.RecommendationRequest{Analysis: *analysis, Preferences: model.MusicPreferences{Languages: []string{"ko", "en"}, VocalMode: "mixed", Count: 10}})
	req, err := http.NewRequest("POST", result.Endpoint+"/api/v1/recommend", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	response, err := (&http.Client{Timeout: cfg.RecommendationTimeout + 5*time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("HTTP recommendation failed; no retry")
	}
	result.RecommendStatus = response.StatusCode
	result.RecommendResponse, err = io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	<-done
	result.RecommendHTTPMS = ms(start)
	if err != nil {
		return err
	}
	if response.StatusCode != 200 {
		return fmt.Errorf("recommendation failed; inspect safe response; no retry")
	}
	fmt.Printf("popularity AB completed: HTTP=%d new_search=%d new_metadata=%d Vertex=0\n", response.StatusCode, len(observed.Searches), len(observed.Metadata))
	return nil
}

// One captured Q1 and at most one new Q2. Source observations record only new
// external requests. The combined run keeps old snapshots with provenance.
type reusedQ1Music struct {
	source                   *musicObservation
	q1                       searchRecord
	metadata                 metadataRecord
	searchUsed, metadataUsed bool
}

func (m *reusedQ1Music) DataMode() string { return "live" }
func (m *reusedQ1Music) SearchMusic(ctx context.Context, q model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !m.searchUsed {
		if q.Text != m.q1.Query.Text || q.Order != m.q1.Query.Order || q.MaxResults != m.q1.Query.MaxResults {
			return nil, fmt.Errorf("captured Q1 mismatch")
		}
		m.searchUsed = true
		out := []model.YouTubeSearchResult{}
		for _, hit := range m.q1.Raw {
			out = append(out, model.YouTubeSearchResult{VideoID: hit.VideoID})
		}
		return out, nil
	}
	if len(m.source.Searches) > 0 || q.Text != m.q1.Query.Text || q.Order != "viewCount" || q.MaxResults != 50 {
		return nil, client.ErrSearchBudget
	}
	return m.source.SearchMusic(ctx, q)
}
func (m *reusedQ1Music) GetVideos(ctx context.Context, ids []string) ([]model.YouTubeVideo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(ids) > 50 {
		return nil, client.ErrSearchBudget
	}
	if !m.metadataUsed {
		known := map[string]bool{}
		for _, id := range m.metadata.IDs {
			known[id] = true
		}
		for _, id := range ids {
			if !known[id] {
				return nil, fmt.Errorf("captured Q1 metadata mismatch")
			}
		}
		m.metadataUsed = true
		out := []model.YouTubeVideo{}
		wanted := map[string]bool{}
		for _, id := range ids {
			wanted[id] = true
		}
		for _, v := range m.metadata.Videos {
			if wanted[v.VideoID] {
				out = append(out, v)
			}
		}
		return out, nil
	}
	if len(m.source.Metadata) > 0 || len(ids) > 50 {
		return nil, client.ErrSearchBudget
	}
	return m.source.GetVideos(ctx, ids)
}
