package main

import (
	"context"
	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	"example.com/sync/internal/model"
	"example.com/sync/internal/recommendation"
	"example.com/sync/internal/service"
	"fmt"
	"google.golang.org/api/googleapi/transport"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const freshQuery = "city pop song -mix -playlist -compilation -backing -karaoke"

type networkObservation struct {
	Method     string              `json:"method"`
	Timestamp  time.Time           `json:"timestamp"`
	Parameters map[string][]string `json:"parameters"`
	Status     int                 `json:"http_status"`
	MS         float64             `json:"latency_ms"`
	CacheHit   bool                `json:"cache_hit"`
	Network    bool                `json:"network_search"`
}
type boundedAuditTransport struct {
	Core             http.RoundTripper
	Calls            []networkObservation
	Search, Metadata int
	SearchLimit      int
}

func (t *boundedAuditTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	method := ""
	switch {
	case strings.HasSuffix(req.URL.Path, "/search"):
		method = "search.list"
		limit := t.SearchLimit
		if limit == 0 {
			limit = 2
		}
		if t.Search >= limit {
			return nil, fmt.Errorf("search budget exhausted")
		}
		t.Search++
	case strings.HasSuffix(req.URL.Path, "/videos"):
		method = "videos.list"
		if t.Metadata >= 2 {
			return nil, fmt.Errorf("metadata budget exhausted")
		}
		t.Metadata++
	default:
		return nil, fmt.Errorf("unexpected diagnostic method")
	}
	params := map[string][]string{}
	q := req.URL.Query()
	for _, k := range []string{"q", "order", "maxResults", "part", "type", "videoCategoryId", "regionCode", "relevanceLanguage", "videoEmbeddable", "videoSyndicated", "safeSearch", "id"} {
		if v, ok := q[k]; ok {
			params[k] = append([]string{}, v...)
		}
	}
	if q.Get("pageToken") != "" {
		return nil, fmt.Errorf("pagination forbidden")
	}
	start := time.Now()
	response, err := t.Core.RoundTrip(req)
	status := 0
	if response != nil {
		status = response.StatusCode
	}
	t.Calls = append(t.Calls, networkObservation{method, start.UTC(), params, status, ms(start), false, method == "search.list"})
	return response, err
}
func unionFresh(raws [][]client.YouTubeSearchSnippet) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range raws {
		for _, v := range raw {
			if v.VideoID != "" && !seen[v.VideoID] {
				seen[v.VideoID] = true
				out = append(out, v.VideoID)
			}
		}
	}
	return out
}
func batchesFresh(ids []string) [][]string {
	out := [][]string{}
	for i := 0; i < len(ids); i += 50 {
		out = append(out, append([]string{}, ids[i:min(i+50, len(ids))]...))
	}
	return out
}

type freshEvaluation struct {
	Funnel      []map[string]any                 `json:"funnel"`
	Candidates  []map[string]any                 `json:"candidates"`
	Initial     []recommendation.RankedCandidate `json:"initial_scores"`
	BeforeVocal []recommendation.RankedCandidate `json:"before_vocal"`
	Final       []recommendation.RankedCandidate `json:"final"`
}

