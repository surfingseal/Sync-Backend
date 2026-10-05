package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"example.com/sync/internal/config"
	"example.com/sync/internal/model"
	"google.golang.org/genai"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStructuredMusicQueries(t *testing.T) {
	for _, tc := range []struct {
		json  string
		valid bool
	}{{`{"queries":["korean calm acoustic music","dream pop warm music"]}`, true}, {`{"queries":[]}`, false}, {`{"queries":["a","b","c"]}`, false}, {`{"queries":null}`, false}, {`{"queries":[""]}`, false}, {`{"queries":["http://example.com"]}`, false}, {`{"queries":["a"],"song":"invented"}`, false}, {"```json\n{}\n```", false}} {
		queries, err := decodeMusicQueries([]byte(tc.json), 2)
		if (err == nil) != tc.valid {
			t.Fatal(tc, queries, err)
		}
		if err != nil && !errors.Is(err, ErrInvalidAIResponse) {
			t.Fatal(err)
		}
	}
}

func TestVertexQueryRequestContainsOnlyAnalysisJSON(t *testing.T) {
	analysisData, err := os.ReadFile("../model/testdata/analysis.json")
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := model.DecodeImageAnalysis(analysisData)
	if err != nil {
		t.Fatal(err)
	}
	vertex := newTestVertex(t, func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		parts := body["contents"].([]any)[0].(map[string]any)["parts"].([]any)
		if len(parts) != 1 {
			t.Fatal("image resent to query generator")
		}
		part := parts[0].(map[string]any)
		if _, exists := part["inlineData"]; exists {
			t.Fatal("inline image in query request")
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(part["text"].(string)), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["analysis"] == nil || payload["preferences"] == nil {
			t.Fatal("missing analysis/preferences")
		}
		cfg := body["generationConfig"].(map[string]any)
		if cfg["responseMimeType"] != "application/json" || cfg["responseJsonSchema"] == nil {
			t.Fatal("missing query schema")
		}
		system := body["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string)
		for _, rule := range []string{"Do not generate song titles.", "Do not recommend specific artists.", "The actual tracks will be retrieved from YouTube Data API."} {
			if !strings.Contains(system, rule) {
				t.Fatal("missing query constraint")
			}
		}
		response := map[string]any{"usageMetadata": map[string]any{"promptTokenCount": 100, "candidatesTokenCount": 20, "thoughtsTokenCount": 10}, "candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]any{"text": `{"queries":["calm acoustic official audio","warm dream pop music"]}`}}}}}}
		data, _ := json.Marshal(response)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	ctx, metrics := MeasureCall(context.Background())
	queries, err := vertex.GenerateMusicQueries(ctx, *analysis, model.MusicPreferences{Languages: []string{"ko", "en"}, Count: 10}, 2)
	snapshot := metrics.Snapshot()
	if snapshot.InputTokens != 100 || snapshot.OutputTokens != 20 || snapshot.ThoughtTokens != 10 || snapshot.OutputBytes == 0 {
		t.Fatal("missing query metrics", snapshot)
	}
	if err != nil || len(queries) != 2 {
		t.Fatal(queries, err)
	}
}

func TestQueryBenchmarkSingleAttemptAndSafeFailureMetrics(t *testing.T) {
	calls := 0
	cfg := config.Config{GoogleCloudProject: "test-project", GoogleCloudLocation: "global", VertexModel: "test-model", VertexTimeout: time.Second, VertexRetryAttempts: 1, VertexRetryMode: "sdk"}
	cc := vertexClientConfig(cfg)
	cc.HTTPClient = &http.Client{Transport: observedTransport{base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"code":429,"message":"do not expose provider detail","status":"RESOURCE_EXHAUSTED"}}`))}, nil
	})}}
	sdk, err := genai.NewClient(context.Background(), cc)
	if err != nil {
		t.Fatal(err)
	}
	vertex := &VertexImageAnalyzer{client: sdk, model: "test-model"}
	ctx, metrics := MeasureCall(context.Background())
	queries, err := vertex.GenerateMusicQueries(ctx, model.ImageAnalysis{}, model.MusicPreferences{Languages: []string{"ko", "en"}}, 2)
	snapshot := metrics.Snapshot()
	if !errors.Is(err, ErrAIRateLimited) || calls != 1 || snapshot.Attempts != 1 || len(queries) != 0 || len(snapshot.StatusCodes) != 1 || snapshot.StatusCodes[0] != 429 {
		t.Fatal(queries, err, snapshot, calls)
	}
	if strings.Contains(err.Error(), "do not expose provider detail") {
		t.Fatal("unsafe error")
	}
}
