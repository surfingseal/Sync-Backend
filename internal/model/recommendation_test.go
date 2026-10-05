package model

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestRecommendationRequest(t *testing.T) {
	analysis, err := os.ReadFile("testdata/analysis.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		prefs string
		valid bool
		count int
	}{
		{"", true, 10}, {`,"preferences":{}`, true, 10}, {`,"preferences":null`, true, 10}, {`,"preferences":{"count":5,"languages":["ja"]}`, true, 5},
		{`,"preferences":{"count":0}`, false, 0}, {`,"preferences":{"count":4}`, false, 0}, {`,"preferences":{"count":21}`, false, 0},
		{`,"preferences":{"languages":[]}`, false, 0}, {`,"preferences":{"languages":["invalid"]}`, false, 0}, {`,"preferences":{"count":"10"}`, false, 0},
		{`,"preferences":{"excluded_artists":[""]}`, false, 0}, {`,"preferences":{"unknown":1}`, false, 0},
	} {
		data := []byte(`{"analysis":` + string(analysis) + tc.prefs + `}`)
		req, err := DecodeRecommendationRequest(data, 10)
		if (err == nil) != tc.valid {
			t.Fatalf("prefs=%s err=%v", tc.prefs, err)
		}
		if tc.valid && req.Preferences.Count != tc.count {
			t.Fatal(req)
		}
	}
	for _, data := range []string{`{}`, `{"analysis":null}`, `{"analysis":{}}`, `{"analysis":` + string(analysis) + `,"unexpected":1}`, `{"analysis":` + string(analysis) + `} {}`} {
		if _, err := DecodeRecommendationRequest([]byte(data), 10); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	var raw map[string]any
	json.Unmarshal(analysis, &raw)
	visual := raw["visual"].(map[string]any)
	delete(visual, "brightness")
	modified, _ := json.Marshal(raw)
	if _, err := DecodeRecommendationRequest([]byte(`{"analysis":`+string(modified)+`}`), 10); err == nil {
		t.Fatal("missing numeric field accepted")
	}
}
func TestPreferenceBounds(t *testing.T) {
	prefs := MusicPreferences{Languages: []string{"ko"}, Count: 10, PreferredGenres: []string{strings.Repeat("a", 81)}}
	if err := prefs.Validate(); err == nil {
		t.Fatal("oversized preference accepted")
	}
}
