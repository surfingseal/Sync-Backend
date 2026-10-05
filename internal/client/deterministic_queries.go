package client

import (
	"context"
	"example.com/sync/internal/model"
	"example.com/sync/internal/music"
	"strings"
)

const (
	GenreConfidenceLow              = music.LowGenreConfidence
	GenreConfidenceHigh             = 0.7
	GenreRelevanceLow               = 0.5
	InstrumentalPreferenceHigh      = 0.7
	MaxQueryMeaningfulTokens        = 7
	MaxDeterministicQueryCharacters = 120
)

// The zero value keeps language keywords enabled. Production mode is still
// selected separately by RECOMMENDATION_QUERY_MODE.
type DeterministicQueryBuilder struct {
	OmitLanguageKeywords bool
	// MixedKeyword: song (default) or music. Instrumental modes always use music.
	MixedKeyword string
	// Only diagnostic callers comparing an older policy should disable exclusions.
	DisableExclusions bool
}

func (DeterministicQueryBuilder) UsesPreparedQueries() bool { return true }
func (g DeterministicQueryBuilder) GenerateMusicQueries(ctx context.Context, a model.ImageAnalysis, p model.MusicPreferences, limit int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 3 {
		return nil, ErrInvalidAIResponse
	}
	intent, err := model.AdaptQueryIntent(a, p)
	if err != nil {
		return nil, ErrInvalidAIResponse
	}
	moods := append([]string{}, intent.PrimaryMood)
	moods = append(moods, intent.SecondaryMoods...)
	languages := p.Languages
	if len(languages) == 0 {
		languages = []string{"ko", "en"}
	}
	languageNames := map[string]string{"ko": "korean", "en": "english", "ja": "japanese", "es": "spanish", "fr": "french", "de": "german", "zh": "chinese", "pt": "portuguese"}
	fallback := len(intent.Genres) == 0 || intent.Genres[0].Category == "other"
	if !intent.GenresFromPreferences && intent.GenreConfidence != nil && *intent.GenreConfidence < GenreConfidenceLow {
		fallback = true
	}
	if !intent.GenresFromPreferences && len(intent.Genres) > 0 && intent.Genres[0].Score != nil && *intent.Genres[0].Score < GenreRelevanceLow {
		fallback = true
	}
	// Preserve the existing supported vocal-friendly coverage when it does not
	// conflict with language assignment; never invent a new analysis genre.
	if p.EffectiveVocalMode() == "mixed" || p.EffectiveVocalMode() == "vocal-first" {
		for i, genre := range intent.Genres {
			if music.VocalFriendly(genre.Name) {
				if i >= limit {
					intent.Genres[limit-1], intent.Genres[i] = intent.Genres[i], intent.Genres[limit-1]
				}
				break
			}
		}
	}
	// Language coverage takes priority over selecting the next incompatible genre.
	usedGenres := map[string]bool{}
	results := []string{}
	seen := map[string]bool{}
	for i := 0; i < limit; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		code := languages[i%len(languages)]
		candidate := model.QueryGenre{}
		if !fallback {
			for _, genre := range intent.Genres {
				if usedGenres[genre.Name] || genre.Category == "other" || (genre.Score != nil && *genre.Score < GenreRelevanceLow) {
					continue
				}
				if koreanGenre(genre.Name) && code != "ko" {
					continue
				}
				candidate = genre
				break
			}
			if candidate.Name == "" {
				// Reuse a compatible genre only if no unused direction remains.
				for _, genre := range intent.Genres {
					if genre.Category != "other" && (genre.Score == nil || *genre.Score >= GenreRelevanceLow) && (!koreanGenre(genre.Name) || code == "ko") {
						candidate = genre
						break
					}
				}
			}
			// Korean-only candidates never acquire an English prefix. Prefer ko only
			// when the caller included it; otherwise use mood/tempo without that genre.
			if candidate.Name == "" && containsLanguage(languages, "ko") {
				for _, genre := range intent.Genres {
					if koreanGenre(genre.Name) && !usedGenres[genre.Name] && (genre.Score == nil || *genre.Score >= GenreRelevanceLow) {
						candidate = genre
						code = "ko"
						break
					}
				}
			}
			if candidate.Name != "" {
				usedGenres[candidate.Name] = true
			}
		}
		prefix := []string{}
		if !g.OmitLanguageKeywords {
			lang := languageNames[code]
			if lang == "" {
				lang = cleanQuery(code) + " language"
			}
			prefix = append(prefix, lang)
		}
		mood := moods[i%len(moods)]
		genreTokens := ""
		if !fallback {
			if candidate.Category != "other" && (candidate.Score == nil || *candidate.Score >= GenreRelevanceLow) {
				genreTokens = candidate.QueryTokens
			}
		}
		parts := append(prefix, genreTokens, mood)
		if !intent.GenresFromPreferences && intent.GenreConfidence != nil && *intent.GenreConfidence >= GenreConfidenceLow && *intent.GenreConfidence < GenreConfidenceHigh {
			parts = append(prefix, mood, genreTokens)
		}
		if genreTokens == "" {
			tempo := intent.Tempo
			if tempo == "" {
				switch {
				case intent.MusicEnergy > 0.7:
					tempo = "fast"
				case intent.MusicEnergy > 0 && intent.MusicEnergy < 0.4:
					tempo = "slow"
				}
			}
			parts = append(parts, tempo)
		}
		vocal := ""
		if p.EffectiveVocalMode() == "instrumental-only" || p.EffectiveVocalMode() == "instrumental-first" {
			vocal = "instrumental"
		} else if p.EffectiveVocalMode() == "vocal-first" {
			vocal = "vocal"
			if genreTokens == "" {
				vocal = "soft vocal"
			}
		}
		// Reserve space for an explicitly chosen vocal mode before capping length.
		tokens := uniqueWords(strings.Join(parts, " "))
		addition := uniqueWords(vocal)
		capacity := MaxQueryMeaningfulTokens - 1 - len(addition)
		if len(tokens) > capacity {
			tokens = tokens[:capacity]
		}
		tokens = uniqueWords(strings.Join(append(tokens, addition...), " "))
		keyword := "music"
		mode := p.EffectiveVocalMode()
		if mode == "vocal-first" || (mode == "mixed" && g.MixedKeyword != "music") {
			keyword = "song"
		}
		if g.MixedKeyword != "" && g.MixedKeyword != "song" && g.MixedKeyword != "music" {
			return nil, ErrInvalidAIResponse
		}
		tokens = uniqueWords(strings.Join(append(tokens, keyword), " "))
		query := strings.Join(tokens, " ")
		if !g.DisableExclusions {
			query += " -mix -playlist -compilation -backing -karaoke"
		}
		if len([]rune(query)) > MaxDeterministicQueryCharacters {
			continue
		}
		if query != "" && !seen[query] {
			seen[query] = true
			results = append(results, query)
		}
	}
	if len(results) == 0 {
		return []string{"music"}, nil
	}
	return results, nil
}
func cleanQuery(s string) string { return music.Normalize(s) }
func uniqueWords(s string) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, word := range strings.Fields(cleanQuery(s)) {
		if !seen[word] {
			seen[word] = true
			result = append(result, word)
		}
	}
	return result
}

// Retained for existing callers/tests; aliases are normalized by the taxonomy.
func uniqueTerms(input []string) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, s := range input {
		s = cleanQuery(s)
		if s != "" && !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

// These are retrieval language hints, not claims about an artist's identity.
func koreanGenre(name string) bool {
	switch name {
	case "k-indie", "k-pop", "korean-ballad", "korean-r&b":
		return true
	}
	return false
}
func containsLanguage(languages []string, code string) bool {
	for _, value := range languages {
		if value == code {
			return true
		}
	}
	return false
}
