package client

import (
	"context"
	"errors"
	"example.com/sync/internal/model"
)

var (
	ErrMusicSearch        = errors.New("music search failed")
	ErrYouTubeQuota       = errors.New("YouTube quota exceeded")
	ErrMusicNotConfigured = errors.New("YouTube search not configured")
)

type MusicQueryGenerator interface {
	GenerateMusicQueries(context.Context, model.ImageAnalysis, model.MusicPreferences, int) ([]string, error)
}
type MusicSearchClient interface {
	SearchMusic(context.Context, model.MusicSearchQuery) ([]model.YouTubeSearchResult, error)
	GetVideos(context.Context, []string) ([]model.YouTubeVideo, error)
}
