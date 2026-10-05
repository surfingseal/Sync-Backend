package lastfmeval

import (
	"context"
	"encoding/json"
	"example.com/sync/internal/lastfm"
	"example.com/sync/internal/music"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func frozenState(t *testing.T) (music.CanonicalCatalog, AutomaticRegistry, *Vocabulary) {
	t.Helper()
	c := music.CanonicalTaxonomy()
	r, e := LoadAutomaticRegistry("../../internal/music/lastfm_retrieval_registry.json", c)
	if e != nil {
		t.Fatal(e)
	}
	v, e := LoadVocabulary("../../internal/music/lastfm_tag_registry.json", "../../internal/music/lastfm_tag_mapping.json")
	if e != nil {
		t.Fatal(e)
	}
	return c, r, v
}
func reasonHas(g GenreResolution, reason ResolutionReason) bool {
	for _, r := range g.ReasonCodes {
		if r == reason {
			return true
		}
	}
	return false
}
func TestStructuredGenreResolution(t *testing.T) {
	c, r, _ := frozenState(t)
	for _, x := range []struct {
		id, status string
		reason     ResolutionReason
	}{{"chillwave", "exact", ReasonEligibleExact}, {"synthwave", "family_fallback", ReasonEligibleFamily}, {"city-pop", "unsupported", ReasonInsufficientReach}, {"k-indie", "unsupported", ReasonNoFamily}, {"other", "sentinel", ReasonOther}, {"unknown", "sentinel", ReasonUnknown}} {
		g := r.ResolveGenre(c, x.id, false)
		if g.Status != x.status || !reasonHas(g, x.reason) {
			t.Fatalf("%s: %+v", x.id, g)
		}
	}
	// Explicit orthographic alias selection; only the existing Hip-Hop/hip hop group.
	for i, g := range r.Genres {
		if g.Canonical == "hip-hop" {
			for j, tag := range g.Tags {
				if tag.MappingType == "exact" {
					tag.Status = "insufficient_evidence"
					g.Tags[j] = tag
				}
			}
			r.Genres[i] = g
		}
	}
	gr := r.ResolveGenre(c, "hip-hop", false)
	if gr.Status != "alias" || !reasonHas(gr, ReasonEligibleAlias) || gr.Provider.WeightFactor != .95 {
		t.Fatal(gr)
	}
}
func TestNightCoverageBudgetAndNoNormalization(t *testing.T) {
	c, r, v := frozenState(t)
	a, _, e := ReadAnalysis("testdata/live-v3-single-analysis.json")
	if e != nil {
		t.Fatal(e)
	}
	profile, e := Profile(a)
	if e != nil {
		t.Fatal(e)
	}
	plan, e := BuildAutomaticPlan(profile, v, DefaultPolicy(), c, r, false)
	if e != nil {
		t.Fatal(e)
	}
	cov := plan.Coverage
	if cov.State != CoveragePartial || cov.Unsupported != 2 || cov.SupportedExact != 1 || cov.WeightedCoverage == nil || math.Abs(*cov.WeightedCoverage-.77/2.45) > 1e-12 {
		t.Fatalf("coverage %+v", cov)
	}
	if cov.WeightSemantics != "model_relevance_not_probability" {
		t.Fatal(cov.WeightSemantics)
	}
	if plan.Routes[0].LastFMTag != "chillwave" || plan.Routes[0].ProfileWeight != .77 {
		t.Fatal("weight normalized or route substituted", plan.Routes)
	}
	profile.Genres = append(profile.Genres, WeightedConcept{"electronic", .5, "Gemini model-reported relevance"}, WeightedConcept{"rock", .4, "Gemini model-reported relevance"})
	plan, e = BuildAutomaticPlan(profile, v, DefaultPolicy(), c, r, false)
	if e != nil {
		t.Fatal(e)
	}
	if plan.Coverage.UsableGenreRoutes != 3 {
		t.Fatal("unsupported consumed budget", plan.Routes)
	}
	for _, route := range plan.Routes {
		if route.LastFMTag == "city pop" || route.LastFMTag == "k-indie" {
			t.Fatal("unsupported fetched")
		}
	}
}
func TestInsufficientAndMoodOnlyPolicy(t *testing.T) {
	c, r, v := frozenState(t)
	p := MusicRetrievalProfile{Genres: []WeightedConcept{{"city-pop", .86, "model"}, {"k-indie", .82, "model"}}, Moods: []WeightedConcept{{"dreamy", 1, "model"}}}
	plan, e := BuildAutomaticPlan(p, v, DefaultPolicy(), c, r, false)
	if e != nil {
		t.Fatal(e)
	}
	if len(plan.Routes) != 0 || plan.Coverage.State != CoverageInsufficient {
		t.Fatal(plan)
	}
	f := &CapturedRetriever{Tracks: map[string][]lastfm.Track{}}
	eval, e := Evaluate(context.Background(), plan, DefaultPolicy(), f)
	if e != nil || eval.CoverageState != CoverageInsufficient || eval.APICalls != 0 {
		t.Fatal(eval, e)
	}
	policy := DefaultPolicy()
	policy.MoodOnly = true
	plan, e = BuildAutomaticPlan(p, v, policy, c, r, false)
	if e != nil || len(plan.Routes) != 1 || plan.Routes[0].Category != "mood" || plan.Coverage.State != CoverageInsufficient {
		t.Fatal("explicit mood-only policy changed", plan, e)
	}
	p = MusicRetrievalProfile{Genres: []WeightedConcept{{"other", 1, "legacy uniform"}, {"unknown", 0, "legacy uniform"}}}
	plan, e = BuildAutomaticPlan(p, v, DefaultPolicy(), c, r, false)
	if e != nil || plan.Coverage.Sentinels != 2 || plan.Coverage.WeightedCoverage != nil || len(plan.Routes) != 0 {
		t.Fatal("sentinel/zero denominator", plan, e)
	}
}
func TestLegacyCoverageAndCandidateProvenance(t *testing.T) {
	c, r, v := frozenState(t)
	profile := MusicRetrievalProfile{Genres: []WeightedConcept{{"chillwave", .5, "legacy uniform weight"}, {"synthwave", .5, "legacy uniform weight"}}}
	plan, e := BuildAutomaticPlan(profile, v, DefaultPolicy(), c, r, false)
	if e != nil {
		t.Fatal(e)
	}
	if plan.Coverage.State != CoverageFull || plan.Coverage.WeightSemantics != "legacy_uniform_diagnostic" {
		t.Fatal(plan.Coverage)
	}
	policy := DefaultPolicy()
	policy.PerRouteLimit = 1
	one := track("Artist", "Title", "same")
	captured := &CapturedRetriever{Tracks: map[string][]lastfm.Track{"chillwave": {one}, "electronic": {one}}}
	eval, e := Evaluate(context.Background(), plan, policy, captured)
	if e != nil {
		t.Fatal(e)
	}
	if len(eval.Merged) != 1 {
		t.Fatal("dedupe changed")
	}
	cand := eval.Merged[0]
	prov := cand.Retrieval
	if prov.RouteCount != 2 || prov.ExactRouteCount != 1 || prov.FallbackRouteCount != 1 || prov.MoodRouteCount != 0 {
		t.Fatal(prov)
	}
	for _, route := range prov.Routes {
		if route.ResolutionType == "family_fallback" && (*route.MappingFactor != .65 || route.ProfileWeight != .5 || route.EffectiveWeight != .325) {
			t.Fatal(route)
		}
	}
	old, _, _ := Merge(eval.Raw, policy)
	if cand.RetrievalScore != old[0].RetrievalScore || !reflect.DeepEqual(cand.Contributions, old[0].Contributions) {
		t.Fatal("metadata join changed scoring")
	}
}
func TestFixtureDedupeAggregationAndDeterminism(t *testing.T) {
	c, r, v := frozenState(t)
	dir := t.TempDir()
	original, e := os.ReadFile("testdata/live-v3-single-analysis.json")
	if e != nil {
		t.Fatal(e)
	}
	for _, sub := range []string{"a", "b"} {
		if e = os.Mkdir(filepath.Join(dir, sub), 0700); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, sub, "analysis.json"), original, 0600); e != nil {
			t.Fatal(e)
		}
	}
	first, e := AuditFixtureCoverage(dir, c, r, v, DefaultPolicy())
	if e != nil {
		t.Fatal(e)
	}
	second, e := AuditFixtureCoverage(dir, c, r, v, DefaultPolicy())
	if e != nil {
		t.Fatal(e)
	}
	x, _ := json.Marshal(first)
	y, _ := json.Marshal(second)
	if !reflect.DeepEqual(x, y) || len(first.UniqueAnalyses) != 1 || len(first.UniqueAnalyses[0].SourcePaths) != 2 {
		t.Fatal("dedup/determinism")
	}
	if len(first.Observations) != 2 {
		t.Fatal(first.Observations)
	}
	for _, o := range first.Observations {
		if o.Count != 1 || o.UniqueAnalyses != 1 || o.AverageWeight != o.TotalWeight || !reasonHas(GenreResolution{ReasonCodes: o.Reasons}, ReasonInsufficientReach) {
			t.Fatal(o)
		}
	}
}

