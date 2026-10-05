package music

import (
	"encoding/json"
	"fmt"
	"golang.org/x/text/unicode/norm"
)

// CanonicalCatalog is a provider-independent view of the existing embedded
// taxonomy.json. It does not introduce a second maintained genre/family source.
type CanonicalGenre struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Family  string   `json:"family"`
	Aliases []string `json:"aliases"`
}
type CanonicalFamily struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type CanonicalCatalog struct {
	Version  int               `json:"version"`
	Source   string            `json:"source"`
	Families []CanonicalFamily `json:"families"`
	Genres   []CanonicalGenre  `json:"genres"`
}

func CanonicalTaxonomy() CanonicalCatalog {
	c := CanonicalCatalog{Version: 1, Source: "internal/music/taxonomy.json (derived snapshot; original remains the canonical source)", Families: []CanonicalFamily{}, Genres: []CanonicalGenre{}}
	for _, family := range vocabulary.Categories {
		c.Families = append(c.Families, CanonicalFamily{family.Slug, family.DisplayName})
		for _, g := range family.Genres {
			c.Genres = append(c.Genres, CanonicalGenre{g.Name, g.DisplayName, family.Slug, append([]string{}, g.Aliases...)})
		}
	}
	return c
}
func CanonicalKey(s string) string { return Normalize(norm.NFKC.String(s)) }
func LoadCanonicalTaxonomy(b []byte) (CanonicalCatalog, error) {
	var c CanonicalCatalog
	err := json.Unmarshal(b, &c)
	if err != nil {
		return c, err
	}
	return c, c.Validate()
}
func (c CanonicalCatalog) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported canonical taxonomy version")
	}
	families := map[string]bool{}
	for _, f := range c.Families {
		if f.ID == "" || families[f.ID] {
			return fmt.Errorf("invalid/duplicate family")
		}
		families[f.ID] = true
	}
	ids := map[string]bool{}
	keys := map[string]string{}
	for _, g := range c.Genres {
		if g.ID == "" || g.Name == "" || ids[g.ID] || !families[g.Family] {
			return fmt.Errorf("duplicate genre ID or invalid family")
		}
		ids[g.ID] = true
		for _, s := range append([]string{g.ID, g.Name}, g.Aliases...) {
			k := CanonicalKey(s)
			if k == "" {
				return fmt.Errorf("empty canonical alias")
			}
			if old, ok := keys[k]; ok && old != g.ID {
				return fmt.Errorf("canonical alias collision")
			}
			keys[k] = g.ID
		}
	}
	return nil
}
func (c CanonicalCatalog) Lookup(id string) (CanonicalGenre, bool) {
	for _, g := range c.Genres {
		if g.ID == id {
			return g, true
		}
	}
	return CanonicalGenre{}, false
}
func (c CanonicalCatalog) Resolve(raw string) (CanonicalGenre, bool) {
	key := CanonicalKey(raw)
	if key == "" {
		return CanonicalGenre{}, false
	}
	for _, g := range c.Genres {
		for _, s := range append([]string{g.ID, g.Name}, g.Aliases...) {
			if CanonicalKey(s) == key {
				return g, true
			}
		}
	}
	return CanonicalGenre{}, false
}
