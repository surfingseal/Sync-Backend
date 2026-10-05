package main

import (
	"example.com/sync/internal/lastfm"
	"example.com/sync/internal/lastfmmapping"
	"testing"
)

func TestBoundedIndependentSpellingPlan(t *testing.T) {
	p := probes()
	if len(p) != 12 {
		t.Fatalf("expected 12 probes, got %d", len(p))
	}
	seen := map[string]bool{}
	for _, v := range p {
		if seen[v.Tag] {
			t.Fatal("duplicate provider tag")
		}
		seen[v.Tag] = true
	}
	for _, tag := range []string{"indie folk", "indie-folk", "indiefolk", "chamber pop", "chamber-pop", "chamberpop", "bossa nova", "bossa-nova", "bossanova", "folk", "pop", "jazz"} {
		if !seen[tag] {
			t.Errorf("missing %s", tag)
		}
	}
}
func TestProposalNeedsSamplesAndNoErrors(t *testing.T) {
	tracks := make([]lastfm.Track, 10)
	n1, n2 := lastfm.Number(10), lastfm.Number(100)
	a := lastfmmapping.ProbeResult{Probe: lastfmmapping.Probe{Canonical: "indie-folk", Tag: "indie folk", Kind: "exact"}, Info: &lastfm.TagInfo{Total: &n1}, Tracks: tracks}
	b := a
	b.Probe.Tag = "indie-folk"
	b.Probe.Kind = "orthographic_alias"
	b.Info = &lastfm.TagInfo{Total: &n2}
	bad := b
	bad.Probe.Tag = "indiefolk"
	bad.Error = "request failed"
	empty := b
	empty.Probe.Canonical = "bossa-nova"
	empty.Tracks = nil
	got := propose([]lastfmmapping.ProbeResult{a, b, bad, empty})
	if len(got) != 1 || got["indie-folk"].Probe.Tag != "indie-folk" {
		t.Fatalf("unexpected proposal: %v", got)
	}
}
