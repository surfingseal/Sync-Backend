// Package lastfmeval is an isolated experiment; it is not part of the production recommendation DI.
package lastfmeval

import (
	"encoding/json"
	"example.com/sync/internal/lastfm"
	"example.com/sync/internal/model"
	"fmt"
	"os"
	"sort"
	"strings"
)

type WeightedConcept struct {
	Name         string  `json:"name"`
	Weight       float64 `json:"weight"`
	WeightSource string  `json:"weight_source"`
}
type MusicRetrievalProfile struct {
	Genres  []WeightedConcept `json:"genres"`
	Moods   []WeightedConcept `json:"moods"`
	Tempo   string            `json:"tempo"`
	Energy  float64           `json:"energy"`
	Valence float64           `json:"valence"`
	Notes   []string          `json:"notes"`
}

func Profile(a *model.ImageAnalysis) (MusicRetrievalProfile, error) {
	if a == nil {
		return MusicRetrievalProfile{}, fmt.Errorf("analysis is missing")
	}
	p := MusicRetrievalProfile{Genres: []WeightedConcept{}, Moods: []WeightedConcept{}, Tempo: a.MusicProfile.Tempo, Energy: a.MusicProfile.Energy, Valence: a.Mood.Valence, Notes: []string{}}
	if err := a.Validate(); err != nil {
		return p, err
	}
	if len(a.MusicProfile.GenreCandidates) > 0 {
		for _, g := range a.MusicProfile.GenreCandidates {
			p.Genres = append(p.Genres, WeightedConcept{g.Name, g.Score, "Gemini model-reported relevance, not a probability"})
		}
	} else {
		for _, g := range a.MusicProfile.Genres {
			p.Genres = append(p.Genres, WeightedConcept{g, 1 / float64(len(a.MusicProfile.Genres)), "legacy uniform weight; no relevance score supplied"})
		}
		p.Notes = append(p.Notes, "Legacy analysis: genre weights are uniform over all input genres, not inflated after unmapped concepts are removed.")
	}
	if a.Mood.Primary != "" {
		p.Moods = append(p.Moods, WeightedConcept{a.Mood.Primary, 1, "policy primary salience, not a Gemini mood score"})
		for _, m := range a.Mood.Secondary {
			p.Moods = append(p.Moods, WeightedConcept{m, .7, "policy secondary salience, not a Gemini mood score"})
		}
	} else {
		for _, m := range a.Mood.Tags {
			p.Moods = append(p.Moods, WeightedConcept{m, 1 / float64(len(a.Mood.Tags)), "legacy uniform salience"})
		}
		p.Notes = append(p.Notes, "Legacy mood tags have no primary/secondary signal; equal salience used.")
	}
	p.Notes = append(p.Notes, "Tempo/energy/valence are preserved for diagnostics; this tag-only score does not establish track tempo or mood match. Legacy vocal preferences and raw labels are not used.")
	return p, nil
}

// Input accepts saved pure analysis, analyze responses, and benchmark wrappers.
// Only the analysis subtree is copied to experiment artifacts; OAuth/key fields are never copied.
func ReadAnalysis(path string) (*model.ImageAnalysis, json.RawMessage, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, nil, e
	}
	if len(b) > 8*1024*1024 {
		return nil, nil, fmt.Errorf("analysis input too large")
	}
	var root map[string]json.RawMessage
	if e = json.Unmarshal(b, &root); e != nil {
		return nil, nil, fmt.Errorf("invalid analysis JSON")
	}
	if v, ok := root["analysis"]; ok {
		b = v
	}
	a, e := model.DecodeImageAnalysis(b)
	return a, b, e
}

type Vocabulary struct {
	Registry         lastfm.Registry
	Mappings         []lastfm.TagMapping
	ExplicitUnmapped []struct {
		Canonical string `json:"canonical"`
		Reason    string `json:"reason"`
	}
}

