package lastfmeval

import (
	"example.com/sync/internal/lastfm"
	"sort"
)

type CanonicalRouteSource struct {
	CanonicalGenre string  `json:"canonical_genre"`
	Family         string  `json:"family"`
	ResolutionType string  `json:"resolution_type"`
	ProfileWeight  float64 `json:"profile_weight"`
	MappingFactor  float64 `json:"mapping_factor"`
}
type CandidateRouteEvidence struct {
	CanonicalConcept string                 `json:"selected_canonical_concept"`
	Family           string                 `json:"family,omitempty"`
	ProviderTag      string                 `json:"provider_tag"`
	ResolutionType   string                 `json:"resolution_type"`
	ProfileWeight    float64                `json:"source_profile_weight"`
	MappingFactor    *float64               `json:"mapping_factor"`
	EffectiveWeight  float64                `json:"effective_route_weight"`
	Rank             int                    `json:"best_provider_rank"`
	RankDecay        float64                `json:"rank_decay"`
	Sources          []CanonicalRouteSource `json:"canonical_sources"`
}
type CandidateRetrievalEvidence struct {
	Routes                 []CandidateRouteEvidence `json:"routes"`
	BestRouteRank          int                      `json:"best_route_rank"`
	RouteCount             int                      `json:"distinct_provider_route_count"`
	ExactRouteCount        int                      `json:"exact_route_count"`
	AliasRouteCount        int                      `json:"alias_route_count"`
	FallbackRouteCount     int                      `json:"fallback_route_count"`
	MoodRouteCount         int                      `json:"mood_route_count"`
	UnclassifiedRouteCount int                      `json:"unclassified_route_count"`
}

// Metadata-only join after the frozen score/dedupe stage; never mutates rank or score.
// Multiple canonical concepts can share one provider route. Preserve their sources
// without counting them as independent provider retrievals.
func attachCandidateProvenance(candidates []CandidateTrack, plan Plan, p Policy) {
	for i := range candidates {
		c := &candidates[i]
		summary := &CandidateRetrievalEvidence{Routes: []CandidateRouteEvidence{}}
		best := map[string]TagRouteEvidence{}
		for _, e := range c.Evidences {
			key := lastfm.Normalize(e.LastFMTag)
			old, exists := best[key]
			if !exists || e.Rank < old.Rank {
				best[key] = e
			}
		}
		keys := []string{}
		for key := range best {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			e := best[key]
			route := CandidateRouteEvidence{CanonicalConcept: e.CanonicalConcept, ProviderTag: e.LastFMTag, ResolutionType: "unclassified", EffectiveWeight: e.RouteWeight, Rank: e.Rank, Sources: []CanonicalRouteSource{}}
			for _, chosen := range plan.Routes {
				if lastfm.Normalize(chosen.LastFMTag) == key {
					route.ProfileWeight = chosen.SourceWeight
					route.CanonicalConcept = chosen.CanonicalConcept
					break
				}
			}
			if e.Category == "mood" {
				route.ResolutionType = "mood"
				factor := p.MoodFactor
				route.MappingFactor = &factor
			}
			if plan.TaxonomyResolution != nil {
				for tag, sources := range plan.TaxonomyResolution.RouteSources {
					if lastfm.Normalize(tag) != key {
						continue
					}
					for _, source := range sources {
						if source.Canonical == nil {
							continue
						}
						route.Sources = append(route.Sources, CanonicalRouteSource{source.Canonical.ID, source.Canonical.Family, source.Provider.ResolutionType, source.Input.Weight, source.Provider.WeightFactor})
						if source.Canonical.ID == route.CanonicalConcept {
							route.Family = source.Canonical.Family
							route.ResolutionType = source.Provider.ResolutionType
							factor := source.Provider.WeightFactor
							route.MappingFactor = &factor
						}
					}
				}
			}
			sort.Slice(route.Sources, func(i, j int) bool {
				a, b := route.Sources[i], route.Sources[j]
				if a.CanonicalGenre != b.CanonicalGenre {
					return a.CanonicalGenre < b.CanonicalGenre
				}
				if a.ProfileWeight != b.ProfileWeight {
					return a.ProfileWeight > b.ProfileWeight
				}
				return a.ResolutionType < b.ResolutionType
			})
			for _, contribution := range c.Contributions {
				if lastfm.Normalize(contribution.Tag) == key {
					route.RankDecay = contribution.RankScore
				}
			}
			summary.Routes = append(summary.Routes, route)
			summary.RouteCount++
			if summary.BestRouteRank == 0 || e.Rank < summary.BestRouteRank {
				summary.BestRouteRank = e.Rank
			}
			switch route.ResolutionType {
			case "exact":
				summary.ExactRouteCount++
			case "alias":
				summary.AliasRouteCount++
			case "family_fallback":
				summary.FallbackRouteCount++
			case "mood":
				summary.MoodRouteCount++
			default:
				summary.UnclassifiedRouteCount++
			}
		}
		c.Retrieval = summary
	}
}
