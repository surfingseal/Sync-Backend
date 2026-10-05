package recommendation

import (
	"example.com/sync/internal/model"
	"fmt"
	"reflect"
	"testing"
)

func TestRankingAuditDoesNotChangeResults(t *testing.T) {
	input := []Candidate{}
	likes := uint64(1000)
	for i := 0; i < 20; i++ {
		input = append(input, Candidate{Video: model.YouTubeVideo{VideoID: fmt.Sprint(i), ChannelID: fmt.Sprint(i % 5), Title: "warm dream pop official lyrics", ChannelTitle: "source - Topic", ViewCount: uint64(1000 * (i + 1)), LikeCount: &likes, LicensedContent: true, DurationSeconds: 240}, SearchRank: i})
	}
	a := model.ImageAnalysis{Mood: model.MoodAnalysis{Tags: []string{"warm"}}, MusicProfile: model.MusicProfile{Genres: []string{"dream-pop"}}}
	p := model.MusicPreferences{Count: 10, CandidatePoolLimit: 20, Languages: []string{"en"}}
	expected := RankV2(input, a, p)
	actual, audit := RankV2WithAudit(input, a, p)
	if !reflect.DeepEqual(actual, expected) || len(audit.InitialScores) != 20 || audit.MinimumScore != .30 {
		t.Fatal("audit changed ranking", len(audit.InitialScores))
	}
}
