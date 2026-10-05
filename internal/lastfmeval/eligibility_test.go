package lastfmeval

import (
	"context"
	"encoding/json"
	"errors"
	"example.com/sync/internal/lastfm"
	"example.com/sync/internal/lastfmmapping"
	"example.com/sync/internal/music"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func fixtureEvidence(reach int64, n int) TagEvidence {
	r := lastfm.Number(reach)
	e := TagEvidence{Tag: "indie folk", Category: "subgenre", Info: &lastfm.TagInfo{Reach: &r}, InfoSuccess: true, TracksSuccess: true, SampleRequested: 20, Tracks: []lastfm.Track{}, Source: "fake HTTP-free observation"}
	for i := 0; i < n; i++ {
		t := lastfm.Track{Name: fmt.Sprintf("Track %d", i)}
		t.Artist.Name = fmt.Sprintf("Artist %d", i)
		e.Tracks = append(e.Tracks, t)
	}
	return e
}
func TestReachAndBasicEvidenceBoundaries(t *testing.T) {
	for _, x := range []struct {
		reach  int64
		status string
	}{{999, "insufficient_evidence"}, {1000, "provisional"}, {4999, "provisional"}, {5000, "eligible"}} {
		if d := DecideEligibility(fixtureEvidence(x.reach, 20)); d.Status != x.status {
			t.Errorf("reach %d: %s", x.reach, d.Status)
		}
	}
	for _, n := range []int{14, 15} {
		e := fixtureEvidence(5000, n)
		d := DecideEligibility(e)
		want := "eligible"
		if n == 14 {
			want = "insufficient_evidence"
		}
		if d.Status != want {
			t.Errorf("unique %d: %s", n, d.Status)
		}
	}
	for _, change := range []func(*TagEvidence){func(e *TagEvidence) { e.Info.Reach = nil }, func(e *TagEvidence) { e.SampleRequested = 19 }, func(e *TagEvidence) { e.InfoSuccess = false }, func(e *TagEvidence) { e.TracksSuccess = false }} {
		e := fixtureEvidence(5000, 20)
		change(&e)
		if d := DecideEligibility(e); d.Status != "insufficient_evidence" {
			t.Fatalf("missing/failure improperly accepted: %v", d)
		}
	}
}
func TestDuplicateRatioAndSharedIdentity(t *testing.T) {
	for _, n := range []int{99, 100, 101} {
		e := fixtureEvidence(5000, 1000-n)
		for i := 0; i < n; i++ {
			e.Tracks = append(e.Tracks, e.Tracks[0])
		}
		d := DecideEligibility(e)
		want := "eligible"
		if n > 100 {
			want = "insufficient_evidence"
		}
		if d.Status != want || d.Evidence.DuplicateRatio != float64(n)/1000 {
			t.Fatalf("duplicate ratio %d: %v", n, d)
		}
	}
	e := fixtureEvidence(5000, 20)
	e.Tracks[0].MBID = "same"
	e.Tracks[1].MBID = "same"
	e.Tracks[2].Name = e.Tracks[1].Name
	e.Tracks[2].Artist.Name = e.Tracks[1].Artist.Name
	d := DecideEligibility(e)
	if d.Evidence.ValidUniqueTracks != 18 {
		t.Fatalf("transitive MBID/pair merge not reused: %d", d.Evidence.ValidUniqueTracks)
	}
	e = fixtureEvidence(5000, 20)
	e.Tracks[0].Name = ""
	e.Tracks[1].Artist.Name = ""
	d = DecideEligibility(e)
	if d.Evidence.InvalidReceived != 2 || d.Evidence.ValidUniqueTracks != 18 || d.Status != "eligible" {
		t.Fatal("invalid metadata handling", d)
	}
	e = fixtureEvidence(5000, 20)
	if d := DecideEligibility(e); d.Status != "eligible" {
		t.Fatal("MBID absence must not block")
	}
}
func concentrated(n int) TagEvidence {
	e := fixtureEvidence(5000, 20)
	for i := 0; i < n; i++ {
		e.Tracks[i].Artist.Name = "Same artist"
	}
	return e
}
func TestDiversityAndConcentrationBoundaries(t *testing.T) {
	for _, n := range []int{8, 9, 12, 13} {
		d := DecideEligibility(concentrated(n))
		warning := len(d.Warnings) > 0
		if warning != (n > 8) {
			t.Errorf("40 percent warning: %d %v", n, d.Warnings)
		}
		want := "eligible"
		if n > 12 {
			want = "provisional"
		}
		if d.Status != want {
			t.Errorf("60 percent boundary: %d %s", n, d.Status)
		}
	}
	for _, artists := range []int{7, 8} {
		e := fixtureEvidence(5000, 20)
		for i := range e.Tracks {
			e.Tracks[i].Artist.Name = fmt.Sprintf("Artist %d", i%artists)
		}
		d := DecideEligibility(e)
		want := "eligible"
		if artists == 7 {
			want = "provisional"
		}
		if d.Status != want {
			t.Errorf("artists %d: %s", artists, d.Status)
		}
	}
	e := fixtureEvidence(5000, 20)
	e.ExplicitRejected = true
	if DecideEligibility(e).Status != "rejected" {
		t.Fatal("explicit noisy rejection")
	}
	e = fixtureEvidence(5000, 20)
	e.InfoSuccess = false
	if DecideEligibility(e).Status == "rejected" {
		t.Fatal("network failure is not rejection")
	}
}
func baseAuto(e []TagEvidence) AutomaticRegistry {
	return BuildAutomaticRegistry(music.CanonicalTaxonomy(), lastfmmapping.Registry{Version: 1}, e)
}
func TestResolverEvidencePriorityAndFallback(t *testing.T) {
	c := music.CanonicalTaxonomy()
	exact := fixtureEvidence(5000, 20)
	alias := fixtureEvidence(5000, 20)
	alias.Tag = "indie-folk"
	r := baseAuto([]TagEvidence{exact, alias})
	if x := r.Resolve(c, "indie-folk", false); x.ResolutionType != "exact" || x.WeightFactor != 1 {
		t.Fatal("exact tie", x)
	}
	exact.SampleRequested = 10
	r = baseAuto([]TagEvidence{exact, alias})
	if x := r.Resolve(c, "indie-folk", false); x.ResolutionType != "alias" || x.WeightFactor != .95 {
		t.Fatal("eligible alias beats insufficient exact", x)
	}
	exact = fixtureEvidence(5000, 20)
	alias.Info.Reach = new(lastfm.Number)
	*alias.Info.Reach = 6000
	r = baseAuto([]TagEvidence{exact, alias})
	if x := r.Resolve(c, "indie-folk", false); x.ProviderTag != "indie-folk" {
		t.Fatal("stronger equivalent must beat exact tie-break", x)
	}
	alias.Info.Reach = new(lastfm.Number)
	*alias.Info.Reach = 1000
	r = baseAuto([]TagEvidence{alias})
	if r.Resolve(c, "indie-folk", false).ResolutionType != "unmapped" || r.Resolve(c, "indie-folk", true).ResolutionType != "alias" {
		t.Fatal("provisional opt-in")
	}
	folk := fixtureEvidence(5000, 20)
	folk.Tag = "folk"
	fd := AutomaticTag{Canonical: "acoustic-folk", MappingType: "family_fallback", EligibilityDecision: DecideEligibility(folk)}
	r.Families = []AutomaticFamily{{Family: "acoustic-folk", Primary: &fd}}
	if x := r.Resolve(c, "indie-folk", false); x.ResolutionType != "family_fallback" || x.WeightFactor != .65 {
		t.Fatal("eligible family", x)
	}
	fd.Status = "insufficient_evidence"
	if r.Resolve(c, "indie-folk", false).ResolutionType != "unmapped" {
		t.Fatal("family policy required")
	}
	for _, id := range []string{"other", "unknown"} {
		if r.Resolve(c, id, true).ResolutionType != "unmapped" {
			t.Fatal("sentinel")
		}
	}
	noise := fixtureEvidence(9000, 20)
	noise.Tag = "baroque pop"
	r = baseAuto([]TagEvidence{noise})
	if r.Resolve(c, "chamber-pop", true).ResolutionType != "unmapped" {
		t.Fatal("semantic-related not allowed")
	}
	old := fixtureEvidence(9000, 10)
	old.SampleRequested = 10
	r = baseAuto([]TagEvidence{old})
	if r.Resolve(c, "indie-folk", false).ResolutionType != "unmapped" {
		t.Fatal("prior metadata validation cannot bypass sample condition")
	}
}

func TestEquivalentSelectionIsStableAndNoUseOfWrongStatus(t *testing.T) {
	c := music.CanonicalTaxonomy()
	a := fixtureEvidence(6000, 20)
	a.Tag = "indie-folk"
	b := fixtureEvidence(6000, 20)
	b.Tag = "indiefolk"
	r1, r2 := baseAuto([]TagEvidence{a, b}), baseAuto([]TagEvidence{b, a})
	x, y := r1.Resolve(c, "indie-folk", false), r2.Resolve(c, "indie-folk", false)
	if x != y || x.ProviderTag != "indie-folk" {
		t.Fatal("lexical equivalent tie-break must be input-order invariant", x, y)
	}
	for i := range r1.Genres {
		if r1.Genres[i].Canonical == "indie-folk" {
			for j := range r1.Genres[i].Tags {
				r1.Genres[i].Tags[j].Status = "validated"
			}
		}
	}
	if r1.Resolve(c, "indie-folk", false).ResolutionType != "unmapped" {
		t.Fatal("old validated may not enter automatic resolver")
	}
	if r1.Validate(c) == nil {
		t.Fatal("old status invalid in automatic registry")
	}
}
func TestSerializationPolicyAndMissingValues(t *testing.T) {
	e := fixtureEvidence(5000, 20)
	r := baseAuto([]TagEvidence{e})
	if err := r.Validate(music.CanonicalTaxonomy()); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(r)
	b, _ := json.Marshal(baseAuto([]TagEvidence{e}))
	if string(a) != string(b) {
		t.Fatal("unstable serialization")
	}
	for _, s := range []string{`"retrieval_policy_version":1`, `"total":null`, `"taggings":null`, `"human_review":"not_performed"`} {
		if !strings.Contains(string(a), s) {
			t.Fatal("missing", s)
		}
	}
	if strings.Contains(string(a), "api_key") {
		t.Fatal("key in artifact")
	}
	r.Policy.EligibleReach = 100
	if r.Validate(music.CanonicalTaxonomy()) == nil {
		t.Fatal("policy tampering")
	}
}

type budgetFake struct{ calls int }

func (f *budgetFake) RoundTrip(*http.Request) (*http.Response, error) {
	f.calls++
	return nil, errors.New("temporary failure")
}
func TestHTTPAttemptBudgetIncludesRetries(t *testing.T) {
	f := &budgetFake{}
	b := &AuditHTTPBudget{Limit: 24, Base: f}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "https://example.invalid", nil)
	for i := 0; i < 30; i++ {
		b.RoundTrip(req)
	}
	if f.calls != 24 || b.Used.Load() != 24 {
		t.Fatal("budget exceeded")
	}
	if _, e := b.RoundTrip(req); !errors.Is(e, ErrAuditHTTPBudget) {
		t.Fatal("missing budget error")
	}
}
func TestSavedEvidenceAndCandidateEligibility(t *testing.T) {
	c := music.CanonicalTaxonomy()
	pool, e := LoadEligibilityEvidence("testdata/lastfm-reviewed-20261004/lastfm_tag_audit.json", "testdata/lastfm-specificity-live-20261004/mappings.json", "testdata/lastfm-retrieval-live-20261004/evaluation.json", c)
	if e != nil {
		t.Fatal(e)
	}
	statuses := map[string]string{}
	for _, x := range pool {
		statuses[x.Tag] = DecideEligibility(x).Status
	}
	for _, tag := range []string{"indie folk", "chamber pop", "bossa nova"} {
		if statuses[tag] != "eligible" {
			t.Fatal("actual reused candidate", tag, statuses[tag])
		}
	}
	if statuses["rock"] != "insufficient_evidence" {
		t.Fatal("prior Top10 cannot be eligible")
	}
}
