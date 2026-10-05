package client

import (
	"context"
	"example.com/sync/internal/model"
	"reflect"
	"strings"
	"testing"
)

func TestDeterministicQueries(t *testing.T) {
	a := model.ImageAnalysis{}
	a.Mood.Tags = []string{"dreamy", "nostalgic"}
	a.MusicProfile.Genres = []string{"dream-pop", "indie-pop", "acoustic"}
	a.MusicProfile.Tempo = "slow-medium"
	a.Scene.Description = "DO NOT include this description"
	p := model.MusicPreferences{Languages: []string{"ko", "en"}}
	g := DeterministicQueryBuilder{}
	q, err := g.GenerateMusicQueries(context.Background(), a, p, 2)
	if err != nil || len(q) != 2 || !strings.Contains(q[0], "korean dream pop dreamy") || !strings.Contains(q[1], "english indie pop nostalgic") {
		t.Fatal(q, err)
	}
	p.InstrumentalOnly = true
	q, _ = g.GenerateMusicQueries(context.Background(), a, p, 2)
	if !strings.Contains(q[0], "instrumental") || strings.Contains(strings.Join(q, " "), "description") {
		t.Fatal(q)
	}
	for _, a := range []model.ImageAnalysis{{}, {MusicProfile: model.MusicProfile{Genres: []string{"indie-pop", "indie pop"}}}} {
		q, _ := g.GenerateMusicQueries(context.Background(), a, p, 2)
		if len(q) < 1 {
			t.Fatal("empty fallback")
		}
		for _, s := range q {
			w := strings.Fields(s)
			seen := map[string]bool{}
			for _, v := range w {
				if seen[v] {
					t.Fatal("duplicate word", s)
				}
				seen[v] = true
			}
		}
		again, _ := g.GenerateMusicQueries(context.Background(), a, p, 2)
		if !reflect.DeepEqual(q, again) {
			t.Fatal("non deterministic")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.GenerateMusicQueries(ctx, a, p, 2); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestEnhancedQueryStrategies(t *testing.T) {
	makeAnalysis := func() model.ImageAnalysis {
		return model.ImageAnalysis{SchemaVersion: "2", Mood: model.MoodAnalysis{Primary: "dreamy", Secondary: []string{"nostalgic"}}, MusicProfile: model.MusicProfile{Tempo: "slow", VocalPreference: "either", GenreCandidates: []model.GenreCandidate{{Category: "pop", Name: "dream-pop", Score: .84}, {Category: "pop", Name: "indie-pop", Score: .73}, {Category: "electronic", Name: "ambient", Score: .61}}}, Confidence: &model.AnalysisConfidence{Genre: .8}}
	}
	prefs := model.MusicPreferences{Languages: []string{"ko", "en"}}
	cases := []struct {
		name    string
		modify  func(*model.ImageAnalysis)
		want    []string
		exclude string
		fail    bool
	}{
		{"high confidence", func(a *model.ImageAnalysis) {}, []string{"korean dream pop dreamy song -mix -playlist -compilation -backing -karaoke", "english indie pop nostalgic song -mix -playlist -compilation -backing -karaoke"}, "", false},
		{"low confidence", func(a *model.ImageAnalysis) { a.Confidence.Genre = .35 }, []string{"korean dreamy slow song -mix -playlist -compilation -backing -karaoke", "english nostalgic slow song -mix -playlist -compilation -backing -karaoke"}, "dream pop", false},
		{"other", func(a *model.ImageAnalysis) {
			a.MusicProfile.GenreCandidates = []model.GenreCandidate{{Category: "other", Name: "other", Score: .8, RawLabel: "ethereal folk artist-name"}}
		}, []string{"korean dreamy slow song -mix -playlist -compilation -backing -karaoke", "english nostalgic slow song -mix -playlist -compilation -backing -karaoke"}, "ethereal", false},
		{"instrumental", func(a *model.ImageAnalysis) { value := .9; a.MusicProfile.InstrumentalPreference = &value }, []string{"korean dream pop dreamy song -mix -playlist -compilation -backing -karaoke", "english indie pop nostalgic song -mix -playlist -compilation -backing -karaoke"}, "", false},
		{"single genre mood variation", func(a *model.ImageAnalysis) { a.MusicProfile.GenreCandidates = a.MusicProfile.GenreCandidates[:1] }, []string{"korean dream pop dreamy song -mix -playlist -compilation -backing -karaoke", "english dream pop nostalgic song -mix -playlist -compilation -backing -karaoke"}, "", false},
		{"score order", func(a *model.ImageAnalysis) { a.MusicProfile.GenreCandidates[2].Score = .95 }, []string{"korean ambient dreamy song -mix -playlist -compilation -backing -karaoke", "english dream pop nostalgic song -mix -playlist -compilation -backing -karaoke"}, "", false},
		{"duplicate genre", func(a *model.ImageAnalysis) { a.MusicProfile.GenreCandidates[1] = a.MusicProfile.GenreCandidates[0] }, []string{"korean dream pop dreamy song -mix -playlist -compilation -backing -karaoke", "english ambient nostalgic song -mix -playlist -compilation -backing -karaoke"}, "", false},
		{"invalid score", func(a *model.ImageAnalysis) { a.MusicProfile.GenreCandidates[0].Score = 1.1 }, nil, "", true},
		{"empty mood", func(a *model.ImageAnalysis) { a.Mood = model.MoodAnalysis{}; a.MusicProfile.GenreCandidates = nil }, []string{"korean slow song -mix -playlist -compilation -backing -karaoke", "english slow song -mix -playlist -compilation -backing -karaoke"}, "", false},
		{"low relevance", func(a *model.ImageAnalysis) {
			for i := range a.MusicProfile.GenreCandidates {
				a.MusicProfile.GenreCandidates[i].Score = .2
			}
		}, []string{"korean dreamy slow song -mix -playlist -compilation -backing -karaoke", "english nostalgic slow song -mix -playlist -compilation -backing -karaoke"}, "dream pop", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := makeAnalysis()
			tc.modify(&a)
			queries, err := (DeterministicQueryBuilder{}).GenerateMusicQueries(context.Background(), a, prefs, 2)
			if (err != nil) != tc.fail {
				t.Fatal(err)
			}
			if tc.fail {
				return
			}
			if !reflect.DeepEqual(queries, tc.want) {
				t.Fatal(queries, tc.want)
			}
			if tc.exclude != "" && strings.Contains(strings.Join(queries, " "), tc.exclude) {
				t.Fatal(queries)
			}
			for _, q := range queries {
				if len(strings.Fields(q)) > MaxQueryMeaningfulTokens+5 {
					t.Fatal("too long", q)
				}
			}
		})
	}
	a := makeAnalysis()
	queries, _ := (DeterministicQueryBuilder{OmitLanguageKeywords: true}).GenerateMusicQueries(context.Background(), a, prefs, 2)
	if queries[0] != "dream pop dreamy song -mix -playlist -compilation -backing -karaoke" || queries[1] != "indie pop nostalgic song -mix -playlist -compilation -backing -karaoke" {
		t.Fatal(queries)
	}
	a.Confidence.Genre = .6
	queries, _ = (DeterministicQueryBuilder{}).GenerateMusicQueries(context.Background(), a, prefs, 2)
	if queries[0] != "korean dreamy dream pop song -mix -playlist -compilation -backing -karaoke" {
		t.Fatal("middle confidence strategy", queries)
	}
	a.Confidence.Genre = .1
	prefs.PreferredGenres = []string{"jazz"}
	queries, _ = (DeterministicQueryBuilder{}).GenerateMusicQueries(context.Background(), a, prefs, 2)
	if !strings.Contains(queries[0], "jazz") {
		t.Fatal("explicit genre preference ignored", queries)
	}
}
