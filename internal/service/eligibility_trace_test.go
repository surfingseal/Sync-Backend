package service

import (
	"example.com/sync/internal/model"
	"testing"
)

func TestDiagnosticEligibilityMatchesProduction(t *testing.T) {
	p := model.MusicPreferences{ExcludedArtists: []string{"excluded singer"}}
	for _, mutate := range []func(*model.YouTubeVideo){func(*model.YouTubeVideo) {}, func(v *model.YouTubeVideo) { v.CategoryID = "22" }, func(v *model.YouTubeVideo) { v.Embeddable = false }, func(v *model.YouTubeVideo) { v.DurationSeconds = 800 }, func(v *model.YouTubeVideo) { v.BlockedRegions = []string{"KR"} }, func(v *model.YouTubeVideo) { v.Title = "AI generated calm mix" }, func(v *model.YouTubeVideo) { v.Title = "calm slowed music" }, func(v *model.YouTubeVideo) { v.ChannelTitle = "excluded singer" }, func(v *model.YouTubeVideo) { v.Public = false }} {
		v := track(1)
		mutate(&v)
		pass := true
		for _, step := range ExplainVideoEligibility(v, p, "KR") {
			pass = pass && step.Passed
		}
		if pass != eligibleVideo(v, p, "KR") {
			t.Fatal("diagnostic disagrees", v)
		}
	}
}
