package client

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"example.com/sync/internal/config"
	"example.com/sync/internal/model"
	"google.golang.org/genai"
)

//go:embed analysis_schema.json
var analysisSchemaJSON []byte

type VertexImageAnalyzer struct {
	client    *genai.Client
	model     string
	project   string
	location  string
	timeout   time.Duration
	schema    map[string]any
	thinking  genai.ThinkingLevel
	retryMode string
	attempts  int
}

var _ ImageAnalyzer = (*VertexImageAnalyzer)(nil)

// vertexClientConfig leaves HTTPClient and Credentials unset so the SDK discovers
// ADC and constructs its authenticated HTTP transport. Explicit project/location
// ensure SDK environment API keys cannot select Vertex express mode.
func vertexClientConfig(cfg config.Config) *genai.ClientConfig {
	attempts := cfg.VertexRetryAttempts
	if attempts == 0 {
		attempts = 3
	}
	if cfg.VertexRetryMode == "budget" {
		attempts = 1
	}
	return &genai.ClientConfig{
		Backend: genai.BackendVertexAI, Project: cfg.GoogleCloudProject, Location: cfg.GoogleCloudLocation,
		HTTPOptions: genai.HTTPOptions{
			APIVersion: "v1", Timeout: genai.Ptr(cfg.VertexTimeout),
			RetryOptions: &genai.HTTPRetryOptions{
				Attempts: genai.Ptr(int32(attempts)), InitialDelay: genai.Ptr(0.5), ExpBase: genai.Ptr(2.0),
				Jitter: genai.Ptr(0.25), MaxDelay: genai.Ptr(2.0),
				HTTPStatusCodes: []int32{429, 500, 502, 503, 504},
			},
		},
	}
}

func NewVertexImageAnalyzer(ctx context.Context, cfg config.Config) (*VertexImageAnalyzer, error) {
	if cfg.GoogleCloudProject == "" || cfg.GoogleCloudLocation == "" || cfg.VertexModel == "" || cfg.VertexTimeout <= 0 {
		return nil, fmt.Errorf("%w: Vertex project, location, model and positive timeout are required", ErrAIConfiguration)
	}
	cc := vertexClientConfig(cfg)
	sdk, err := genai.NewClient(ctx, cc)
	if err != nil {
		// SDK errors can contain credential paths or credential JSON. Do not wrap
		// their contents into errors consumed by logs or API handlers.
		return nil, fmt.Errorf("%w: ADC/client initialization failed; check ADC availability and validity (cause=%T)", ErrAIConfiguration, err)
	}
	base := cc.HTTPClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	cc.HTTPClient.Transport = observedTransport{base: base}
	schema, err := AnalysisSchema()
	if err != nil {
		return nil, fmt.Errorf("%w: invalid embedded analysis schema", ErrAIConfiguration)
	}
	return &VertexImageAnalyzer{client: sdk, model: cfg.VertexModel, project: cfg.GoogleCloudProject,
		location: cfg.GoogleCloudLocation, timeout: cfg.VertexTimeout, schema: schema, thinking: genai.ThinkingLevel(cfg.VertexThinkingLevel), retryMode: cfg.VertexRetryMode, attempts: cfg.VertexRetryAttempts}, nil
}

