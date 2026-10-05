// Offline-only server: no environment credentials, no provider constructors.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"example.com/sync/internal/auth"
	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	"example.com/sync/internal/directapi"
	imageprocessing "example.com/sync/internal/image"
	"example.com/sync/internal/model"
	"example.com/sync/internal/router"
	"example.com/sync/internal/service"
)

type simulatedSession struct{}

func (simulatedSession) WithAuthenticatedClient(ctx context.Context, id string, f func(*http.Client) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id != "offline-session" {
		return auth.ErrSessionNotFound
	}
	return f(&http.Client{})
}

type simulatedPlaylist struct{}

func (simulatedPlaylist) CreatePlaylist(ctx context.Context, r model.CreatePlaylistRequest) (*model.PlaylistResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &model.PlaylistResult{ID: "simulation-only", Title: r.Title, PrivacyStatus: r.PrivacyStatus, URL: ""}, nil
}
func (simulatedPlaylist) AddVideo(ctx context.Context, _ string, id string, _ int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "simulation-" + id, nil
}
func main() {
	port := flag.String("port", "8081", "loopback fixture port")
	dir := flag.String("fixture", "internal/directapi/testdata/mvp-contract", "explicit synthetic MVP contract fixture")
	flag.Parse()
	result, err := directapi.LoadFixture(*dir)
	if err != nil {
		log.Fatal("failed to load offline fixture")
	}
	processor, err := imageprocessing.NewProcessor(imageprocessing.DefaultMaxDimension, imageprocessing.DefaultHardMaxBytes)
	if err != nil {
		log.Fatal(err)
	}
	store := directapi.NewStore()
	direct := &directapi.Recommender{Processor: processor, Store: store, NewRunner: func() directapi.Runner { return directapi.FixtureRunner{Result: result} }}
	sessions := simulatedSession{}
	creator := service.NewPlaylistService(sessions, func(context.Context, *http.Client) (client.PlaylistClient, error) { return simulatedPlaylist{}, nil }, service.DefaultPlaylistTimeout)
	playlists := directapi.NewPlaylists(store, sessions, creator)
	cfg := config.Config{AppEnv: "development", RecommendationEngine: config.DirectEngine, RecommendationCount: 5}
	engine := router.NewWithDirect(cfg, nil, nil, nil, direct, playlists)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Sync-Data-Mode", "fixture")
		engine.ServeHTTP(w, r)
	})
	srv := &http.Server{Addr: "127.0.0.1:" + *port, Handler: h, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("OFFLINE fixture server http://%s; uploaded image is validated, explicit synthetic fixture is replayed; no real OAuth/playlist", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