func LoadVocabulary(registry, mapping string) (*Vocabulary, error) {
	b, e := os.ReadFile(registry)
	if e != nil {
		return nil, e
	}
	v := &Vocabulary{}
	if e = json.Unmarshal(b, &v.Registry); e != nil {
		return nil, e
	}
	if v.Registry.Version != 1 {
		return nil, fmt.Errorf("unsupported registry version")
	}
	seen := map[string]bool{}
	names := map[string]bool{}
	for _, t := range v.Registry.Tags {
		if t.Canonical == "" || strings.TrimSpace(t.Tag) == "" || seen[t.Canonical] || names[lastfm.Normalize(t.Tag)] {
			return nil, fmt.Errorf("invalid/duplicate registry entry")
		}
		seen[t.Canonical] = true
		names[lastfm.Normalize(t.Tag)] = true
		switch t.Status {
		case "validated", "candidate", "rejected", "unknown":
		default:
			return nil, fmt.Errorf("unknown registry status")
		}
	}
	b, e = os.ReadFile(mapping)
	if e != nil {
		return nil, e
	}
	var doc struct {
		Version  int                 `json:"version"`
		Mappings []lastfm.TagMapping `json:"mappings"`
		Unmapped []struct {
			Canonical string `json:"canonical"`
			Reason    string `json:"reason"`
		} `json:"unmapped"`
	}
	if e = json.Unmarshal(b, &doc); e != nil {
		return nil, e
	}
	if doc.Version != 1 {
		return nil, fmt.Errorf("unsupported mapping version")
	}
	v.Mappings = doc.Mappings
	v.ExplicitUnmapped = doc.Unmapped
	seen = map[string]bool{}
	for _, m := range v.Mappings {
		key := ConceptKey(m.Canonical)
		if key == "" || seen[key] {
			return nil, fmt.Errorf("ambiguous mapping canonical")
		}
		seen[key] = true
		found := false
		for _, t := range v.Registry.Tags {
			if lastfm.Normalize(t.Tag) == lastfm.Normalize(m.Primary) {
				if t.Category != m.Type {
					return nil, fmt.Errorf("mapping/registry category mismatch")
				}
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("mapping points to absent registry tag")
		}
	}
	return v, nil
}

// Formatting normalization only. No semantic alias generation or family fallback.
func ConceptKey(s string) string { return lastfm.Canonical(s) }

type ConceptMapping struct {
	Input     WeightedConcept `json:"input"`
	InputType string          `json:"input_type"`
	LastFMTag string          `json:"lastfm_tag,omitempty"`
	Category  string          `json:"category,omitempty"`
	Status    string          `json:"registry_status,omitempty"`
	Allowed   bool            `json:"allowed"`
	Reason    string          `json:"reason"`
}
type RetrievalRoute struct {
	CanonicalConcept string  `json:"canonical_concept"`
	LastFMTag        string  `json:"lastfm_tag"`
	Category         string  `json:"category"`
	SourceWeight     float64 `json:"source_weight"`
	ProfileWeight    float64 `json:"profile_weight"`
}
type Plan struct {
	Coverage           *GenreProfileCoverage `json:"genre_coverage,omitempty"`
	TaxonomyResolution *CanonicalResolution  `json:"canonical_resolution,omitempty"`
	Profile            MusicRetrievalProfile `json:"profile"`
	Mapped             []ConceptMapping      `json:"mapped_concepts"`
	Unmapped           []ConceptMapping      `json:"unmapped_concepts"`
	Routes             []RetrievalRoute      `json:"selected_routes"`
	Warnings           []string              `json:"warnings"`
}
type Policy struct {
	MaxGenreRoutes int     `json:"max_genre_routes"`
	MaxMoodRoutes  int     `json:"max_mood_routes"`
	MaxRoutes      int     `json:"max_routes"`
	MoodOnly       bool    `json:"mood_only_experiment"`
	PerRouteLimit  int     `json:"per_route_limit"`
	Concurrency    int     `json:"concurrency"`
	TopN           int     `json:"top_n"`
	ArtistCap      int     `json:"artist_cap"`
	MoodFactor     float64 `json:"mood_factor"`
	OverlapBonus   float64 `json:"overlap_bonus"`
}

func DefaultPolicy() Policy {
	return Policy{MaxGenreRoutes: 3, MaxMoodRoutes: 1, MaxRoutes: 4, PerRouteLimit: 50, Concurrency: 2, TopN: 20, ArtistCap: 2, MoodFactor: .6, OverlapBonus: .05}
}
func (p Policy) Validate() error {
	if p.MaxGenreRoutes < 1 || p.MaxGenreRoutes > 3 || p.MaxMoodRoutes < 0 || p.MaxMoodRoutes > 1 || p.MaxRoutes < 1 || p.MaxRoutes > 4 || p.PerRouteLimit < 1 || p.PerRouteLimit > 50 || p.Concurrency < 1 || p.Concurrency > 3 || p.TopN < 1 || p.TopN > 50 || p.ArtistCap < 1 || p.ArtistCap > p.TopN || !model.UnitSignal(p.MoodFactor) || !model.UnitSignal(p.OverlapBonus) {
		return fmt.Errorf("invalid experiment policy")
	}
	return nil
}
func (v *Vocabulary) Map(c WeightedConcept, typ string) ConceptMapping {
	out := ConceptMapping{Input: c, InputType: typ, Reason: "no explicit mapping"}
	key := ConceptKey(c.Name)
	for _, u := range v.ExplicitUnmapped {
		if key == ConceptKey(u.Canonical) {
			out.Reason = u.Reason
			return out
		}
	}
	var mapping *lastfm.TagMapping
	for i, m := range v.Mappings {
		if ConceptKey(m.Canonical) == key {
			mapping = &v.Mappings[i]
			break
		}
	}
	var tag *lastfm.RegistryTag
	for i, t := range v.Registry.Tags {
		if mapping != nil && lastfm.Normalize(t.Tag) == lastfm.Normalize(mapping.Primary) {
			tag = &v.Registry.Tags[i]
			break
		}
	}
	if tag == nil {
		for _, t := range v.Registry.Tags {
			if t.Canonical == key {
				out.LastFMTag = t.Tag
				out.Category = t.Category
				out.Status = t.Status
				out.Reason = "no explicit mapping; registry entry is diagnostic only"
				break
			}
		}
		return out
	}
	out.LastFMTag = tag.Tag
	out.Category = tag.Category
	out.Status = tag.Status
	if tag.Status != "validated" {
		out.Reason = "tag status is not validated"
		return out
	}
	genre := tag.Category == "genre" || tag.Category == "subgenre" || tag.Category == "style"
	if typ == "genre" && !genre || typ == "mood" && tag.Category != "mood" {
		out.Reason = "input and tag semantic category differ"
		return out
	}
	out.Allowed = true
	out.Reason = "explicit mapping to validated tag"
	return out
}
func BuildPlan(profile MusicRetrievalProfile, v *Vocabulary, policy Policy) (Plan, error) {
	out := Plan{Profile: profile, Mapped: []ConceptMapping{}, Unmapped: []ConceptMapping{}, Routes: []RetrievalRoute{}, Warnings: []string{}}
	if err := policy.Validate(); err != nil {
		return out, err
	}
	genres, moods := []ConceptMapping{}, []ConceptMapping{}
	for _, group := range []struct {
		typ      string
		concepts []WeightedConcept
	}{{"genre", profile.Genres}, {"mood", profile.Moods}} {
		for _, c := range group.concepts {
			if !model.UnitSignal(c.Weight) {
				return out, fmt.Errorf("invalid concept weight")
			}
			m := v.Map(c, group.typ)
			if m.Allowed {
				out.Mapped = append(out.Mapped, m)
				if group.typ == "genre" {
					genres = append(genres, m)
				} else {
					moods = append(moods, m)
				}
			} else {
				out.Unmapped = append(out.Unmapped, m)
			}
		}
	}
	priority := func(cat string) int {
		switch cat {
		case "genre":
			return 0
		case "subgenre":
			return 1
		case "style":
			return 2
		}
		return 3
	}
	sort.SliceStable(genres, func(i, j int) bool {
		if priority(genres[i].Category) != priority(genres[j].Category) {
			return priority(genres[i].Category) < priority(genres[j].Category)
		}
		return genres[i].Input.Weight > genres[j].Input.Weight
	})
	sort.SliceStable(moods, func(i, j int) bool { return moods[i].Input.Weight > moods[j].Input.Weight })
	seen := map[string]bool{}
	add := func(m ConceptMapping, factor float64) bool {
		key := lastfm.Normalize(m.LastFMTag)
		if seen[key] || m.Input.Weight == 0 || len(out.Routes) >= policy.MaxRoutes {
			return false
		}
		seen[key] = true
		out.Routes = append(out.Routes, RetrievalRoute{ConceptKey(m.Input.Name), m.LastFMTag, m.Category, m.Input.Weight, m.Input.Weight * factor})
		return true
	}
	count := 0
	for _, g := range genres {
		if count < policy.MaxGenreRoutes && add(g, 1) {
			count++
		}
	}
	if count > 0 || policy.MoodOnly {
		n := 0
		for _, m := range moods {
			if n < policy.MaxMoodRoutes && add(m, policy.MoodFactor) {
				n++
			}
		}
	} else {
		out.Warnings = append(out.Warnings, "insufficient validated genre/style mapping; mood-only retrieval disabled")
	}
	if policy.MoodOnly {
		out.Warnings = append(out.Warnings, "explicit mood-only experiment flag enabled; not a default production policy")
	}
	if len(out.Unmapped) > 0 {
		out.Warnings = append(out.Warnings, "unmapped/candidate concepts excluded; profile coverage is incomplete")
	}
	return out, nil
}
