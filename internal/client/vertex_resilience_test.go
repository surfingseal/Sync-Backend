package client

import (
	"context"
	"errors"
	"example.com/sync/internal/model"
	"fmt"
	"google.golang.org/genai"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func validVertexReply(t *testing.T) *http.Response {
	t.Helper()
	raw, err := os.ReadFile("../model/testdata/analysis-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":%q}]}}],"usageMetadata":{"promptTokenCount":123,"candidatesTokenCount":42,"thoughtsTokenCount":10}}`, string(raw))))}
}
func TestObservedSDKRetryExhaustionAndSuccess(t *testing.T) {
	for _, success := range []bool{false, true} {
		calls := 0
		g := newTestVertex(t, func(r *http.Request) (*http.Response, error) {
			calls++
			if success && calls == 3 {
				return validVertexReply(t), nil
			}
			return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":503,"message":"private"}}`))}, nil
		})
		cc := g.client.ClientConfig()
		cc.HTTPClient.Transport = observedTransport{base: cc.HTTPClient.Transport}
		ctx, m := MeasureCall(context.Background())
		_, err := g.AnalyzeImage(ctx, []byte("image"), "image/jpeg")
		s := m.Snapshot()
		if calls != 3 || s.Attempts != 3 || len(s.StatusCodes) != 3 {
			t.Fatal(calls, s)
		}
		if success && (err != nil || s.OutputBytes == 0 || s.InputTokens != 123 || s.OutputTokens != 42) {
			t.Fatal(s, err)
		}
		if !success && !errors.Is(err, ErrAIUnavailable) {
			t.Fatal(err)
		}
	}
}
func TestDeadlineAwareBudgetSingleRetryLayer(t *testing.T) {
	for _, code := range []int{400, 401, 403, 429, 503, 504} {
		calls := 0
		g := newTestVertex(t, func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 2 {
				return validVertexReply(t), nil
			}
			return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"error":{"code":%d}}`, code)))}, nil
		})
		cc := g.client.ClientConfig()
		*cc.HTTPOptions.RetryOptions.Attempts = 1
		g.retryMode = "budget"
		g.attempts = 2
		g.timeout = 10 * time.Second
		_, _ = g.AnalyzeImage(context.Background(), []byte("image"), "image/jpeg")
		want := 1
		if code == 429 || code == 503 || code == 504 {
			want = 2
		}
		if calls != want {
			t.Fatal(code, calls, want)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if canBudgetRetry(ctx, genai.APIError{Code: 503}, time.Millisecond) {
		t.Fatal("insufficient deadline retried")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	if canBudgetRetry(ctx2, genai.APIError{Code: 503}, 3*time.Second) || canBudgetRetry(ctx2, context.DeadlineExceeded, time.Millisecond) {
		t.Fatal("slow/cancelled timeout retried")
	}
	cancel2()
	if canBudgetRetry(ctx2, genai.APIError{Code: 503}, time.Millisecond) {
		t.Fatal("cancelled request retried")
	}
}

type resilienceFunc func(context.Context, []byte, string) (*model.ImageAnalysis, error)

func (f resilienceFunc) AnalyzeImage(c context.Context, b []byte, m string) (*model.ImageAnalysis, error) {
	return f(c, b, m)
}
func TestFallbackConditionsAndFailure(t *testing.T) {
	for _, problem := range []error{ErrAIRateLimited, ErrAIUnavailable, ErrInvalidAIResponse, ErrAIConfiguration, context.Canceled, context.DeadlineExceeded} {
		for _, fallbackErr := range []error{nil, ErrAIUnavailable} {
			called := 0
			f := FallbackAnalyzer{Primary: resilienceFunc(func(context.Context, []byte, string) (*model.ImageAnalysis, error) { return nil, problem }), Fallback: resilienceFunc(func(context.Context, []byte, string) (*model.ImageAnalysis, error) {
				called++
				return &model.ImageAnalysis{}, fallbackErr
			}), MinimumRemaining: time.Second}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, err := f.AnalyzeImage(ctx, []byte("image"), "image/jpeg")
			cancel()
			allowed := problem == ErrAIRateLimited || problem == ErrAIUnavailable || problem == ErrInvalidAIResponse || problem == context.DeadlineExceeded
			if (called == 1) != allowed {
				t.Fatal(problem, called)
			}
			if allowed && !errors.Is(err, fallbackErr) {
				t.Fatal(err, fallbackErr)
			}
		}
	}
}

func TestFallbackRejectsCancelledOrInsufficientBudget(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if cancelled {
			cancel()
		}
		calls := 0
		f := FallbackAnalyzer{
			Primary:          resilienceFunc(func(context.Context, []byte, string) (*model.ImageAnalysis, error) { return nil, ErrAIUnavailable }),
			Fallback:         resilienceFunc(func(context.Context, []byte, string) (*model.ImageAnalysis, error) { calls++; return nil, nil }),
			MinimumRemaining: 2 * time.Second,
		}
		_, _ = f.AnalyzeImage(ctx, nil, "image/jpeg")
		cancel()
		if calls != 0 {
			t.Fatal("fallback exceeded parent deadline/cancellation")
		}
	}
}
