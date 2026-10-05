package recommendation

import (
	"example.com/sync/internal/model"
	"testing"
)

func TestVocalMixSoftTarget(t *testing.T) {
	pool := []RankedCandidate{}
	for i := 0; i < 15; i++ {
		title := "instrumental"
		if i >= 5 {
			title = "official lyrics"
		}
		pool = append(pool, RankedCandidate{Video: model.YouTubeVideo{Title: title}, Track: model.RecommendedTrack{MatchScore: .8 - float64(i)*.002}})
	}
	out := SelectVocalMix(pool, model.MusicPreferences{Count: 10})
	vocal := 0
	for _, v := range out {
		if VocalKind(v.Video) == "vocal" {
			vocal++
		}
	}
	if vocal < 6 || vocal > 8 {
		t.Fatal(vocal)
	}
	out = SelectVocalMix(pool, model.MusicPreferences{Count: 10, VocalMode: "instrumental-only"})
	if len(out) != 5 {
		t.Fatal(len(out))
	}
	for _, v := range out {
		if VocalKind(v.Video) != "instrumental" {
			t.Fatal(v)
		}
	}
}
func TestMixDoesNotSacrificeQuality(t *testing.T) {
	pool := []RankedCandidate{{Video: model.YouTubeVideo{Title: "instrumental"}, Track: model.RecommendedTrack{MatchScore: .95}}, {Video: model.YouTubeVideo{Title: "lyrics"}, Track: model.RecommendedTrack{MatchScore: .4}}}
	out := SelectVocalMix(pool, model.MusicPreferences{Count: 1})
	if VocalKind(out[0].Video) != "instrumental" {
		t.Fatal(out)
	}
	if VocalKind(model.YouTubeVideo{Title: "unknown song"}) != "unknown" {
		t.Fatal("invented classification")
	}
}
