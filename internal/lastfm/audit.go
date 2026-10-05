package lastfm

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var Families = []string{"pop", "rock", "electronic", "hip-hop-rap", "rnb-soul", "jazz", "classical", "folk-acoustic", "metal", "ambient", "dance", "soundtrack-cinematic", "experimental", "country", "reggae", "latin", "world-regional"}
var Moods = strings.Fields("dreamy melancholic sad happy uplifting mellow calm dark romantic nostalgic energetic peaceful aggressive atmospheric")

type Audit struct {
	Version   int          `json:"version"`
	StartedAt time.Time    `json:"started_at"`
	ElapsedMS float64      `json:"elapsed_ms"`
	Config    AuditConfig  `json:"configuration"`
	Tags      []AuditedTag `json:"tags"`
	Events    []Event      `json:"requests"`
	Errors    []string     `json:"errors"`
	Complete  bool         `json:"complete"`
}
type AuditConfig struct {
	MaxEnrich   int  `json:"max_enrich"`
	SampleLimit int  `json:"sample_limit"`
	Fresh       bool `json:"fresh"`
	IntervalMS  int  `json:"interval_ms"`
}

// Selection is fixed before live results: one observed global representative per
// family, then mood hypotheses, then additional useful global tags in rank order.
// Missing mood probes have no fabricated global rank or count.
func Select(tags *[]AuditedTag, max int) []int {
	out := []int{}
	seen := map[int]bool{}
	add := func(i int) {
		if len(out) < max && !seen[i] {
			out = append(out, i)
			seen[i] = true
		}
	}
	for _, family := range Families {
		for i, t := range *tags {
			if t.Family == family && t.Status == "candidate" {
				add(i)
				break
			}
		}
	}
	for _, mood := range Moods {
		idx := -1
		for i, t := range *tags {
			if Normalize(t.Name) == mood {
				idx = i
				break
			}
		}
		if idx < 0 {
			s, status := Classify(mood)
			*tags = append(*tags, AuditedTag{Name: mood, Canonical: Canonical(mood), Category: s.Category, Status: status, Origin: "targeted_mood_hypothesis", Notes: "Not observed in global list; explicit hypothesis probe, not an assumed usable tag."})
			idx = len(*tags) - 1
		}
		add(idx)
	}
	for i, t := range *tags {
		if t.Status == "candidate" {
			add(i)
		}
	}
	return out
}
func fatal(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Code == 10 || e.Code == 26 || e.Code == 29 || e.Status == 429)
}
func Run(ctx context.Context, c *Client, cfg AuditConfig, dir string) (*Audit, error) {
	a := &Audit{Version: 1, StartedAt: time.Now().UTC(), Config: cfg, Tags: []AuditedTag{}, Errors: []string{}}
	finish := func() {
		a.ElapsedMS = float64(time.Since(a.StartedAt)) / float64(time.Millisecond)
		a.Events = append([]Event{}, c.Events...)
	}
	global, err := c.GetTopTags(ctx)
	if err != nil {
		a.Errors = append(a.Errors, err.Error())
		finish()
		return a, err
	}
	a.Tags = AuditGlobal(global)
	indices := Select(&a.Tags, cfg.MaxEnrich)
	consecutive := 0
	for _, i := range indices {
		if ctx.Err() != nil {
			err = ctx.Err()
			break
		}
		t := &a.Tags[i]
		var info *TagInfo
		info, err = c.GetInfo(ctx, t.Name)
		if err != nil {
			a.Errors = append(a.Errors, fmt.Sprintf("%s: %s", t.Name, err))
			consecutive++
			if fatal(err) || consecutive >= 3 {
				break
			}
		} else {
			consecutive = 0
			t.Info = info
			t.Reach = info.Reach
			t.Taggings = info.Taggings
			// 'total' observed in JSON is retained separately, never silently renamed.
			active := info.Reach != nil && *info.Reach > 0 || info.Taggings != nil && *info.Taggings > 0 || info.Total != nil && *info.Total > 0
			if active {
				var tracks []Track
				tracks, err = c.GetTopTracks(ctx, t.Name, cfg.SampleLimit)
				if err != nil {
					a.Errors = append(a.Errors, fmt.Sprintf("%s: %s", t.Name, err))
					consecutive++
					if fatal(err) || consecutive >= 3 {
						break
					}
				} else {
					consecutive = 0
					t.Samples = Samples(tracks)
					t.SamplingWarnings = sampleWarnings(t.Samples)
					if len(tracks) == 0 {
						t.SamplingWarnings = append(t.SamplingWarnings, "empty retrieval")
					}
				}
			} else {
				t.Notes += " No positive usage evidence; no topTracks call."
			}
		}
		finish()
		if e := Write(a, dir); e != nil {
			return a, e
		}
		fmt.Printf("audit progress: %d selected processed; current=%s samples=%d\n", indexPosition(indices, i)+1, t.Name, len(t.Samples))
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-time.After(time.Duration(cfg.IntervalMS) * time.Millisecond):
		}
	}
	a.Complete = err == nil
	finish()
	return a, err
}
func indexPosition(ids []int, id int) int {
	for i, v := range ids {
		if v == id {
			return i
		}
	}
	return 0
}
func sampleWarnings(samples []Sample) []string {
	out := []string{}
	artists := map[string]int{}
	missing := 0
	dup := 0
	for _, s := range samples {
		artists[Normalize(s.Track.Artist.Name)]++
		if s.Track.MBID == "" {
			missing++
		}
		if s.Duplicate {
			dup++
		}
	}
	if len(samples) > 0 {
		for _, count := range artists {
			if count*2 >= len(samples) {
				out = append(out, "artist concentration >=50%; manual review needed")
				break
			}
		}
		if missing > 0 {
			out = append(out, fmt.Sprintf("missing track MBID %d/%d", missing, len(samples)))
		}
	}
	if dup > 0 {
		out = append(out, fmt.Sprintf("duplicate artist/title pairs: %d", dup))
	}
	return out
}

