package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/sync/internal/config"
	"google.golang.org/genai"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newTestVertex(t *testing.T, transport roundTripFunc) *VertexImageAnalyzer {
	t.Helper()
	cfg := config.Config{GoogleCloudProject: "test-project", GoogleCloudLocation: "global", VertexModel: "test-model", VertexTimeout: time.Second}
	cc := vertexClientConfig(cfg)
	// Unit tests supply a fake transport, intentionally bypassing ADC/network.
	cc.HTTPClient = &http.Client{Transport: transport}
	cc.HTTPOptions.RetryOptions.InitialDelay = genai.Ptr(0.0)
	cc.HTTPOptions.RetryOptions.MaxDelay = genai.Ptr(0.0)
	cc.HTTPOptions.RetryOptions.Jitter = genai.Ptr(0.0)
	sdk, err := genai.NewClient(context.Background(), cc)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := AnalysisSchema()
	if err != nil {
		t.Fatal(err)
	}
	return &VertexImageAnalyzer{client: sdk, model: cfg.VertexModel, project: cfg.GoogleCloudProject, location: cfg.GoogleCloudLocation, timeout: cfg.VertexTimeout, schema: schema}
}

func TestVertexStructuredImageRequest(t *testing.T) {
	analysis, err := os.ReadFile("../model/testdata/analysis-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	g := newTestVertex(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "aiplatform.googleapis.com" || req.URL.Path != "/v1/projects/test-project/locations/global/publishers/google/models/test-model:generateContent" {
			t.Fatalf("path=%s", req.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		cfg := body["generationConfig"].(map[string]any)
		if cfg["responseMimeType"] != "application/json" || cfg["responseJsonSchema"] == nil {
			t.Fatal("missing structured output")
		}
		contents := body["contents"].([]any)
		parts := contents[0].(map[string]any)["parts"].([]any)
		inline := parts[1].(map[string]any)["inlineData"].(map[string]any)
		if inline["mimeType"] != "image/png" || inline["data"] != base64.StdEncoding.EncodeToString([]byte("image bytes")) {
			t.Fatal("incorrect inline image")
		}
		instructions := body["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string)
		if !strings.Contains(instructions, "Do not infer the user's actual emotions") || !strings.Contains(instructions, "Mood tags describe the image") {
			t.Fatal("missing analysis constraints")
		}
		payload, err := json.Marshal(map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]any{"text": string(analysis)}}}}}})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
	})
	result, err := g.AnalyzeImage(context.Background(), []byte("image bytes"), "image/png")
	if err != nil || result == nil || result.Scene.Category != "ocean" || calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
	}
}

func TestVertexErrors(t *testing.T) {
	for _, tc := range []struct {
		name               string
		status             int
		body               string
		transportErr, want error
	}{
		{"API error", 400, `{"error":{"code":400,"message":"secret test-key private provider details"}}`, nil, ErrAIService},
		{"timeout", 0, "", context.DeadlineExceeded, context.DeadlineExceeded},
		{"upstream timeout", 504, `{"error":{"code":504,"message":"upstream timeout"}}`, nil, context.DeadlineExceeded},
		{"rate limited", 429, `{"error":{"code":429,"message":"private quota details"}}`, nil, ErrAIRateLimited},
		{"unavailable", 503, `{"error":{"code":503,"message":"private project details"}}`, nil, ErrAIUnavailable},
		{"permission", 403, `{"error":{"code":403,"message":"private IAM details"}}`, nil, ErrAIConfiguration},
		{"unknown model", 404, `{"error":{"code":404,"message":"private model details"}}`, nil, ErrAIConfiguration},
		{"invalid JSON", 200, `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"not json"}]}}]}`, nil, ErrInvalidAIResponse},
		{"missing candidate", 200, `{}`, nil, ErrInvalidAIResponse},
		{"incomplete", 200, `{"candidates":[{"finishReason":"MAX_TOKENS"}]}`, nil, ErrInvalidAIResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newTestVertex(t, func(_ *http.Request) (*http.Response, error) {
				if tc.transportErr != nil {
					return nil, tc.transportErr
				}
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			_, err := g.AnalyzeImage(context.Background(), []byte("image"), "image/jpeg")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
			if strings.Contains(err.Error(), "test-key") || strings.Contains(err.Error(), "private provider details") {
				t.Fatal("upstream secrets leaked")
			}
		})
	}
}

func TestVertexRetryPolicy(t *testing.T) {
	cc := vertexClientConfig(config.Config{GoogleCloudProject: "project", GoogleCloudLocation: "global", VertexTimeout: 20 * time.Second})
	if cc.Backend != genai.BackendVertexAI || cc.HTTPOptions.APIVersion != "v1" || cc.HTTPClient != nil || cc.Credentials != nil || cc.APIKey != "" {
		t.Fatal("Vertex ADC configuration changed")
	}
	opts := cc.HTTPOptions.RetryOptions
	if *opts.Attempts != 3 || *opts.InitialDelay != 0.5 || *opts.ExpBase != 2 || *opts.Jitter <= 0 || *cc.HTTPOptions.Timeout != 20*time.Second {
		t.Fatal("invalid retry/timeout policy")
	}
}

func TestVertexRetryAttempts(t *testing.T) {
	for _, status := range []int{429, 500, 502, 503, 504, 400, 401, 403, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			g := newTestVertex(t, func(_ *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"code":` + fmt.Sprint(status) + `,"message":"private details"}}`))}, nil
			})
			_, err := g.AnalyzeImage(context.Background(), []byte("image"), "image/jpeg")
			if err == nil {
				t.Fatal("failure accepted")
			}
			want := 1
			if status == 429 || status == 500 || status == 502 || status == 503 || status == 504 {
				want = 3
			}
			if calls != want {
				t.Fatalf("status=%d calls=%d want=%d", status, calls, want)
			}
		})
	}
}

func TestVertexCancelsDuringBackoff(t *testing.T) {
	calls := 0
	cfg := config.Config{GoogleCloudProject: "test-project", GoogleCloudLocation: "global", VertexModel: "test-model", VertexTimeout: time.Second}
	cc := vertexClientConfig(cfg)
	cc.HTTPClient = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"code":503}}`))}, nil
	})}
	sdk, err := genai.NewClient(context.Background(), cc)
	if err != nil {
		t.Fatal(err)
	}
	g := &VertexImageAnalyzer{client: sdk, model: cfg.VertexModel, timeout: cfg.VertexTimeout}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = g.AnalyzeImage(ctx, []byte("image"), "image/png")
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("err=%v calls=%d duration=%s", err, calls, time.Since(start))
	}
}

func TestMissingADCSanitized(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing-service-account.json"))
	_, err := NewVertexImageAnalyzer(context.Background(), config.Config{GoogleCloudProject: "project", GoogleCloudLocation: "global", VertexModel: "test-model", VertexTimeout: time.Second})
	if !errors.Is(err, ErrAIConfiguration) || strings.Contains(err.Error(), "missing-service-account.json") {
		t.Fatalf("unexpected credential error: %v", err)
	}
}
