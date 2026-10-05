package lastfmeval

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func WriteReport(dir string, input json.RawMessage, e *Evaluation, photoID string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	files := map[string]any{"input-analysis.json": input, "routes.json": e.Plan, "raw-candidates.json": e.Raw, "merged-candidates.json": e.Merged, "reranked-candidates.json": e.Reranked, "deferred-candidates.json": e.Deferred, "evaluation.json": e}
	for name, data := range files {
		b, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0644); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(Report(e, photoID)), 0644); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, "human-review.csv"))
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{"photo_id", "rank", "artist", "title", "routes", "retrieval_score", "genre_fit_1_5", "mood_fit_1_5", "would_listen_1_5", "song_quality_1_5", "playlist_fit_1_5", "notes", "lastfm_url"})
	for _, c := range e.Reranked {
		_ = w.Write([]string{photoID, strconv.Itoa(c.DiversityRank), c.Artist, c.Title, routeNames(c), fmt.Sprintf("%.6f", c.RetrievalScore), "", "", "", "", "", "", c.URL})
	}
	w.Flush()
	err = w.Error()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func routeNames(c CandidateTrack) string {
	seen := map[string]bool{}
	names := []string{}
	for _, e := range c.Evidences {
		if !seen[e.LastFMTag] {
			names = append(names, e.LastFMTag)
			seen[e.LastFMTag] = true
		}
	}
	return strings.Join(names, "; ")
}
func md(s string) string { return strings.NewReplacer("|", "/", "\n", " ", "\r", " ").Replace(s) }
func Report(e *Evaluation, photoID string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Last.fm Photo Candidate Retrieval Experiment\n\nPhoto ID: %s. Outcome: **%s**. Quality: **%s**. Partial: %t.\n\nExisting /api/v1/recommend, YouTube ranking and Gemini schema are unchanged. No new Gemini calls, resolver, playlist writes or track enrichment. Registry validation is based on the previous metadata audit; no audio quality certification.\n\n## 1. Input MusicRetrievalProfile\n\nTempo: %s; energy: %.3f; valence: %.3f.\n\n", md(photoID), e.Outcome, e.HumanQuality, e.Partial, e.Plan.Profile.Tempo, e.Plan.Profile.Energy, e.Plan.Profile.Valence)
	for _, group := range []struct {
		name     string
		concepts []WeightedConcept
	}{{"Genres", e.Plan.Profile.Genres}, {"Moods", e.Plan.Profile.Moods}} {
		fmt.Fprintf(&b, "- %s: ", group.name)
		for _, c := range group.concepts {
			fmt.Fprintf(&b, "%s %.3f (%s); ", md(c.Name), c.Weight, c.WeightSource)
		}
		fmt.Fprintln(&b)
	}
	for _, n := range e.Plan.Profile.Notes {
		fmt.Fprintf(&b, "- %s\n", n)
	}
	if e.Plan.TaxonomyResolution != nil {
		fmt.Fprint(&b, "\n## Sync canonical → provider genre resolution\n\n| Raw input | Canonical ID | Existing family | Resolution | Tag | Factor |\n|---|---|---|---|---|---:|\n")
		for _, g := range e.Plan.TaxonomyResolution.Genres {
			id, family := "unmapped", ""
			if g.Canonical != nil {
				id = g.Canonical.ID
				family = g.Canonical.Family
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %.2f |\n", md(g.Input.Name), id, family, g.Provider.ResolutionType, g.Provider.ProviderTag, g.Provider.WeightFactor)
		}
	}
	fmt.Fprint(&b, "\n## 2–3. Mapped / unmapped concepts\n\n| Input | Type | Weight | Tag | Status | Eligible | Reason |\n|---|---|---:|---|---|---|---|\n")
	for _, m := range append(append([]ConceptMapping{}, e.Plan.Mapped...), e.Plan.Unmapped...) {
		fmt.Fprintf(&b, "| %s | %s | %.3f | %s | %s | %t | %s |\n", md(m.Input.Name), m.InputType, m.Input.Weight, md(m.LastFMTag), m.Status, m.Allowed, md(m.Reason))
	}
	fmt.Fprintf(&b, "\n## 4–5. Selected routes / calls per route\n\nPolicy: genre > subgenre > style > mood, then descending input weight within category, stable input ties. Maximum %d genre/style, %d mood, %d total. Mood factor %.2f; auxiliary only unless explicit mood-only flag. Aliases are not expanded. Concurrency %d; per-route limit %d.\n\n| Tag | Concept | Category | Source weight | Weighted profile | Rows | Actual calls | Cache hits | Route elapsed ms | HTTP latency ms | Error |\n|---|---|---|---:|---:|---:|---:|---:|---:|---|---|\n", e.Policy.MaxGenreRoutes, e.Policy.MaxMoodRoutes, e.Policy.MaxRoutes, e.Policy.MoodFactor, e.Policy.Concurrency, e.Policy.PerRouteLimit)
	for _, r := range e.RouteResults {
		calls, hits := 0, 0
		lat := []string{}
		for _, v := range r.Events {
			if v.Cached {
				hits++
			} else if v.HTTPAttempted {
				calls++
			}
			lat = append(lat, fmt.Sprintf("%.1f", v.MS))
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %.3f | %.3f | %d | %d | %d | %.1f | %s | %s |\n", md(r.Route.LastFMTag), md(r.Route.CanonicalConcept), r.Route.Category, r.Route.SourceWeight, r.Route.ProfileWeight, r.RawCount, calls, hits, r.LatencyMS, strings.Join(lat, ", "), r.Error)
	}
	fmt.Fprintf(&b, "\n## 6–9. Merge / dedupe / overlap\n\nRaw rows: **%d**; usable unique: **%d**; duplicate merges: **%d**; invalid raw records: **%d**; distinct-route overlap candidates: **%d**. NFKC/case/whitespace and typographic quote/dash normalization; parentheses/feat/remaster/version labels retained. Same nonempty MBID is a strong identity signal; raw provider records retained for collision review. No missing-MBID rejection.\n\nRetrievalScore = min(1, max(route weight × 1/log2(rank+1)) + min(0.15, %.3f × (distinct routes−1))). Genre/subgenre/style factor 1.0; mood factor %.2f. Model scores are relevance signals, not probabilities. No popularity/listener data used. Same-route duplicate does not earn overlap bonus. Weights frozen before the live run.\n\n", len(e.Raw), len(e.Merged), e.DuplicateMerges, e.InvalidRaw, e.OverlapCount, e.Policy.OverlapBonus, e.Policy.MoodFactor)
	fmt.Fprint(&b, "## 10. Per-route artist concentration\n\nShares are row-based provider metadata diagnostics, not audio/artist identity normalization.\n\n| Route | Unique artists | Top1 share | Top3 share | Max tracks/artist |\n|---|---:|---:|---:|---:|\n")
	for _, r := range e.RouteResults {
		s := r.ArtistStats
		fmt.Fprintf(&b, "| %s | %d | %.1f%% | %.1f%% | %d |\n", md(r.Route.LastFMTag), s.UniqueArtists, 100*s.Top1Share, 100*s.Top3Share, s.MaxTracks)
	}
	table := func(title string, tracks []CandidateTrack, div bool) {
		fmt.Fprintf(&b, "\n## %s\n\n| Rank | Artist | Track | Score | Routes | Original rank | Last.fm URL |\n|---:|---|---|---:|---|---:|---|\n", title)
		for i, c := range tracks {
			rank := i + 1
			if div {
				rank = c.DiversityRank
			}
			fmt.Fprintf(&b, "| %d | %s | %s | %.6f | %s | %d | %s |\n", rank, md(c.Artist), md(c.Title), c.RetrievalScore, md(routeNames(c)), c.RawRank, c.URL)
		}
	}
	rawTop := e.Merged
	if len(rawTop) > e.Policy.TopN {
		rawTop = rawTop[:e.Policy.TopN]
	}
	table("11. Raw retrieval Top candidates", rawTop, false)
	table("12. Diversity reranked Top candidates", e.Reranked, true)
	fmt.Fprintf(&b, "\n## 13. Diversity before / after\n\nMaximum %d tracks per normalized artist in Top %d; stable score-order scan. Skipped records remain in deferred-candidates.json, never deleted from raw ranking. Cap is not relaxed to fill vacancies.\n\n| Metric | Raw Top | Diversity Top |\n|---|---:|---:|\n| Track count | %d | %d |\n| Unique artists | %d | %d |\n| Top1 artist share | %.1f%% | %.1f%% |\n| Top3 artist share | %.1f%% | %.1f%% |\n| Max tracks/artist | %d | %d |\n", e.Policy.ArtistCap, e.Policy.TopN, e.Before.TrackCount, e.After.TrackCount, e.Before.UniqueArtists, e.After.UniqueArtists, 100*e.Before.Top1Share, 100*e.After.Top1Share, 100*e.Before.Top3Share, 100*e.After.Top3Share, e.Before.MaxTracks, e.After.MaxTracks)
	fmt.Fprintf(&b, "\n## 14–16. MBID / API calls / timing\n\nMerged missing MBID: %d/%d. Actual API calls %d, cache hits %d, misses %d. Only tag.getTopTracks; page 1; no retries.\n\nMapping %.3fms; retrieval %.3fms; merge/scoring %.3fms; diversity %.3fms; experiment total %.3fms (includes mapping; excludes Go compilation and report file serialization). Route elapsed includes semaphore queueing; event latency is per HTTP/cache request.\n\n", e.MBIDMissing, len(e.Merged), e.APICalls, e.CacheHits, e.CacheMisses, e.MappingMS, e.RetrievalMS, e.MergeRankMS, e.DiversityMS, e.TotalMS)
	fmt.Fprintf(&b, "Data mode: %s. Captured offline timing is not comparable to provider HTTP latency.\n\n", e.DataMode)
	fmt.Fprint(&b, "## 17. Warnings / anomalies / interpretation\n\n")
	for _, w := range e.Warnings {
		fmt.Fprintf(&b, "- %s\n", md(w))
	}
	fmt.Fprint(&b, "\nHuman-review.csv scores intentionally blank. These are provider-backed candidates, not production recommendations or proven photo-mood matches. Weak/zero overlap and unmapped high-weight concepts can mean separate broad pools rather than joint fit. Artist diversity improves exposure, not semantic correctness. Popularity enrichment should be judged after listening review and mapping coverage; it will not repair wrong routes or tag bias.\n\n[Official tag.getTopTracks documentation](https://www.last.fm/api/show/tag.getTopTracks): tracks ordered by tag count; no listeners/playcount field inferred for this method.\n")
	return b.String()
}
