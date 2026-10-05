package lastfmeval

import (
	"context"
	"encoding/json"
	"example.com/sync/internal/lastfm"
	"example.com/sync/internal/lastfmmapping"
	"example.com/sync/internal/music"
	"os"
	"testing"
)

func TestCanonicalRegressionPlan(t *testing.T) {
	c := music.CanonicalTaxonomy()
	v := vocabulary(t)
	n := lastfm.Number(100)
	prior := &lastfm.Audit{Tags: []lastfm.AuditedTag{}}
	for _, name := range []string{"folk", "pop", "acoustic"} {
		tag := lastfm.AuditedTag{Name: name, Category: "genre", Status: "validated", Reviewed: true, Reach: &n, Info: &lastfm.TagInfo{Reach: &n}, Samples: make([]lastfm.Sample, 5)}
		if name == "acoustic" {
			tag.Category = "style"
		}
		prior.Tags = append(prior.Tags, tag)
	}
	audit, err := lastfmmapping.Plan(c, prior, "test", lastfmmapping.Options{Sample: 20, DryRun: true, Concurrency: 2, MaxProbes: 12})
	if err != nil {
		t.Fatal(err)
	}
	profile := MusicRetrievalProfile{Genres: []WeightedConcept{concept("indie folk", .25), concept("acoustic", .25), concept("bossa nova", .25), concept("chamber pop", .25)}, Moods: []WeightedConcept{concept("dreamy", .2)}}
	plan, err := BuildCanonicalPlan(profile, v, DefaultPolicy(), c, audit.Registry.Runtime())
	if err != nil || len(plan.Routes) != 4 {
		t.Fatal("canonical plan", err)
	}
	if plan.TaxonomyResolution.Genres[0].Provider.ResolutionType != "family_fallback" || plan.TaxonomyResolution.Genres[2].Provider.ResolutionType != "unmapped" {
		t.Fatal("existing jazz family must not be invented into latin")
	}
	for _, r := range plan.Routes {
		if r.LastFMTag == "folk" && r.ProfileWeight != .25*.65 {
			t.Fatal("fallback factor")
		}
	}
	profile.Genres = append(profile.Genres, concept("folk", .3))
	plan, err = BuildCanonicalPlan(profile, v, DefaultPolicy(), c, audit.Registry.Runtime())
	if err != nil || len(plan.TaxonomyResolution.RouteSources["folk"]) != 2 {
		t.Fatal("shared-provider concepts evidence")
	}
	count := 0
	for _, r := range plan.Routes {
		if r.LastFMTag == "folk" {
			count++
			if r.ProfileWeight != .3 {
				t.Fatal("strongest route contribution retained")
			}
		}
	}
	if count != 1 {
		t.Fatal("one request per provider tag")
	}
}
func TestCapturedRetrieverNoFreshClaims(t *testing.T) {
	a := lastfm.Audit{Tags: []lastfm.AuditedTag{{Name: "folk", Samples: []lastfm.Sample{{Rank: 1, Track: track("Artist", "Song", "id")}}}}}
	b, _ := json.Marshal(a)
	p := t.TempDir() + "/audit.json"
	_ = os.WriteFile(p, b, 0600)
	c, e := LoadCaptured(p, "")
	if e != nil {
		t.Fatal(e)
	}
	r, e := c.Fetch(context.Background(), "folk", 1)
	if e != nil || len(r.Tracks) != 1 || len(r.Events) != 0 {
		t.Fatal("capture should not count as HTTP/cache hit")
	}
	if _, e = c.Fetch(context.Background(), "folk", 50); e == nil {
		t.Fatal("must reject insufficient captured sample")
	}
}
