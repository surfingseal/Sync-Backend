package model

import (
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

const DirectSchemaVersion = "direct_music_schema_v2_mvp"

type LyricLanguage string

const (
	LyricKO           LyricLanguage = "ko"
	LyricEN           LyricLanguage = "en"
	LyricKOEN         LyricLanguage = "ko_en"
	LyricInstrumental LyricLanguage = "instrumental"
	LyricUnknown      LyricLanguage = "unknown"
)

func NormalizeLyricLanguage(l LyricLanguage) LyricLanguage {
	switch l {
	case LyricKO, LyricEN, LyricKOEN, LyricInstrumental:
		return l
	default:
		return LyricUnknown
	}
}
func (l LyricLanguage) KoreanEligible() bool { return l == LyricKO || l == LyricKOEN }
func (l LyricLanguage) Allowed() bool {
	return l == LyricKO || l == LyricEN || l == LyricKOEN || l == LyricInstrumental
}

type DirectScene struct {
	Description string `json:"description"`
}
type DirectPlaylist struct {
	Title string `json:"title"`
}

func ValidDirectScene(s string) bool {
	if strings.TrimSpace(s) == "" || !validDirectText(s, 120) || strings.ContainsAny(s, "\r\n") {
		return false
	}
	for _, r := range s {
		if unicode.In(r, unicode.Hangul) {
			return true
		}
	}
	return false
}
func ValidDirectPlaylist(s string) bool {
	return strings.TrimSpace(s) != "" && validDirectText(s, 100) && !strings.ContainsAny(s, "\r\n")
}

type DirectTrack struct {
	LyricLanguage LyricLanguage `json:"lyric_language"`
	Artist        string        `json:"artist"`
	Title         string        `json:"title"`
	FitScore      float64       `json:"fit_score"`
	Reason        string        `json:"reason"`
	GeminiRank    int           `json:"gemini_rank,omitempty"`
}
type DirectMusicRecommendation struct {
	Scene           DirectScene    `json:"scene"`
	Playlist        DirectPlaylist `json:"playlist"`
	AnalysisSummary string         `json:"analysis_summary"`
	Tracks          []DirectTrack  `json:"tracks"`
}

// DecodeDirectRecommendation requires all structured fields, including an explicit
// score (zero is valid). Backend ranks cannot be supplied by the model.
func DecodeDirectRecommendation(data []byte, max int) (*DirectMusicRecommendation, error) {
	var raw struct {
		Summary  *string        `json:"analysis_summary"`
		Scene    DirectScene    `json:"scene"`
		Playlist DirectPlaylist `json:"playlist"`
		Tracks   []struct {
			Language *LyricLanguage `json:"lyric_language"`
			Artist   *string        `json:"artist"`
			Title    *string        `json:"title"`
			Score    *float64       `json:"fit_score"`
			Reason   *string        `json:"reason"`
		} `json:"tracks"`
	}
	if err := decodeStrict(data, &raw); err != nil || raw.Summary == nil || len(raw.Tracks) < 1 || len(raw.Tracks) > max || !validDirectText(*raw.Summary, 1500) || !ValidDirectScene(raw.Scene.Description) || !ValidDirectPlaylist(raw.Playlist.Title) {
		return nil, fmt.Errorf("invalid direct recommendation structure")
	}
	out := &DirectMusicRecommendation{Scene: raw.Scene, Playlist: raw.Playlist, AnalysisSummary: *raw.Summary, Tracks: make([]DirectTrack, 0, len(raw.Tracks))}
	for i, t := range raw.Tracks {
		if t.Language == nil || t.Artist == nil || t.Title == nil || t.Score == nil || t.Reason == nil || !validDirectText(*t.Artist, 200) || !validDirectText(*t.Title, 200) || !validDirectText(*t.Reason, 400) || math.IsNaN(*t.Score) || math.IsInf(*t.Score, 0) || *t.Score < 0 || *t.Score > 1 {
			return nil, fmt.Errorf("invalid direct track fields")
		}
		out.Tracks = append(out.Tracks, DirectTrack{LyricLanguage: NormalizeLyricLanguage(*t.Language), Artist: *t.Artist, Title: *t.Title, FitScore: *t.Score, Reason: *t.Reason, GeminiRank: i + 1})
	}
	return out, nil
}
func validDirectText(s string, n int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= n && !strings.Contains(strings.ToLower(s), "http:") && !strings.Contains(strings.ToLower(s), "https:")
}
