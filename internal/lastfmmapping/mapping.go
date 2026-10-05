// Package lastfmmapping owns provider-specific offline evidence and resolution.
// Sync taxonomy stays in internal/music; production recommend does not import this package.
package lastfmmapping

import (
	"encoding/json"
	"example.com/sync/internal/lastfm"
	"example.com/sync/internal/music"
	"fmt"
	"os"
	"strings"
)

const ExactFactor = 1.0
const AliasFactor = .95
const FamilyFactor = .65
const ValidationScope = "Metadata-semantic retrieval evidence only; not audio certification, genre purity or recommendation-quality guarantee."

type Evidence struct {
	Source        string          `json:"source"`
	Reused        bool            `json:"prior_evidence_reused"`
	Reach         *lastfm.Number  `json:"reach"`
	Total         *lastfm.Number  `json:"total"`
	Taggings      *lastfm.Number  `json:"taggings"`
	SampleSize    int             `json:"sample_size"`
	Samples       []lastfm.Sample `json:"sample_tracks"`
	UniqueArtists int             `json:"unique_artist_count"`
	Top1Share     float64         `json:"top1_artist_share"`
	Top3Share     float64         `json:"top3_artist_share"`
	MaxTracks     int             `json:"max_tracks_per_artist"`
	MBIDMissing   int             `json:"mbid_missing_count"`
	Duplicates    int             `json:"duplicate_count"`
	APICalls      int             `json:"api_calls"`
	Warnings      []string        `json:"warnings"`
	ReviewNotes   string          `json:"review_notes"`
}
type ProviderTag struct {
	Tag         string         `json:"tag"`
	MappingType string         `json:"mapping_type"`
	Status      string         `json:"status"`
	Category    string         `json:"category"`
	Reach       *lastfm.Number `json:"reach"`
	Total       *lastfm.Number `json:"total"`
	Evidence    Evidence       `json:"evidence"`
}
type FamilyMapping struct {
	Family  string       `json:"family"`
	Primary *ProviderTag `json:"primary,omitempty"`
	Reason  string       `json:"reason"`
}
type GenreMapping struct {
	CanonicalGenreID string         `json:"canonical_genre_id"`
	Exact            *ProviderTag   `json:"exact_tag,omitempty"`
	Aliases          []ProviderTag  `json:"aliases"`
	FamilyFallback   *FamilyMapping `json:"family_fallback,omitempty"`
	Status           string         `json:"status"`
}
type Registry struct {
	Version         int             `json:"version"`
	TaxonomySource  string          `json:"taxonomy_source"`
	ValidationScope string          `json:"validation_scope"`
	Genres          []GenreMapping  `json:"genres"`
	Families        []FamilyMapping `json:"families"`
}
type ResolvedProviderRoute struct {
	CanonicalGenreID string  `json:"canonical_genre_id"`
	Family           string  `json:"family"`
	ProviderTag      string  `json:"provider_tag,omitempty"`
	Category         string  `json:"provider_category,omitempty"`
	ResolutionType   string  `json:"resolution_type"`
	WeightFactor     float64 `json:"weight_factor"`
	Reason           string  `json:"reason"`
}

