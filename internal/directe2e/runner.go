// Package directe2e is an explicitly enabled, single-photo local experiment.
package directe2e

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"example.com/sync/internal/client"
	"example.com/sync/internal/directbench"
	"example.com/sync/internal/directmusic"
	imageproc "example.com/sync/internal/image"
	"example.com/sync/internal/model"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const SearchCap = 9

func Config(final int) (directmusic.Config, error) {
	c := directmusic.DefaultConfig()
	c.CandidateCount = 8
	c.FinalCount = final
	c.MaxSearchCalls = SearchCap
	if final < 1 || final > 5 {
		return c, fmt.Errorf("E2E final limit must be 1..5")
	}
	return c, c.Validate()
}

type Checkpoint struct {
	ImageID      string                   `json:"image_id"`
	SHA256       string                   `json:"image_sha256"`
	Tracks       []model.RecommendedTrack `json:"tracks"`
	Diagnostics  directmusic.Diagnostics  `json:"diagnostics"`
	SearchCalls  int                      `json:"search_calls_used"`
	SearchBudget int                      `json:"search_budget"`
	Remaining    int                      `json:"search_budget_remaining"`
}
type Runner struct {
	Mu                         sync.Mutex
	Service                    *directmusic.Service
	Processor                  imageproc.ImageProcessor
	Budget                     *client.SearchBudget
	Output, ImageID, ImageHash string
	Checkpoint                 *Checkpoint
}

func Save(out, name string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(out, name), append(b, '\n'), 0600)
}
func Load(out string) (*Checkpoint, error) {
	b, e := os.ReadFile(filepath.Join(out, "verified-e2e-tracks.json"))
	if e != nil {
		return nil, e
	}
	var cp Checkpoint
	if json.Unmarshal(b, &cp) != nil || len(cp.Tracks) > 5 || cp.SearchCalls < 0 || cp.SearchCalls > SearchCap || cp.SearchBudget != SearchCap || cp.ImageID == "" {
		return nil, fmt.Errorf("invalid checkpoint")
	}
	seen := map[string]bool{}
	metadata, err := os.ReadFile(filepath.Join(out, "resolver-results.json"))
	if err != nil {
		return nil, fmt.Errorf("checkpoint resolver evidence missing")
	}
	var resolutions []directmusic.Resolution
	if json.Unmarshal(metadata, &resolutions) != nil {
		return nil, fmt.Errorf("invalid checkpoint resolver evidence")
	}
	verified := map[string]bool{}
	for _, row := range resolutions {
		if directmusic.IsResolved(row.Status) && row.Video != nil {
			_, reason := directmusic.CheckIdentity(row.Candidate, *row.Video, directmusic.DefaultConfig())
			if reason == "" {
				verified[row.Video.VideoID] = true
			}
		}
	}
	for _, t := range cp.Tracks {
		if t.VideoID == "" || seen[t.VideoID] || !verified[t.VideoID] {
			return nil, fmt.Errorf("invalid checkpoint tracks")
		}
		seen[t.VideoID] = true
	}
	return &cp, nil
}
func Adapt(r *directmusic.Result, target int) model.RecommendationResponse {
	out := model.RecommendationResponse{DataMode: "live", Tracks: []model.RecommendedTrack{}, RequestedCount: target, Partial: len(r.Final) < target}
	ranks := map[string]int{}
	if r.Generated != nil {
		for i, t := range r.Generated.Tracks {
			ranks[directmusic.NormalizeKey(t.Artist)+"\x00"+directmusic.NormalizeKey(t.Title)] = i + 1
		}
	}
	for _, t := range r.Final {
		thumb := ""
		for _, rr := range r.Resolutions {
			if rr.Video != nil && rr.Video.VideoID == t.VideoID {
				thumb = rr.Video.ThumbnailURL
				break
			}
		}
		fit := t.Gemini.FitScore
		out.Tracks = append(out.Tracks, model.RecommendedTrack{Artist: t.Gemini.Artist, Title: t.VideoTitle, TrackTitle: t.Gemini.Title, Rank: ranks[directmusic.NormalizeKey(t.Gemini.Artist)+"\x00"+directmusic.NormalizeKey(t.Gemini.Title)], FitScore: &fit, VideoID: t.VideoID, ChannelTitle: t.Channel, ThumbnailURL: thumb, DurationSeconds: t.DurationSeconds, MatchScore: fit, MatchReasons: []string{t.Gemini.Reason}, YouTubeURL: t.YouTubeURL})
	}
	out.ReturnedCount = len(out.Tracks)
	return out
}
func (r *Runner) Response() model.RecommendationResponse {
	target := r.Service.Config.FinalCount
	if r.Checkpoint == nil {
		return model.RecommendationResponse{Tracks: []model.RecommendedTrack{}, RequestedCount: target, Partial: true}
	}
	return model.RecommendationResponse{DataMode: "live", Tracks: r.Checkpoint.Tracks, RequestedCount: target, ReturnedCount: len(r.Checkpoint.Tracks), Partial: len(r.Checkpoint.Tracks) < target}
}
func (r *Runner) Run(ctx context.Context, image model.UploadedImage) (model.RecommendationResponse, error) {
	r.Mu.Lock()
	defer r.Mu.Unlock()
	hash := fmt.Sprintf("%x", sha256.Sum256(image.Data))
	if hash != r.ImageHash {
		return model.RecommendationResponse{}, fmt.Errorf("selected image hash mismatch")
	}
	if r.Checkpoint != nil {
		return r.Response(), nil
	}
	marker, e := os.OpenFile(filepath.Join(r.Output, "phase-a-started"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return model.RecommendationResponse{}, fmt.Errorf("phase A already attempted; never regenerate")
	}
	marker.Close()
	processed, e := r.Processor.Process(ctx, image.Data, image.ContentType)
	if e != nil {
		return model.RecommendationResponse{}, fmt.Errorf("image preprocessing failed")
	}
	result, runErr := r.Service.Run(ctx, processed.Data, processed.MIMEType)
	if result == nil {
		return model.RecommendationResponse{}, fmt.Errorf("Direct configuration failed")
	}
	safety := directbench.AuditResult(result)
	if safety != (directbench.Safety{}) {
		return model.RecommendationResponse{}, fmt.Errorf("Direct safety violation")
	}
	result.Diagnostics.AverageFitScore = nil
	out := Adapt(result, r.Service.Config.FinalCount)
	cp := &Checkpoint{r.ImageID, r.ImageHash, out.Tracks, result.Diagnostics, r.Budget.Used(), SearchCap, SearchCap - r.Budget.Used()}
	// The checkpoint is persisted before Phase B, even for an empty/partial result.
	for n, v := range map[string]any{"gemini-candidates.json": result.Generated, "resolver-results.json": result.Resolutions, "recommendation-response.json": out, "search-budget.json": map[string]any{"configured": SearchCap, "actual": cp.SearchCalls, "primary": result.Diagnostics.Calls.PrimaryCalls, "fallback": result.Diagnostics.Calls.FallbackCalls, "remaining": cp.Remaining}, "verified-e2e-tracks.json": cp} {
		if e = Save(r.Output, n, v); e != nil {
			return out, fmt.Errorf("checkpoint persistence failed")
		}
	}
	r.Checkpoint = cp
	if len(out.Tracks) > 0 {
		return out, nil
	}
	if runErr != nil && !errors.Is(runErr, directmusic.ErrSearchBudget) {
		return out, fmt.Errorf("Direct provider failure")
	}
	return out, nil
}
