package main

import (
	"encoding/json"
	"errors"
	"example.com/sync/internal/lastfm"
	"example.com/sync/internal/lastfmeval"
	"example.com/sync/internal/lastfmmapping"
	"example.com/sync/internal/music"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestScopedPlanAndFamilyRecovery(t *testing.T) {
	c := music.CanonicalTaxonomy()
	old, err := lastfmmapping.LoadRegistry("../../internal/music/lastfm_genre_mapping.json", c)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := lastfmeval.LoadEligibilityEvidence("../../internal/lastfmeval/testdata/lastfm-reviewed-20261004/lastfm_tag_audit.json", "../../internal/lastfmeval/testdata/lastfm-specificity-live-20261004/mappings.json", "../../internal/lastfmeval/testdata/lastfm-retrieval-live-20261004/evaluation.json", c)
	if err != nil {
		t.Fatal(err)
	}
	before := lastfmeval.BuildAutomaticRegistry(c, old, pool)
	plan := coreNeeds(lastfmeval.MissingEvidencePlan(c, before, pool, lastfmeval.MusicRetrievalProfile{}))
	if len(plan) != 4 {
		t.Fatalf("targets: %v", plan)
	}
	for _, need := range plan {
		if need.NeedInfo || !need.NeedTracks || need.ExpectedCalls != 1 {
			t.Fatalf("must reuse prior info: %v", need)
		}
	}
	for i, e := range pool {
		if len(observations([]lastfmeval.TagEvidence{e}, "Classical", "Hip-Hop", "hip hop", "electronic")) == 0 {
			continue
		}
		if lastfmeval.DecideEligibility(e).Status != "insufficient_evidence" {
			t.Fatal("Top10 accepted")
		}
		previousInfoAt := e.InfoRecordedAt
		e.Tracks = nil
		for n := 0; n < 20; n++ {
			track := lastfm.Track{Name: string(rune('A' + n))}
			track.Artist.Name = track.Name
			e.Tracks = append(e.Tracks, track)
		}
		e.SampleRequested = 20
		e.RecordedAt = "new_tracks_snapshot"
		e.TracksSuccess = true
		if e.InfoRecordedAt != previousInfoAt || e.InfoRecordedAt == e.RecordedAt {
			t.Fatal("mixed provenance lost")
		}
		pool[i] = e
	}
	after := lastfmeval.BuildAutomaticRegistry(c, old, pool)
	if err = after.Validate(c); err != nil {
		t.Fatal(err)
	}
	for _, family := range []string{"classical", "hip-hop", "electronic"} {
		recovered := 0
		for _, g := range c.Genres {
			if g.Family == family && before.Resolve(c, g.ID, false).ResolutionType == "unmapped" && after.Resolve(c, g.ID, false).ResolutionType != "unmapped" {
				recovered++
			}
		}
		if recovered == 0 {
			t.Fatal("family not recovered", family)
		}
	}
	for i, f := range before.Families {
		a := after.Families[i]
		if (f.Primary == nil) != (a.Primary == nil) || f.Primary != nil && f.Primary.Tag != a.Primary.Tag {
			t.Fatal("family graph changed")
		}
	}
	for i, g := range before.Genres {
		a := after.Genres[i]
		for j, tag := range g.Tags {
			if tag.Tag != a.Tags[j].Tag || tag.MappingType != a.Tags[j].MappingType {
				t.Fatal("alias graph changed")
			}
		}
	}
	one, _ := json.Marshal(after)
	two, _ := json.Marshal(lastfmeval.BuildAutomaticRegistry(c, old, pool))
	if !reflect.DeepEqual(one, two) {
		t.Fatal("serialization unstable")
	}
	// Recent still-life route IDs are unaffected by this supplementation.
	for _, id := range []string{"indie-folk", "acoustic", "bossa-nova", "chamber-pop"} {
		if !reflect.DeepEqual(before.Resolve(c, id, false), after.Resolve(c, id, false)) {
			t.Fatal("recent-photo route changed", id)
		}
	}
}

func TestTwelveAttemptBudgetIncludingRepeatedRequests(t *testing.T) {
	base := &countTransport{}
	budget := &lastfmeval.AuditHTTPBudget{Limit: 12, Base: base}
	req, _ := http.NewRequest(http.MethodGet, "https://audit.invalid", nil)
	for i := 0; i < 15; i++ {
		_, err := budget.RoundTrip(req)
		if (i >= 12) != errors.Is(err, lastfmeval.ErrAuditHTTPBudget) {
			t.Fatal("budget enforcement", i, err)
		}
	}
	if base.n != 12 || budget.Used.Load() != 12 {
		t.Fatal("attempts escaped hard cap")
	}
}

type countTransport struct{ n int }

func (t *countTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.n++
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
}
