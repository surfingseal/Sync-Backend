package directbench

import (
	"crypto/sha256"
	"encoding/json"
	"example.com/sync/internal/directmusic"
	"example.com/sync/internal/model"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestManifestContentDedupeAndValidation(t *testing.T) {
	dir := t.TempDir()
	data := []byte("test-only bytes, never live benchmark photo")
	os.WriteFile(filepath.Join(dir, "image"), data, 0600)
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	rows := []Image{{ID: "one", Path: "image", SHA256: hash, Enabled: true}, {ID: "two", Path: "image", SHA256: hash, Enabled: true}}
	path := filepath.Join(dir, "manifest.json")
	save := func() { b, _ := json.Marshal(rows); os.WriteFile(path, b, 0600) }
	save()
	d, e := LoadManifest(path)
	if e != nil || len(d.Images) != 1 || d.Duplicates != 1 {
		t.Fatalf("%+v %v", d, e)
	}
	rows[1].SHA256 = "wrong"
	save()
	if _, e = LoadManifest(path); e == nil {
		t.Fatal("hash mismatch accepted")
	}
	rows[1] = rows[0]
	save()
	if _, e = LoadManifest(path); e == nil {
		t.Fatal("duplicate ID accepted")
	}
	rows[1].ID = "../escape"
	save()
	if _, e = LoadManifest(path); e == nil {
		t.Fatal("unsafe ID accepted")
	}
}
func readySamples() []Sample {
	s := make([]Sample, 20)
	for i := range s {
		s[i] = Sample{PipelineMS: 19000, Diagnostics: directmusic.Diagnostics{FinalCount: 10, Attempted: 11, Verified: 10, Unresolved: 1, UniqueArtists: 10, SameArtistMax: 1, GeminiMS: 10000, ResolverMS: 9000, Calls: directmusic.CallStats{SearchCalls: 11, VideosCalls: 11}}}
	}
	return s
}
func TestAggregatePromotion(t *testing.T) {
	s := readySamples()
	a := Summarize(s, Safety{})
	if *a.CompleteRate != 1 || *a.AvgFinal != 10 || *a.CallsPerTrack != 2.2 || a.SearchCalls != 220 {
		t.Fatalf("bad totals %+v", a)
	}
	if d := Evaluate(a, 22, true, DefaultGates()); d.Status != "READY_FOR_PRIMARY" {
		t.Fatal(d)
	}
	if d := Evaluate(Summarize(s[:19], Safety{}), 22, true, DefaultGates()); d.Status != "NEEDS_MORE_TECHNICAL_VALIDATION" {
		t.Fatal(d)
	}
	if d := Evaluate(a, 22, false, DefaultGates()); d.Status != "NEEDS_MORE_TECHNICAL_VALIDATION" {
		t.Fatal(d)
	}
	s[0].Diagnostics.FinalCount = 7
	s[1].Diagnostics.FinalCount = 7
	if d := Evaluate(Summarize(s, Safety{}), 22, true, DefaultGates()); d.Status != "NEEDS_MORE_TECHNICAL_VALIDATION" {
		t.Fatal(d)
	}
	for _, safety := range []Safety{{FalsePositives: 1}, {Broad: 1}, {Substitute: 1}, {LastFM: 1}, {APIValid: 1}, {Bounds: 1}, {Diversity: 1}} {
		if d := Evaluate(Summarize(readySamples(), safety), 22, true, DefaultGates()); d.Status != "BLOCKED" {
			t.Fatal(d)
		}
	}
	empty := Summarize(nil, Safety{})
	if empty.P50 != nil || empty.CompleteRate != nil {
		t.Fatal("missing measurements became zero")
	}
	if Evaluate(empty, 22, true, DefaultGates()).Status != "NEEDS_MORE_TECHNICAL_VALIDATION" {
		t.Fatal("empty promoted")
	}
	b, e := json.Marshal(empty)
	if e != nil || !json.Valid(b) {
		t.Fatal(e)
	}
	values := []float64{3, 1, 2}
	if *Quantile(values, .5) != 2 || values[0] != 3 {
		t.Fatal("quantile mutated input")
	}
}
func TestAdversarialAndRuntimeInvariants(t *testing.T) {
	rows, s := RunFixtures()
	if len(rows) < 24 || s.FixtureFailures != 0 {
		t.Fatalf("fixtures %+v safety %+v", rows, s)
	}
	tr := model.DirectTrack{Artist: "A", Title: "X"}
	video := model.YouTubeVideo{VideoID: "real-provider-id"}
	rr := directmusic.Resolution{Candidate: tr, Status: "RESOLVED_STRONG", Video: &video, Searches: []directmusic.SearchAttempt{{Kind: "primary", Query: directmusic.IdentityPrimaryQuery(tr), Calls: 1}}}
	r := &directmusic.Result{Normalized: []model.DirectTrack{tr}, Resolutions: []directmusic.Resolution{rr}, Final: []directmusic.VerifiedTrack{{Gemini: tr, VideoID: video.VideoID}}}
	if a := AuditResult(r); a != (Safety{}) {
		t.Fatal(a)
	}
	r.Final = append(r.Final, r.Final[0], r.Final[0])
	if AuditResult(r).Diversity != 1 {
		t.Fatal("artist cap missed")
	}
	r.Final = r.Final[:1]
	r.Resolutions[0].Searches[0].Query = "calm indie music"
	if AuditResult(r).Broad != 1 {
		t.Fatal("broad query missed")
	}
	r.Resolutions[0].Status = "API_FAILURE"
	a := AuditResult(r)
	if a.APIValid != 1 || a.Substitute != 1 {
		t.Fatal(a)
	}
	r.Resolutions[0] = rr
	r.Resolutions[0].Searches = []directmusic.SearchAttempt{{Kind: "fallback", Query: directmusic.IdentityTokenQuery(tr), Calls: 1}}
	if AuditResult(r).Bounds != 1 {
		t.Fatal("fallback without primary accepted")
	}
	r.Final[0].Gemini.Title = "Substitute"
	if AuditResult(r).Substitute != 1 {
		t.Fatal("substitute missed")
	}
}
