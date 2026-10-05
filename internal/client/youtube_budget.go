package client

import (
	"context"
	"google.golang.org/api/option"
	googlehttp "google.golang.org/api/transport/http"
	"google.golang.org/api/youtube/v3"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// BudgetTransport guards actual outbound search attempts, including caller/SDK
// retries. One atomic budget is shared for the entire local E2E process.
type BudgetTransport struct {
	Base   http.RoundTripper
	Budget *SearchBudget
}

func (t BudgetTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/search") && (t.Budget == nil || !t.Budget.Take()) {
		return nil, ErrSearchBudget
	}
	return t.Base.RoundTrip(r)
}
func (b *SearchBudget) Used() int { return int(b.used.Load()) }
func NewBudgetedYouTubeClient(ctx context.Context, key string, budget *SearchBudget) (*YouTubeClient, error) {
	if strings.TrimSpace(key) == "" {
		return nil, ErrMusicNotConfigured
	}
	// No reused-connection retry inside net/http; redirects cannot spend hidden calls.
	base := &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second}
	tr, err := googlehttp.NewTransport(ctx, BudgetTransport{Base: base, Budget: budget}, option.WithAPIKey(key))
	if err != nil {
		return nil, ErrMusicSearch
	}
	hc := &http.Client{Transport: tr, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	sdk, err := youtube.NewService(ctx, option.WithHTTPClient(hc), option.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		return nil, ErrMusicSearch
	}
	return &YouTubeClient{service: sdk}, nil
}
