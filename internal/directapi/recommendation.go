// Package directapi implements the local Android contract, not a production
// readiness override. It owns request checkpoints, never images or OAuth tokens.
package directapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"example.com/sync/internal/client"
	"log"
	"math"
	"regexp"
	"sort"
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
const TargetTrackCount = 5
const RequiredKoreanTracks = 2
const MaxTracksPerArtist = 2

type LanguagePolicy struct {
	RequiredKoreanTracks int  `json:"required_korean_tracks"`
	ActualKoreanTracks   int  `json:"actual_korean_tracks"`
	Satisfied            bool `json:"satisfied"`
}
type RecommendationMetrics struct {
	ArtworkLookups            int     `json:"artwork_lookups"`
	ArtworkMS                 float64 `json:"artwork_ms"`
	TotalMS                   float64 `json:"total_recommendation_ms"`
	ProviderCacheHitsTotal    int64   `json:"provider_cache_hits_total"`
	ProviderCacheMissesTotal  int64   `json:"provider_cache_misses_total"`
	AvailableKoreanCandidates int     `json:"available_korean_candidates"`
}
type Track struct {
	LyricLanguage       model.LyricLanguage `json:"lyric_language"`
	KoreanEligible      bool                `json:"korean_eligible"`
	AlbumTitle          *string             `json:"album_title"`
	AlbumArtworkURL     *string             `json:"album_artwork_url"`
	YouTubeThumbnailURL string              `json:"youtube_thumbnail_url"`
	Rank                int                 `json:"rank"`
	Artist              string              `json:"artist"`
	TrackTitle          string              `json:"track_title"`
	VideoID             string              `json:"video_id"`
	YouTubeTitle        string              `json:"youtube_title"`
	ThumbnailURL        string              `json:"thumbnail_url"`
	FitScore            float64             `json:"fit_score"`
}
type Response struct {
	Scene            model.DirectScene    `json:"scene"`
	Playlist         model.DirectPlaylist `json:"playlist"`
	LanguagePolicy   LanguagePolicy       `json:"language_policy"`
	RecommendationID string               `json:"recommendation_id"`
	Partial          bool                 `json:"partial"`
	TargetTrackCount int                  `json:"target_track_count"`
	Tracks           []Track              `json:"tracks"`
}
type Checkpoint struct {
	Metrics         RecommendationMetrics  `json:"metrics"`
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
	return s.save(ctx, response, evidence, RecommendationMetrics{})
}
func (s *Store) save(ctx context.Context, response Response, evidence []directmusic.Evidence, metrics RecommendationMetrics) (*Response, error) {
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
	s.entries[response.RecommendationID] = clone(Checkpoint{Response: response, CreatedAt: now, ExpiresAt: now.Add(CheckpointTTL), ResolverVersion: directmusic.ResolverVersion, Evidence: evidence, Metrics: metrics})
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
type AlbumArtworkProvider interface {
	Resolve(context.Context, string, string) (*model.AlbumArtwork, error)
}
type mvpRunner interface {
	RunMVP(context.Context, []byte, string) (*directmusic.Result, error)
}

// Artwork is explicitly injected. Nil is offline/no-enrichment, never a hidden live call.
type Recommender struct {
	Artwork   AlbumArtworkProvider
	Processor imageprocessing.ImageProcessor
	// A fresh runner/resolver per request prevents sharing mutable call counters.
	NewRunner func() Runner
	Store     *Store
}

func (s *Recommender) Recommend(ctx context.Context, img model.UploadedImage) (*Response, error) {
	if s == nil || s.Processor == nil || s.NewRunner == nil || s.Store == nil {
		return nil, ErrUnavailable
	}
	start := time.Now()
	p, err := s.Processor.Process(ctx, img.Data, img.ContentType)
	if err != nil {
		return nil, err
	}
	runner := s.NewRunner()
	if runner == nil {
		return nil, ErrUnavailable
	}
	var result *directmusic.Result
	if mvp, ok := runner.(mvpRunner); ok {
		result, err = mvp.RunMVP(ctx, p.Data, p.MIMEType)
	} else {
		result, err = runner.Run(ctx, p.Data, p.MIMEType)
	}
	if err != nil && (result == nil || len(result.Final) == 0) {
		return nil, err
	}
	if result == nil {
		return nil, ErrInvalidResult
	}
	// An incomplete provider operation may still yield known verified tracks.
	audit := directbench.AuditResult(result)
	if audit.Bounds+audit.Broad+audit.Substitute+audit.APIValid != 0 {
		return nil, ErrInvalidResult
	}
	if result.Generated == nil || !model.ValidDirectScene(result.Generated.Scene.Description) || !model.ValidDirectPlaylist(result.Generated.Playlist.Title) {
		return nil, ErrInvalidResult
	}
	// Validate every supplied verified track's provenance before policy selection.
	videos := map[string]*model.YouTubeVideo{}
	for _, t := range result.Final {
		if t.VideoID == "" || math.IsNaN(t.Gemini.FitScore) || math.IsInf(t.Gemini.FitScore, 0) || t.Gemini.FitScore < 0 || t.Gemini.FitScore > 1 {
			return nil, ErrInvalidResult
		}
		var video *model.YouTubeVideo
		for _, r := range result.Resolutions {
			if directmusic.IsResolved(r.Status) && r.Video != nil && r.Video.VideoID == t.VideoID && r.Candidate.Artist == t.Gemini.Artist && r.Candidate.Title == t.Gemini.Title && r.Candidate.LyricLanguage == t.Gemini.LyricLanguage {
				video = r.Video
				break
			}
		}
		if video == nil || video.Title != t.VideoTitle {
			return nil, ErrInvalidResult
		}
		videos[t.VideoID] = video
	}
	artistLimit := MaxTracksPerArtist
	if service, ok := runner.(*directmusic.Service); ok {
		artistLimit = service.Config.MaxPerArtist
	}
	selected, available := selectMVPWithArtistLimit(result.Final, artistLimit)
	response := Response{Scene: result.Generated.Scene, Playlist: result.Generated.Playlist, TargetTrackCount: TargetTrackCount, Tracks: []Track{}, LanguagePolicy: LanguagePolicy{RequiredKoreanTracks: RequiredKoreanTracks}}
	evidence := []directmusic.Evidence{}
	for _, t := range selected {
		v := videos[t.VideoID]
		lang := model.NormalizeLyricLanguage(t.Gemini.LyricLanguage)
		response.Tracks = append(response.Tracks, Track{Rank: len(response.Tracks) + 1, Artist: t.Gemini.Artist, TrackTitle: t.Gemini.Title, VideoID: t.VideoID, YouTubeTitle: v.Title, ThumbnailURL: v.ThumbnailURL, YouTubeThumbnailURL: v.ThumbnailURL, FitScore: t.Gemini.FitScore, LyricLanguage: lang, KoreanEligible: lang.KoreanEligible()})
		if lang.KoreanEligible() {
			response.LanguagePolicy.ActualKoreanTracks++
		}
		evidence = append(evidence, t.ResolutionEvidence)
	}
	response.LanguagePolicy.Satisfied = response.LanguagePolicy.ActualKoreanTracks >= RequiredKoreanTracks
	response.Partial = len(response.Tracks) < TargetTrackCount || !response.LanguagePolicy.Satisfied
	metrics := RecommendationMetrics{AvailableKoreanCandidates: available}
	s.enrichArtworkWithVideos(ctx, response.Tracks, &metrics, videos)
	metrics.TotalMS = float64(time.Since(start)) / float64(time.Millisecond)
	log.Printf("direct recommendation returned=%d korean=%d language_satisfied=%t artwork_lookups=%d artwork_ms=%.2f total_ms=%.2f", len(response.Tracks), response.LanguagePolicy.ActualKoreanTracks, response.LanguagePolicy.Satisfied, metrics.ArtworkLookups, metrics.ArtworkMS, metrics.TotalMS)
	return s.Store.save(ctx, response, evidence, metrics)
}

// selectMVP reserves two diverse Korean-eligible tracks, fills by model fit,
// then restores descending fit order. No language inference, retry or padding.
func selectMVP(pool []directmusic.VerifiedTrack) ([]directmusic.VerifiedTrack, int) {
	return selectMVPWithArtistLimit(pool, MaxTracksPerArtist)
}
func selectMVPWithArtistLimit(pool []directmusic.VerifiedTrack, artistLimit int) ([]directmusic.VerifiedTrack, int) {
	if artistLimit < 1 || artistLimit > MaxTracksPerArtist {
		return []directmusic.VerifiedTrack{}, 0
	}
	sorted := append([]directmusic.VerifiedTrack(nil), pool...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Gemini.FitScore > sorted[j].Gemini.FitScore })
	eligible := []directmusic.VerifiedTrack{}
	seenVideo := map[string]bool{}
	seenPair := map[string]bool{}
	available := 0
	for _, t := range sorted {
		pair := directmusic.NormalizeKey(t.Gemini.Artist) + "\x00" + directmusic.NormalizeKey(t.Gemini.Title)
		if !t.Gemini.LyricLanguage.Allowed() || seenVideo[t.VideoID] || seenPair[pair] {
			continue
		}
		seenVideo[t.VideoID] = true
		seenPair[pair] = true
		eligible = append(eligible, t)
		if t.Gemini.LyricLanguage.KoreanEligible() {
			available++
		}
	}
	selected := []directmusic.VerifiedTrack{}
	artists := map[string]int{}
	chosen := map[string]bool{}
	for _, t := range eligible {
		if !t.Gemini.LyricLanguage.KoreanEligible() {
			continue
		}
		a := directmusic.NormalizeKey(t.Gemini.Artist)
		if artists[a] >= artistLimit {
			continue
		}
		selected = append(selected, t)
		chosen[t.VideoID] = true
		artists[a]++
		if len(selected) == RequiredKoreanTracks {
			break
		}
	}
	for _, t := range eligible {
		if len(selected) == TargetTrackCount {
			break
		}
		a := directmusic.NormalizeKey(t.Gemini.Artist)
		if chosen[t.VideoID] || artists[a] >= artistLimit {
			continue
		}
		selected = append(selected, t)
		chosen[t.VideoID] = true
		artists[a]++
	}
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].Gemini.FitScore > selected[j].Gemini.FitScore })
	return selected, available
}
func (s *Recommender) enrichArtwork(ctx context.Context, tracks []Track, m *RecommendationMetrics) {
	s.enrichArtworkWithVideos(ctx, tracks, m, nil)
}

