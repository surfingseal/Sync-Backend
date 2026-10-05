package main

import (
	"context"
	"crypto/sha256"
	"example.com/sync/internal/auth"
	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	"example.com/sync/internal/directbench"
	"example.com/sync/internal/directe2e"
	"example.com/sync/internal/directmusic"
	imageproc "example.com/sync/internal/image"
	"example.com/sync/internal/router"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		log.Print(e)
		os.Exit(1)
	}
}
func run() error {
	live := flag.Bool("live", false, "explicitly enable single-photo providers and private playlist write")
	resume := flag.Bool("resume", false, "load completed Phase A only, never generate/search again")
	output := flag.String("output", "artifacts/direct-e2e-5tracks-"+time.Now().UTC().Format("20060102T150405Z"), "new artifact directory, or existing with --resume")
	image := flag.String("image", "../../work/gemini-photos/scones.jpg", "selected real image")
	flag.Parse()
	if !*live {
		fmt.Println("dry run: providers=0 Gemini max calls=1 candidates<=8 final<=5 primary+fallback+retry total<=9 private playlist; --live required")
		return nil
	}
	cfg, e := config.Load()
	if e != nil {
		return e
	}
	if cfg.RecommendationEngine != config.DirectEngine || cfg.AppEnv != "development" {
		return fmt.Errorf("local E2E requires RECOMMENDATION_ENGINE=gemini_direct APP_ENV=development; production remains disabled")
	}
	final := 5
	if v := os.Getenv("DIRECT_E2E_FINAL_TRACK_LIMIT"); v != "" {
		final, e = strconv.Atoi(v)
		if e != nil {
			return fmt.Errorf("invalid E2E final limit")
		}
	}
	dc, e := directe2e.Config(final)
	if e != nil {
		return e
	}
	for _, n := range []string{"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "YOUTUBE_API_KEY", "GOOGLE_OAUTH_CLIENT_ID", "GOOGLE_OAUTH_CLIENT_SECRET"} {
		if os.Getenv(n) == "" {
			return fmt.Errorf("missing local E2E environment: %s", n)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	oauth := auth.NewGoogleOAuthService(auth.Settings{ClientID: cfg.GoogleOAuthClientID, ClientSecret: cfg.GoogleOAuthClientSecret, RedirectURL: cfg.GoogleOAuthRedirectURL, Scope: cfg.GoogleOAuthScope, CookieSecure: cfg.OAuthCookieSecure, ForceConsent: cfg.OAuthForceConsent, Invalid: cfg.OAuthInvalid}, auth.NewInMemoryTokenStore(), auth.YouTubeChannelVerifier{})
	if !oauth.Configured() || cfg.GoogleOAuthRedirectURL != "http://localhost:8080/api/v1/auth/google/callback" {
		return fmt.Errorf("local OAuth config or registered redirect URL invalid")
	}
	// Bind first: port conflict cannot consume paid provider calls.
	listener, e := net.Listen("tcp", "127.0.0.1:8080")
	if e != nil {
		return fmt.Errorf("localhost:8080 unavailable; no provider run started")
	}
	defer listener.Close()
	b, e := directbench.ReadImage(*image)
	if e != nil {
		return e
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(b))
	if !*resume {
		if _, e = os.Stat(*output); !os.IsNotExist(e) {
			return fmt.Errorf("output must be new")
		}
		if e = os.MkdirAll(*output, 0700); e != nil {
			return e
		}
	}
	budget := client.NewSearchBudget(directe2e.SearchCap)
	cache, e := directmusic.NewCache(".cache/direct-resolver-v1.json")
	if e != nil {
		return e
	}
	processor, e := imageproc.NewProcessor(1920, imageproc.DefaultHardMaxBytes)
	if e != nil {
		return e
	}
	svc := &directmusic.Service{Config: dc}
	runner := &directe2e.Runner{Service: svc, Processor: processor, Budget: budget, Output: *output, ImageID: "still-life-scones", ImageHash: hash}
	if *resume {
		cp, e := directe2e.Load(*output)
		if e != nil {
			return e
		}
		if cp.SHA256 != hash {
			return fmt.Errorf("checkpoint image mismatch")
		}
		runner.Checkpoint = cp
	} else {
		vc := cfg
		vc.VertexModel = config.DefaultVertexModel
		vc.VertexThinkingLevel = "MEDIUM"
		vc.VertexTimeout = 60 * time.Second
		generator, e := client.NewVertexDirectRecommender(ctx, vc)
		if e != nil {
			return e
		}
		yt, e := client.NewBudgetedYouTubeClient(ctx, cfg.YouTubeAPIKey, budget)
		if e != nil {
			return e
		}
		svc.Generator = generator
		svc.Resolver = &directmusic.Resolver{Client: yt, Cache: cache, Config: dc}
		directe2e.Save(*output, "config.json", map[string]any{"engine": "gemini_direct", "local_only": true, "model": vc.VertexModel, "prompt_version": client.DirectPromptVersion, "resolver": directmusic.ResolverVersion, "candidate_max": 8, "final_target": final, "whole_search_cap": 9, "cache": "existing_positive_revalidated", "production_default": "legacy", "playlist_privacy": "private"})
		absolute, _ := filepath.Abs(*image)
		directe2e.Save(*output, "selected-image.json", map[string]any{"id": runner.ImageID, "path": absolute, "sha256": hash, "bytes": len(b)})
	}
	fmt.Printf("LOCAL E2E image=%s hash=%s candidates<=8 final=%d whole_search_hard_cap=9 expected_max_search=9 private=true artifacts=%s\n", *image, hash, final, *output)
	app := &directe2e.App{Runner: runner, Base: router.New(cfg, nil, nil, oauth), OAuth: oauth}
	server := &http.Server{Handler: app, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case e := <-done:
		if e != http.ErrServerClosed {
			return e
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 130*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
	return nil
}