func (r Registry) Resolve(c music.CanonicalCatalog, id string) ResolvedProviderRoute {
	g, ok := c.Lookup(id)
	out := ResolvedProviderRoute{CanonicalGenreID: id, Family: g.Family, ResolutionType: "unmapped", Reason: "no validated exact/orthographic/family route"}
	if !ok || g.Family == "other" {
		out.Reason = "unknown canonical or sentinel concept"
		return out
	}
	pick := func(t *ProviderTag, typ string, factor float64) {
		out.ProviderTag = t.Tag
		out.Category = t.Category
		out.ResolutionType = typ
		out.WeightFactor = factor
		out.Reason = ValidationScope
	}
	for _, m := range r.Genres {
		if m.CanonicalGenreID != id {
			continue
		}
		if m.Exact != nil && m.Exact.Status == "validated" && m.Exact.MappingType == "exact" && lastfm.Normalize(m.Exact.Tag) == lastfm.Normalize(g.Name) {
			pick(m.Exact, "exact", ExactFactor)
			return out
		}
		for i, t := range m.Aliases {
			if t.Status == "validated" && t.MappingType == "orthographic_alias" && Orthographic(t.Tag, g.Name) {
				pick(&m.Aliases[i], "alias", AliasFactor)
				return out
			}
		}
		break
	}
	// The family relationship comes only from Sync, never a guessed provider synonym.
	for _, f := range r.Families {
		if f.Family == g.Family && f.Primary != nil && f.Primary.Status == "validated" {
			pick(f.Primary, "family_fallback", FamilyFactor)
			return out
		}
	}
	return out
}
func Orthographic(a, b string) bool {
	return strings.ReplaceAll(music.CanonicalKey(a), " ", "") == strings.ReplaceAll(music.CanonicalKey(b), " ", "")
}
func (r Registry) Validate(c music.CanonicalCatalog) error {
	if r.Version != 1 {
		return fmt.Errorf("unsupported provider mapping version")
	}
	families := map[string]bool{}
	for _, f := range c.Families {
		families[f.ID] = true
	}
	seen := map[string]bool{}
	validTag := func(t ProviderTag) bool {
		return strings.TrimSpace(t.Tag) != "" && (t.Status == "validated" || t.Status == "candidate" || t.Status == "rejected" || t.Status == "unmapped") && (t.MappingType == "exact" || t.MappingType == "orthographic_alias" || t.MappingType == "related" || t.MappingType == "none")
	}
	for _, m := range r.Genres {
		g, ok := c.Lookup(m.CanonicalGenreID)
		if !ok || seen[g.ID] {
			return fmt.Errorf("unknown/duplicate canonical mapping")
		}
		seen[g.ID] = true
		if m.Exact != nil {
			if !validTag(*m.Exact) || m.Exact.MappingType != "exact" || lastfm.Normalize(m.Exact.Tag) != lastfm.Normalize(g.Name) {
				return fmt.Errorf("non-exact tag in exact slot")
			}
		}
		for _, t := range m.Aliases {
			if !validTag(t) || t.MappingType == "orthographic_alias" && !Orthographic(t.Tag, g.Name) {
				return fmt.Errorf("invalid orthographic alias")
			}
		}
		if m.FamilyFallback != nil && m.FamilyFallback.Family != g.Family {
			return fmt.Errorf("fallback family differs from taxonomy")
		}
	}
	seen = map[string]bool{}
	for _, f := range r.Families {
		if !families[f.Family] || seen[f.Family] {
			return fmt.Errorf("unknown/duplicate family fallback")
		}
		seen[f.Family] = true
		if f.Primary != nil && (!validTag(*f.Primary) || f.Primary.Status != "validated" || f.Primary.MappingType == "related" || f.Primary.MappingType == "none") {
			return fmt.Errorf("non-validated family fallback")
		}
	}
	return nil
}
func LoadRegistry(path string, c music.CanonicalCatalog) (Registry, error) {
	var r Registry
	b, e := os.ReadFile(path)
	if e != nil {
		return r, e
	}
	if e = json.Unmarshal(b, &r); e != nil {
		return r, e
	}
	return r, r.Validate(c)
}

// Runtime excludes all candidate/rejected/related tags but keeps unmapped IDs explicit.
func (r Registry) Runtime() Registry {
	out := r
	out.Genres = []GenreMapping{}
	out.Families = []FamilyMapping{}
	strip := func(t ProviderTag) ProviderTag { t.Evidence.Samples = nil; return t }
	for _, m := range r.Genres {
		entry := m
		entry.Aliases = []ProviderTag{}
		entry.FamilyFallback = nil
		entry.Exact = nil
		if m.Exact != nil && m.Exact.Status == "validated" {
			t := strip(*m.Exact)
			entry.Exact = &t
		}
		for _, t := range m.Aliases {
			if t.Status == "validated" && t.MappingType == "orthographic_alias" {
				entry.Aliases = append(entry.Aliases, strip(t))
			}
		}
		if entry.Exact == nil && len(entry.Aliases) == 0 {
			entry.Status = "unmapped"
		}
		out.Genres = append(out.Genres, entry)
	}
	for _, f := range r.Families {
		copy := f
		if f.Primary != nil {
			t := strip(*f.Primary)
			copy.Primary = &t
		}
		out.Families = append(out.Families, copy)
	}
	return out
}
