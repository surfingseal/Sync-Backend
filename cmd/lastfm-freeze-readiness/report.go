package main

import (
	"example.com/sync/internal/lastfmeval"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func signalInventory() map[string]any {
	return map[string]any{
		"available":   []string{"artist/title/provider URL (source metadata)", "optional MBID; provider records and normalized pair/MBID dedupe", "canonical genre/family route sources", "exact/alias/family_fallback/mood distinction", "source profile weight and mapping factor", "effective route weight", "provider rank and existing rank decay", "distinct-provider route overlap", "unchanged RetrievalScore and overlap bonus", "existing artist diversity and raw/diversity ranks"},
		"unavailable": []string{"track.getInfo listeners", "track.getInfo playcount", "full track tags", "track.getSimilar", "MusicBrainz metadata", "YouTube video trust", "audio features"},
		"limitations": []string{"model relevance is not confidence/probability", "Last.fm tag rank is not independent popularity evidence", "family fallback is not exact genre synonym", "provider metadata is not verified audio mood", "unclassified legacy direct-plan provenance must not be invented", "no enrichment or new ranking implementation"},
	}
}
func percent(p *float64) string {
	if p == nil {
		return "undefined (zero denominator)"
	}
	return fmt.Sprintf("%.2f%%", 100**p)
}
func writeSummary(dir string, metadata map[string]any, counts map[string]int, unsupported []lastfmeval.GenreResolution, fixture lastfmeval.FixtureCoverageReport, night lastfmeval.Plan, still *lastfmeval.Evaluation, ready string) error {
	var s strings.Builder
	fmt.Fprintf(&s, "# Last.fm baseline freeze and ranking readiness\n\n**%s**\n\nThis freeze is a reproducible experiment baseline, not a claim of full product support. Mapping expansion stops here; unsupported inputs stay explicit. No policy, taxonomy, family graph, mapping factor, ranking/diversity, Gemini, public /recommend, OAuth or playlist change. External API calls: 0.\n\n## Frozen identity\n\nPolicy version: %v; registry version: %v.\n\nRegistry SHA256: `%s`\n\nCanonical taxonomy SHA256: `%s`\n\nEvidence: `%s`\n\n## Coverage\n\n%v (sentinels excluded from the 59 music-genre denominator).\n\nUnsupported:\n", ready, metadata["policy_version"], metadata["registry_version"], metadata["registry_sha256"], metadata["canonical_taxonomy_sha256"], metadata["evidence_snapshot"], counts)
	for _, g := range unsupported {
		fmt.Fprintf(&s, "- %s (%s): %v\n", g.Canonical.ID, g.Canonical.Family, g.ReasonCodes)
	}
	fmt.Fprintf(&s, "\n## Offline fixture coverage\n\n%d files, %d unique decoded analysis snapshots. Scope: offline fixture coverage, not production usage or representative population. Supported occurrences %d/%d = %s. Combined legacy/model weighted diagnostic: %s. Model-relevance-only weighted coverage: %s (not calibrated confidence).\n\n", len(fixture.Files), len(fixture.UniqueAnalyses), fixture.SupportedGenreOccurrences, fixture.TotalGenreOccurrences, percent(fixture.CountCoverage), percent(fixture.CombinedWeightedDiagnostic), percent(fixture.ModelRelevanceCoverage))
	for _, f := range fixture.UniqueAnalyses {
		fmt.Fprintf(&s, "- Analysis `%s`: %s; supported weight %.3f / %.3f = %s; semantics=%s; routes=%v\n", f.AnalysisID[:12], f.Plan.Coverage.State, f.Plan.Coverage.SupportedWeight, f.Plan.Coverage.TotalWeight, percent(f.Plan.Coverage.WeightedCoverage), f.Plan.Coverage.WeightSemantics, f.Plan.Routes)
	}
	fmt.Fprint(&s, "\nUnsupported observations (image/user data not copied):\n\n")
	for _, o := range fixture.Observations {
		fmt.Fprintf(&s, "- %s: occurrences=%d; unique analyses=%d; total weight=%.3f; average=%.3f; reasons=%v\n", o.CanonicalGenre, o.Count, o.UniqueAnalyses, o.TotalWeight, o.AverageWeight, o.Reasons)
	}
	if night.Coverage != nil {
		fmt.Fprintf(&s, "\n## Saved night-city regression\n\nState=%s; weighted genre coverage=%s. Unsupported city-pop/k-indie skipped; eligible chillwave retains weight .77. No renormalization.\n\n", night.Coverage.State, percent(night.Coverage.WeightedCoverage))
		for _, g := range night.Coverage.Resolutions {
			fmt.Fprintf(&s, "- %s %.2f → %s (%v)\n", g.Input.Name, g.Input.Weight, g.Status, g.ReasonCodes)
		}
		fmt.Fprintf(&s, "\nSelected routes: %v. Unsupported does not consume route slots. Existing mood policy preserved. Coverage measures eligibility of all inputs, not number of routes selected by the budget.\n", night.Routes)
	}
	fmt.Fprintf(&s, "\n## Still-life regression\n\nOriginal route weights, raw records, dedupe, score, order and diversity are unchanged. Metadata was added only. FULL coverage; routes=indie folk / bossa nova / chamber pop / dreamy. Raw=%d, unique=%d, artists=%d, overlap=%d, Top1=%.1f%%, Top3=%.1f%%; captured local replay=%.3f ms. Not fresh provider retrieval.\n", len(still.Raw), len(still.Merged), still.After.UniqueArtists, still.OverlapCount, 100*still.After.Top1Share, 100*still.After.Top3Share, still.TotalMS)
	fmt.Fprint(&s, "\n## Next milestone\n\nRanking/enrichment experiments may start on eligible routes. Inventory separates existing provider rank, profile/mapping provenance and artist diversity from unavailable popularity, track tags, audio features and resolver trust. No final ranker or enrichment calls added. Retain original weights and observe unsupported profile mass.\n\nNo automatic further mapping audit. Trigger only after repeated independent unsupported observations, repeated high unsupported weight, an explicit evidence need, or a planned policy version change. Known low reach is not solved by repetitive calls.\n")
	return os.WriteFile(filepath.Join(dir, "summary.md"), []byte(s.String()), 0644)
}

const modelNotes = `# Ranking candidate model (existing type extended)

CandidateTrack remains the same identity/score model: Artist, Title, optional MBID,
URL, normalized artist/title, ProviderRecords, Evidences, Contributions,
RetrievalScore, OverlapBonus, RawRank and DiversityRank.

Added Retrieval *CandidateRetrievalEvidence (retrieval_provenance) after existing
Merge/Score and before unchanged Diversity. It includes distinct provider routes,
best route rank, exact/alias/fallback/mood/unclassified route counts.

CandidateRouteEvidence includes the selected canonical concept, family, provider
Tag, resolution type, original profile weight, mapping factor, effective route
weight, best provider rank, existing rank decay, and all CanonicalRouteSource
records when multiple canonical concepts collapse into one provider request.

Exact 1.0 / alias .95 / family .65 stay unchanged. Mood factor remains the existing
policy value; mood is a separate route type. Route counts count distinct provider
tags, not multiple canonical sources. Unknown legacy provenance stays unclassified,
not fabricated as exact. The summary never changes existing score, weight or order.

No empty enrichment object or duplicate score wrapper added. Existing TrackEnricher
boundary can be used in a later explicitly scoped enrichment experiment. No
listeners/playcount, full tags, similarity, resolver trust or audio features exist
in this milestone. Raw Last.fm title/artist/MBID do not prove playable track identity.
`
const freezeNotes = `# Baseline freeze policy

This snapshot freezes policy v1 and existing explicit mappings for reproducible
recommendation experiments, not permanently immutable taxonomy or full product
coverage. Use the frozen-registry.json with the existing retrieval-eval
--provider-mapping option. Record snapshot hashes and dataset hashes for comparisons.

Unsupported inputs produce no provider route. Never create hidden semantic aliases,
new family relationships, arbitrary default tags, or automatic mood/YouTube fallback.
Sentinels other/unknown remain separately excluded. Partial profiles may proceed
with usable genre routes, original weights unchanged. Zero usable genre routes
produce INSUFFICIENT_ROUTES; existing explicitly enabled mood-only mode is preserved.
Coverage-state and transport partial failures are separate diagnostics.

Pause automatic provider auditing. Reopen only for repeated independent unsupported
observations or high unsupported-weight profiles, explicit new evidence need, or a
planned policy version change. Preserve provenance and scoped API budgets. This
milestone does not implement production telemetry; offline observations are aggregate
canonical/reason/weight/hash records, without images, tokens or private user metadata.
`
