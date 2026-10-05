package lastfmeval

import (
	"encoding/json"
	"example.com/sync/internal/lastfm"
	"example.com/sync/internal/lastfmmapping"
	"example.com/sync/internal/music"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Operational thresholds, not calibrated musical quality boundaries.
type RetrievalPolicy struct {
	Version              int     `json:"retrieval_policy_version"`
	EligibleReach        int64   `json:"eligible_reach"`
	ProvisionalReach     int64   `json:"provisional_reach"`
	SampleRequested      int     `json:"minimum_sample_requested"`
	UniqueTracks         int     `json:"minimum_valid_unique_tracks"`
	UniqueArtists        int     `json:"minimum_unique_artists"`
	DuplicateRatio       float64 `json:"maximum_duplicate_ratio"`
	ConcentrationWarning float64 `json:"concentration_warning_above"`
	SevereConcentration  float64 `json:"severe_concentration_above"`
}

func RetrievalPolicyV1() RetrievalPolicy {
	return RetrievalPolicy{1, 5000, 1000, 20, 15, 8, .10, .40, .60}
}

type TagEvidence struct {
	Tag              string          `json:"lastfm_tag"`
	Category         string          `json:"category"`
	Info             *lastfm.TagInfo `json:"info"`
	Tracks           []lastfm.Track  `json:"tracks"`
	InfoSuccess      bool            `json:"info_success"`
	TracksSuccess    bool            `json:"tracks_success"`
	SampleRequested  int             `json:"sample_requested"`
	Source           string          `json:"source"`
	RecordedAt       string          `json:"recorded_at"`
	InfoRecordedAt   string          `json:"info_recorded_at"`
	Reused           bool            `json:"reused"`
	ExplicitRejected bool            `json:"explicit_rejection_evidence"`
	SemanticReview   string          `json:"semantic_review"`
	HumanReview      string          `json:"human_review"`
}
type RetrievalEvidence struct {
	Reach              *lastfm.Number `json:"reach"`
	Total              *lastfm.Number `json:"total"`
	Taggings           *lastfm.Number `json:"taggings"`
	SampleRequested    int            `json:"sample_requested"`
	SampleReceived     int            `json:"sample_received"`
	ValidReceived      int            `json:"valid_received"`
	InvalidReceived    int            `json:"invalid_received"`
	ValidUniqueTracks  int            `json:"valid_unique_tracks"`
	DuplicateRatio     float64        `json:"duplicate_ratio"`
	UniqueArtists      int            `json:"unique_artists"`
	MaxTracksPerArtist int            `json:"max_tracks_per_artist"`
	Top1Share          float64        `json:"top1_artist_share"`
	Top3Share          float64        `json:"top3_artist_share"`
	Source             string         `json:"source"`
	RecordedAt         string         `json:"recorded_at"`
	InfoRecordedAt     string         `json:"info_recorded_at"`
	Reused             bool           `json:"reused"`
	InfoSuccess        bool           `json:"info_success"`
	TracksSuccess      bool           `json:"tracks_success"`
}
type EligibilityDecision struct {
	Tag            string            `json:"lastfm_tag"`
	Category       string            `json:"category"`
	Status         string            `json:"retrieval_status"`
	PolicyVersion  int               `json:"retrieval_policy_version"`
	SemanticReview string            `json:"semantic_review"`
	HumanReview    string            `json:"human_review"`
	Evidence       RetrievalEvidence `json:"evidence"`
	Reasons        []string          `json:"decision_reasons"`
	Warnings       []string          `json:"warnings"`
}

func DecideEligibility(t TagEvidence) EligibilityDecision {
	p := RetrievalPolicyV1()
	d := EligibilityDecision{Tag: t.Tag, Category: t.Category, PolicyVersion: p.Version, Status: "insufficient_evidence", SemanticReview: t.SemanticReview, HumanReview: t.HumanReview, Reasons: []string{}, Warnings: []string{}}
	if d.SemanticReview == "" {
		d.SemanticReview = "not_performed"
	}
	if d.HumanReview == "" {
		d.HumanReview = "not_performed"
	}
	e := RetrievalEvidence{SampleRequested: t.SampleRequested, SampleReceived: len(t.Tracks), Source: t.Source, RecordedAt: t.RecordedAt, InfoRecordedAt: t.InfoRecordedAt, Reused: t.Reused, InfoSuccess: t.InfoSuccess, TracksSuccess: t.TracksSuccess}
	if t.Info != nil {
		e.Reach = t.Info.Reach
		e.Total = t.Info.Total
		e.Taggings = t.Info.Taggings
	}
	raw := []RawCandidate{}
	for _, track := range t.Tracks {
		raw = append(raw, RawCandidate{Track: track})
	}
	merged, identityWarnings, invalid := Merge(raw, DefaultPolicy()) // existing normalized pair/MBID transitive identity
	d.Warnings = append(d.Warnings, identityWarnings...)
	e.InvalidReceived = invalid
	e.ValidReceived = len(t.Tracks) - invalid
	e.ValidUniqueTracks = len(merged)
	if e.ValidReceived > 0 {
		e.DuplicateRatio = float64(e.ValidReceived-len(merged)) / float64(e.ValidReceived)
	}
	artists := map[string]int{}
	for _, track := range merged {
		artists[track.NormalizedArtist]++
	}
	counts := []int{}
	for _, n := range artists {
		counts = append(counts, n)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(counts)))
	e.UniqueArtists = len(counts)
	if len(counts) > 0 {
		e.MaxTracksPerArtist = counts[0]
		e.Top1Share = float64(counts[0]) / float64(len(merged))
		for i, n := range counts {
			if i < 3 {
				e.Top3Share += float64(n) / float64(len(merged))
			}
		}
	}
	d.Evidence = e
	if e.Top1Share > p.ConcentrationWarning {
		d.Warnings = append(d.Warnings, "artist_concentration_above_40_percent")
	}
	if e.Top1Share > p.SevereConcentration {
		d.Warnings = append(d.Warnings, "severe_artist_concentration_above_60_percent")
	}
	if t.ExplicitRejected {
		d.Status = "rejected"
		d.Reasons = append(d.Reasons, "explicit_prior_rejection_evidence")
		return d
	}
	if t.InfoSuccess && t.TracksSuccess && t.SampleRequested >= p.SampleRequested && len(t.Tracks) >= p.UniqueTracks && len(merged) == 0 {
		d.Status = "rejected"
		d.Reasons = append(d.Reasons, "successful_sample_contains_no_valid_tracks")
		return d
	}
	if !t.InfoSuccess {
		d.Reasons = append(d.Reasons, "info_success_not_established")
	}
	if !t.TracksSuccess {
		d.Reasons = append(d.Reasons, "top_tracks_success_not_established")
	}
	if e.Reach == nil {
		d.Reasons = append(d.Reasons, "reach_missing")
	}
	if t.SampleRequested < p.SampleRequested {
		d.Reasons = append(d.Reasons, "requested_sample_below_20")
	}
	if e.ValidUniqueTracks < p.UniqueTracks {
		d.Reasons = append(d.Reasons, "valid_unique_tracks_below_15")
	}
	if e.DuplicateRatio > p.DuplicateRatio {
		d.Reasons = append(d.Reasons, "duplicate_ratio_above_10_percent")
	}
	if len(d.Reasons) > 0 {
		return d
	}
	if int64(*e.Reach) < p.ProvisionalReach {
		d.Reasons = append(d.Reasons, "reach_below_1000")
		return d
	}
	d.Status = "eligible"
	if int64(*e.Reach) < p.EligibleReach {
		d.Status = "provisional"
		d.Reasons = append(d.Reasons, "reach_between_1000_and_4999")
	} else {
		d.Reasons = append(d.Reasons, "reach_at_least_5000")
	}
	if e.UniqueArtists < p.UniqueArtists {
		d.Status = "provisional"
		d.Reasons = append(d.Reasons, "unique_artists_below_8")
	} else {
		d.Reasons = append(d.Reasons, "artist_diversity_sufficient")
	}
	if e.Top1Share > p.SevereConcentration {
		d.Status = "provisional"
		d.Reasons = append(d.Reasons, "severe_concentration_limits_to_provisional")
	}
	d.Reasons = append(d.Reasons, "requested_sample_at_least_20", "valid_unique_tracks_at_least_15", "duplicate_ratio_within_limit")
	return d
}

