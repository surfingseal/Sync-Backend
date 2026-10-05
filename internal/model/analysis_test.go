package model

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

func TestDecodeImageAnalysis(t *testing.T) {
	data, err := os.ReadFile("testdata/analysis.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeImageAnalysis(data); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		modify func(map[string]any)
	}{
		{"missing number", func(m map[string]any) { delete(m["visual"].(map[string]any), "brightness") }},
		{"null number", func(m map[string]any) { m["mood"].(map[string]any)["energy"] = nil }},
		{"unknown field", func(m map[string]any) { m["extra"] = true }},
		{"wrong type", func(m map[string]any) { m["mood"].(map[string]any)["energy"] = "0.3" }},
		{"negative", func(m map[string]any) { m["mood"].(map[string]any)["valence"] = -0.1 }},
		{"out of range", func(m map[string]any) { m["music_profile"].(map[string]any)["energy"] = 1.1 }},
		{"enum", func(m map[string]any) { m["visual"].(map[string]any)["motion"] = "very-high" }},
		{"missing tags", func(m map[string]any) { m["mood"].(map[string]any)["tags"] = []string{} }},
		{"too many colors", func(m map[string]any) {
			m["visual"].(map[string]any)["dominant_colors"] = []string{"a", "b", "c", "d", "e", "f"}
		}},
		{"empty scene", func(m map[string]any) { m["scene"].(map[string]any)["category"] = " " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			tc.modify(m)
			modified, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeImageAnalysis(modified); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
	for _, invalid := range [][]byte{[]byte("```json\n" + string(data) + "\n```"), []byte("{}"), []byte("null"), append(append([]byte{}, data...), data...)} {
		if _, err := DecodeImageAnalysis(invalid); err == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
}

func TestNumericRanges(t *testing.T) {
	data, err := os.ReadFile("testdata/analysis.json")
	if err != nil {
		t.Fatal(err)
	}
	for field := 0; field < 4; field++ {
		for _, value := range []float64{0, 1, -0.01, 1.01, math.NaN(), math.Inf(1)} {
			a, err := DecodeImageAnalysis(data)
			if err != nil {
				t.Fatal(err)
			}
			values := []*float64{&a.Visual.Brightness, &a.Mood.Energy, &a.Mood.Valence, &a.MusicProfile.Energy}
			*values[field] = value
			err = a.Validate()
			valid := value == 0 || value == 1
			if (err == nil) != valid {
				t.Fatalf("field=%d value=%v err=%v", field, value, err)
			}
		}
	}
}
