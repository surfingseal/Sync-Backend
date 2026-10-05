package client

import (
	"encoding/json"
	"example.com/sync/internal/music"
)

// AnalysisSchema derives enum values from the single embedded taxonomy. Callers
// receive a fresh schema so they cannot mutate production shared vocabulary.
func AnalysisSchema() (map[string]any, error) {
	var schema map[string]any
	if err := json.Unmarshal(analysisSchemaJSON, &schema); err != nil {
		return nil, err
	}
	props := schema["properties"].(map[string]any)
	mood := props["mood"].(map[string]any)["properties"].(map[string]any)
	mood["primary"].(map[string]any)["enum"] = music.Moods()
	for _, key := range []string{"tags", "secondary"} {
		mood[key].(map[string]any)["items"].(map[string]any)["enum"] = music.Moods()
	}
	profile := props["music_profile"].(map[string]any)["properties"].(map[string]any)
	profile["genres"].(map[string]any)["items"].(map[string]any)["enum"] = music.GenreNames()
	genres := profile["genre_candidates"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	genres["name"].(map[string]any)["enum"] = music.GenreNames()
	genres["category"].(map[string]any)["enum"] = music.Categories()
	return schema, nil
}
