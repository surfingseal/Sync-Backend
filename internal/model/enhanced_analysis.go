package model

import (
	"encoding/json"
	"example.com/sync/internal/music"
	"fmt"
	"math"
	"reflect"
	"unicode/utf8"
)

const EnhancedAnalysisVersion = "3"

func UnitSignal(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

// ValidateEnhancements also rejects unversioned partial enhancements. Legacy
// analyses without new fields retain their previous contract and validation.
func (a *ImageAnalysis) ValidateEnhancements() error {
	enhanced := a.SchemaVersion != "" || a.Mood.Primary != "" || a.Mood.Secondary != nil || a.Visual.Contrast != "" || a.Visual.Saturation != "" || a.Confidence != nil || a.MusicProfile.GenreCandidates != nil || a.MusicProfile.InstrumentalPreference != nil
	if !enhanced {
		return nil
	}
	if a.SchemaVersion != EnhancedAnalysisVersion && a.SchemaVersion != "2" {
		return fmt.Errorf("enhanced analysis requires schema_version 2 or 3")
	}
	if !oneOf(a.Visual.Contrast, "low", "medium", "high") || !oneOf(a.Visual.Saturation, "low", "medium", "high") {
		return fmt.Errorf("invalid visual enum")
	}
	if !music.IsMood(a.Mood.Primary) || len(a.Mood.Secondary) > 3 {
		return fmt.Errorf("invalid primary/secondary mood")
	}
	seen := map[string]bool{a.Mood.Primary: true}
	for _, m := range a.Mood.Secondary {
		if !music.IsMood(m) || seen[m] {
			return fmt.Errorf("invalid or duplicate mood")
		}
		seen[m] = true
	}
	expected := append([]string{a.Mood.Primary}, a.Mood.Secondary...)
	if !reflect.DeepEqual(expected, a.Mood.Tags) {
		return fmt.Errorf("tags must mirror primary and secondary mood")
	}
	if err := a.ValidateQuerySignals(); err != nil {
		return err
	}
	if len(a.MusicProfile.GenreCandidates) < 1 || len(a.MusicProfile.GenreCandidates) > 3 {
		return fmt.Errorf("genre candidates must contain 1 to 3 items")
	}
	if a.SchemaVersion == "2" && a.MusicProfile.InstrumentalPreference == nil {
		return fmt.Errorf("instrumental preference is required")
	}
	names := []string{}
	seenGenres := map[string]bool{}
	for _, g := range a.MusicProfile.GenreCandidates {
		if seenGenres[g.Name] {
			return fmt.Errorf("duplicate genre")
		}
		seenGenres[g.Name] = true
		names = append(names, g.Name)
	}
	if !reflect.DeepEqual(names, a.MusicProfile.Genres) {
		return fmt.Errorf("genres must mirror canonical genre candidates")
	}
	return nil
}

// ValidateQuerySignals allows a builder to deduplicate repeated canonical genres,
// while refusing malformed/unknown enhanced signals before constructing a query.
func (a *ImageAnalysis) ValidateQuerySignals() error {
	if !UnitSignal(a.MusicProfile.Energy) || !UnitSignal(a.Mood.Energy) || !UnitSignal(a.Mood.Valence) {
		return fmt.Errorf("invalid musical atmosphere signal")
	}
	if a.Mood.Primary != "" && !music.IsMood(a.Mood.Primary) {
		return fmt.Errorf("invalid primary mood")
	}
	if len(a.Mood.Secondary) > 3 {
		return fmt.Errorf("too many secondary moods")
	}
	for _, m := range a.Mood.Secondary {
		if !music.IsMood(m) {
			return fmt.Errorf("invalid secondary mood")
		}
	}
	for _, g := range a.MusicProfile.GenreCandidates {
		canonical, ok := music.Lookup(g.Name)
		if !ok || canonical.Category != g.Category || !UnitSignal(g.Score) {
			return fmt.Errorf("invalid genre category/name/score")
		}
		if utf8.RuneCountInString(g.RawLabel) > 80 || g.RawLabel != "" && g.Category != "other" {
			return fmt.Errorf("raw genre label is diagnostic-only for other/unknown")
		}
	}
	if pref := a.MusicProfile.InstrumentalPreference; pref != nil && !UnitSignal(*pref) {
		return fmt.Errorf("invalid instrumental preference")
	}
	if c := a.Confidence; c != nil && (!UnitSignal(c.Mood) || !UnitSignal(c.Genre) || !UnitSignal(c.Tempo)) {
		return fmt.Errorf("invalid confidence signal")
	}
	return nil
}

// DecodeEnhancedImageAnalysis is the provider boundary: legacy data is accepted
// by DecodeImageAnalysis. Historical v2 is supported here; the Vertex client
// additionally enforces the current version at its live provider boundary.
func DecodeEnhancedImageAnalysis(data []byte) (*ImageAnalysis, error) {
	a, err := DecodeImageAnalysis(data)
	if err != nil {
		return nil, err
	}
	if a.SchemaVersion != EnhancedAnalysisVersion && a.SchemaVersion != "2" {
		return nil, fmt.Errorf("Vertex response must use enhanced schema")
	}
	var root map[string]json.RawMessage
	_ = json.Unmarshal(data, &root)
	var mood map[string]json.RawMessage
	_ = json.Unmarshal(root["mood"], &mood)
	if _, ok := mood["secondary"]; !ok {
		return nil, fmt.Errorf("Vertex secondary mood array is required")
	}
	// Secondary may be empty, but must be present in a structured provider response.
	// Public re-serialization omits an empty optional secondary array for compatibility.
	if a.SchemaVersion == "2" && a.Confidence == nil {
		return nil, fmt.Errorf("Vertex confidence signals are required")
	}
	return a, nil
}
