package recommendation

import (
	"example.com/sync/internal/model"
	"example.com/sync/internal/music"
	"fmt"
	"strings"
)

type NeutralPolicy struct {
	MinMediumEstablished int      `json:"minimum_medium_established"`
	MinLanguageCoverage  int      `json:"minimum_language_coverage_per_preference"`
	RequireRelease       bool     `json:"require_release_confidence"`
	AllowedRelease       []string `json:"allowed_release_confidence"`
	MinChannels          int      `json:"minimum_channels"`
}

func DefaultNeutralPolicy() NeutralPolicy {
	return NeutralPolicy{6, 2, true, []string{"HIGH", "MEDIUM"}, 2}
}
func (p NeutralPolicy) AllowsRelease(confidence string) bool {
	if !p.RequireRelease {
		return true
	}
	for _, allowed := range p.AllowedRelease {
		if allowed == confidence {
			return true
		}
	}
	return false
}
func (p NeutralPolicy) Validate() error {
	if p.MinMediumEstablished < 0 || p.MinMediumEstablished > 20 || p.MinLanguageCoverage < 0 || p.MinLanguageCoverage > 5 || p.MinChannels < 1 {
		return fmt.Errorf("invalid neutral retrieval policy")
	}
	if p.RequireRelease && len(p.AllowedRelease) == 0 {
		return fmt.Errorf("empty release gate")
	}
	for _, level := range p.AllowedRelease {
		if level != "HIGH" && level != "MEDIUM" && level != "LOW" && level != "UNKNOWN" {
			return fmt.Errorf("invalid release confidence")
		}
	}
	return nil
}

type PlannedQuery struct {
	Role                string   `json:"role"`
	Query               string   `json:"query"`
	Genre               string   `json:"genre"`
	Language            *string  `json:"language"`
	Mood                *string  `json:"mood"`
	Order               string   `json:"order"`
	MaxResults          int64    `json:"max_results"`
	ModelScore          *float64 `json:"model_genre_score"`
	RetrievalWeight     float64  `json:"retrieval_weight"`
	AdjustedPriority    *float64 `json:"adjusted_genre_priority"`
	MoodKeywordIncluded bool     `json:"mood_keyword_included"`
}
type RetrievalPlan struct {
	Q1     PlannedQuery      `json:"q1"`
	Q2     *PlannedQuery     `json:"q2"`
	Intent model.QueryIntent `json:"intent"`
}

func KoreanGenre(name string) bool {
	switch name {
	case "k-indie", "k-pop", "korean-ballad", "korean-r&b":
		return true
	}
	return false
}
func validGenre(g model.QueryGenre, intent model.QueryIntent) bool {
	return g.Category != "other" && g.QueryTokens != "" && (g.Score == nil || *g.Score >= .5) && (intent.GenresFromPreferences || intent.GenreConfidence == nil || *intent.GenreConfidence >= music.LowGenreConfidence)
}
func PlanNeutral(a model.ImageAnalysis, p model.MusicPreferences, maxResults int64, mixedKeyword string) (RetrievalPlan, error) {
	intent, err := model.AdaptQueryIntent(a, p)
	if err != nil {
		return RetrievalPlan{}, err
	}
	if maxResults < 1 || maxResults > 50 {
		return RetrievalPlan{}, fmt.Errorf("invalid maxResults")
	}
	genre := model.QueryGenre{}
	for _, g := range intent.Genres {
		if validGenre(g, intent) {
			genre = g
			break
		}
	}
	return RetrievalPlan{Q1: makeQuery("genre_discovery", genre, "", "relevance", maxResults, p, mixedKeyword), Intent: intent}, nil
}
func makeQuery(role string, g model.QueryGenre, language, order string, maxResults int64, p model.MusicPreferences, mixedKeyword string) PlannedQuery {
	core := g.QueryTokens
	if core == "" {
		core = "music"
	}
	if language != "" {
		names := map[string]string{"ko": "korean", "en": "english", "ja": "japanese", "es": "spanish", "fr": "french", "de": "german", "zh": "chinese", "pt": "portuguese"}
		name := names[language]
		if name == "" {
			name = language + " language"
		}
		core = name + " " + core
	}
	keyword := "song"
	mode := p.EffectiveVocalMode()
	if mode == "instrumental-first" || mode == "instrumental-only" {
		keyword = "instrumental music"
	} else if mode == "mixed" && mixedKeyword == "music" {
		keyword = "music"
	}
	core = strings.TrimSpace(core + " " + keyword)
	// Avoid "music music" in the unknown-genre fallback.
	// Deduplicate lexical tokens before adding NOT operators. Registry tokens
	// such as "korean indie" already contain the language word.
	unique := []string{}
	seenWords := map[string]bool{}
	for _, word := range strings.Fields(core) {
		if !seenWords[word] {
			unique = append(unique, word)
			seenWords[word] = true
		}
	}
	core = strings.Join(unique, " ")
	q := PlannedQuery{Role: role, Query: core + " -mix -playlist -compilation -backing -karaoke", Genre: g.Name, Order: order, MaxResults: maxResults, ModelScore: g.Score, RetrievalWeight: music.RetrievalWeight(g.Name)}
	if language != "" {
		q.Language = &language
	}
	if g.Score != nil {
		value := *g.Score * q.RetrievalWeight
		q.AdjustedPriority = &value
	}
	return q
}

