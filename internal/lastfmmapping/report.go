package lastfmmapping

import (
	"encoding/csv"
	"encoding/json"
	"example.com/sync/internal/music"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func jsonFile(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}
func Write(dir string, a *AuditResult, c music.CanonicalCatalog) error {
	if e := os.MkdirAll(filepath.Join(dir, "raw"), 0755); e != nil {
		return e
	}
	if e := jsonFile(filepath.Join(dir, "mappings.json"), a); e != nil {
		return e
	}
	if e := jsonFile(filepath.Join(dir, "runtime-mapping.json"), a.Registry.Runtime()); e != nil {
		return e
	}
	if e := jsonFile(filepath.Join(dir, "sync-taxonomy.json"), c); e != nil {
		return e
	}
	if e := jsonFile(filepath.Join(dir, "unknown-observations.json"), a.Unknown); e != nil {
		return e
	}
	candidates, unmapped := []GenreMapping{}, []GenreMapping{}
	for _, m := range a.Registry.Genres {
		if m.Exact != nil && m.Exact.Status == "candidate" {
			candidates = append(candidates, m)
		}
		if a.Registry.Resolve(c, m.CanonicalGenreID).ResolutionType == "unmapped" {
			unmapped = append(unmapped, m)
		}
	}
	if e := jsonFile(filepath.Join(dir, "candidates.json"), candidates); e != nil {
		return e
	}
	if e := jsonFile(filepath.Join(dir, "unmapped.json"), unmapped); e != nil {
		return e
	}
	for i, r := range a.Results {
		if e := jsonFile(filepath.Join(dir, "raw", fmt.Sprintf("probe-%03d.json", i+1)), r); e != nil {
			return e
		}
	}
	f, e := os.Create(filepath.Join(dir, "mappings.csv"))
	if e != nil {
		return e
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{"canonical_id", "family", "exact_tag", "exact_status", "resolution", "selected_provider_tag", "factor"})
	for _, m := range a.Registry.Genres {
		r := a.Registry.Resolve(c, m.CanonicalGenreID)
		tag, status := "", ""
		if m.Exact != nil {
			tag = m.Exact.Tag
			status = m.Exact.Status
		}
		_ = w.Write([]string{m.CanonicalGenreID, r.Family, tag, status, r.ResolutionType, r.ProviderTag, strconv.FormatFloat(r.WeightFactor, 'f', 2, 64)})
	}
	w.Flush()
	e = w.Error()
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	f, e = os.Create(filepath.Join(dir, "human-review.csv"))
	if e != nil {
		return e
	}
	w = csv.NewWriter(f)
	_ = w.Write([]string{"canonical_id", "provider_tag", "mapping_type", "status", "reach", "sample_size", "unique_artists", "top1_share", "semantic_review", "notes"})
	for _, m := range a.Registry.Genres {
		tags := append([]ProviderTag{}, m.Aliases...)
		if m.Exact != nil {
			tags = append([]ProviderTag{*m.Exact}, tags...)
		}
		for _, t := range tags {
			reach := ""
			if t.Reach != nil {
				reach = strconv.FormatInt(int64(*t.Reach), 10)
			}
			_ = w.Write([]string{m.CanonicalGenreID, t.Tag, t.MappingType, t.Status, reach, strconv.Itoa(t.Evidence.SampleSize), strconv.Itoa(t.Evidence.UniqueArtists), fmt.Sprintf("%.3f", t.Evidence.Top1Share), "", ""})
		}
	}
	w.Flush()
	e = w.Error()
	closeErr = f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(Report(a, c)), 0644)
}
func Report(a *AuditResult, c music.CanonicalCatalog) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Sync canonical taxonomy → Last.fm mapping audit\n\nSource: %s. Total canonical IDs %d (other/unknown included), families %d. No changes to production YouTube recommendation or Gemini public schema. Existing category relationships retained: bossa-nova → jazz, ambient → electronic, acoustic-folk/hip-hop/cinematic are existing family IDs. Last.fm's prior family labels do not redefine these relationships.\n\n", c.Source, len(c.Genres), len(c.Families))
	counts := map[string]int{}
	exact, alias, candidate := 0, 0, 0
	for _, m := range a.Registry.Genres {
		counts[a.Registry.Resolve(c, m.CanonicalGenreID).ResolutionType]++
		if m.Exact != nil && m.Exact.Status == "validated" {
			exact++
		}
		for _, t := range m.Aliases {
			if t.Status == "validated" {
				alias++
			} else if t.Status == "candidate" {
				candidate++
			}
		}
		if m.Exact != nil && m.Exact.Status == "candidate" {
			candidate++
		}
	}
	fallback := 0
	for _, f := range a.Registry.Families {
		if f.Primary != nil {
			fallback++
		}
	}
	fmt.Fprintf(&b, "## Counts / calls\n\nValidated exact tags %d; validated orthographic alias records %d; explicit family representatives %d; candidate tag mappings (exact/alias) %d; resolver outcomes %v. Reused unique provider tag evidence %d. Planned probes %d; executed %d; actual calls %d (%v); cache hits %d / misses %d; elapsed %.3fms. Dry run %t; live probes %t.\n\n", exact, alias, fallback, candidate, counts, a.EvidenceReused, len(a.Planned), len(a.Results), a.APICalls, a.PerEndpoint, a.CacheHits, a.CacheMisses, a.ElapsedMS, a.Options.DryRun, a.Options.Live)
	fmt.Fprint(&b, "## Canonical taxonomy (complete existing list)\n\n| Family | ID | Display name | Explicit aliases |\n|---|---|---|---|\n")
	for _, g := range c.Genres {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", g.Family, g.ID, g.Name, strings.Join(g.Aliases, ", "))
	}
	fmt.Fprint(&b, "\n## Provider resolution\n\nExact 1.0 → orthographic alias 0.95 → explicitly validated family fallback 0.65 → unmapped. Related/candidate/rejected never selected. Family fallback is a broader taxonomy relationship, not an exact synonym.\n\n| Canonical | Exact tag/status | Aliases | Family | Resolution | Provider tag | Factor |\n|---|---|---|---|---|---|---:|\n")
	for _, m := range a.Registry.Genres {
		r := a.Registry.Resolve(c, m.CanonicalGenreID)
		exact := "—"
		if m.Exact != nil {
			exact = m.Exact.Tag + " / " + m.Exact.Status
		}
		aliases := []string{}
		for _, t := range m.Aliases {
			aliases = append(aliases, t.Tag+" / "+t.Status)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %.2f |\n", m.CanonicalGenreID, exact, strings.Join(aliases, ", "), r.Family, r.ResolutionType, r.ProviderTag, r.WeightFactor)
	}
	fmt.Fprint(&b, "\n## Family registry\n\n| Existing Sync family | Explicit primary provider | Reason |\n|---|---|---|\n")
	for _, f := range a.Registry.Families {
		tag := "unmapped"
		if f.Primary != nil {
			tag = f.Primary.Tag
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", f.Family, tag, f.Reason)
	}
	fmt.Fprint(&b, "\n## Evidence / diagnostics\n\nValidation means metadata-semantic provider evidence only. No listening, audio-level genre certification, purity guarantee or recommendation-success score. Existing candidate evidence is not promoted. Alias spellings preserve separate provider samples.\n\n")
	for _, m := range a.Registry.Genres {
		tags := append([]ProviderTag{}, m.Aliases...)
		if m.Exact != nil {
			tags = append([]ProviderTag{*m.Exact}, tags...)
		}
		for _, t := range tags {
			e := t.Evidence
			fmt.Fprintf(&b, "### %s → %s (%s / %s)\n\nSample %d, unique artists %d, top1 %.1f%%, top3 %.1f%%, max tracks %d, missing MBID %d, duplicates %d, reused %t, new calls %d.\n\nReview: %s\n\n", m.CanonicalGenreID, t.Tag, t.MappingType, t.Status, e.SampleSize, e.UniqueArtists, e.Top1Share*100, e.Top3Share*100, e.MaxTracks, e.MBIDMissing, e.Duplicates, e.Reused, e.APICalls, e.ReviewNotes)
			for _, w := range e.Warnings {
				fmt.Fprintf(&b, "- %s\n", w)
			}
			for _, s := range e.Samples {
				fmt.Fprintf(&b, "%d. %s — %s (%s)\n", s.Rank, s.Track.Artist.Name, s.Track.Name, s.Track.URL)
			}
			fmt.Fprintln(&b)
		}
	}
	fmt.Fprint(&b, "## Pending exact probes (not evidence of tag existence)\n\n")
	for _, p := range a.Planned {
		fmt.Fprintf(&b, "- %s: %s\n", p.Canonical, p.Tag)
	}
	fmt.Fprint(&b, "\n## Warnings / limitations\n\n")
	for _, w := range a.Warnings {
		fmt.Fprintf(&b, "- %s\n", w)
	}
	fmt.Fprint(&b, "\nThe existing taxonomy blends genres/styles with Korean-oriented and city/retro contexts. This milestone does not invent provider-independent parent families for them. Existing explicit aliases can also be broad (e.g. lo-fi → lo-fi-hip-hop, urban jazz → jazz); retained and reported, not expanded. QueryTokens are search expressions, not new canonical aliases in this resolver. New concepts remain unknown; repeated unknown labels can be aggregated offline.\n\nLive mapping audit requires LASTFM_API_KEY already present in the environment and explicit --live. Without it, no live calls are attempted. New provider probes remain candidate until explicit evidence review. Popularity enrichment cannot fix incorrect or overly broad family mappings.\n")
	return b.String()
}
