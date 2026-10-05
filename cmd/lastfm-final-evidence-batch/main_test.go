package main

import (
	"encoding/json"
	"errors"
	"example.com/sync/internal/lastfmeval"
	"example.com/sync/internal/lastfmmapping"
	"example.com/sync/internal/music"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"testing"
)

func loadState(t *testing.T) (music.CanonicalCatalog, lastfmmapping.Registry, lastfmeval.AutomaticRegistry, []lastfmeval.TagEvidence) {
	t.Helper()
	c := music.CanonicalTaxonomy()
	legacy, e := lastfmmapping.LoadRegistry("../../internal/music/lastfm_genre_mapping.json", c)
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile("../../internal/lastfmeval/testdata/lastfm-eligibility-supplement-20261004T142044Z/provider-observations.json")
	if e != nil {
		t.Fatal(e)
	}
	var pool []lastfmeval.TagEvidence
	if e = json.Unmarshal(b, &pool); e != nil {
		t.Fatal(e)
	}
	return c, legacy, lastfmeval.BuildAutomaticRegistry(c, legacy, pool), pool
}
func TestPriorityDeterminismAndLeverage(t *testing.T) {
	c, l, r, pool := loadState(t)
	usage := map[string]int{"city-pop": 1, "k-indie": 1, "chillwave": 1, "indie-folk": 1, "bossa-nova": 1, "chamber-pop": 1, "acoustic": 1}
	a := priorityRanking(c, l, r, pool, usage)
	reversed := append([]lastfmeval.TagEvidence{}, pool...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	b := priorityRanking(c, l, r, reversed, usage)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("unstable ranking")
	}
	selected := selectBatch(a, 6, 16)
	if len(selected) != 6 {
		t.Fatal(len(selected))
	}
	calls := 0
	for _, p := range selected {
		calls += p.ExpectedCalls
		fmt.Println("planned", p.Tag, p.ExpectedCalls, p.Reason)
	}
	if calls > 16 {
		t.Fatal("cap exceeded")
	}
	for _, p := range a {
		if p.Tag == "rock" || p.Tag == "soul" {
			if p.FamilyRecoverable == 0 || !p.HasInfo || p.ExpectedCalls != 1 {
				t.Fatalf("missing leverage/info reuse: %+v", p)
			}
		}
	}
	if len(selectBatch(b, 8, 1)) > 1 {
		t.Fatal("call plan escaped small cap")
	}
}
func TestPolicyAndGraphRecovery(t *testing.T) {
	c, l, b, pool := loadState(t)
	for i, tg := range pool {
		if tg.Tag != "rock" && tg.Tag != "soul" {
			continue
		}
		if lastfmeval.DecideEligibility(tg).Status != "insufficient_evidence" {
			t.Fatal("Top10 accepted")
		}
		info, at := tg.Info, tg.InfoRecordedAt
		next := hypothetical(tg)
		next.Info = info
		next.InfoRecordedAt = at
		next.RecordedAt = "new_top20"
		pool[i] = next
	}
	a := lastfmeval.BuildAutomaticRegistry(c, l, pool)
	if a.Policy != b.Policy {
		t.Fatal("threshold changed")
	}
	if e := a.Validate(c); e != nil {
		t.Fatal(e)
	}
	for i, f := range b.Families {
		af := a.Families[i]
		if (f.Primary == nil) != (af.Primary == nil) || f.Primary != nil && f.Primary.Tag != af.Primary.Tag {
			t.Fatal("family graph changed")
		}
	}
	for _, id := range []string{"rock", "soul"} {
		if a.Resolve(c, id, false).ResolutionType != "exact" {
			t.Fatal("exact not restored")
		}
	}
	for _, g := range c.Genres {
		if g.Family == "rock" && g.ID != "rock" {
			route := a.Resolve(c, g.ID, false)
			if route.ResolutionType != "family_fallback" && route.ResolutionType != "exact" && route.ResolutionType != "alias" {
				t.Fatal("rock fallback not restored")
			}
		}
	}
	for _, id := range []string{"other", "unknown"} {
		if a.Resolve(c, id, false).ResolutionType != "unmapped" {
			t.Fatal("sentinel routed")
		}
	}
	for _, id := range []string{"indie-folk", "bossa-nova", "chamber-pop", "acoustic"} {
		if !reflect.DeepEqual(a.Resolve(c, id, false), b.Resolve(c, id, false)) {
			t.Fatal("still-life route changed")
		}
	}
	for i, f := range a.Families {
		if f.Family == "rock" {
			f.Primary.Status = "provisional"
			a.Families[i] = f
		}
	}
	// Force an exact-absent existing canonical; provisional family must not be used by default.
	for i, g := range a.Genres {
		if g.Canonical == "post-rock" {
			a.Genres[i].Tags = nil
		}
	}
	if a.Resolve(c, "post-rock", false).ResolutionType != "unmapped" {
		t.Fatal("provisional family allowed by default")
	}
	if a.Resolve(c, "post-rock", true).ResolutionType != "family_fallback" {
		t.Fatal("explicit opt-in not working")
	}
	for i, f := range a.Families {
		if f.Family == "rock" {
			f.Primary.Status = "insufficient_evidence"
			a.Families[i] = f
		}
	}
	if a.Resolve(c, "post-rock", true).ResolutionType != "unmapped" {
		t.Fatal("insufficient family allowed")
	}
}
func TestSixteenAttemptBudgetCountsRetries(t *testing.T) {
	base := &attemptTransport{}
	budget := &lastfmeval.AuditHTTPBudget{Limit: 16, Base: base}
	req, _ := http.NewRequest("GET", "https://audit.invalid", nil)
	for i := 0; i < 20; i++ {
		_, e := budget.RoundTrip(req)
		if i >= 16 && !errors.Is(e, lastfmeval.ErrAuditHTTPBudget) {
			t.Fatal("cap")
		}
	}
	if base.n != 16 || budget.Used.Load() != 16 {
		t.Fatal("repeated attempts escaped cap")
	}
}

type attemptTransport struct{ n int }

func (t *attemptTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.n++
	return nil, errors.New("fake network failure")
}
