package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
)

// DecodeImageAnalysis accepts a single JSON object, never markdown or prose.
func DecodeImageAnalysis(data []byte) (*ImageAnalysis, error) {
	if err := requireFields(data, reflect.TypeOf(ImageAnalysis{})); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result ImageAnalysis
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("analysis JSON has invalid types or unknown fields")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("analysis must contain exactly one JSON object")
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return &result, nil
}

// Check presence before decoding so omitted/null numeric fields cannot become 0.
func requireFields(data []byte, typ reflect.Type) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return fmt.Errorf("analysis object is required")
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := field.Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		value, ok := fields[name]
		if !ok && strings.Contains(tag, ",omitempty") {
			continue
		}
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("missing analysis field: %s", name)
		}
		typeOf := field.Type
		if typeOf.Kind() == reflect.Pointer {
			typeOf = typeOf.Elem()
		}
		if typeOf.Kind() == reflect.Struct {
			if err := requireFields(value, typeOf); err != nil {
				return err
			}
		}
		if typeOf.Kind() == reflect.Slice && typeOf.Elem().Kind() == reflect.Struct {
			var elements []json.RawMessage
			if json.Unmarshal(value, &elements) != nil {
				return fmt.Errorf("invalid analysis array")
			}
			for _, element := range elements {
				if err := requireFields(element, typeOf.Elem()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (a *ImageAnalysis) Validate() error {
	if a == nil {
		return fmt.Errorf("analysis is missing")
	}
	for name, value := range map[string]float64{
		"visual.brightness": a.Visual.Brightness, "mood.energy": a.Mood.Energy,
		"mood.valence": a.Mood.Valence, "music_profile.energy": a.MusicProfile.Energy,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			return fmt.Errorf("%s must be between 0 and 1", name)
		}
	}
	for _, value := range []string{a.Scene.Category, a.Scene.Description, a.Scene.TimeOfDay, a.Scene.Weather} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("scene fields must be nonempty")
		}
	}
	if !oneOf(a.Visual.ColorTemperature, "warm", "neutral", "cool") || !oneOf(a.Visual.Motion, "low", "medium", "high") ||
		!oneOf(a.MusicProfile.Tempo, "slow", "slow-medium", "medium", "medium-fast", "fast") ||
		!(a.SchemaVersion == "3" && a.MusicProfile.VocalPreference == "") && !oneOf(a.MusicProfile.VocalPreference, "instrumental", "soft-vocal", "vocal", "either") {
		return fmt.Errorf("analysis contains an invalid enum value")
	}
	for _, list := range []struct {
		values   []string
		min, max int
	}{
		{a.Visual.DominantColors, 1, 5}, {a.Mood.Tags, 1, 6}, {a.MusicProfile.Genres, 1, 5},
	} {
		if len(list.values) < list.min || len(list.values) > list.max {
			return fmt.Errorf("analysis array length is invalid")
		}
		for _, value := range list.values {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("analysis array contains an empty value")
			}
		}
	}
	return a.ValidateEnhancements()
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
