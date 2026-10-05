package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

type MusicPreferences struct {
	// Internal pool capacity; requested Count still governs the V2 discovery cap.
	CandidatePoolLimit int      `json:"-"`
	VocalMode          string   `json:"vocal_mode,omitempty"`
	Languages          []string `json:"languages"`
	PreferredGenres    []string `json:"preferred_genres"`
	ExcludedArtists    []string `json:"excluded_artists"`
	InstrumentalOnly   bool     `json:"instrumental_only"`
	Count              int      `json:"count"`
}
type RecommendationRequest struct {
	Analysis    ImageAnalysis    `json:"analysis"`
	Preferences MusicPreferences `json:"preferences"`
}
type RecommendedTrack struct {
	TrackTitle      string   `json:"track_title,omitempty"`
	Artist          string   `json:"artist,omitempty"`
	Rank            int      `json:"rank,omitempty"`
	FitScore        *float64 `json:"fit_score,omitempty"`
	VideoID         string   `json:"video_id"`
	Title           string   `json:"title"`
	ChannelTitle    string   `json:"channel_title"`
	ThumbnailURL    string   `json:"thumbnail_url"`
	DurationSeconds int64    `json:"duration_seconds"`
	MatchScore      float64  `json:"match_score"`
	MatchReasons    []string `json:"match_reasons"`
	YouTubeURL      string   `json:"youtube_url"`
}
type RecommendationResponse struct {
	DataMode       string             `json:"data_mode,omitempty"`
	Tracks         []RecommendedTrack `json:"tracks"`
	RequestedCount int                `json:"requested_count"`
	ReturnedCount  int                `json:"returned_count"`
	Partial        bool               `json:"partial"`
}

type MusicSearchQuery struct {
	Text, Region, RelevanceLanguage, Order string
	MaxResults                             int64
}
type YouTubeSearchResult struct{ VideoID string }

const MusicCategoryID = "10"

type YouTubeVideo struct {
	Description, ChannelID, PublishedAt                    string
	ViewCount                                              uint64
	LikeCount                                              *uint64
	CommentCount                                           *uint64
	LicensedContent                                        bool
	VideoID, Title, ChannelTitle, ThumbnailURL, CategoryID string
	DurationSeconds                                        int64
	Embeddable                                             bool
	Public                                                 bool
	Live                                                   bool
	AllowedRegions, BlockedRegions                         []string
	DefaultAudioLanguage, DefaultLanguage                  string
}

func DecodeRecommendationRequest(data []byte, defaultCount int) (*RecommendationRequest, error) {
	var envelope struct {
		Analysis    json.RawMessage `json:"analysis"`
		Preferences json.RawMessage `json:"preferences"`
	}
	if err := decodeStrict(data, &envelope); err != nil {
		return nil, fmt.Errorf("invalid recommendation request")
	}
	analysis, err := DecodeImageAnalysis(envelope.Analysis)
	if err != nil {
		return nil, fmt.Errorf("invalid analysis")
	}
	// Bound model strings/lists used in prompts and ranking. The HTTP layer also
	// bounds total request bytes; no unbounded prompt input reaches providers.
	for _, text := range []string{analysis.Scene.Category, analysis.Scene.Description, analysis.Scene.TimeOfDay, analysis.Scene.Weather} {
		if utf8.RuneCountInString(text) > 1000 {
			return nil, fmt.Errorf("analysis text is too long")
		}
	}
	for _, list := range [][]string{analysis.Mood.Tags, analysis.MusicProfile.Genres, analysis.Visual.DominantColors} {
		for _, text := range list {
			if utf8.RuneCountInString(text) > 80 {
				return nil, fmt.Errorf("analysis keyword is too long")
			}
		}
	}
	prefs := MusicPreferences{VocalMode: "mixed", Languages: []string{"ko", "en"}, PreferredGenres: []string{}, ExcludedArtists: []string{}, Count: defaultCount}
	if len(envelope.Preferences) > 0 && !bytes.Equal(bytes.TrimSpace(envelope.Preferences), []byte("null")) {
		if err := decodeStrict(envelope.Preferences, &prefs); err != nil {
			return nil, fmt.Errorf("invalid preferences")
		}
	}
	if err := prefs.Validate(); err != nil {
		return nil, err
	}
	return &RecommendationRequest{Analysis: *analysis, Preferences: prefs}, nil
}
func (p MusicPreferences) Validate() error {
	switch p.VocalMode {
	case "", "mixed", "vocal-first", "instrumental-first", "instrumental-only":
	default:
		return fmt.Errorf("invalid vocal_mode")
	}
	if p.Count < 5 || p.Count > 20 {
		return fmt.Errorf("count must be between 5 and 20")
	}
	if len(p.Languages) < 1 || len(p.Languages) > 5 {
		return fmt.Errorf("languages must contain 1 to 5 codes")
	}
	for _, code := range p.Languages {
		if len(code) != 2 || code[0] < 'a' || code[0] > 'z' || code[1] < 'a' || code[1] > 'z' {
			return fmt.Errorf("languages must contain lowercase two-letter codes")
		}
	}
	for _, list := range [][]string{p.PreferredGenres, p.ExcludedArtists} {
		if len(list) > 20 {
			return fmt.Errorf("too many preference values")
		}
		for _, s := range list {
			if strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > 80 {
				return fmt.Errorf("invalid preference value")
			}
			for _, r := range s {
				if unicode.IsControl(r) {
					return fmt.Errorf("invalid preference value")
				}
			}
		}
	}
	return nil
}
func decodeStrict(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}

// Legacy instrumental_only is a user preference, never an image-derived signal.
func (p MusicPreferences) EffectiveVocalMode() string {
	if p.InstrumentalOnly {
		return "instrumental-only"
	}
	if p.VocalMode == "" {
		return "mixed"
	}
	return p.VocalMode
}
