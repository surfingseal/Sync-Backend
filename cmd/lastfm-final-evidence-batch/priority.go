package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"example.com/sync/internal/lastfm"
	"example.com/sync/internal/lastfmeval"
	"example.com/sync/internal/lastfmmapping"
	"example.com/sync/internal/music"
)

type Usage struct {
	Counts         map[string]int   `json:"canonical_occurrences_in_unique_saved_analyses"`
	Files          []map[string]any `json:"sources"`
	UniqueAnalyses int              `json:"unique_analysis_count"`
	Note           string           `json:"limitations"`
}

func savedUsage(c music.CanonicalCatalog) (Usage, error) {
	u := Usage{Counts: map[string]int{}, Files: []map[string]any{}, Note: "Saved experiments only, not production telemetry. Identical decoded analysis snapshots deduplicated; repeated experiments are not independent usage."}
	seen := map[[32]byte]bool{}
	err := filepath.WalkDir("../benchmark-results", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || (d.Name() != "analyze.json" && d.Name() != "analysis.json") {
			return nil
		}
		a, _, e := lastfmeval.ReadAnalysis(path)
		if e != nil {
			u.Files = append(u.Files, map[string]any{"path": path, "accepted": false, "reason": "not a valid saved ImageAnalysis"})
			return nil
		}
		b, _ := json.Marshal(a)
		h := sha256.Sum256(b)
		duplicate := seen[h]
		u.Files = append(u.Files, map[string]any{"path": path, "accepted": true, "duplicate_snapshot": duplicate, "sha256": fmt.Sprintf("%x", h)})
		if duplicate {
			return nil
		}
		seen[h] = true
		u.UniqueAnalyses++
		p, e := lastfmeval.Profile(a)
		if e != nil {
			return e
		}
		present := map[string]bool{}
		for _, g := range p.Genres {
			if canonical, ok := c.Resolve(g.Name); ok && canonical.Family != "other" && !present[canonical.ID] {
				u.Counts[canonical.ID]++
				present[canonical.ID] = true
			}
		}
		return nil
	})
	return u, err
}

type Priority struct {
	Tag               string                  `json:"provider_tag"`
	Canonicals        []string                `json:"canonical_ids"`
	FamilyPrimaries   []string                `json:"family_primaries"`
	CurrentStatus     string                  `json:"retrieval_status"`
	SampleRequested   int                     `json:"sample_requested"`
	SampleReceived    int                     `json:"sample_received"`
	HasInfo           bool                    `json:"has_successful_info_and_reach"`
	Reach             *lastfm.Number          `json:"reach"`
	Recoverable       []string                `json:"counterfactual_unmapped_recovery"`
	FamilyRecoverable int                     `json:"counterfactual_new_family_fallback_count"`
	ExactUpgrade      int                     `json:"counterfactual_family_to_equivalent_count"`
	Usage             int                     `json:"unique_saved_analysis_frequency"`
	ExpectedCalls     int                     `json:"expected_api_calls"`
	Need              lastfmeval.EvidenceNeed `json:"missing_evidence"`
	Score             int                     `json:"priority_score"`
	Selectable        bool                    `json:"selectable"`
	Reason            string                  `json:"selection_or_exclusion_reason"`
}