func (v *VertexImageAnalyzer) AnalyzeImage(ctx context.Context, image []byte, mimeType string) (result *model.ImageAnalysis, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	ctx, metrics := WithCallMetrics(ctx)
	start := time.Now()
	status := 0
	defer func() {
		metrics.mu.Lock()
		metrics.VertexMS = float64(time.Since(start)) / float64(time.Millisecond)
		metrics.mu.Unlock()
		m := metrics.Snapshot()
		if external, _ := ctx.Value(outerMetricsKey{}).(*CallMetrics); external != nil {
			external.mu.Lock()
			external.Attempts = m.Attempts
			external.RetryWaitMS = m.RetryWaitMS
			external.StatusCodes = m.StatusCodes
			external.VertexMS = m.VertexMS
			external.OutputBytes = m.OutputBytes
			external.InputTokens = m.InputTokens
			external.OutputTokens = m.OutputTokens
			external.ThoughtTokens = m.ThoughtTokens
			external.mu.Unlock()
		}
		log.Printf("vertex model=%s thinking_level=%s vertex_total_ms=%.2f attempt_count=%d retry_wait_ms=%.2f http_status=%d output_bytes=%d input_tokens=%d output_tokens=%d thought_tokens=%d success=%t", v.model, v.thinking, m.VertexMS, m.Attempts, m.RetryWaitMS, status, m.OutputBytes, m.InputTokens, m.OutputTokens, m.ThoughtTokens, resultErr == nil)
	}()
	generate := func() (*genai.GenerateContentResponse, error) {
		return v.client.Models.GenerateContent(ctx, v.model, []*genai.Content{{
			Role: "user", Parts: []*genai.Part{
				{Text: "Analyze the visual atmosphere of this image using the supplied JSON schema."},
				{InlineData: &genai.Blob{Data: image, MIMEType: mimeType}},
			},
		}}, &genai.GenerateContentConfig{
			SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: EnhancedAnalysisPrompt()}}},
			ResponseMIMEType:  "application/json", ResponseJsonSchema: v.schema, MaxOutputTokens: 4096, ThinkingConfig: &genai.ThinkingConfig{ThinkingLevel: v.thinking},
		})
	}
	response, err := generate()
	if v.retryMode == "budget" && v.attempts > 1 && canBudgetRetry(ctx, err, time.Since(start)) {
		if pauseErr := retryPause(ctx); pauseErr != nil {
			return nil, pauseErr
		}
		response, err = generate()
	}
	if response != nil {
		metrics.mu.Lock()
		metrics.OutputBytes = len([]byte(response.Text()))
		if u := response.UsageMetadata; u != nil {
			metrics.InputTokens = u.PromptTokenCount
			metrics.OutputTokens = u.CandidatesTokenCount
			metrics.ThoughtTokens = u.ThoughtsTokenCount
		}
		metrics.mu.Unlock()
	}
	status = http.StatusOK
	if err != nil {
		var apiErr genai.APIError
		status = 0
		if errors.As(err, &apiErr) {
			status = apiErr.Code
		}
	}
	if err != nil {
		return nil, mapVertexError(ctx, err)
	}
	if response == nil || len(response.Candidates) != 1 || response.Candidates[0] == nil || response.Candidates[0].FinishReason != genai.FinishReasonStop {
		return nil, fmt.Errorf("%w: Vertex returned no complete candidate", ErrInvalidAIResponse)
	}
	analysis, err := model.DecodeEnhancedImageAnalysis([]byte(response.Text()))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidAIResponse, err)
	}
	if analysis.SchemaVersion != model.EnhancedAnalysisVersion || analysis.MusicProfile.VocalPreference != "" || analysis.MusicProfile.InstrumentalPreference != nil {
		return nil, ErrInvalidAIResponse
	}
	return analysis, nil
}

func mapVertexError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return context.DeadlineExceeded
	}
	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case 408, 504:
			return context.DeadlineExceeded
		case 429:
			return fmt.Errorf("%w: Vertex HTTP 429", ErrAIRateLimited)
		case 500, 502, 503:
			return fmt.Errorf("%w: Vertex HTTP %d", ErrAIUnavailable, apiErr.Code)
		case 401, 403, 404:
			return fmt.Errorf("%w: Vertex HTTP %d; check ADC, model availability and aiplatform.endpoints.predict permission", ErrAIConfiguration, apiErr.Code)
		default:
			return fmt.Errorf("%w: Vertex HTTP %d", ErrAIService, apiErr.Code)
		}
	}
	// Authentication refresh failures are not always genai.APIError. Unknown
	// failures are sanitized so token endpoints and credential contents stay private.
	return fmt.Errorf("%w: Vertex transport/authentication failure (cause=%T)", ErrAIConfiguration, err)
}