type AutomaticTag struct {
	Canonical   string `json:"canonical"`
	MappingType string `json:"mapping_type"`
	EligibilityDecision
}
type AutomaticGenre struct {
	Canonical string         `json:"canonical"`
	Tags      []AutomaticTag `json:"tags"`
}
type AutomaticFamily struct {
	Family  string        `json:"family"`
	Primary *AutomaticTag `json:"primary"`
}
type AutomaticRegistry struct {
	Version        int                   `json:"version"`
	Policy         RetrievalPolicy       `json:"policy"`
	Genres         []AutomaticGenre      `json:"genres"`
	Families       []AutomaticFamily     `json:"families"`
	AuxiliaryMoods []EligibilityDecision `json:"auxiliary_moods"`
}

func semanticKind(g music.CanonicalGenre, tag string) string {
	if lastfm.Normalize(tag) == lastfm.Normalize(g.Name) {
		return "exact"
	}
	if lastfmmapping.Orthographic(tag, g.Name) {
		return "orthographic_alias"
	}
	for _, alias := range g.Aliases {
		if lastfmmapping.Orthographic(tag, alias) {
			return "explicit_alias"
		}
	}
	return "related"
}
func BuildAutomaticRegistry(c music.CanonicalCatalog, baseline lastfmmapping.Registry, evidence []TagEvidence) AutomaticRegistry {
	r := AutomaticRegistry{Version: 2, Policy: RetrievalPolicyV1(), Genres: []AutomaticGenre{}, Families: []AutomaticFamily{}, AuxiliaryMoods: []EligibilityDecision{}}
	for _, g := range c.Genres {
		m := AutomaticGenre{Canonical: g.ID, Tags: []AutomaticTag{}}
		if g.Family != "other" {
			for _, e := range evidence {
				kind := semanticKind(g, e.Tag)
				if kind != "related" {
					m.Tags = append(m.Tags, AutomaticTag{g.ID, kind, DecideEligibility(e)})
				}
			}
		}
		sort.Slice(m.Tags, func(i, j int) bool { return m.Tags[i].Tag < m.Tags[j].Tag })
		r.Genres = append(r.Genres, m)
	}
	// Preserve only the existing explicit primary relationships; no jazz guess.
	for _, f := range baseline.Families {
		m := AutomaticFamily{Family: f.Family}
		if f.Primary != nil {
			for _, e := range evidence {
				if lastfm.Normalize(e.Tag) == lastfm.Normalize(f.Primary.Tag) {
					d := AutomaticTag{f.Family, "family_fallback", DecideEligibility(e)}
					m.Primary = &d
					break
				}
			}
		}
		r.Families = append(r.Families, m)
	}
	for _, e := range evidence {
		if e.Category == "mood" {
			r.AuxiliaryMoods = append(r.AuxiliaryMoods, DecideEligibility(e))
		}
	}
	return r
}
func statusRank(s string) int {
	switch s {
	case "eligible":
		return 3
	case "provisional":
		return 2
	case "insufficient_evidence":
		return 1
	}
	return 0
}
func (r AutomaticRegistry) Resolve(c music.CanonicalCatalog, id string, allowProvisional bool) lastfmmapping.ResolvedProviderRoute {
	g, ok := c.Lookup(id)
	out := lastfmmapping.ResolvedProviderRoute{CanonicalGenreID: id, Family: g.Family, ResolutionType: "unmapped", Reason: "no policy-v1 eligible equivalent or explicit family primary"}
	if !ok || g.Family == "other" {
		out.Reason = "unknown/sentinel excluded"
		return out
	}
	allowed := func(t AutomaticTag) bool {
		return t.PolicyVersion == 1 && (t.Status == "eligible" || allowProvisional && t.Status == "provisional")
	}
	candidates := []AutomaticTag{}
	for _, m := range r.Genres {
		if m.Canonical == id {
			for _, t := range m.Tags {
				if allowed(t) && semanticKind(g, t.Tag) != "related" && t.MappingType != "related" {
					candidates = append(candidates, t)
				}
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if statusRank(a.Status) != statusRank(b.Status) {
			return statusRank(a.Status) > statusRank(b.Status)
		}
		// All runtime-eligible candidates already pass completeness. For provisional
		// compare available completeness first, without counting missing data as zero usage.
		ae, be := a.Evidence, b.Evidence
		if min(ae.ValidUniqueTracks, r.Policy.SampleRequested) != min(be.ValidUniqueTracks, r.Policy.SampleRequested) {
			return min(ae.ValidUniqueTracks, r.Policy.SampleRequested) > min(be.ValidUniqueTracks, r.Policy.SampleRequested)
		}
		if ae.UniqueArtists != be.UniqueArtists {
			return ae.UniqueArtists > be.UniqueArtists
		}
		if (ae.Top1Share > .60) != (be.Top1Share > .60) {
			return ae.Top1Share <= .60
		}
		ar, br := int64(0), int64(0)
		if ae.Reach != nil {
			ar = int64(*ae.Reach)
		}
		if be.Reach != nil {
			br = int64(*be.Reach)
		}
		if ar != br {
			return ar > br
		}
		if (a.MappingType == "exact") != (b.MappingType == "exact") {
			return a.MappingType == "exact"
		}
		return a.Tag < b.Tag
	})
	pick := func(t AutomaticTag, typ string, factor float64) {
		out.ProviderTag = t.Tag
		out.Category = t.Category
		out.ResolutionType = typ
		out.WeightFactor = factor
		reach := int64(0)
		if t.Evidence.Reach != nil {
			reach = int64(*t.Evidence.Reach)
		}
		out.Reason = fmt.Sprintf("policy_v1 %s; unique=%d artists=%d reach=%d; semantic/audio/human quality not certified", t.Status, t.Evidence.ValidUniqueTracks, t.Evidence.UniqueArtists, reach)
	}
	if len(candidates) > 0 {
		t := candidates[0]
		if t.MappingType == "exact" {
			pick(t, "exact", lastfmmapping.ExactFactor)
		} else {
			pick(t, "alias", lastfmmapping.AliasFactor)
		}
		return out
	}
	for _, f := range r.Families {
		if f.Family == g.Family && f.Primary != nil && allowed(*f.Primary) {
			pick(*f.Primary, "family_fallback", lastfmmapping.FamilyFactor)
			return out
		}
	}
	return out
}
func (r AutomaticRegistry) Validate(c music.CanonicalCatalog) error {
	if r.Version != 2 || r.Policy != RetrievalPolicyV1() {
		return fmt.Errorf("invalid automatic registry policy/version")
	}
	seen := map[string]bool{}
	valid := func(t AutomaticTag) bool {
		if t.PolicyVersion != 1 || !(t.Status == "eligible" || t.Status == "provisional" || t.Status == "insufficient_evidence" || t.Status == "rejected") {
			return false
		}
		if t.Status == "eligible" || t.Status == "provisional" {
			e := t.Evidence
			p := r.Policy
			if !e.InfoSuccess || !e.TracksSuccess || e.Reach == nil || int64(*e.Reach) < p.ProvisionalReach || e.SampleRequested < p.SampleRequested || e.ValidUniqueTracks < p.UniqueTracks || e.DuplicateRatio > p.DuplicateRatio {
				return false
			}
			if t.Status == "eligible" && (int64(*e.Reach) < p.EligibleReach || e.UniqueArtists < p.UniqueArtists || e.Top1Share > p.SevereConcentration) {
				return false
			}
		}
		return true
	}
	for _, m := range r.Genres {
		g, ok := c.Lookup(m.Canonical)
		if !ok || seen[g.ID] {
			return fmt.Errorf("invalid genre ID")
		}
		seen[g.ID] = true
		for _, t := range m.Tags {
			if !valid(t) || t.Canonical != g.ID || t.MappingType != semanticKind(g, t.Tag) || t.MappingType == "related" || g.Family == "other" {
				return fmt.Errorf("invalid equivalent tag")
			}
		}
	}
	seen = map[string]bool{}
	for _, f := range r.Families {
		exists := false
		for _, family := range c.Families {
			if family.ID == f.Family {
				exists = true
			}
		}
		if !exists || seen[f.Family] {
			return fmt.Errorf("invalid family")
		}
		seen[f.Family] = true
		if f.Primary != nil && (!valid(*f.Primary) || f.Primary.MappingType != "family_fallback") {
			return fmt.Errorf("invalid family primary")
		}
	}
	return nil
}
func LoadAutomaticRegistry(path string, c music.CanonicalCatalog) (AutomaticRegistry, error) {
	var r AutomaticRegistry
	b, e := os.ReadFile(path)
	if e != nil {
		return r, e
	}
	if e = json.Unmarshal(b, &r); e != nil {
		return r, e
	}
	return r, r.Validate(c)
}
func BuildAutomaticPlan(profile MusicRetrievalProfile, v *Vocabulary, p Policy, c music.CanonicalCatalog, r AutomaticRegistry, allowProvisional bool) (Plan, error) {
	if e := r.Validate(c); e != nil {
		return Plan{}, e
	}
	// Mood lookup still uses explicit existing mappings, but eligibility replaces
	// old metadata-review gating. The vocabulary copy never mutates prior evidence.
	copyV := *v
	copyV.Registry.Tags = append([]lastfm.RegistryTag{}, v.Registry.Tags...)
	for i, t := range copyV.Registry.Tags {
		if t.Category == "mood" {
			copyV.Registry.Tags[i].Status = "candidate"
			for _, d := range r.AuxiliaryMoods {
				if lastfm.Normalize(d.Tag) == lastfm.Normalize(t.Tag) && (d.Status == "eligible" || allowProvisional && d.Status == "provisional") {
					copyV.Registry.Tags[i].Status = "validated"
				}
			}
		}
	}
	out, e := buildResolvedPlan(profile, &copyV, p, c, func(id string) lastfmmapping.ResolvedProviderRoute { return r.Resolve(c, id, allowProvisional) })
	for i := range out.Mapped {
		out.Mapped[i].Status = "eligible"
		if strings.Contains(out.Mapped[i].Reason, "provisional") {
			out.Mapped[i].Status = "provisional"
		}
		out.Mapped[i].Reason = strings.ReplaceAll(out.Mapped[i].Reason, "validated tag", "eligible tag")
		out.Mapped[i].Reason += "; automatic eligibility, not semantic validation"
	}
	out.Warnings = append(out.Warnings, "automatic eligibility policy v1; reach threshold is operational policy, not a quality boundary; human review is optional")
	for i := range out.Warnings {
		out.Warnings[i] = strings.ReplaceAll(out.Warnings[i], "validated provider route", "eligible provider route")
		out.Warnings[i] = strings.ReplaceAll(out.Warnings[i], "validated genre/style", "eligible genre/style")
	}
	if e == nil {
		annotateCoverage(&out, c, r, p, allowProvisional)
	}
	return out, e
}
