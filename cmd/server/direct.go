package main

import (
	"context"
	"example.com/sync/internal/auth"
	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	"example.com/sync/internal/directapi"
	"example.com/sync/internal/directmusic"
	imageproc "example.com/sync/internal/image"
	"example.com/sync/internal/service"
	"fmt"
)

// Constructors initialize credentials/SDKs, but do not generate or search music.
func buildDirect(ctx context.Context, cfg config.Config, processor imageproc.ImageProcessor, oauth *auth.GoogleOAuthService) (*directapi.Recommender, *directapi.Playlists, error) {
	if err := config.ValidateDirectStartup(cfg); err != nil {
		return nil, nil, err
	}
	if cfg.RecommendationEngine != config.DirectEngine {
		return nil, nil, nil
	}
	generator, err := client.NewVertexDirectRecommender(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	music, err := client.NewYouTubeClient(ctx, cfg.YouTubeAPIKey)
	if err != nil {
		return nil, nil, err
	}
	return assembleDirect(cfg, processor, generator, music, client.NewAlbumArtworkResolver(nil, "", client.DefaultArtworkCountry), oauth)
}

func assembleDirect(cfg config.Config, processor imageproc.ImageProcessor, generator client.DirectTrackRecommender, music client.MusicSearchClient, artwork directapi.AlbumArtworkProvider, oauth *auth.GoogleOAuthService) (*directapi.Recommender, *directapi.Playlists, error) {
	if err := config.ValidateDirectStartup(cfg); err != nil {
		return nil, nil, err
	}
	if cfg.RecommendationEngine != config.DirectEngine {
		return nil, nil, nil
	}
	if processor == nil || generator == nil || music == nil || artwork == nil {
		return nil, nil, fmt.Errorf("gemini_direct dependencies are not initialized")
	}
	policy := directmusic.DefaultConfig()
	policy.CandidateCount = directmusic.MVPCandidateCount
	policy.FinalCount = directmusic.MVPFinalCount
	policy.MaxSearchCalls = min(cfg.YouTubeSearchMaxCalls, directmusic.MVPMaxSearchCalls)
	policy.Region = cfg.YouTubeRegion
	if err := policy.Validate(); err != nil {
		return nil, nil, err
	}
	store := directapi.NewStore()
	recommender := &directapi.Recommender{Processor: processor, Store: store, Artwork: artwork, NewRunner: func() directapi.Runner {
		// Request-local cache/counters: bounded by twelve candidates, no shared
		// mutable resolver state or unbounded process-wide identity cache.
		cache, _ := directmusic.NewCache("")
		return &directmusic.Service{Generator: generator, Resolver: &directmusic.Resolver{Client: music, Cache: cache, Config: policy}, Config: policy}
	}}
	creator := service.NewPlaylistService(oauth, client.NewYouTubePlaylistClient, service.DefaultPlaylistTimeout)
	return recommender, directapi.NewPlaylists(store, oauth, creator), nil
}
