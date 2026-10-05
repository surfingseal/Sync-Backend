package client

import (
	"context"
	"example.com/sync/internal/model"
	"reflect"
	"strings"
	"testing"
)

func TestRetrievalPriorityAndVocalPolicy(t *testing.T) {
	a := model.ImageAnalysis{Mood: model.MoodAnalysis{Primary: "serene", Secondary: []string{"reflective"}}, MusicProfile: model.MusicProfile{Tempo: "slow", VocalPreference: "instrumental", GenreCandidates: []model.GenreCandidate{{Category: "cinematic", Name: "soundtrack", Score: .85}, {Category: "electronic", Name: "ambient", Score: .8}, {Category: "classical", Name: "neo-classical", Score: .7}}}, Confidence: &model.AnalysisConfidence{Genre: .8}}
	p := model.MusicPreferences{Languages: []string{"ko", "en"}}
	g := DeterministicQueryBuilder{}
	q, err := g.GenerateMusicQueries(context.Background(), a, p, 2)
	if err != nil || !strings.Contains(q[0], "ambient") || !strings.Contains(q[1], "neo classical") {
		t.Fatal(q, err)
	}
	for _, text := range q {
		if strings.Contains(text, "instrumental") || len(strings.Fields(text)) > 12 {
			t.Fatal(q)
		}
	}
	q2, _ := g.GenerateMusicQueries(context.Background(), a, p, 2)
	if !reflect.DeepEqual(q, q2) {
		t.Fatal("nondeterministic")
	}
	p.VocalMode = "instrumental-only"
	q, _ = g.GenerateMusicQueries(context.Background(), a, p, 2)
	for _, text := range q {
		if !strings.Contains(text, "instrumental") {
			t.Fatal(q)
		}
	}
	p.VocalMode = "mixed"
	a.MusicProfile.GenreCandidates[2] = model.GenreCandidate{Category: "pop", Name: "dream-pop", Score: .6}
	q, _ = g.GenerateMusicQueries(context.Background(), a, p, 2)
	if !strings.Contains(strings.Join(q, " "), "dream pop") {
		t.Fatal("missing supported vocal direction", q)
	}
}
