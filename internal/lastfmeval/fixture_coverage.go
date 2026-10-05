package lastfmeval

import (
	"crypto/sha256"
	"encoding/json"
	"example.com/sync/internal/music"
	"os"
	"path/filepath"
	"sort"
)

type FixtureCoverage struct {
	AnalysisID  string   `json:"analysis_id"`
	SourcePaths []string `json:"source_paths"`
	Plan        Plan     `json:"plan"`
}
type UnsupportedGenreObservation struct {
	CanonicalGenre     string             `json:"canonical_genre"`
	Family             string             `json:"family"`
	Count              int                `json:"occurrence_count"`
	UniqueAnalyses     int                `json:"unique_analysis_count"`
	TotalWeight        float64            `json:"total_weight"`
	AverageWeight      float64            `json:"average_weight"`
	Reasons            []ResolutionReason `json:"reason_codes"`
	ExampleAnalysisIDs []string           `json:"example_analysis_ids"`
}
type FixtureCoverageReport struct {
	Scope                      string                        `json:"scope"`
	Files                      []map[string]any              `json:"files"`
	UniqueAnalyses             []FixtureCoverage             `json:"unique_analyses"`
	Observations               []UnsupportedGenreObservation `json:"unsupported_observations"`
	TotalGenreOccurrences      int                           `json:"total_genre_occurrences"`
	SupportedGenreOccurrences  int                           `json:"supported_genre_occurrences"`
	CountCoverage              *float64                      `json:"count_coverage"`
	CombinedWeightedDiagnostic *float64                      `json:"combined_weighted_diagnostic"`
	ModelRelevanceCoverage     *float64                      `json:"model_relevance_only_coverage"`
	Note                       string                        `json:"note"`
}

func AggregateUnsupported(fixtures []FixtureCoverage) []UnsupportedGenreObservation {
	type accumulator struct {
		ob      UnsupportedGenreObservation
		ids     map[string]bool
		reasons map[ResolutionReason]bool
	}
	all := map[string]*accumulator{}
	for _, f := range fixtures {
		if f.Plan.Coverage == nil {
			continue
		}
		for _, g := range f.Plan.Coverage.Resolutions {
			if g.Status != ResolutionUnsupported || g.Canonical == nil {
				continue
			}
			key := g.Canonical.ID
			a := all[key]
			if a == nil {
				a = &accumulator{ob: UnsupportedGenreObservation{CanonicalGenre: key, Family: g.Canonical.Family}, ids: map[string]bool{}, reasons: map[ResolutionReason]bool{}}
				all[key] = a
			}
			a.ob.Count++
			a.ob.TotalWeight += g.Input.Weight
			a.ids[f.AnalysisID] = true
			for _, reason := range g.ReasonCodes {
				a.reasons[reason] = true
			}
		}
	}
	keys := []string{}
	for key := range all {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := []UnsupportedGenreObservation{}
	for _, key := range keys {
		a := all[key]
		a.ob.UniqueAnalyses = len(a.ids)
		a.ob.AverageWeight = a.ob.TotalWeight / float64(a.ob.Count)
		for id := range a.ids {
			a.ob.ExampleAnalysisIDs = append(a.ob.ExampleAnalysisIDs, id)
		}
		sort.Strings(a.ob.ExampleAnalysisIDs)
		if len(a.ob.ExampleAnalysisIDs) > 3 {
			a.ob.ExampleAnalysisIDs = a.ob.ExampleAnalysisIDs[:3]
		}
		for reason := range a.reasons {
			a.ob.Reasons = append(a.ob.Reasons, reason)
		}
		sort.Slice(a.ob.Reasons, func(i, j int) bool { return a.ob.Reasons[i] < a.ob.Reasons[j] })
		out = append(out, a.ob)
	}
	return out
}

// No provider client is accepted here: this command cannot spend API quota.
func AuditFixtureCoverage(root string, c music.CanonicalCatalog, r AutomaticRegistry, v *Vocabulary, p Policy) (FixtureCoverageReport, error) {
	report := FixtureCoverageReport{Scope: "offline fixture coverage", Files: []map[string]any{}, UniqueAnalyses: []FixtureCoverage{}, Note: "Decoded analysis content deduplicated. Not production telemetry or usage-frequency estimates. Mixed legacy/model combined weight is diagnostic only. No images or personal data copied."}
	seen := map[string]int{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || (d.Name() != "analyze.json" && d.Name() != "analysis.json") {
			return nil
		}
		a, _, e := ReadAnalysis(path)
		if e != nil {
			report.Files = append(report.Files, map[string]any{"path": path, "accepted": false, "reason": "not a valid ImageAnalysis snapshot"})
			return nil
		}
		b, _ := json.Marshal(a)
		hash := sha256.Sum256(b)
		id := stringHex(hash[:])
		index, duplicate := seen[id]
		report.Files = append(report.Files, map[string]any{"path": path, "analysis_id": id, "accepted": true, "duplicate": duplicate})
		if duplicate {
			report.UniqueAnalyses[index].SourcePaths = append(report.UniqueAnalyses[index].SourcePaths, path)
			return nil
		}
		profile, e := Profile(a)
		if e != nil {
			return e
		}
		plan, e := BuildAutomaticPlan(profile, v, p, c, r, false)
		if e != nil {
			return e
		}
		seen[id] = len(report.UniqueAnalyses)
		report.UniqueAnalyses = append(report.UniqueAnalyses, FixtureCoverage{id, []string{path}, plan})
		return nil
	})
	if err != nil {
		return report, err
	}
	sort.Slice(report.UniqueAnalyses, func(i, j int) bool { return report.UniqueAnalyses[i].AnalysisID < report.UniqueAnalyses[j].AnalysisID })
	supported, total, modelSupported, modelTotal := 0.0, 0.0, 0.0, 0.0
	for _, f := range report.UniqueAnalyses {
		cov := f.Plan.Coverage
		report.TotalGenreOccurrences += cov.TotalGenreInputs
		report.SupportedGenreOccurrences += cov.SupportedExact + cov.SupportedAlias + cov.SupportedFallback
		supported += cov.SupportedWeight
		total += cov.TotalWeight
		if cov.WeightSemantics == "model_relevance_not_probability" {
			modelSupported += cov.SupportedWeight
			modelTotal += cov.TotalWeight
		}
	}
	report.CountCoverage = ratio(float64(report.SupportedGenreOccurrences), float64(report.TotalGenreOccurrences))
	report.CombinedWeightedDiagnostic = ratio(supported, total)
	report.ModelRelevanceCoverage = ratio(modelSupported, modelTotal)
	report.Observations = AggregateUnsupported(report.UniqueAnalyses)
	return report, nil
}
func stringHex(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 2*len(b))
	for i, n := range b {
		out[2*i] = hex[n>>4]
		out[2*i+1] = hex[n&15]
	}
	return string(out)
}