type CandidateStrength struct {
	Raw                          int            `json:"raw"`
	HardEligible                 int            `json:"hard_eligible"`
	PoolCount                    int            `json:"eligible_after_release_and_quality_gate"`
	ReleaseCounts                map[string]int `json:"hard_eligible_release_counts"`
	ReleaseTrusted               int            `json:"release_high_medium"`
	Discovery                    int            `json:"discovery"`
	Medium                       int            `json:"medium"`
	Established                  int            `json:"established"`
	UniqueChannels               int            `json:"unique_channels"`
	LanguageCounts               map[string]int `json:"vocal_language_coverage_counts"`
	AffinityCounts               map[string]int `json:"language_affinity_counts"`
	TextOnlyCount                int            `json:"weak_upload_text_only"`
	AudioEvidenceCount           int            `json:"reported_audio_language"`
	InstrumentalCoverageExcluded int            `json:"instrumental_excluded_from_vocal_coverage"`
	PreferredLanguages           []string       `json:"preferred_languages"`
	Instrumental                 int            `json:"instrumental"`
	VocalFriendly                int            `json:"vocal_friendly"`
	VocalUnknown                 int            `json:"vocal_unknown"`
	MedianViews                  float64        `json:"median_views"`
	MedianLikes                  *float64       `json:"median_likes"`
}
type AdaptiveDecision struct {
	Q2Executed                   bool     `json:"q2_executed"`
	Reasons                      []string `json:"reasons"`
	SelectedRole                 string   `json:"selected_q2_role"`
	Explanation                  string   `json:"decision_explanation"`
	PopularityStrengthSufficient bool     `json:"popularity_strength_sufficient"`
	LanguageCoverageSufficient   bool     `json:"language_coverage_sufficient"`
	MissingLanguages             []string `json:"missing_languages"`
}

