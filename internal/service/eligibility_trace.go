package service

import (
	"example.com/sync/internal/model"
	"example.com/sync/internal/recommendation"
)

// Diagnostic predicates mirror the existing eligibility checks without changing
// execution or policy. They contain no scores inferred from the image uploader.
type EligibilityCheck struct {
	Stage  string `json:"stage"`
	Passed bool   `json:"passed"`
}

func ExplainVideoEligibility(v model.YouTubeVideo, p model.MusicPreferences, region string) []EligibilityCheck {
	regionOK := (v.AllowedRegions == nil || contains(v.AllowedRegions, region)) && !contains(v.BlockedRegions, region)
	artistOK := true
	for _, artist := range p.ExcludedArtists {
		if hasPhrase(v.Title, artist) || hasPhrase(v.ChannelTitle, artist) {
			artistOK = false
		}
	}
	reason := recommendation.ExclusionReason(v)
	return []EligibilityCheck{
		{"metadata_present", v.VideoID != "" && v.Title != "" && v.ChannelTitle != ""},
		{"music_category", v.CategoryID == model.MusicCategoryID},
		{"public_and_not_live", v.Public && !v.Live},
		{"embeddable", v.Embeddable},
		{"song_form", len(recommendation.NonSongReasons(v)) == 0},
		{"duration", v.DurationSeconds >= MinTrackDurationSeconds && v.DurationSeconds <= MaxTrackDurationSeconds},
		{"region", regionOK},
		{"ai_generated", reason != "explicit AI content phrase"},
		{"transformed_audio", reason != "transformed or collection content phrase"},
		{"excluded_artist", artistOK},
	}
}
