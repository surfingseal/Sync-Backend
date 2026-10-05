package main

import (
	"encoding/json"
	"example.com/sync/internal/client"
	"example.com/sync/internal/recommendation"
	"example.com/sync/internal/service"
	"os"
	"path/filepath"
	"testing"
)

func TestOfflineReportUsesNoProvider(t *testing.T) {
	dir := t.TempDir()
	analysis, err := os.ReadFile("../../internal/model/testdata/analysis-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	response, _ := json.Marshal(map[string]json.RawMessage{"analysis": analysis})
	run := runResult{AnalyzeStatus: 200, AnalyzeResponse: response, Search: []searchRecord{}, Metadata: []metadataRecord{}, Vertex: analyzerObservationJSON{Metrics: client.CallSnapshot{}}}
	if err = save(filepath.Join(dir, "run.json"), run); err != nil {
		t.Fatal(err)
	}
	if err = buildDiagnostics(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "quota.json"))
	if err != nil {
		t.Fatal(err)
	}
	var quota map[string]any
	if json.Unmarshal(raw, &quota) != nil {
		t.Fatal("quota format")
	}
	for _, key := range []string{"search_list_actual_calls", "videos_list_actual_calls", "vertex_text_query_calls", "user_oauth_calls", "playlist_writes"} {
		if quota[key].(float64) != 0 {
			t.Fatal(key, quota[key])
		}
	}
	for _, name := range []string{"queries.json", "retrieval.json", "ranking.json", "vocal-mix.json", "sanity.json", "timings.json", "image.json"} {
		if _, err = os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(name, err)
		}
	}
}

func TestNeutralDiagnosticsRemainOffline(t *testing.T) {
	dir := t.TempDir()
	run := runResult{AnalysisReused: true, Trace: service.RecommendationTrace{Plan: &recommendation.RetrievalPlan{}}, Search: []searchRecord{}, Metadata: []metadataRecord{}}
	if err := buildNeutralDiagnostics(dir, run); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"retrieval-plan.json", "adaptive-decision.json", "release-confidence.json", "language-coverage.json", "quota.json", "sanity.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}
