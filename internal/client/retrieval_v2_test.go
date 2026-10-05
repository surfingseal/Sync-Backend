package client

import (
	"context"
	"example.com/sync/internal/model"
	"strings"
	"testing"
)

func TestRetrievalLanguageCoverage(t *testing.T) {
	a := model.ImageAnalysis{Mood: model.MoodAnalysis{Tags: []string{"cinematic", "dreamy"}}, MusicProfile: model.MusicProfile{Genres: []string{"city-pop", "k-indie", "chillwave"}}}
	p := model.MusicPreferences{Languages: []string{"ko", "en"}, VocalMode: "mixed"}
	q, err := (DeterministicQueryBuilder{}).GenerateMusicQueries(context.Background(), a, p, 2)
	if err != nil || len(q) != 2 || !strings.HasPrefix(q[0], "korean city pop cinematic song") || !strings.HasPrefix(q[1], "english chillwave dreamy song") {
		t.Fatal(q, err)
	}
	a.MusicProfile.Genres = []string{"k-indie", "chillwave"}
	p.Languages = []string{"en", "ko"}
	q, _ = (DeterministicQueryBuilder{}).GenerateMusicQueries(context.Background(), a, p, 2)
	if !strings.HasPrefix(q[0], "english chillwave") || !strings.HasPrefix(q[1], "korean indie") || strings.Contains(strings.Join(q, " "), "english korean indie") {
		t.Fatal(q)
	}
	a.MusicProfile.Genres = []string{"k-indie"}
	p.Languages = []string{"en"}
	q, _ = (DeterministicQueryBuilder{}).GenerateMusicQueries(context.Background(), a, p, 2)
	if strings.Contains(strings.Join(q, " "), "korean") {
		t.Fatal("incompatible genre forced", q)
	}
	// Identical genre/mood/language yields only one search direction.
	a.Mood.Tags = []string{"calm"}
	a.MusicProfile.Genres = []string{"city-pop"}
	p.Languages = []string{"ko"}
	q, _ = (DeterministicQueryBuilder{}).GenerateMusicQueries(context.Background(), a, p, 2)
	if len(q) != 1 {
		t.Fatal(q)
	}
}
func TestQueryOperatorsAndKeywordPolicy(t *testing.T) {
	a := model.ImageAnalysis{Mood: model.MoodAnalysis{Tags: []string{"calm"}}, MusicProfile: model.MusicProfile{Genres: []string{"acoustic"}}}
	p := model.MusicPreferences{Languages: []string{"en"}, VocalMode: "mixed"}
	for _, tc := range []struct{ mode, keyword, want string }{{"mixed", "", "song"}, {"mixed", "music", "music"}, {"vocal-first", "music", "song"}, {"instrumental-first", "song", "music"}, {"instrumental-only", "song", "music"}} {
		p.VocalMode = tc.mode
		q, err := (DeterministicQueryBuilder{MixedKeyword: tc.keyword}).GenerateMusicQueries(context.Background(), a, p, 1)
		if err != nil || !strings.Contains(q[0], tc.want+" -mix -playlist -compilation -backing -karaoke") {
			t.Fatal(q, err)
		}
		if strings.Contains(q[0], "-instrumental") || strings.Contains(q[0], "-lyrics") || strings.Contains(q[0], "-remix") {
			t.Fatal(q)
		}
		with := model.MusicSearchQuery{Text: q[0], MaxResults: 50}
		without := with
		without.Text = strings.ReplaceAll(with.Text, "-mix", "mix")
		if SearchCacheKey(with) == SearchCacheKey(without) {
			t.Fatal("NOT operator lost in cache normalization")
		}
	}
}
