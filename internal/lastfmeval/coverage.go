package lastfmeval

import (
	"example.com/sync/internal/lastfmmapping"
	"example.com/sync/internal/music"
	"sort"
	"strings"
)

type ResolutionReason string

const (
	ReasonEligibleExact         ResolutionReason = "eligible_exact"
	ReasonEligibleAlias         ResolutionReason = "eligible_alias"
	ReasonEligibleFamily        ResolutionReason = "eligible_family_fallback"
	ReasonInsufficientReach     ResolutionReason = "exact_insufficient_reach"
	ReasonInsufficientSample    ResolutionReason = "exact_insufficient_sample"
	ReasonEvidenceUnavailable   ResolutionReason = "exact_evidence_unavailable"
	ReasonEquivalentNotEligible ResolutionReason = "equivalent_not_eligible"
	ReasonEquivalentRejected    ResolutionReason = "equivalent_rejected"
	ReasonNoEquivalent          ResolutionReason = "no_equivalent_mapping"
	ReasonFamilyNotEligible     ResolutionReason = "family_primary_not_eligible"
	ReasonNoFamily              ResolutionReason = "no_family_fallback"
	ReasonOther                 ResolutionReason = "sentinel_other"
	ReasonUnknown               ResolutionReason = "sentinel_unknown"
	ReasonNonCanonical          ResolutionReason = "noncanonical_input"
	ReasonProvisionalOptIn      ResolutionReason = "provisional_route_explicit_opt_in"
)
const (
	CoverageFull          = "FULL"
	CoveragePartial       = "PARTIAL"
	CoverageInsufficient  = "INSUFFICIENT_ROUTES"
	ResolutionUnsupported = "unsupported"
	ResolutionSentinel    = "sentinel"
)

// ResolveGenre adds stable diagnostics to the unchanged policy-v1 resolver.
// It never introduces a provider tag, family relationship or semantic alias.
func (r AutomaticRegistry) ResolveGenre(c music.CanonicalCatalog, id string, allowProvisional bool) GenreResolution {
	g, ok := c.Lookup(id)
	out := GenreResolution{Provider: r.Resolve(c, id, allowProvisional), Status: ResolutionUnsupported, ReasonCodes: []ResolutionReason{}}
	if !ok {
		out.ReasonCodes = append(out.ReasonCodes, ReasonNonCanonical)
		return out
	}
	out.Canonical = &g
	if g.Family == "other" {
		out.Status = ResolutionSentinel
		if g.ID == "unknown" {
			out.ReasonCodes = append(out.ReasonCodes, ReasonUnknown)
		} else {
			out.ReasonCodes = append(out.ReasonCodes, ReasonOther)
		}
		return out
	}
	if out.Provider.ResolutionType != "unmapped" {
		out.Status = out.Provider.ResolutionType
		switch out.Status {
		case "exact":
			out.ReasonCodes = append(out.ReasonCodes, ReasonEligibleExact)
		case "alias":
			out.ReasonCodes = append(out.ReasonCodes, ReasonEligibleAlias)
		case "family_fallback":
			out.ReasonCodes = append(out.ReasonCodes, ReasonEligibleFamily)
		}
		if strings.Contains(out.Provider.Reason, "provisional") {
			out.ReasonCodes = []ResolutionReason{ReasonProvisionalOptIn}
		}
		return out
	}
	reasons := map[ResolutionReason]bool{}
	equivalent := false
	for _, m := range r.Genres {
		if m.Canonical != id {
			continue
		}
		for _, t := range m.Tags {
			equivalent = true
			e := t.Evidence
			switch {
			case t.Status == "rejected":
				reasons[ReasonEquivalentRejected] = true
			case e.Reach != nil && int64(*e.Reach) < r.Policy.EligibleReach:
				reasons[ReasonInsufficientReach] = true
			case !e.InfoSuccess || e.Reach == nil:
				reasons[ReasonEvidenceUnavailable] = true
			case !e.TracksSuccess || e.SampleRequested < r.Policy.SampleRequested || e.ValidUniqueTracks < r.Policy.UniqueTracks:
				reasons[ReasonInsufficientSample] = true
			default:
				reasons[ReasonEquivalentNotEligible] = true
			}
		}
	}
	if !equivalent {
		reasons[ReasonNoEquivalent] = true
	}
	familyFound := false
	for _, f := range r.Families {
		if f.Family == g.Family && f.Primary != nil {
			familyFound = true
			reasons[ReasonFamilyNotEligible] = true
		}
	}
	if !familyFound {
		reasons[ReasonNoFamily] = true
	}
	for reason := range reasons {
		out.ReasonCodes = append(out.ReasonCodes, reason)
	}
	sort.Slice(out.ReasonCodes, func(i, j int) bool { return out.ReasonCodes[i] < out.ReasonCodes[j] })
	return out
}