func TestSavedStillLifeRankingStability(t *testing.T) {
	c, r, v := frozenState(t)
	a, _, e := ReadAnalysis("testdata/e2e-analysis.json")
	if e != nil {
		t.Fatal(e)
	}
	profile, e := Profile(a)
	if e != nil {
		t.Fatal(e)
	}
	policy := DefaultPolicy()
	policy.PerRouteLimit = 20
	plan, e := BuildAutomaticPlan(profile, v, policy, c, r, false)
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile("testdata/lastfm-final-evidence-batch-20261004T144123Z/provider-observations.json")
	if e != nil {
		t.Fatal(e)
	}
	var pool []TagEvidence
	if e = json.Unmarshal(b, &pool); e != nil {
		t.Fatal(e)
	}
	captured := &CapturedRetriever{Tracks: map[string][]lastfm.Track{}}
	for _, t := range pool {
		if t.TracksSuccess {
			captured.Tracks[lastfm.Normalize(t.Tag)] = t.Tracks
		}
	}
	got, e := Evaluate(context.Background(), plan, policy, captured)
	if e != nil {
		t.Fatal(e)
	}
	b, e = os.ReadFile("testdata/lastfm-final-evidence-batch-20261004T144123Z/regression-after.json")
	if e != nil {
		t.Fatal(e)
	}
	var prior Evaluation
	if e = json.Unmarshal(b, &prior); e != nil {
		t.Fatal(e)
	}
	if got.CoverageState != CoverageFull || got.APICalls != 0 || !reflect.DeepEqual(got.Plan.Routes, prior.Plan.Routes) || !reflect.DeepEqual(got.Raw, prior.Raw) || !reflect.DeepEqual(got.After, prior.After) || got.OverlapCount != prior.OverlapCount {
		t.Fatal("saved still-life regression changed")
	}
	for i := range got.Merged {
		a, b := got.Merged[i], prior.Merged[i]
		a.Retrieval = nil
		b.Retrieval = nil
		if !reflect.DeepEqual(a, b) {
			t.Fatal("score/order/dedupe changed", i)
		}
	}
	for i := range got.Reranked {
		a, b := got.Reranked[i], prior.Reranked[i]
		a.Retrieval = nil
		b.Retrieval = nil
		if !reflect.DeepEqual(a, b) {
			t.Fatal("diversity order changed", i)
		}
	}
}
