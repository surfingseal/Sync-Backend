package model

import (
	"example.com/sync/internal/music"
	"fmt"
	"sort"
)

// QueryIntent is an adapter projection. Nil scores/confidence mean unreported,
// never synthetic AI certainty. RawLabel is diagnostic and never query text.
type QueryGenre struct {
	Name        string   `json:"name"`
	Category    string   `json:"category"`
	QueryTokens string   `json:"query_tokens"`
	Score       *float64 `json:"score,omitempty"`
	RawLabel    string   `json:"raw_label,omitempty"`
}
type QueryIntent struct {
	SourceFormat           string       `json:"source_format"`
	PrimaryMood            string       `json:"primary_mood"`
	SecondaryMoods         []string     `json:"secondary_moods"`
	Genres                 []QueryGenre `json:"genres"`
	GenreConfidence        *float64     `json:"genre_confidence,omitempty"`
	GenresFromPreferences  bool         `json:"genres_from_preferences"`
	Tempo                  string       `json:"tempo"`
	MusicEnergy            float64      `json:"music_energy"`
	VocalPreference        string       `json:"vocal_preference"`
	InstrumentalPreference *float64     `json:"instrumental_preference,omitempty"`
}

func AdaptQueryIntent(a ImageAnalysis, p MusicPreferences) (QueryIntent, error) {
	if err := a.ValidateQuerySignals(); err != nil {
		return QueryIntent{}, err
	}
	if len(a.MusicProfile.GenreCandidates) > 3 {
		return QueryIntent{}, fmt.Errorf("too many genre candidates")
	}
	intent := QueryIntent{SourceFormat: "legacy_unscored", Tempo: a.MusicProfile.Tempo, MusicEnergy: a.MusicProfile.Energy, SecondaryMoods: []string{}, Genres: []QueryGenre{}}
	rawMoods := a.Mood.Tags
	if a.SchemaVersion == EnhancedAnalysisVersion || a.Mood.Primary != "" {
		rawMoods = append([]string{a.Mood.Primary}, a.Mood.Secondary...)
		intent.SourceFormat = "enhanced"
	}
	moods := []string{}
	seenMood := map[string]bool{}
	for _, raw := range rawMoods {
		m, ok := music.ResolveMood(raw)
		if ok && !seenMood[m] {
			seenMood[m] = true
			moods = append(moods, m)
		}
	}
	if len(moods) > 0 {
		intent.PrimaryMood = moods[0]
		intent.SecondaryMoods = append(intent.SecondaryMoods, moods[1:]...)
	}
	if a.Confidence != nil {
		value := a.Confidence.Genre
		intent.GenreConfidence = &value
	}
	if len(p.PreferredGenres) > 0 {
		intent.GenresFromPreferences = true
		for _, raw := range p.PreferredGenres {
			intent.Genres = append(intent.Genres, queryGenre(raw))
		}
	} else if len(a.MusicProfile.GenreCandidates) > 0 {
		for _, g := range a.MusicProfile.GenreCandidates {
			canonical, _ := music.Lookup(g.Name)
			score := g.Score
			intent.Genres = append(intent.Genres, QueryGenre{Name: g.Name, Category: g.Category, QueryTokens: canonical.QueryTokens, Score: &score, RawLabel: g.RawLabel})
		}
		sort.SliceStable(intent.Genres, func(i, j int) bool {
			return *intent.Genres[i].Score*music.RetrievalWeight(intent.Genres[i].Name) > *intent.Genres[j].Score*music.RetrievalWeight(intent.Genres[j].Name)
		})
	} else {
		for _, raw := range a.MusicProfile.Genres {
			intent.Genres = append(intent.Genres, queryGenre(raw))
		}
	}
	genres := []QueryGenre{}
	seen := map[string]bool{}
	for _, g := range intent.Genres {
		if !seen[g.Name] {
			seen[g.Name] = true
			genres = append(genres, g)
		}
	}
	if len(genres) > 3 {
		genres = genres[:3]
	}
	intent.Genres = genres
	return intent, nil
}
func queryGenre(raw string) QueryGenre {
	g, ok := music.Resolve(raw)
	result := QueryGenre{Name: g.Name, Category: g.Category, QueryTokens: g.QueryTokens}
	if !ok {
		result.RawLabel = raw
	}
	return result
}