type GenreProfileCoverage struct {
	State                string            `json:"state"`
	TotalGenreInputs     int               `json:"total_genre_inputs_excluding_sentinels"`
	TotalCanonicalGenres int               `json:"total_canonical_genre_inputs"`
	SupportedExact       int               `json:"supported_exact_count"`
	SupportedAlias       int               `json:"supported_alias_count"`
	SupportedFallback    int               `json:"supported_fallback_count"`
	Unsupported          int               `json:"unsupported_canonical_count"`
	UnknownInputs        int               `json:"noncanonical_input_count"`
	Sentinels            int               `json:"sentinel_count"`
	SupportedWeight      float64           `json:"supported_weight"`
	UnsupportedWeight    float64           `json:"unsupported_weight"`
	SentinelWeight       float64           `json:"sentinel_weight_excluded"`
	TotalWeight          float64           `json:"total_weight_excluding_sentinels"`
	ExactWeight          float64           `json:"exact_weight"`
	AliasWeight          float64           `json:"alias_weight"`
	FallbackWeight       float64           `json:"fallback_weight"`
	CountCoverage        *float64          `json:"count_coverage"`
	WeightedCoverage     *float64          `json:"weighted_genre_coverage"`
	ExactCoverage        *float64          `json:"exact_count_coverage"`
	FallbackCoverage     *float64          `json:"fallback_count_coverage"`
	WeightSemantics      string            `json:"weight_semantics"`
	UsableGenreRoutes    int               `json:"usable_genre_routes"`
	MoodRoutes           int               `json:"selected_mood_routes"`
	ExplicitMoodOnly     bool              `json:"explicit_mood_only_policy"`
	AllowProvisional     bool              `json:"explicit_allow_provisional"`
	Resolutions          []GenreResolution `json:"resolutions"`
	Note                 string            `json:"note"`
}

func ratio(n, d float64) *float64 {
	if d == 0 {
		return nil
	}
	v := n / d
	return &v
}
func annotateCoverage(out *Plan, c music.CanonicalCatalog, r AutomaticRegistry, p Policy, allow bool) {
	cov := &GenreProfileCoverage{State: CoverageInsufficient, Resolutions: []GenreResolution{}, ExplicitMoodOnly: p.MoodOnly, AllowProvisional: allow, WeightSemantics: "unspecified_diagnostic", Note: "Eligibility coverage of all input concepts, not selected-budget coverage or recommendation quality. Original weights preserved; model relevance is not calibrated confidence."}
	model, legacy := len(out.Profile.Genres) > 0, len(out.Profile.Genres) > 0
	for _, raw := range out.Profile.Genres {
		model = model && strings.HasPrefix(raw.WeightSource, "Gemini model-reported relevance")
		legacy = legacy && strings.HasPrefix(raw.WeightSource, "legacy uniform")
		g, ok := c.Resolve(raw.Name)
		var gr GenreResolution
		if ok {
			gr = r.ResolveGenre(c, g.ID, allow)
		} else {
			gr = GenreResolution{Status: ResolutionUnsupported, Provider: lastfmmapping.ResolvedProviderRoute{ResolutionType: "unmapped"}, ReasonCodes: []ResolutionReason{ReasonNonCanonical}}
		}
		gr.Input = raw
		cov.Resolutions = append(cov.Resolutions, gr)
		if gr.Status == ResolutionSentinel {
			cov.Sentinels++
			cov.SentinelWeight += raw.Weight
			continue
		}
		cov.TotalGenreInputs++
		cov.TotalWeight += raw.Weight
		if ok {
			cov.TotalCanonicalGenres++
		} else {
			cov.UnknownInputs++
		}
		switch gr.Status {
		case "exact":
			cov.SupportedExact++
			cov.SupportedWeight += raw.Weight
			cov.ExactWeight += raw.Weight
		case "alias":
			cov.SupportedAlias++
			cov.SupportedWeight += raw.Weight
			cov.AliasWeight += raw.Weight
		case "family_fallback":
			cov.SupportedFallback++
			cov.SupportedWeight += raw.Weight
			cov.FallbackWeight += raw.Weight
		default:
			if ok {
				cov.Unsupported++
			}
			cov.UnsupportedWeight += raw.Weight
		}
	}
	if model {
		cov.WeightSemantics = "model_relevance_not_probability"
	} else if legacy {
		cov.WeightSemantics = "legacy_uniform_diagnostic"
	}
	for _, route := range out.Routes {
		if route.Category == "mood" {
			cov.MoodRoutes++
		} else {
			cov.UsableGenreRoutes++
		}
	}
	if cov.UsableGenreRoutes > 0 {
		cov.State = CoverageFull
		if cov.Unsupported > 0 || cov.UnknownInputs > 0 {
			cov.State = CoveragePartial
		}
	}
	cov.CountCoverage = ratio(float64(cov.SupportedExact+cov.SupportedAlias+cov.SupportedFallback), float64(cov.TotalGenreInputs))
	cov.WeightedCoverage = ratio(cov.SupportedWeight, cov.TotalWeight)
	cov.ExactCoverage = ratio(float64(cov.SupportedExact), float64(cov.TotalGenreInputs))
	cov.FallbackCoverage = ratio(float64(cov.SupportedFallback), float64(cov.TotalGenreInputs))
	out.Coverage = cov
	if out.TaxonomyResolution != nil {
		for i := range out.TaxonomyResolution.Genres {
			out.TaxonomyResolution.Genres[i].Status = cov.Resolutions[i].Status
			out.TaxonomyResolution.Genres[i].ReasonCodes = cov.Resolutions[i].ReasonCodes
		}
	}
}
