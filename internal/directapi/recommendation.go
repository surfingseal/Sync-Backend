// Package directapi implements the local Android contract, not a production
// readiness override. It owns request checkpoints, never images or OAuth tokens.
package directapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"sync"
	"time"

	"example.com/sync/internal/directbench"
	"example.com/sync/internal/directmusic"
	imageprocessing "example.com/sync/internal/image"
	"example.com/sync/internal/model"
)

var (
	ErrInvalidID     = errors.New("invalid recommendation id")
	ErrMissing       = errors.New("recommendation not found")
	ErrExpired       = errors.New("recommendation expired")
	ErrCapacity      = errors.New("checkpoint capacity reached")
	ErrInvalidResult = errors.New("invalid verified recommendation")
	ErrUnavailable   = errors.New("direct recommendation unavailable")
)
var validID = regexp.MustCompile(`^rec_[A-Za-z0-9_-]{43}$`)

const CheckpointTTL = time.Hour
const MaxCheckpoints = 128

type Track struct {
	Rank         int     `json:"rank"`
	Artist       string  `json:"artist"`
	TrackTitle   string  `json:"track_title"`
	VideoID      string  `json:"video_id"`
	YouTubeTitle string  `json:"youtube_title"`
	ThumbnailURL string  `json:"thumbnail_url"`
	FitScore     float64 `json:"fit_score"`
}
type Response struct {
	RecommendationID string  `json:"recommendation_id"`
	Partial          bool    `json:"partial"`
	TargetTrackCount int     `json:"target_track_count"`
	Tracks           []Track `json:"tracks"`
}
type Checkpoint struct {
	Response        Response               `json:"response"`
	CreatedAt       time.Time              `json:"created_at"`
	ExpiresAt       time.Time              `json:"expires_at"`
	ResolverVersion string                 `json:"resolver_version"`
	Evidence        []directmusic.Evidence `json:"resolver_evidence"`
}

// Store is bounded and process-local. IDs are opaque bearer capabilities: do
// not log them or publish them. Restart loses checkpoints and retry protection.
type Store struct {
	mu      sync.Mutex
	entries map[string]Checkpoint
	now     func() time.Time
}

func NewStore() *Store   { return &Store{entries: make(map[string]Checkpoint), now: time.Now} }
func clone[T any](v T) T { b, _ := json.Marshal(v); var out T; _ = json.Unmarshal(b, &out); return out }
func (s *Store) Save(ctx context.Context, response Response, evidence []directmusic.Evidence) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if len(s.entries) >= MaxCheckpoints {
		for id, c := range s.entries {
			if !now.Before(c.ExpiresAt) {
				delete(s.entries, id)
			}
		}
	}
	if len(s.entries) >= MaxCheckpoints {
		return nil, ErrCapacity
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	response.RecommendationID = "rec_" + base64.RawURLEncoding.EncodeToString(b[:])
	s.entries[response.RecommendationID] = clone(Checkpoint{response, now, now.Add(CheckpointTTL), directmusic.ResolverVersion, evidence})
	out := clone(response)
	return &out, nil
}
func (s *Store) Get(ctx context.Context, id string) (*Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validID.MatchString(id) {
		return nil, ErrInvalidID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.entries[id]
	if !ok {
		return nil, ErrMissing
	}
	if !s.now().Before(c.ExpiresAt) {
		return nil, ErrExpired
	}
	out := clone(c)
	return &out, nil
}

type Runner interface {
	Run(context.Context, []byte, string) (*directmusic.Result, error)
}
type Recommender struct {
	Processor imageprocessing.ImageProcessor
	// A fresh runner/resolver per request prevents sharing mutable call counters.
	NewRunner func() Runner
	Store     *Store
}

func (s *Recommender) Recommend(ctx context.Context, img model.UploadedImage) (*Response, error) {
	if s == nil || s.Processor == nil || s.NewRunner == nil || s.Store == nil {
		return nil, ErrUnavailable
	}
	p, err := s.Processor.Process(ctx, img.Data, img.ContentType)
	if err != nil {
		return nil, err
	}
	runner := s.NewRunner()
	if runner == nil {
		return nil, ErrUnavailable
	}
	result, err := runner.Run(ctx, p.Data, p.MIMEType)
	if err != nil && (result == nil || len(result.Final) == 0) {
		return nil, err
	}
	if result == nil {
		return nil, ErrInvalidResult
	}
	// An incomplete provider operation may still yield known verified tracks.
	audit := directbench.AuditResult(result)
	if audit.Bounds+audit.Broad+audit.Substitute+audit.Diversity+audit.APIValid != 0 {
		return nil, ErrInvalidResult
	}
	response := Response{TargetTrackCount: 10, Tracks: []Track{}}
	evidence := []directmusic.Evidence{}
	seen := map[string]bool{}
	for _, t := range result.Final {
		if t.VideoID == "" || seen[t.VideoID] || math.IsNaN(t.Gemini.FitScore) || math.IsInf(t.Gemini.FitScore, 0) || t.Gemini.FitScore < 0 || t.Gemini.FitScore > 1 {
			return nil, ErrInvalidResult
		}
		var video *model.YouTubeVideo
		for _, r := range result.Resolutions {
			if directmusic.IsResolved(r.Status) && r.Video != nil && r.Video.VideoID == t.VideoID && r.Candidate.Artist == t.Gemini.Artist && r.Candidate.Title == t.Gemini.Title {
				video = r.Video
				break
			}
		}
		if video == nil || video.Title != t.VideoTitle {
			return nil, ErrInvalidResult
		}
		seen[t.VideoID] = true
		response.Tracks = append(response.Tracks, Track{len(response.Tracks) + 1, t.Gemini.Artist, t.Gemini.Title, t.VideoID, video.Title, video.ThumbnailURL, t.Gemini.FitScore})
		evidence = append(evidence, t.ResolutionEvidence)
	}
	response.Partial = len(response.Tracks) < response.TargetTrackCount
	return s.Store.Save(ctx, response, evidence)
}
