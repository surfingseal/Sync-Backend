package music

import (
	"encoding/json"
	"testing"
)

func TestTaxonomyIntegrity(t *testing.T) {
	var v Vocabulary
	if err := json.Unmarshal(taxonomyJSON, &v); err != nil {
		t.Fatal(err)
	}
	if err := Validate(v); err != nil {
		t.Fatal(err)
	}
	if len(Categories()) < 10 || len(Categories()) > 15 || len(Genres()) < 40 || len(Genres()) > 70 {
		t.Fatal("taxonomy size")
	}
	for _, g := range Genres() {
		if g.Category == "" {
			t.Fatal("orphan child")
		}
		if g.Category != "other" && g.QueryTokens == "" {
			t.Fatal("missing mapping")
		}
	}
	for _, tc := range []struct{ label, name, tokens string }{{"dream pop", "dream-pop", "dream pop"}, {"rnb", "r&b", "rnb"}, {"lo-fi", "lo-fi-hip-hop", "lofi hip hop"}, {"k-indie", "k-indie", "korean indie"}} {
		g, ok := Resolve(tc.label)
		if !ok || g.Name != tc.name || g.QueryTokens != tc.tokens {
			t.Fatal(tc, g)
		}
	}
	g, ok := Resolve("invented ethereal folk artist")
	if ok || g.Name != "other" || g.QueryTokens != "" {
		t.Fatal("raw label entered query")
	}
	for _, name := range []string{"other", "unknown"} {
		if _, ok := Lookup(name); !ok {
			t.Fatal(name)
		}
	}
	// Returned snapshots must not allow mutation of the registry.
	genres := Genres()
	genres[0].Name = "tampered"
	if GenreNames()[0] == "tampered" {
		t.Fatal("mutable registry")
	}
}
func TestInvalidTaxonomies(t *testing.T) {
	for _, mutate := range []func(*Vocabulary){
		func(v *Vocabulary) {
			v.Categories[0].Genres = append(v.Categories[0].Genres, v.Categories[0].Genres[0])
		},
		func(v *Vocabulary) { v.Categories[0].Genres[0].QueryTokens = "" },
		func(v *Vocabulary) { v.Categories[0].Genres[0].Aliases = []string{"rock"} },
		func(v *Vocabulary) { v.Categories = v.Categories[:len(v.Categories)-1] },
		func(v *Vocabulary) { v.Categories[0].Genres[0].DisplayName = v.Categories[0].Genres[1].DisplayName },
	} {
		var v Vocabulary
		_ = json.Unmarshal(taxonomyJSON, &v)
		mutate(&v)
		if Validate(v) == nil {
			t.Fatal("invalid taxonomy accepted")
		}
	}
}
