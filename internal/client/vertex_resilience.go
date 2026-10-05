package client

import (
	"context"
	"errors"
	"example.com/sync/internal/model"
	"google.golang.org/genai"
	"math/rand/v2"
	"time"
)

// Only fast, explicitly transient HTTP failures qualify. Credential, request,
// transport/auth ambiguity and exhausted context never enter this retry layer.
func canBudgetRetry(ctx context.Context, err error, elapsed time.Duration) bool {
	if ctx.Err() != nil || elapsed > 2*time.Second {
		return false
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < 6*time.Second {
		return false
	}
	var api genai.APIError
	if !errors.As(err, &api) {
		return false
	}
	switch api.Code {
	case 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}
func retryPause(ctx context.Context) error {
	timer := time.NewTimer(150*time.Millisecond + time.Duration(rand.IntN(50))*time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// FallbackAnalyzer is opt-in after measured quality and latency acceptance.
// It never hedges, never retries authentication errors, and reserves the measured
// fallback p95 + safety margin supplied by the caller. Same-provider fallback
// does not provide protection against an entire Vertex service outage.
type FallbackAnalyzer struct {
	Primary, Fallback ImageAnalyzer
	MinimumRemaining  time.Duration
}

func (f FallbackAnalyzer) AnalyzeImage(ctx context.Context, image []byte, mime string) (*model.ImageAnalysis, error) {
	result, err := f.Primary.AnalyzeImage(ctx, image, mime)
	if err == nil || ctx.Err() != nil || f.Fallback == nil {
		return result, err
	}
	eligible := errors.Is(err, ErrAIRateLimited) || errors.Is(err, ErrAIUnavailable) || errors.Is(err, ErrInvalidAIResponse) || errors.Is(err, context.DeadlineExceeded)
	deadline, ok := ctx.Deadline()
	if !eligible || !ok || f.MinimumRemaining <= 0 || time.Until(deadline) < f.MinimumRemaining {
		return nil, err
	}
	return f.Fallback.AnalyzeImage(ctx, image, mime)
}
