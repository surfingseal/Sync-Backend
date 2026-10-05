package model

import (
	"strings"
	"testing"
)

func TestDirectStructuredParsing(t *testing.T) {
	good := `{"analysis_summary":"warm","tracks":[{"artist":"Artist","title":"Song","fit_score":0,"reason":"gentle"}]}`
	r, e := DecodeDirectRecommendation([]byte(good), 20)
	if e != nil || r.Tracks[0].GeminiRank != 1 || r.Tracks[0].FitScore != 0 {
		t.Fatal(r, e)
	}
	for _, bad := range []string{strings.Replace(good, `"fit_score":0,`, "", 1), strings.Replace(good, `"fit_score":0`, `"fit_score":2`, 1), strings.Replace(good, `"reason":"gentle"`, `"reason":"gentle","video_id":"invented"`, 1), strings.Replace(good, `"Artist"`, `"https://example.com"`, 1), "```json\n" + good + "\n```", good + good, `{"analysis_summary":"warm","tracks":[]}`} {
		if _, e := DecodeDirectRecommendation([]byte(bad), 20); e == nil {
			t.Fatal("accepted invalid structure")
		}
	}
	blank := strings.Replace(good, `"Artist"`, `" "`, 1)
	if _, e := DecodeDirectRecommendation([]byte(blank), 20); e != nil {
		t.Fatal("blanks should be removed during normalization", e)
	}
}