// Review is an offline, explicit metadata-semantic assessment. It does not prove
// audio mood, independent attribution, or recommendation quality.
type Review struct {
	Status string `json:"status"`
	Notes  string `json:"notes"`
}

func ApplyReviews(a *Audit, reviews map[string]Review) error {
	for name, r := range reviews {
		found := false
		for i := range a.Tags {
			t := &a.Tags[i]
			if Normalize(t.Name) != Normalize(name) {
				continue
			}
			found = true
			if r.Status != "validated" && r.Status != "candidate" && r.Status != "rejected" && r.Status != "unknown" {
				return fmt.Errorf("invalid review status for %s", name)
			}
			active := t.Info != nil && (t.Reach != nil && *t.Reach > 0 || t.Taggings != nil && *t.Taggings > 0 || t.Info.Total != nil && *t.Info.Total > 0)
			if r.Status == "validated" && (!active || len(t.Samples) < 5 || t.Status == "rejected" || t.Category == "unknown" || strings.TrimSpace(r.Notes) == "") {
				return fmt.Errorf("insufficient review evidence for %s", name)
			}
			t.Status = r.Status
			t.Reviewed = true
			t.Notes += " Review: " + r.Notes
		}
		if !found {
			return fmt.Errorf("review tag not found: %s", name)
		}
	}
	return nil
}
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}
func Write(a *Audit, dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "lastfm_tag_audit.json"), a); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, "lastfm_tag_audit.csv"))
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{"name", "rank", "count", "reach", "taggings", "category", "family", "status", "samples", "notes"})
	for _, t := range a.Tags {
		rank := ""
		if t.Rank != nil {
			rank = strconv.Itoa(*t.Rank)
		}
		_ = w.Write([]string{t.Name, rank, num(t.Count), num(t.Reach), num(t.Taggings), t.Category, t.Family, t.Status, strconv.Itoa(len(t.Samples)), t.Notes})
	}
	w.Flush()
	err = w.Error()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = writeJSON(filepath.Join(dir, "lastfm_tag_registry.json"), GenerateRegistry(a.Tags)); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "lastfm_tag_report.md"), []byte(Report(a)), 0644)
}
func num(n *Number) string {
	if n == nil {
		return ""
	}
	return strconv.FormatInt(int64(*n), 10)
}
func Report(a *Audit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Last.fm Tag Vocabulary Audit\n\n## 1. Audit configuration\n\nStarted: %s. Complete: %t. Enrichment cap %d; sample %d; fresh %t; interval %dms. Global tags queried once, no undocumented pagination. Selection: observed family representatives → 14 explicit mood hypotheses → remaining useful global ranks. No retries.\n\n", a.StartedAt.Format(time.RFC3339), a.Complete, a.Config.MaxEnrich, a.Config.SampleLimit, a.Config.Fresh, a.Config.IntervalMS)
	calls := map[string]int{}
	hits := 0
	lat := []float64{}
	enriched, sampled, global := 0, 0, 0
	statuses := map[string]int{}
	for _, e := range a.Events {
		if e.Cached {
			hits++
		} else {
			calls[e.Method]++
			lat = append(lat, e.MS)
		}
	}
	for _, t := range a.Tags {
		if t.Origin == "global" {
			global++
		}
		if t.Info != nil {
			enriched++
		}
		if t.Samples != nil {
			sampled++
		}
		statuses[t.Status]++
	}
	sort.Float64s(lat)
	pct := func(p float64) float64 {
		if len(lat) == 0 {
			return 0
		}
		i := int(float64(len(lat)-1) * p)
		return lat[i]
	}
	fmt.Fprintf(&b, "## 2. API calls / elapsed time\n\nActual calls: %d; cache hits: %d; cache misses: %d. getTopTags=%d, getInfo=%d, getTopTracks=%d. Elapsed %.2fs; request p50 %.1fms / p95 %.1fms (nearest lower order statistic). Enriched %d; sampled %d; global unique %d. Status totals: %v.\n\n", len(lat), hits, len(lat), calls["tag.getTopTags"], calls["tag.getInfo"], calls["tag.getTopTracks"], a.ElapsedMS/1000, pct(.5), pct(.95), enriched, sampled, global, statuses)
	for _, e := range a.Errors {
		fmt.Fprintf(&b, "- Error: %s\n", e)
	}
	section := func(title string, predicate func(AuditedTag) bool) {
		fmt.Fprintf(&b, "\n## %s\n\n| Tag | Category | Status | Rank | Count | Reach | Taggings | Samples |\n|---|---|---|---:|---:|---:|---:|---:|\n", title)
		for _, t := range a.Tags {
			if !predicate(t) {
				continue
			}
			rank := "—"
			if t.Rank != nil {
				rank = strconv.Itoa(*t.Rank)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %d |\n", strings.ReplaceAll(t.Name, "|", "/"), t.Category, t.Status, rank, num(t.Count), num(t.Reach), num(t.Taggings), len(t.Samples))
		}
	}
	section("3. Top global tags", func(t AuditedTag) bool { return t.Rank != nil && *t.Rank <= 30 })
	section("4. Genre/style tags", func(t AuditedTag) bool { return t.Family != "" })
	section("5. Mood tags", func(t AuditedTag) bool { return t.Category == "mood" })
	section("6. Rejected/noisy tags", func(t AuditedTag) bool { return t.Status == "rejected" })
	fmt.Fprint(&b, "\n## 7. Genre coverage matrix\n\nCounts are observed global/explicit candidates, not a complete Last.fm taxonomy. Unprobed candidates do not establish reliable retrieval.\n\n| Family | Validated | Candidate | Representatives | Gap |\n|---|---:|---:|---|---|\n")
	for _, family := range Families {
		v, c := 0, 0
		names := []string{}
		for _, t := range a.Tags {
			if t.Family != family {
				continue
			}
			if t.Status == "validated" {
				v++
			}
			if t.Status == "candidate" {
				c++
			}
			if len(names) < 4 {
				names = append(names, t.Name)
			}
		}
		gap := "No complete subgenre coverage claim"
		if v == 0 {
			gap = "No validated representative"
		}
		if c+v == 0 {
			gap = "Coverage gap: no observed classified tag"
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %s | %s |\n", family, v, c, strings.Join(names, ", "), gap)
	}
	fmt.Fprint(&b, "\n## 8. Mood coverage\n\nMood tags remain weaker signals than genres. Positive usage and nonempty top tracks do not establish that the audio expresses that mood. Track rankings are tag-count based, not general popularity. Wiki descriptions and metadata are evidence for review, not objective mood ratings.\n\n")
	for _, t := range a.Tags {
		if t.Category == "mood" {
			fmt.Fprintf(&b, "- %s: %s; %d sampled; %s\n", t.Name, t.Status, len(t.Samples), t.Notes)
		}
	}
	fmt.Fprint(&b, "\n## 9. Tags with poor retrieval quality / risk signals\n\nNo audio listening performed; semantic mismatches are manual assessments only. Artist concentration, absent MBIDs and duplicates are diagnostic flags, not automatic evidence of bad music.\n\n")
	for _, t := range a.Tags {
		if len(t.SamplingWarnings) > 0 {
			fmt.Fprintf(&b, "- %s: %s\n", t.Name, strings.Join(t.SamplingWarnings, "; "))
		}
	}
	fmt.Fprint(&b, "\n## 10. Coverage gaps\n\nGlobal list is popularity-limited; unknown tags are deliberately unclassified. Classification seeds are a review aid, not Last.fm's own genre ontology. Era/geography/language/vocal/context tags are not interchangeable with moods. Live JSON may expose `total`; it is preserved as `info.total`, never silently renamed to documented `taggings`. Absent fields stay null. Missing global moods are explicitly marked targeted hypotheses. No Korean-language coverage assessment.\n\n## 11. Recommended validated vocabulary\n\nOnly explicitly reviewed samples with usage evidence can be validated. Validation is limited to metadata-semantic retrieval evidence from this run, not guaranteed recommendation quality. Candidates must not be automatically promoted in production.\n\n")
	for _, t := range a.Tags {
		if t.Status == "validated" {
			fmt.Fprintf(&b, "- %s → %s: %s\n", t.Canonical, t.Name, t.Notes)
		}
	}
	fmt.Fprint(&b, "\n## 12. Uncertain tags requiring manual review\n\n")
	for _, t := range a.Tags {
		if t.Status == "candidate" {
			fmt.Fprintf(&b, "- %s (%s): %s\n", t.Name, t.Category, t.Notes)
		}
	}
	fmt.Fprint(&b, "\n## Track samples (API metadata, not fabricated tracks)\n\n")
	for _, t := range a.Tags {
		if len(t.Samples) == 0 {
			continue
		}
		fmt.Fprintf(&b, "### %s\n\n", t.Name)
		for _, s := range t.Samples {
			fmt.Fprintf(&b, "%d. %s — %s; MBID=%s; duplicate=%t; %s\n", s.Rank, s.Track.Artist.Name, s.Track.Name, s.Track.MBID, s.Duplicate, s.Track.URL)
		}
		fmt.Fprintln(&b)
	}
	fmt.Fprint(&b, "\n## Official references / next mapping step\n\n- [tag.getTopTags](https://www.last.fm/api/show/tag.getTopTags): name/count/url; no documented limit/page.\n- [tag.getInfo](https://www.last.fm/api/show/tag.getInfo): name/reach/taggings/wiki metadata.\n- [tag.getTopTracks](https://www.last.fm/api/show/tag.getTopTracks): track name/mbid/url, artist name/mbid/url and retrieval rank; optional limit/page.\n\nUse an allowlist of explicitly validated primary tags for future Gemini canonical mapping. Keep aliases separately reviewed; reflective and mellow are not assumed synonyms. Unmapped concepts should remain unmapped. Last.fm alone does not provide playable IDs, independently verified mood, or comprehensive family coverage; candidate retrieval suitability requires later human listening tests.\n")
	return b.String()
}
