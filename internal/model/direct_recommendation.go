package model

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

const DirectSchemaVersion = "direct_music_schema_v1"

type DirectTrack struct {
	Artist     string  `json:"artist"`
	Title      string  `json:"title"`
	FitScore   float64 `json:"fit_score"`
	Reason     string  `json:"reason"`
	GeminiRank int     `json:"gemini_rank,omitempty"`
}
type DirectMusicRecommendation struct {
	AnalysisSummary string        `json:"analysis_summary"`
	Tracks          []DirectTrack `json:"tracks"`
}

// DecodeDirectRecommendation requires all structured fields, including an explicit
// score (zero is valid). Backend ranks cannot be supplied by the model.
func DecodeDirectRecommendation(data []byte, max int) (*DirectMusicRecommendation, error) {
	var raw struct {
		Summary *string `json:"analysis_summary"`
		Tracks  []struct {
			Artist *string  `json:"artist"`
			Title  *string  `json:"title"`
			Score  *float64 `json:"fit_score"`
			Reason *string  `json:"reason"`
		} `json:"tracks"`
	}
	if err := decodeStrict(data, &raw); err != nil || raw.Summary == nil || len(raw.Tracks) < 1 || len(raw.Tracks) > max || !validDirectText(*raw.Summary, 1500) {
		return nil, fmt.Errorf("invalid direct recommendation structure")
	}
	out := &DirectMusicRecommendation{AnalysisSummary: *raw.Summary, Tracks: make([]DirectTrack, 0, len(raw.Tracks))}
	for i, t := range raw.Tracks {
		if t.Artist == nil || t.Title == nil || t.Score == nil || t.Reason == nil || !validDirectText(*t.Artist, 200) || !validDirectText(*t.Title, 200) || !validDirectText(*t.Reason, 400) || math.IsNaN(*t.Score) || math.IsInf(*t.Score, 0) || *t.Score < 0 || *t.Score > 1 {
			return nil, fmt.Errorf("invalid direct track fields")
		}
		out.Tracks = append(out.Tracks, DirectTrack{Artist: *t.Artist, Title: *t.Title, FitScore: *t.Score, Reason: *t.Reason, GeminiRank: i + 1})
	}
	return out, nil
}
func validDirectText(s string, n int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= n && !strings.Contains(strings.ToLower(s), "http:") && !strings.Contains(strings.ToLower(s), "https:")
}
