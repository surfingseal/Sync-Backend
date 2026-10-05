package model

import (
	"encoding/json"
	"example.com/sync/internal/music"
	"os"
	"testing"
)

func TestV3ContractAndOptionalConfidence(t *testing.T) {
	raw, err := os.ReadFile("testdata/analysis-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	a, err := DecodeEnhancedImageAnalysis(raw)
	if err != nil || a.MusicProfile.VocalPreference != "" || a.MusicProfile.InstrumentalPreference != nil {
		t.Fatal(a, err)
	}
	var root map[string]any
	_ = json.Unmarshal(raw, &root)
	delete(root, "confidence")
	data, _ := json.Marshal(root)
	if _, err := DecodeEnhancedImageAnalysis(data); err != nil {
		t.Fatal("optional confidence", err)
	}
	if len(music.Moods()) != 24 || len(music.Genres()) != 61 || len(music.Categories()) != 12 {
		t.Fatal("controlled vocabulary size")
	}
	for _, name := range []string{"ethereal", "reflective"} {
		root["mood"].(map[string]any)["primary"] = name
		root["mood"].(map[string]any)["secondary"] = []string{}
		root["mood"].(map[string]any)["tags"] = []string{name}
		data, _ = json.Marshal(root)
		if _, err = DecodeEnhancedImageAnalysis(data); err != nil {
			t.Fatal(name, err)
		}
	}
}
func TestVocalModeRequestAndFixtureWriteGuard(t *testing.T) {
	for _, mode := range []string{"", "mixed", "vocal-first", "instrumental-first", "instrumental-only"} {
		p := MusicPreferences{VocalMode: mode, Languages: []string{"en"}, Count: 10}
		if err := p.Validate(); err != nil {
			t.Fatal(mode, err)
		}
	}
	p := MusicPreferences{VocalMode: "invalid", Languages: []string{"en"}, Count: 10}
	if p.Validate() == nil {
		t.Fatal("bad vocal mode")
	}
	request := CreatePlaylistRequest{Title: "test", Tracks: []PlaylistTrack{{VideoID: "fixture_sunset_01"}}}
	if request.Validate() == nil {
		t.Fatal("synthetic data accepted for playlist write")
	}
}
