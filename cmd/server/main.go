package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/sync/internal/auth"
	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	imageproc "example.com/sync/internal/image"
	"example.com/sync/internal/recommendation"
	"example.com/sync/internal/router"
	"example.com/sync/internal/service"
)

func main() {
	if err := run(); err != nil {
		log.Printf("server error: %v", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := config.ValidateServerEngine(cfg.RecommendationEngine); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	analyzer, err := client.NewVertexImageAnalyzer(ctx, cfg)
	if err != nil {
		return err
	}
	processor, err := imageproc.NewProcessor(cfg.ImageMaxDimension, cfg.ImageHardMaxBytes)
	if err != nil {
		return err
	}
	images := service.NewImageService(analyzer, processor, cfg.VertexTimeout)
	var music client.MusicSearchClient
	switch cfg.RecommendationDataMode {
	case "fixture":
		music = client.NewFixtureMusicClient()
	case "replay":
		music, err = client.NewReplayMusicClient(cfg.RecommendationReplayPath)
	case "live":
		var source *client.YouTubeClient
		source, err = client.NewYouTubeClient(ctx, cfg.YouTubeAPIKey)
		if err == nil {
			music = &client.CachedMusicClient{Source: source, Cache: client.NewInMemorySearchCache(), TTL: cfg.YouTubeSearchCacheTTL, Budget: client.NewSearchBudget(cfg.YouTubeSearchMaxCalls), LiveEnabled: cfg.YouTubeLiveSearchEnabled}
		}
	}
	if err != nil {
		return err
	}
	var generator client.MusicQueryGenerator = client.DeterministicQueryBuilder{OmitLanguageKeywords: !cfg.RecommendationQueryLanguageKeywords, MixedKeyword: cfg.RecommendationMixedKeyword}
	if cfg.RecommendationQueryMode == "gemini" && cfg.RecommendationDataMode == "live" {
		generator = analyzer
	}
	ranker := recommendation.RankV2
	if cfg.RecommendationRanker == "legacy" {
		ranker = recommendation.Rank
	}
	recommendations := service.NewRecommendationService(generator, music, cfg.YouTubeRegion, cfg.YouTubeRelevanceLanguage, cfg.YouTubeSearchQueryCount, cfg.RecommendationTimeout, service.WithRanker(ranker), service.WithAdaptiveSafetyMargin(cfg.RecommendationSafetyMargin), service.WithSearchMaxResults(cfg.YouTubeSearchMaxResults), service.WithNeutralRetrieval(recommendation.NeutralPolicy{MinMediumEstablished: cfg.RecommendationMinMediumEstablished, MinLanguageCoverage: cfg.RecommendationMinLanguageCoverage, RequireRelease: cfg.RecommendationRequireRelease, AllowedRelease: cfg.RecommendationAllowedRelease, MinChannels: 2}, cfg.RecommendationMixedKeyword))
	log.Printf("recommendation retrieval_planner=neutral_v4 query_mode=%s data_mode=%s ranker=%s live_search=%t", cfg.RecommendationQueryMode, cfg.RecommendationDataMode, cfg.RecommendationRanker, cfg.YouTubeLiveSearchEnabled)

	oauth := auth.NewGoogleOAuthService(auth.Settings{
		ClientID: cfg.GoogleOAuthClientID, ClientSecret: cfg.GoogleOAuthClientSecret,
		RedirectURL: cfg.GoogleOAuthRedirectURL, Scope: cfg.GoogleOAuthScope,
		CookieSecure: cfg.OAuthCookieSecure, ForceConsent: cfg.OAuthForceConsent, Invalid: cfg.OAuthInvalid,
	}, auth.NewInMemoryTokenStore(), auth.YouTubeChannelVerifier{})
	if !oauth.Configured() {
		log.Print("Google OAuth disabled: configuration is missing or invalid")
	}
	server := newHTTPServer(cfg, router.New(cfg, images, recommendations, oauth))
	serverErr := make(chan error, 1)
	go func() {
		log.Printf("starting sync server on %s (env=%s)", server.Addr, cfg.AppEnv)
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		stop() // A second signal can terminate the process immediately.
	}

	log.Print("shutting down server")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), max(cfg.VertexTimeout, cfg.RecommendationTimeout, service.DefaultPlaylistTimeout)+5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return err
	}
	if err := <-serverErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Print("server stopped gracefully")
	return nil
}

// An empty host binds all interfaces, including Render's public service port.
func newHTTPServer(cfg config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}
