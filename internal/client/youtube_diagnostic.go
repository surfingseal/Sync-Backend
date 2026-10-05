package client

import (
	"context"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
	"io"
	"log/slog"
	"net/http"
)

// Diagnostic dependency injection only. Callers supply the authenticated bounded
// transport; normal server construction, SDK filters and metadata parsing remain unchanged.
func NewDiagnosticYouTubeClient(ctx context.Context, h *http.Client, observer YouTubeSearchObserver) (*YouTubeClient, error) {
	sdk, err := youtube.NewService(ctx, option.WithHTTPClient(h), option.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		return nil, err
	}
	return &YouTubeClient{service: sdk, observer: observer}, nil
}
