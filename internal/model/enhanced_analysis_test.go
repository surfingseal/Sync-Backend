package model

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

func TestEnhancedAndLegacyContracts(t *testing.T) {
	legacy, _ := os.ReadFile("testdata/analysis.json")
	a, err := DecodeImageAnalysis(legacy)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := AdaptQueryIntent(*a, MusicPreferences{})
	if err != nil || projection.Genres[0].Score != nil || projection.GenreConfidence != nil {
		t.Fatal("invented legacy certainty", projection, err)
	}
	if _, err := DecodeEnhancedImageAnalysis(legacy); err == nil {
		t.Fatal("legacy provider result accepted")
	}
	enhanced, _ := os.ReadFile("testdata/enhanced-analysis.json")
	a, err = DecodeEnhancedImageAnalysis(enhanced)
	if err != nil {
		t.Fatal(err)
	}
	// Additive version still projects strings for existing ranker and clients.
	if len(a.Mood.Tags) != 4 || len(a.MusicProfile.Genres) != 3 || a.SchemaVersion != "2" {
		t.Fatal(a)
	}
	b, _ := json.Marshal(a)
	if _, err := DecodeImageAnalysis(b); err != nil {
		t.Fatal("enhanced round trip", err)
	}
	for _, change := range []func(map[string]any){
		func(m map[string]any) { m["mood"].(map[string]any)["primary"] = "invented-mood" },
		func(m map[string]any) { m["mood"].(map[string]any)["secondary"] = []string{"calm", "calm"} },
		func(m map[string]any) { m["visual"].(map[string]any)["contrast"] = "very-high" },
		func(m map[string]any) { m["music_profile"].(map[string]any)["tempo"] = "super-fast" },
		func(m map[string]any) { m["music_profile"].(map[string]any)["instrumental_preference"] = 1.1 },
		func(m map[string]any) { m["confidence"].(map[string]any)["genre"] = -.1 },
		func(m map[string]any) { delete(m["confidence"].(map[string]any), "genre") },
		func(m map[string]any) { delete(m, "schema_version") },
		func(m map[string]any) { m["music_profile"].(map[string]any)["genre_candidates"] = []any{} },
		func(m map[string]any) {
			g := m["music_profile"].(map[string]any)["genre_candidates"].([]any)[0].(map[string]any)
			delete(g, "score")
		},
		func(m map[string]any) {
			g := m["music_profile"].(map[string]any)["genre_candidates"].([]any)[0].(map[string]any)
			g["score"] = 1.1
		},
		func(m map[string]any) {
			g := m["music_profile"].(map[string]any)["genre_candidates"].([]any)[0].(map[string]any)
			g["category"] = "rock"
		},
		func(m map[string]any) {
			g := m["music_profile"].(map[string]any)["genre_candidates"].([]any)[0].(map[string]any)
			g["name"] = "invented-genre"
		},
		func(m map[string]any) {
			g := m["music_profile"].(map[string]any)["genre_candidates"].([]any)[0].(map[string]any)
			g["raw_label"] = "invented label"
		},
		func(m map[string]any) { m["music_profile"].(map[string]any)["genres"] = []string{"rock"} },
		func(m map[string]any) { m["confidence"] = nil },
	} {
		var m map[string]any
		_ = json.Unmarshal(enhanced, &m)
		change(m)
		raw, _ := json.Marshal(m)
		if _, err := DecodeEnhancedImageAnalysis(raw); err == nil {
			t.Fatal("invalid enhanced analysis accepted", string(raw))
		}
	}
}
func TestEnhancedSignalBounds(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), -.1, 1.1} {
		a := ImageAnalysis{MusicProfile: MusicProfile{GenreCandidates: []GenreCandidate{{Category: "pop", Name: "dream-pop", Score: v}}}}
		if a.ValidateQuerySignals() == nil {
			t.Fatal(v)
		}
	}
}

func TestEnhancedRecommendationEnvelopeCompatibility(t *testing.T) {
	for _, path := range []string{"testdata/analysis.json", "testdata/enhanced-analysis.json"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(map[string]any{"analysis": json.RawMessage(raw)})
		req, err := DecodeRecommendationRequest(body, 10)
		if err != nil || req.Preferences.Count != 10 || len(req.Analysis.MusicProfile.Genres) == 0 {
			t.Fatal(path, req, err)
		}
	}
	raw, _ := os.ReadFile("testdata/enhanced-analysis.json")
	a, err := DecodeEnhancedImageAnalysis(raw)
	if err != nil {
		t.Fatal(err)
	}
	a.Mood.Secondary = []string{}
	a.Mood.Tags = []string{a.Mood.Primary}
	// Empty secondaries may disappear in public serialization, but provider data
	// must include them explicitly to satisfy its stronger structured contract.
	serialized, _ := json.Marshal(a)
	if _, err := DecodeImageAnalysis(serialized); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeEnhancedImageAnalysis(serialized); err == nil {
		t.Fatal("missing provider secondary accepted")
	}
	var object map[string]any
	_ = json.Unmarshal(serialized, &object)
	object["mood"].(map[string]any)["secondary"] = []string{}
	full, _ := json.Marshal(object)
	if _, err := DecodeEnhancedImageAnalysis(full); err != nil {
		t.Fatal(err)
	}
}
