package lastfmmapping

import (
	"context"
	"encoding/json"
	"example.com/sync/internal/lastfm"
	"example.com/sync/internal/music"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func evidenceTag(name, status string) lastfm.AuditedTag {
	n := lastfm.Number(100)
	t := lastfm.AuditedTag{Name: name, Category: "genre", Status: status, Reach: &n, Info: &lastfm.TagInfo{Reach: &n, Total: &n}, Reviewed: true, Notes: "Explicit previous metadata review; no audio guarantee"}
	for i := 0; i < 5; i++ {
		x := lastfm.Track{Name: "Track", URL: "https://last.fm/music/example"}
		x.Artist.Name = string(rune('A' + i))
		t.Samples = append(t.Samples, lastfm.Sample{Rank: i + 1, Track: x})
	}
	return t
}
func options() Options { return Options{Sample: 20, DryRun: true, Concurrency: 2, MaxProbes: 120} }
func TestPriorReuseAndFamilyResolution(t *testing.T) {
	c := music.CanonicalTaxonomy()
	prior := &lastfm.Audit{Tags: []lastfm.AuditedTag{evidenceTag("folk", "validated"), evidenceTag("acoustic", "validated"), evidenceTag("pop", "validated"), evidenceTag("jazz", "candidate"), evidenceTag("Hip-Hop", "validated"), evidenceTag("hip hop", "validated")}}
	a, e := Plan(c, prior, "prior-audit.json", options())
	if e != nil {
		t.Fatal(e)
	}
	if a.EvidenceReused != 6 || a.APICalls != 0 {
		t.Fatal("reuse count")
	}
	if r := a.Registry.Resolve(c, "acoustic"); r.ResolutionType != "exact" || r.WeightFactor != 1 {
		t.Fatal("exact precedence", r)
	}
	if r := a.Registry.Resolve(c, "indie-folk"); r.ResolutionType != "family_fallback" || r.ProviderTag != "folk" || r.WeightFactor != .65 {
		t.Fatal("fallback", r)
	}
	if r := a.Registry.Resolve(c, "bossa-nova"); r.ResolutionType != "unmapped" {
		t.Fatal("no validated jazz family")
	}
	if r := a.Registry.Resolve(c, "other"); r.ResolutionType != "unmapped" {
		t.Fatal("sentinel")
	}
	found := false
	for _, m := range a.Registry.Genres {
		if m.CanonicalGenreID == "hip-hop" {
			found = len(m.Aliases) == 1 && m.Aliases[0].Tag == "hip hop" && m.Aliases[0].Evidence.SampleSize == 5
		}
	}
	if !found {
		t.Fatal("separate orthographic evidence")
	}
	for _, p := range a.Planned {
		if p.Canonical == "acoustic" {
			t.Fatal("validated existing evidence should not be re-probed")
		}
	}
}
func TestAliasRelatedAndStatus(t *testing.T) {
	c := music.CanonicalTaxonomy()
	a, e := Plan(c, &lastfm.Audit{Tags: []lastfm.AuditedTag{evidenceTag("hip hop", "validated"), evidenceTag("pop", "validated")}}, "prior", options())
	if e != nil {
		t.Fatal(e)
	}
	if r := a.Registry.Resolve(c, "hip-hop"); r.ResolutionType != "alias" || r.WeightFactor != .95 {
		t.Fatal("alias", r)
	}
	for i, m := range a.Registry.Genres {
		if m.CanonicalGenreID == "chamber-pop" {
			a.Registry.Genres[i].Aliases = append(m.Aliases, ProviderTag{Tag: "baroque pop", MappingType: "related", Status: "validated"})
		}
	}
	if r := a.Registry.Resolve(c, "chamber-pop"); r.ResolutionType != "family_fallback" || r.ProviderTag != "pop" {
		t.Fatal("related must never route")
	}
	for _, status := range []string{"candidate", "rejected", "unmapped"} {
		r := a.Registry
		r.Genres = append([]GenreMapping{}, a.Registry.Genres...)
		for i, m := range r.Genres {
			if m.CanonicalGenreID == "hip-hop" {
				r.Genres[i].Aliases = append([]ProviderTag{}, m.Aliases...)
				r.Genres[i].Aliases[0].Status = status
			}
		}
		if r.Resolve(c, "hip-hop").ResolutionType != "unmapped" {
			t.Fatal("status filtering")
		}
	}
	runtime := a.Registry.Runtime()
	for _, m := range runtime.Genres {
		for _, tag := range m.Aliases {
			if tag.MappingType == "related" {
				t.Fatal("related in runtime")
			}
		}
	}
	if Orthographic("bossa nova", "jazz") || Orthographic("chamber pop", "baroque pop") {
		t.Fatal("semantic synonyms")
	}
}

type fakeProbe struct {
	fetch func(context.Context, Probe, int) ProbeResult
}

func (f fakeProbe) Fetch(c context.Context, p Probe, n int) ProbeResult { return f.fetch(c, p, n) }
func TestPartialCancellationSerialization(t *testing.T) {
	c := music.CanonicalTaxonomy()
	o := options()
	o.DryRun = false
	o.Live = true
	o.MaxProbes = 2
	a, e := Plan(c, &lastfm.Audit{}, "none", o)
	if e != nil {
		t.Fatal(e)
	}
	f := fakeProbe{func(ctx context.Context, p Probe, n int) ProbeResult {
		if p.Canonical == "pop" {
			v := lastfm.Number(20)
			t := evidenceTag(p.Tag, "candidate")
			tracks := []lastfm.Track{}
			for _, s := range t.Samples {
				tracks = append(tracks, s.Track)
			}
			return ProbeResult{Probe: p, Info: &lastfm.TagInfo{Reach: &v}, Tracks: tracks, Events: []lastfm.Event{{Method: "tag.getInfo", HTTPAttempted: true}, {Method: "tag.getTopTracks", HTTPAttempted: true}}}
		}
		return ProbeResult{Probe: p, Error: "controlled failure"}
	}}
	Execute(context.Background(), a, f)
	if len(a.Results) != 2 || a.APICalls != 2 || len(a.Warnings) == 0 {
		t.Fatal("partial accounting")
	}
	if a.Registry.Genres[0].Exact == nil || a.Registry.Genres[0].Exact.Status != "candidate" {
		t.Fatal("new evidence never auto validated")
	}
	dir := t.TempDir()
	if e = Write(dir, a, c); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(dir, "mappings.json"))
	if e != nil || strings.Contains(string(b), "unit-secret") {
		t.Fatal("serialization/key leak")
	}
	var loaded AuditResult
	if json.Unmarshal(b, &loaded) != nil || loaded.APICalls != 2 {
		t.Fatal("round trip")
	}
	for _, name := range []string{"human-review.csv", "unmapped.json", "candidates.json", "runtime-mapping.json", "report.md"} {
		if _, e = os.Stat(filepath.Join(dir, name)); e != nil {
			t.Fatal(name, e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a, _ = Plan(c, &lastfm.Audit{}, "none", o)
	f.fetch = func(ctx context.Context, p Probe, n int) ProbeResult { return ProbeResult{Probe: p, Error: "canceled"} }
	Execute(ctx, a, f)
	if a.APICalls != 0 {
		t.Fatal("canceled before actual calls")
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	a, _ = Plan(c, &lastfm.Audit{}, "none", o)
	f.fetch = func(ctx context.Context, p Probe, n int) ProbeResult {
		<-ctx.Done()
		return ProbeResult{Probe: p, Error: "timeout"}
	}
	Execute(ctx, a, f)
	if len(a.Results) != 2 || a.APICalls != 0 {
		t.Fatal("timeout")
	}
}
func TestStablePlanAndUnknown(t *testing.T) {
	c := music.CanonicalTaxonomy()
	prior := &lastfm.Audit{Tags: []lastfm.AuditedTag{evidenceTag("pop", "validated")}}
	a, _ := Plan(c, prior, "same", options())
	b, _ := Plan(c, prior, "same", options())
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	if string(x) != string(y) {
		t.Fatal("nondeterministic plan")
	}
	unknown := AggregateUnknown(c, []string{"baroque pop", "indie folk", "baroque pop", "ethereal wave"})
	if len(unknown) != 2 || unknown[0].RawGenre != "baroque pop" || unknown[0].Count != 2 {
		t.Fatal("unknown aggregation")
	}
	o := options()
	o.Genre = "made-up"
	if _, e := Plan(c, prior, "same", o); e == nil {
		t.Fatal("invalid filter")
	}
}

func TestOrthographicVariantsAreIndependentCandidates(t *testing.T) {
	c := music.CanonicalTaxonomy()
	o := options()
	o.Genre = "dream-pop"
	o.OrthographicVariants = true
	a, e := Plan(c, &lastfm.Audit{}, "none", o)
	if e != nil {
		t.Fatal(e)
	}
	probes := []Probe{}
	for _, p := range a.Planned {
		if p.Canonical == "dream-pop" {
			probes = append(probes, p)
		}
	}
	if len(probes) != 3 || probes[0].Tag != "dream pop" || probes[1].Tag != "dream-pop" || probes[2].Tag != "dreampop" {
		t.Fatalf("spelling-only probes %+v", probes)
	}
	for _, p := range probes {
		if p.Tag == "shoegaze" {
			t.Fatal("no related tag generation")
		}
	}
	o.DryRun = false
	o.Live = true
	o.MaxProbes = 3
	a, _ = Plan(c, &lastfm.Audit{}, "none", o)
	f := fakeProbe{func(ctx context.Context, p Probe, n int) ProbeResult {
		v := lastfm.Number(10)
		return ProbeResult{Probe: p, Info: &lastfm.TagInfo{Reach: &v}, Tracks: []lastfm.Track{{Name: "Song"}}, Events: []lastfm.Event{}}
	}}
	Execute(context.Background(), a, f)
	for _, m := range a.Registry.Genres {
		if m.CanonicalGenreID == "dream-pop" {
			if m.Exact == nil || m.Exact.Status != "candidate" || len(m.Aliases) != 2 || m.Aliases[0].Tag == m.Aliases[1].Tag {
				t.Fatal("independent candidate evidence")
			}
		}
	}
}