func evaluateFresh(ids []string, ranks map[string]int, sources map[string][]string, videos map[string]model.YouTubeVideo, a model.ImageAnalysis) freshEvaluation {
	p := model.MusicPreferences{Languages: []string{"ko", "en"}, Count: 10, VocalMode: "mixed"}
	out := freshEvaluation{Candidates: []map[string]any{}, Funnel: []map[string]any{}}
	pool := []recommendation.Candidate{}
	checks := map[string]map[string]bool{}
	for _, id := range ids {
		v, ok := videos[id]
		m := map[string]bool{"metadata_available": ok}
		for _, c := range service.ExplainVideoEligibility(v, p, "KR") {
			m[c.Stage] = c.Passed
		}
		m["release_confidence"] = recommendation.DefaultNeutralPolicy().AllowsRelease(recommendation.ReleaseConfidence(v).Confidence)
		m["quality_vocal"] = !recommendation.FailsQualityGate(v)
		checks[id] = m
	}
	remaining := append([]string{}, ids...)
	out.Funnel = append(out.Funnel, map[string]any{"stage": "raw", "remaining": len(ids), "removed": 0, "ids": append([]string{}, ids...)})
	for _, stage := range []string{"metadata_available", "metadata_present", "music_category", "public_and_not_live", "embeddable", "song_form", "duration", "region", "ai_generated", "transformed_audio", "excluded_artist", "release_confidence", "quality_vocal"} {
		next := []string{}
		for _, id := range remaining {
			if checks[id][stage] {
				next = append(next, id)
			}
		}
		out.Funnel = append(out.Funnel, map[string]any{"stage": stage, "remaining": len(next), "removed": len(remaining) - len(next), "ids": next})
		remaining = next
	}
	for _, id := range remaining {
		pool = append(pool, recommendation.Candidate{Video: videos[id], SearchRank: ranks[id]})
	}
	pp := p
	pp.CandidatePoolLimit = len(pool)
	selected, audit := recommendation.RankV2WithAudit(pool, a, pp)
	out.Initial = audit.InitialScores
	out.BeforeVocal = selected
	out.Final = recommendation.SelectVocalMix(selected, p)
	scores := map[string]recommendation.RankedCandidate{}
	scoreIDs := []string{}
	for _, v := range audit.InitialScores {
		scores[v.Video.VideoID] = v
		if v.Scores.Final >= audit.MinimumScore {
			scoreIDs = append(scoreIDs, v.Video.VideoID)
		}
	}
	out.Funnel = append(out.Funnel, map[string]any{"stage": "minimum_score", "remaining": len(scoreIDs), "removed": len(remaining) - len(scoreIDs), "ids": scoreIDs})
	selectedIDs := []string{}
	for _, v := range out.Final {
		selectedIDs = append(selectedIDs, v.Video.VideoID)
	}
	out.Funnel = append(out.Funnel, map[string]any{"stage": "final_selected", "remaining": len(out.Final), "removed": len(scoreIDs) - len(out.Final), "ids": selectedIDs})
	selectedSet := map[string]bool{}
	for _, id := range selectedIDs {
		selectedSet[id] = true
	}
	for _, id := range ids {
		v := videos[id]
		reasons := []string{}
		for _, check := range service.ExplainVideoEligibility(v, p, "KR") {
			if !check.Passed {
				if check.Stage == "song_form" {
					reasons = append(reasons, recommendation.SongFormReasons(v)...)
				} else {
					reasons = append(reasons, check.Stage)
				}
			}
		}
		score, scored := scores[id]
		out.Candidates = append(out.Candidates, map[string]any{"video_id": id, "search_rank": ranks[id] + 1, "sources": sources[id], "metadata_available": checks[id]["metadata_available"], "video": v, "checks": checks[id], "hard_filter_reasons": reasons, "release": recommendation.ReleaseConfidence(v), "language": recommendation.LanguageAffinity(v, "city-pop", ""), "content": recommendation.DiagnoseContent(v), "mood_audit": recommendation.AuditMood(v, ranks[id], a, p), "scored": scored, "scores": score.Scores, "minimum_score_pass": scored && score.Scores.Final >= audit.MinimumScore, "final_selected": selectedSet[id]})
	}
	return out
}
func runFreshOrderAudit(live bool, input, dir string, q2Only bool) error {
	if !live || input == "" || dir == "" {
		return fmt.Errorf("explicit live/input/new output required")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		return fmt.Errorf("new output required")
	}
	raw, err := os.ReadFile(input)
	if err != nil {
		return err
	}
	a, err := model.DecodeImageAnalysis(raw)
	if err != nil || a.SchemaVersion != "3" {
		return fmt.Errorf("v3 analysis required")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.YouTubeAPIKey == "" {
		return fmt.Errorf("key required")
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		return err
	}
	started := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rt := &boundedAuditTransport{Core: &transport.APIKey{Key: cfg.YouTubeAPIKey, Transport: http.DefaultTransport}}
	if q2Only {
		rt.SearchLimit = 1
	}
	snippets := []client.YouTubeSearchSnippet{}
	sdk, err := client.NewDiagnosticYouTubeClient(ctx, &http.Client{Transport: rt, Timeout: 20 * time.Second}, func(_ model.MusicSearchQuery, v []client.YouTubeSearchSnippet) {
		snippets = append([]client.YouTubeSearchSnippet{}, v...)
	})
	if err != nil {
		return err
	}
	defer func() {
		_ = save(filepath.Join(dir, "network.json"), rt.Calls)
		_ = save(filepath.Join(dir, "quota.json"), map[string]any{"search_list_actual_calls": rt.Search, "videos_list_actual_calls": rt.Metadata, "Gemini": 0, "playlist": 0, "pagination": 0, "reruns": 0})
		_ = save(filepath.Join(dir, "experiment.json"), map[string]any{"started_at": started, "completed_at": time.Now().UTC(), "analysis_source": input, "cache_bypass": true, "historical_candidates_used": false, "adaptive_planner_used": false, "q2_only_after_Q1_failure": q2Only, "query": freshQuery, "region": "KR", "maxResults": 50})
	}()
	raws := [][]client.YouTubeSearchSnippet{}
	orders := []string{"relevance", "viewCount"}
	if q2Only {
		orders = []string{"viewCount"}
		raws = append(raws, []client.YouTubeSearchSnippet{})
	}
	for i, order := range orders {
		rawIndex := i + 1
		if q2Only {
			rawIndex = 2
		}
		_, err = sdk.SearchMusic(ctx, model.MusicSearchQuery{Text: freshQuery, Order: order, MaxResults: 50, Region: "KR"})
		if err != nil {
			return err
		}
		raws = append(raws, append([]client.YouTubeSearchSnippet{}, snippets...))
		if err = save(filepath.Join(dir, fmt.Sprintf("q%d-raw.json", rawIndex)), snippets); err != nil {
			return err
		}
	}
	ids := unionFresh(raws)
	videos := map[string]model.YouTubeVideo{}
	metadata := []metadataRecord{}
	for _, batch := range batchesFresh(ids) {
		t := time.Now()
		v, err := sdk.GetVideos(ctx, batch)
		metadata = append(metadata, metadataRecord{batch, v, ms(t), err == nil})
		if err != nil {
			return err
		}
		for _, item := range v {
			videos[item.VideoID] = item
		}
	}
	if err = save(filepath.Join(dir, "metadata.json"), metadata); err != nil {
		return err
	}
	mergedRanks := map[string]int{}
	sources := map[string][]string{}
	evaluations := map[string]freshEvaluation{}
	for i, raw := range raws {
		ranks := map[string]int{}
		queryIDs := []string{}
		tag := []string{"q1_relevance", "q2_viewcount"}[i]
		for rank, v := range raw {
			if _, ok := ranks[v.VideoID]; !ok {
				ranks[v.VideoID] = rank
				queryIDs = append(queryIDs, v.VideoID)
				sources[v.VideoID] = append(sources[v.VideoID], tag)
			}
			if old, ok := mergedRanks[v.VideoID]; !ok || rank < old {
				mergedRanks[v.VideoID] = rank
			}
		}
		evaluations[fmt.Sprintf("q%d", i+1)] = evaluateFresh(queryIDs, ranks, sources, videos, *a)
	}
	evaluations["merged"] = evaluateFresh(ids, mergedRanks, sources, videos, *a)
	if err = save(filepath.Join(dir, "evaluations.json"), evaluations); err != nil {
		return err
	}
	if err = save(filepath.Join(dir, "sources.json"), sources); err != nil {
		return err
	}
	if err = save(filepath.Join(dir, "analysis.json"), a); err != nil {
		return err
	}
	fmt.Printf("fresh audit complete: search=%d metadata=%d unique=%d merged_final=%d\n", rt.Search, rt.Metadata, len(ids), len(evaluations["merged"].Final))
	return nil
}
