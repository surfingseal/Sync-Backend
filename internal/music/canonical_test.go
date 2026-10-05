package music

import (
	"encoding/json"
	"testing"
)

func TestCanonicalViewAndResolution(t *testing.T) {
	c := CanonicalTaxonomy()
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	if len(c.Genres) != len(Genres()) || len(c.Families) != len(Categories()) {
		t.Fatal("must derive from existing source")
	}
	for _, raw := range []string{"indie folk", "Indie-Folk", " Ｉｎｄｉｅ　Ｆｏｌｋ "} {
		g, ok := c.Resolve(raw)
		if !ok || g.ID != "indie-folk" || g.Family != "acoustic-folk" {
			t.Fatal("canonical normalization")
		}
	}
	g, ok := c.Resolve("rnb")
	if !ok || g.ID != "r&b" {
		t.Fatal("existing explicit alias")
	}
	if _, ok = c.Resolve("baroque pop"); ok {
		t.Fatal("no semantic synonym inference")
	}
	if _, ok = c.Resolve("korean indie"); ok {
		t.Fatal("query token is not an explicit canonical alias")
	}
	g, _ = c.Resolve("bossa nova")
	if g.Family != "jazz" {
		t.Fatal("retain current source relationship")
	}
	b, _ := json.Marshal(c)
	loaded, e := LoadCanonicalTaxonomy(b)
	if e != nil || len(loaded.Genres) != len(c.Genres) {
		t.Fatal("load", e)
	}
}
func TestCanonicalRejectsInvalidSource(t *testing.T) {
	c := CanonicalTaxonomy()
	c.Genres = append(c.Genres, c.Genres[0])
	if c.Validate() == nil {
		t.Fatal("duplicate ID")
	}
	c = CanonicalTaxonomy()
	c.Genres[0].Family = "invented"
	if c.Validate() == nil {
		t.Fatal("unknown family")
	}
	c = CanonicalTaxonomy()
	c.Genres[1].Aliases = append(c.Genres[1].Aliases, c.Genres[0].Name)
	if c.Validate() == nil {
		t.Fatal("alias collision")
	}
}
