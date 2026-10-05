package client

import (
	"context"
	"net/http"
	"sync"
	"time"
)

type CallMetrics struct {
	mu                                       sync.Mutex
	Attempts                                 int     `json:"attempt_count"`
	RetryWaitMS                              float64 `json:"retry_wait_ms"`
	StatusCodes                              []int   `json:"http_statuses"`
	VertexMS                                 float64 `json:"vertex_total_ms"`
	OutputBytes                              int     `json:"output_bytes"`
	InputTokens, OutputTokens, ThoughtTokens int32
	lastEnd                                  time.Time
}
type CallSnapshot struct {
	Attempts      int     `json:"attempt_count"`
	RetryWaitMS   float64 `json:"retry_wait_ms"`
	StatusCodes   []int   `json:"http_statuses"`
	VertexMS      float64 `json:"vertex_total_ms"`
	OutputBytes   int     `json:"output_bytes"`
	InputTokens   int32   `json:"input_tokens"`
	OutputTokens  int32   `json:"output_tokens"`
	ThoughtTokens int32   `json:"thought_tokens"`
}
type metricsKey struct{}
type outerMetricsKey struct{}

func MeasureCall(ctx context.Context) (context.Context, *CallMetrics) {
	m := &CallMetrics{}
	return context.WithValue(ctx, outerMetricsKey{}, m), m
}
func WithCallMetrics(ctx context.Context) (context.Context, *CallMetrics) {
	m := &CallMetrics{}
	return context.WithValue(ctx, metricsKey{}, m), m
}
func (m *CallMetrics) Snapshot() CallSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return CallSnapshot{m.Attempts, m.RetryWaitMS, append([]int(nil), m.StatusCodes...), m.VertexMS, m.OutputBytes, m.InputTokens, m.OutputTokens, m.ThoughtTokens}
}

// Wrap the SDK-created authenticated transport, preserving ADC, connection reuse
// and cancellation. Never inspect/log headers, URLs, request bodies or tokens.
// retry_wait_ms estimates the inter-attempt gap including small SDK overhead.
type observedTransport struct{ base http.RoundTripper }

func (t observedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	m, _ := r.Context().Value(metricsKey{}).(*CallMetrics)
	if m == nil {
		return t.base.RoundTrip(r)
	}
	m.mu.Lock()
	if !m.lastEnd.IsZero() {
		m.RetryWaitMS += float64(time.Since(m.lastEnd)) / float64(time.Millisecond)
	}
	m.Attempts++
	m.mu.Unlock()
	resp, err := t.base.RoundTrip(r)
	code := 0
	if resp != nil {
		code = resp.StatusCode
	}
	m.mu.Lock()
	m.StatusCodes = append(m.StatusCodes, code)
	m.lastEnd = time.Now()
	m.mu.Unlock()
	return resp, err
}
