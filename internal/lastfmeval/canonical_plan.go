package lastfmeval

import (
	"example.com/sync/internal/lastfmmapping"
	"example.com/sync/internal/music"
	"fmt"
	"sort"
)

type GenreResolution struct {
	Status      string                              `json:"status,omitempty"`
	ReasonCodes []ResolutionReason                  `json:"reason_codes,omitempty"`
	Input       WeightedConcept                     `json:"input_raw_concept"`
	Canonical   *music.CanonicalGenre               `json:"sync_canonical,omitempty"`
	Provider    lastfmmapping.ResolvedProviderRoute `json:"provider_resolution"`
}
type CanonicalResolution struct {
	Genres       []GenreResolution                       `json:"genres"`
	Unknown      []lastfmmapping.UnknownGenreObservation `json:"unknown_observations"`
	RouteSources map[string][]GenreResolution            `json:"route_canonical_sources"`
}

// Canonical mapping changes only this experiment's genre path. Mood routing
// retains the prior conservative validated-only policy with no family fallback.
func BuildCanonicalPlan(profile MusicRetrievalProfile, v *Vocabulary, p Policy, c music.CanonicalCatalog, registry lastfmmapping.Registry) (Plan, error) {
	if err := registry.Validate(c); err != nil {
		return Plan{}, err
	}
	return buildResolvedPlan(profile, v, p, c, func(id string) lastfmmapping.ResolvedProviderRoute { return registry.Resolve(c, id) })
}

func buildResolvedPlan(profile MusicRetrievalProfile, v *Vocabulary, p Policy, c music.CanonicalCatalog, resolve func(string) lastfmmapping.ResolvedProviderRoute) (Plan, error) {
	out := Plan{Profile: profile, Mapped: []ConceptMapping{}, Unmapped: []ConceptMapping{}, Routes: []RetrievalRoute{}, Warnings: []string{}, TaxonomyResolution: &CanonicalResolution{Genres: []GenreResolution{}, RouteSources: map[string][]GenreResolution{}}}
	if err := p.Validate(); err != nil {
		return out, err
	}
	if err := c.Validate(); err != nil {
		return out, err
	}
	type routeChoice struct {
		Mapping    ConceptMapping
		ID         string
		Effective  float64
		Resolution GenreResolution
	}
	choices := []routeChoice{}
	labels := []string{}
	for _, raw := range profile.Genres {
		if !finiteWeight(raw.Weight) {
			return out, fmt.Errorf("invalid concept weight")
		}
		labels = append(labels, raw.Name)
		g, ok := c.Resolve(raw.Name)
		gr := GenreResolution{Input: raw, Provider: lastfmmapping.ResolvedProviderRoute{ResolutionType: "unmapped", Reason: "raw concept not in Sync taxonomy"}}
		m := ConceptMapping{Input: raw, InputType: "genre", Reason: gr.Provider.Reason}
		if ok {
			gr.Canonical = &g
			gr.Provider = resolve(g.ID)
			m.LastFMTag = gr.Provider.ProviderTag
			m.Category = gr.Provider.Category
			m.Allowed = gr.Provider.ResolutionType != "unmapped"
			m.Reason = gr.Provider.ResolutionType + ": " + gr.Provider.Reason
			if m.Allowed {
				m.Status = "validated"
			}
		}
		out.TaxonomyResolution.Genres = append(out.TaxonomyResolution.Genres, gr)
		if m.Allowed {
			out.Mapped = append(out.Mapped, m)
			choices = append(choices, routeChoice{m, g.ID, raw.Weight * gr.Provider.WeightFactor, gr})
		} else {
			out.Unmapped = append(out.Unmapped, m)
		}
	}
	out.TaxonomyResolution.Unknown = lastfmmapping.AggregateUnknown(c, labels)
	priority := func(category string) int {
		switch category {
		case "genre":
			return 0
		case "subgenre":
			return 1
		case "style":
			return 2
		}
		return 3
	}
	sort.SliceStable(choices, func(i, j int) bool {
		a, b := choices[i], choices[j]
		if priority(a.Mapping.Category) != priority(b.Mapping.Category) {
			return priority(a.Mapping.Category) < priority(b.Mapping.Category)
		}
		return a.Effective > b.Effective
	})
	seen := map[string]int{}
	genreCount := 0
	for _, choice := range choices {
		tag := choice.Mapping.LastFMTag
		if idx, ok := seen[tag]; ok {
			out.TaxonomyResolution.RouteSources[tag] = append(out.TaxonomyResolution.RouteSources[tag], choice.Resolution)
			if choice.Effective > out.Routes[idx].ProfileWeight {
				out.Routes[idx].ProfileWeight = choice.Effective
				out.Routes[idx].SourceWeight = choice.Mapping.Input.Weight
				out.Routes[idx].CanonicalConcept = choice.ID
			}
			continue
		}
		if genreCount >= p.MaxGenreRoutes || len(out.Routes) >= p.MaxRoutes || choice.Effective == 0 {
			continue
		}
		seen[tag] = len(out.Routes)
		out.Routes = append(out.Routes, RetrievalRoute{choice.ID, tag, choice.Mapping.Category, choice.Mapping.Input.Weight, choice.Effective})
		out.TaxonomyResolution.RouteSources[tag] = []GenreResolution{choice.Resolution}
		genreCount++
	}
	moods := []ConceptMapping{}
	for _, mood := range profile.Moods {
		if !finiteWeight(mood.Weight) {
			return out, fmt.Errorf("invalid concept weight")
		}
		m := v.Map(mood, "mood")
		if m.Allowed {
			out.Mapped = append(out.Mapped, m)
			moods = append(moods, m)
		} else {
			out.Unmapped = append(out.Unmapped, m)
		}
	}
	sort.SliceStable(moods, func(i, j int) bool { return moods[i].Input.Weight > moods[j].Input.Weight })
	if genreCount > 0 || p.MoodOnly {
		moodCount := 0
		for _, m := range moods {
			if moodCount >= p.MaxMoodRoutes || len(out.Routes) >= p.MaxRoutes {
				break
			}
			if _, ok := seen[m.LastFMTag]; ok || m.Input.Weight == 0 {
				continue
			}
			seen[m.LastFMTag] = len(out.Routes)
			out.Routes = append(out.Routes, RetrievalRoute{ConceptKey(m.Input.Name), m.LastFMTag, m.Category, m.Input.Weight, m.Input.Weight * p.MoodFactor})
			moodCount++
		}
	} else {
		out.Warnings = append(out.Warnings, "insufficient validated genre/style mapping; mood-only disabled")
	}
	if len(out.Unmapped) > 0 {
		out.Warnings = append(out.Warnings, "some canonical concepts still lack a validated provider route")
	}
	if p.MoodOnly {
		out.Warnings = append(out.Warnings, "explicit mood-only experiment")
	}
	out.Warnings = append(out.Warnings, "family fallback is broad retrieval with factor 0.65; not an exact genre synonym or proof of quality")
	return out, nil
}
