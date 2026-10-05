package client

import (
	"example.com/sync/internal/music"
	"reflect"
	"strings"
	"testing"
)

func TestEnhancedSchemaUsesSingleTaxonomy(t *testing.T) {
	schema, err := AnalysisSchema()
	if err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	profile := properties["music_profile"].(map[string]any)["properties"].(map[string]any)
	candidate := profile["genre_candidates"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	if !reflect.DeepEqual(candidate["name"].(map[string]any)["enum"], music.GenreNames()) {
		t.Fatal("genre enum diverged")
	}
	if !reflect.DeepEqual(candidate["category"].(map[string]any)["enum"], music.Categories()) {
		t.Fatal("category enum diverged")
	}
	mood := properties["mood"].(map[string]any)["properties"].(map[string]any)
	if !reflect.DeepEqual(mood["primary"].(map[string]any)["enum"], music.Moods()) {
		t.Fatal("mood enum diverged")
	}
	prompt := EnhancedAnalysisPrompt()
	for _, rule := range []string{"visual characteristics -> mood -> musical attributes -> genre", "Do not generate actual song titles or artist names", "Do not invent genre names", "NOT calibrated probabilities", "pop: pop, indie-pop, dream-pop"} {
		if !strings.Contains(prompt, rule) {
			t.Fatal("missing prompt constraint", rule)
		}
	}
}
