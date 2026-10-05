package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"example.com/sync/internal/config"
	"google.golang.org/genai"
)

func TestVertexDirectStructuredRequest(t *testing.T) {
	calls := 0
	cfg := config.Config{GoogleCloudProject: "test", GoogleCloudLocation: "global", VertexModel: config.DefaultVertexModel, VertexTimeout: time.Second, VertexRetryMode: "budget", VertexRetryAttempts: 1}
	cc := vertexClientConfig(cfg)
	cc.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		var b map[string]any
		if e := json.NewDecoder(req.Body).Decode(&b); e != nil {
			t.Fatal(e)
		}
		g := b["generationConfig"].(map[string]any)
		if g["responseMimeType"] != "application/json" || g["responseJsonSchema"] == nil || g["maxOutputTokens"] != float64(8192) {
			t.Fatal(g)
		}
		parts := b["contents"].([]any)[0].(map[string]any)["parts"].([]any)
		if len(parts) != 2 || parts[1].(map[string]any)["inlineData"] == nil {
			t.Fatal("image missing")
		}
		prompt := b["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string)
		if prompt != DirectMusicPrompt || !strings.Contains(prompt, "Do not invent") || !strings.Contains(prompt, "Do not infer") {
			t.Fatal("prompt changed")
		}
		raw := `{"analysis_summary":"warm","tracks":[{"artist":"Real Artist","title":"Real Song","fit_score":0.9,"reason":"gentle"}]}`
		payload, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]any{"text": raw}}}}}})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
	})}
	sdk, e := genai.NewClient(context.Background(), cc)
	if e != nil {
		t.Fatal(e)
	}
	v := &VertexDirectRecommender{sdk: sdk, model: cfg.VertexModel, thinking: genai.ThinkingLevelMedium, timeout: time.Second}
	r, e := v.RecommendTracks(context.Background(), []byte("image"), "image/png", 20)
	if e != nil || r.Tracks[0].GeminiRank != 1 || calls != 1 {
		t.Fatal(r, e, calls)
	}
	if *cc.HTTPOptions.RetryOptions.Attempts != 1 {
		t.Fatal("retry enabled")
	}
}