func (s *Recommender) enrichArtworkWithVideos(ctx context.Context, tracks []Track, m *RecommendationMetrics, videos map[string]*model.YouTubeVideo) {
	if s.Artwork == nil || len(tracks) == 0 {
		return
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	jobs := make(chan int)
	var wg sync.WaitGroup
	for n := 0; n < 3; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				t := &tracks[i]
				var art *model.AlbumArtwork
				var err error
				if provider, ok := s.Artwork.(interface {
					ResolveWithArtistEvidence(context.Context, string, string, string) (*model.AlbumArtwork, error)
				}); ok && videos[t.VideoID] != nil {
					art, err = provider.ResolveWithArtistEvidence(ctx, t.Artist, t.TrackTitle, videos[t.VideoID].Title)
				} else {
					art, err = s.Artwork.Resolve(ctx, t.Artist, t.TrackTitle)
				}
				if err == nil && art != nil && art.Source == "itunes" && art.AlbumTitle != "" && art.URL != "" {
					album, url := art.AlbumTitle, art.URL
					t.AlbumTitle = &album
					t.AlbumArtworkURL = &url
				}
			}
		}()
	}
enqueue:
	for i := range tracks {
		select {
		case jobs <- i:
			m.ArtworkLookups++
		case <-ctx.Done():
			break enqueue
		}
	}
	close(jobs)
	wg.Wait()
	m.ArtworkMS = float64(time.Since(start)) / float64(time.Millisecond)
	if stats, ok := s.Artwork.(interface{ Snapshot() client.ArtworkStats }); ok {
		snapshot := stats.Snapshot()
		m.ProviderCacheHitsTotal = snapshot.CacheHits
		m.ProviderCacheMissesTotal = snapshot.CacheMisses
	}
}