// Counterfactual provider records are resolver-test scaffolding only, never API
// evidence and never persisted as live observations. Current family graph stays fixed.
func hypothetical(t lastfmeval.TagEvidence) lastfmeval.TagEvidence {
	n := lastfm.Number(5000)
	t.Info = &lastfm.TagInfo{Reach: &n}
	t.InfoSuccess = true
	t.TracksSuccess = true
	t.SampleRequested = 20
	t.Tracks = nil
	for i := 0; i < 20; i++ {
		track := lastfm.Track{Name: fmt.Sprintf("counterfactual-%d", i)}
		track.Artist.Name = fmt.Sprintf("counterfactual-artist-%d", i)
		t.Tracks = append(t.Tracks, track)
	}
	return t
}
func priorityRanking(c music.CanonicalCatalog, legacy lastfmmapping.Registry, r lastfmeval.AutomaticRegistry, pool []lastfmeval.TagEvidence, usage map[string]int) []Priority {
	out := []Priority{}
	tagMaps := map[string][]string{}
	primaries := map[string][]string{}
	for _, g := range r.Genres {
		for _, t := range g.Tags {
			tagMaps[lastfm.Normalize(t.Tag)] = append(tagMaps[lastfm.Normalize(t.Tag)], g.Canonical)
		}
	}
	for _, f := range r.Families {
		if f.Primary != nil {
			primaries[lastfm.Normalize(f.Primary.Tag)] = append(primaries[lastfm.Normalize(f.Primary.Tag)], f.Family)
		}
	}
	for i, t := range pool {
		key := lastfm.Normalize(t.Tag)
		ids := tagMaps[key]
		if len(ids) == 0 {
			continue
		}
		d := lastfmeval.DecideEligibility(t)
		if d.Status != "insufficient_evidence" {
			continue
		}
		p := Priority{Tag: t.Tag, Canonicals: ids, FamilyPrimaries: primaries[key], CurrentStatus: d.Status, SampleRequested: t.SampleRequested, SampleReceived: len(t.Tracks), HasInfo: t.InfoSuccess && t.Info != nil && t.Info.Reach != nil, Recoverable: []string{}}
		if t.Info != nil {
			p.Reach = t.Info.Reach
		}
		for _, id := range ids {
			p.Usage += usage[id]
		}
		p.Need = lastfmeval.EvidenceNeed{Tag: t.Tag, NeedInfo: !p.HasInfo, NeedTracks: !t.TracksSuccess || t.SampleRequested < 20, Reason: fmt.Sprint(d.Reasons)}
		if p.Need.NeedInfo {
			p.ExpectedCalls++
		}
		if p.Need.NeedTracks {
			p.ExpectedCalls++
		}
		p.Need.ExpectedCalls = p.ExpectedCalls
		copyPool := append([]lastfmeval.TagEvidence{}, pool...)
		copyPool[i] = hypothetical(t)
		next := lastfmeval.BuildAutomaticRegistry(c, legacy, copyPool)
		for _, g := range c.Genres {
			if g.Family == "other" {
				continue
			}
			b, a := r.Resolve(c, g.ID, false), next.Resolve(c, g.ID, false)
			if b.ResolutionType == "unmapped" && a.ResolutionType != "unmapped" {
				p.Recoverable = append(p.Recoverable, g.ID)
				if a.ResolutionType == "family_fallback" {
					p.FamilyRecoverable++
				}
			}
			if b.ResolutionType == "family_fallback" && (a.ResolutionType == "exact" || a.ResolutionType == "alias") {
				p.ExactUpgrade++
			}
		}
		p.Score = 100*p.FamilyRecoverable + 10*len(p.Recoverable) + 50*p.Usage + p.ExactUpgrade
		if p.HasInfo {
			p.Score += 5
		}
		p.Selectable = p.ExpectedCalls > 0 && (len(p.FamilyPrimaries) > 0 || p.Usage > 0 || len(p.Recoverable) > 0)
		p.Reason = "not selected: lower priority in bounded batch"
		if p.HasInfo && *p.Reach < 5000 {
			p.Selectable = false
			p.Reason = "known reach cannot meet eligible threshold; no coverage-restoration probe"
		}
		if p.ExpectedCalls == 0 {
			p.Selectable = false
			p.Reason = "sufficient-size sample already collected; no retry to chase eligibility"
		}
		if len(p.FamilyPrimaries) == 0 && p.Usage == 0 && len(p.Recoverable) == 0 {
			p.Reason = "already covered family, no observed usage or unmapped recovery"
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Selectable != b.Selectable {
			return a.Selectable
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.ExpectedCalls != b.ExpectedCalls {
			return a.ExpectedCalls < b.ExpectedCalls
		}
		return a.Tag < b.Tag
	})
	return out
}
func selectBatch(r []Priority, limit, cap int) []lastfmeval.EvidenceNeed {
	out := []lastfmeval.EvidenceNeed{}
	calls := 0
	covered := map[string]bool{}
	for i := range r {
		p := &r[i]
		if !p.Selectable || len(out) >= limit || calls+p.ExpectedCalls > cap {
			continue
		}
		overlap := false
		for _, id := range p.Canonicals {
			if covered[id] {
				overlap = true
			}
		}
		if overlap && len(p.FamilyPrimaries) == 0 {
			p.Reason = "equivalent route already probed in this batch; no spelling expansion"
			continue
		}
		p.Reason = fmt.Sprintf("selected: family recovery=%d, unmapped recovery=%d, saved usage=%d, info reuse=%t, score=%d", p.FamilyRecoverable, len(p.Recoverable), p.Usage, p.HasInfo, p.Score)
		p.Need.Reason = p.Reason
		out = append(out, p.Need)
		calls += p.ExpectedCalls
		for _, id := range p.Canonicals {
			covered[id] = true
		}
	}
	return out
}