func DecideNeutral(plan RetrievalPlan, strength CandidateStrength, p model.MusicPreferences, policy NeutralPolicy, margin int, mixedKeyword string) (AdaptiveDecision, *PlannedQuery) {
	d := AdaptiveDecision{Reasons: []string{}, MissingLanguages: []string{}, LanguageCoverageSufficient: true}
	if strength.PoolCount < p.Count+margin {
		d.Reasons = append(d.Reasons, "insufficient_candidate_count")
	}
	if strength.ReleaseTrusted < p.Count {
		d.Reasons = append(d.Reasons, "insufficient_release_trust")
	}
	d.PopularityStrengthSufficient = PopularityStrengthSufficient(strength, policy)
	if !d.PopularityStrengthSufficient {
		d.Reasons = append(d.Reasons, "insufficient_popularity_strength")
	}
	if strength.UniqueChannels < policy.MinChannels {
		d.Reasons = append(d.Reasons, "insufficient_diversity")
	}
	seen := map[string]bool{}
	for _, lang := range p.Languages {
		if !seen[lang] && strength.LanguageCounts[lang] < policy.MinLanguageCoverage {
			d.MissingLanguages = append(d.MissingLanguages, lang)
		}
		seen[lang] = true
	}
	if len(d.MissingLanguages) > 0 {
		d.LanguageCoverageSufficient = false
		d.Reasons = append(d.Reasons, "insufficient_language_coverage")
	}
	mode := p.EffectiveVocalMode()
	if (mode == "mixed" || mode == "vocal-first") && strength.PoolCount > 0 && float64(strength.Instrumental)/float64(strength.PoolCount) > .4 {
		d.Reasons = append(d.Reasons, "instrumental_pool_bias")
	}
	if len(d.Reasons) == 0 {
		d.Explanation = "quantity, release, popularity, channel diversity, language coverage and vocal balance sufficient"
		return d, nil
	}
	genre := model.QueryGenre{}
	for _, g := range plan.Intent.Genres {
		if g.Name == plan.Q1.Genre {
			genre = g
			break
		}
	}
	role, lang, order := "secondary_genre_coverage", "", "relevance"
	switch {
	case strength.PoolCount < p.Count+margin || strength.ReleaseTrusted < p.Count || !d.PopularityStrengthSufficient || strength.UniqueChannels < policy.MinChannels:
		role = "popularity_rescue"
		order = "viewCount"
	case !d.LanguageCoverageSufficient:
		role = "language_coverage"
		lang = d.MissingLanguages[0]
		// Choose the most under-covered preferred affinity; ties keep preference order.
		for _, missing := range d.MissingLanguages {
			if strength.LanguageCounts[missing] < strength.LanguageCounts[lang] {
				lang = missing
			}
		}
		chosen := model.QueryGenre{}
		if lang == "ko" {
			for _, g := range plan.Intent.Genres {
				if validGenre(g, plan.Intent) && KoreanGenre(g.Name) {
					chosen = g
					break
				}
			}
		}
		if chosen.Name == "" {
			for _, g := range plan.Intent.Genres {
				if validGenre(g, plan.Intent) && g.Name != plan.Q1.Genre && (!KoreanGenre(g.Name) || lang == "ko") {
					chosen = g
					break
				}
			}
		}
		if chosen.Name != "" {
			genre = chosen
		} else if KoreanGenre(genre.Name) && lang != "ko" {
			genre = model.QueryGenre{}
		}
	default:
		for _, g := range plan.Intent.Genres {
			if validGenre(g, plan.Intent) && g.Name != plan.Q1.Genre {
				genre = g
				break
			}
		}
	}
	q := makeQuery(role, genre, lang, order, plan.Q1.MaxResults, p, mixedKeyword)
	d.Q2Executed = true
	d.SelectedRole = role
	d.Explanation = "priority: candidate count, release trust, popularity strength, diversity, then language coverage; weak core pool uses neutral popularity rescue; all deficient axes retained"
	if q.Query == plan.Q1.Query && q.Order == plan.Q1.Order {
		d.Q2Executed = false
		d.Explanation += "; identical query/order has no additional coverage, so no repeated search"
		return d, nil
	}
	return d, &q
}

// Reuses the existing medium bucket boundary (10k views); no new popularity
// anchor or ranking threshold. Coverage cannot hide a discovery-heavy pool.
func PopularityStrengthSufficient(s CandidateStrength, p NeutralPolicy) bool {
	return s.Medium+s.Established >= p.MinMediumEstablished && s.Established > 0 && s.Discovery <= s.Medium+s.Established && s.MedianViews >= float64(policy.ViewAnchors[2].Count)
}
