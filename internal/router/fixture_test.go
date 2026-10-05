package router

import (
	"bytes"
	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	"example.com/sync/internal/recommendation"
	"example.com/sync/internal/service"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestFixtureRecommendationHTTP(t *testing.T) {
	raw, err := os.ReadFile("../model/testdata/analysis-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewRecommendationService(client.DeterministicQueryBuilder{}, client.NewFixtureMusicClient(), "KR", "ko", 2, time.Second, service.WithRanker(recommendation.RankV2))
	r := New(config.Config{AppEnv: "production", RecommendationCount: 10}, nil, svc)
	payload := append([]byte(`{"analysis":`), raw...)
	payload = append(payload, []byte(`,"preferences":{"vocal_mode":"mixed"}}`)...)
	request := httptest.NewRequest("POST", "/api/v1/recommend", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, request)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"data_mode":"fixture"`) || !strings.Contains(w.Body.String(), "SYNTHETIC") {
		t.Fatal(w.Code, w.Body.String())
	}
}
