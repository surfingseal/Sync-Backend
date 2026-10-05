package lastfmmapping

import (
	"context"
	"example.com/sync/internal/lastfm"
	"example.com/sync/internal/music"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// These explicit family representatives are supported by the prior tag audit.
// The keys follow existing Sync categories, not the Last.fm audit's family labels.
var familyPrimary = map[string]string{"pop": "pop", "rock": "rock", "rnb-soul": "soul", "electronic": "electronic", "hip-hop": "Hip-Hop", "acoustic-folk": "folk", "classical": "Classical"}

type Options struct {
	OrthographicVariants bool   `json:"probe_orthographic_variants"`
	Genre                string `json:"genre_filter,omitempty"`
	Family               string `json:"family_filter,omitempty"`
	Sample               int    `json:"sample_limit"`
	Fresh                bool   `json:"fresh"`
	DryRun               bool   `json:"dry_run"`
	Live                 bool   `json:"live"`
	Concurrency          int    `json:"concurrency"`
	MaxProbes            int    `json:"max_probes"`
}
type Probe struct {
	Canonical string `json:"canonical_genre_id"`
	Tag       string `json:"tag"`
	Kind      string `json:"mapping_type"`
}
type ProbeResult struct {
	Probe     Probe           `json:"probe"`
	Info      *lastfm.TagInfo `json:"info,omitempty"`
	Tracks    []lastfm.Track  `json:"tracks,omitempty"`
	Events    []lastfm.Event  `json:"requests"`
	Error     string          `json:"error,omitempty"`
	ElapsedMS float64         `json:"elapsed_ms"`
}
type AuditResult struct {
	Registry       Registry                  `json:"registry"`
	Options        Options                   `json:"options"`
	Planned        []Probe                   `json:"planned_probes"`
	Results        []ProbeResult             `json:"probe_results"`
	EvidenceReused int                       `json:"unique_prior_tags_reused"`
	APICalls       int                       `json:"api_calls"`
	PerEndpoint    map[string]int            `json:"calls_per_endpoint"`
	CacheHits      int                       `json:"cache_hits"`
	CacheMisses    int                       `json:"cache_misses"`
	ElapsedMS      float64                   `json:"elapsed_ms"`
	Warnings       []string                  `json:"warnings"`
	Unknown        []UnknownGenreObservation `json:"unknown_genres"`
}
type UnknownGenreObservation struct {
	RawGenre string `json:"raw_genre"`
	Count    int    `json:"count"`
}

func AggregateUnknown(c music.CanonicalCatalog, labels []string) []UnknownGenreObservation {
	counts := map[string]int{}
	for _, raw := range labels {
		if _, ok := c.Resolve(raw); !ok {
			counts[raw]++
		}
	}
	keys := []string{}
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []UnknownGenreObservation{}
	for _, k := range keys {
		out = append(out, UnknownGenreObservation{k, counts[k]})
	}
	return out
}
func EvidenceFrom(t lastfm.AuditedTag, source string, reused bool) Evidence {
	e := Evidence{Source: source, Reused: reused, Reach: t.Reach, Taggings: t.Taggings, Samples: append([]lastfm.Sample{}, t.Samples...), SampleSize: len(t.Samples), Warnings: append([]string{}, t.SamplingWarnings...), ReviewNotes: t.Notes}
	if t.Info != nil {
		e.Total = t.Info.Total
	}
	counts := map[string]int{}
	seen := map[string]bool{}
	for _, s := range t.Samples {
		artist := music.CanonicalKey(s.Track.Artist.Name)
		counts[artist]++
		key := artist + "\x00" + lastfm.Normalize(s.Track.Name)
		if seen[key] {
			e.Duplicates++
		}
		seen[key] = true
		if s.Track.MBID == "" {
			e.MBIDMissing++
		}
	}
	sizes := []int{}
	for _, n := range counts {
		sizes = append(sizes, n)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(sizes)))
	e.UniqueArtists = len(counts)
	if len(sizes) > 0 {
		e.MaxTracks = sizes[0]
		e.Top1Share = float64(sizes[0]) / float64(e.SampleSize)
		total := 0
		for i, n := range sizes {
			if i < 3 {
				total += n
			}
		}
		e.Top3Share = float64(total) / float64(e.SampleSize)
	}
	if e.Top1Share >= .4 {
		e.Warnings = append(e.Warnings, "artist concentration >=40%; genre purity/quality not implied")
	}
	return e
}
func priorTag(t lastfm.AuditedTag, kind, source string) *ProviderTag {
	evidence := EvidenceFrom(t, source, true)
	status := t.Status
	if status == "unknown" {
		status = "unmapped"
	}
	active := t.Reach != nil && *t.Reach > 0 || t.Taggings != nil && *t.Taggings > 0 || evidence.Total != nil && *evidence.Total > 0
	if status == "validated" && (!t.Reviewed || !active || len(t.Samples) < 5) {
		status = "candidate"
		evidence.Warnings = append(evidence.Warnings, "prior validation lacks explicit usage/sample review evidence")
	}
	return &ProviderTag{t.Name, kind, status, t.Category, t.Reach, evidence.Total, evidence}
}

// Plan reuses exact and orthographic prior evidence independently. No semantic
// alternatives are manufactured, and sentinels are never probed.
func Plan(c music.CanonicalCatalog, prior *lastfm.Audit, source string, o Options) (*AuditResult, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if o.Sample < 5 || o.Sample > 50 || o.Concurrency < 1 || o.Concurrency > 3 || o.MaxProbes < 1 || o.MaxProbes > 120 {
		return nil, fmt.Errorf("invalid mapping audit bounds")
	}
	if o.Genre != "" {
		if _, ok := c.Lookup(o.Genre); !ok {
			return nil, fmt.Errorf("unknown canonical --genre")
		}
	}
	if o.Family != "" {
		found := false
		for _, f := range c.Families {
			if f.ID == o.Family {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown --family")
		}
	}
	a := &AuditResult{Registry: Registry{Version: 1, TaxonomySource: c.Source, ValidationScope: ValidationScope, Genres: []GenreMapping{}, Families: []FamilyMapping{}}, Options: o, Planned: []Probe{}, Results: []ProbeResult{}, PerEndpoint: map[string]int{}, Warnings: []string{}, Unknown: []UnknownGenreObservation{}}
	reused := map[string]bool{}
	for _, f := range c.Families {
		fm := FamilyMapping{Family: f.ID, Reason: "no explicitly validated primary provider fallback"}
		if tag, ok := familyPrimary[f.ID]; ok {
			for _, t := range prior.Tags {
				if lastfm.Normalize(t.Name) == lastfm.Normalize(tag) {
					pt := priorTag(t, "exact", source)
					if pt.Status == "validated" {
						fm.Primary = pt
						fm.Reason = "explicit broad-family representative reused from metadata-reviewed prior audit"
						reused[lastfm.Normalize(t.Name)] = true
					}
					break
				}
			}
		}
		a.Registry.Families = append(a.Registry.Families, fm)
	}
	for _, g := range c.Genres {
		m := GenreMapping{CanonicalGenreID: g.ID, Aliases: []ProviderTag{}, Status: "unmapped"}
		if g.Family != "other" {
			for _, t := range prior.Tags {
				if lastfm.Normalize(t.Name) == lastfm.Normalize(g.Name) {
					pt := priorTag(t, "exact", source)
					m.Exact = pt
					reused[lastfm.Normalize(t.Name)] = true
				} else if Orthographic(t.Name, g.Name) {
					pt := priorTag(t, "orthographic_alias", source)
					m.Aliases = append(m.Aliases, *pt)
					reused[lastfm.Normalize(t.Name)] = true
				}
			}
			// Stable alias preference by provider spelling, not map iteration or usage.
			sort.Slice(m.Aliases, func(i, j int) bool { return lastfm.Normalize(m.Aliases[i].Tag) < lastfm.Normalize(m.Aliases[j].Tag) })
			for _, f := range a.Registry.Families {
				if f.Family == g.Family && f.Primary != nil {
					copy := f
					m.FamilyFallback = &copy
					break
				}
			}
			probeAllowed := (o.Genre == "" || o.Genre == g.ID) && (o.Family == "" || o.Family == g.Family)
			// Existing candidate evidence is reused too; not automatically re-probed.
			if probeAllowed && (o.Fresh || m.Exact == nil) {
				a.Planned = append(a.Planned, Probe{g.ID, strings.ToLower(g.Name), "exact"})
				if o.OrthographicVariants {
					for _, variant := range Variants(g.Name) {
						exists := false
						for _, priorTag := range prior.Tags {
							if lastfm.Normalize(priorTag.Name) == lastfm.Normalize(variant) {
								exists = true
								break
							}
						}
						if !exists || o.Fresh {
							a.Planned = append(a.Planned, Probe{g.ID, variant, "orthographic_alias"})
						}
					}
				}
			}
		}
		if m.Exact != nil {
			m.Status = m.Exact.Status
		} else {
			for _, t := range m.Aliases {
				if t.Status == "validated" {
					m.Status = "validated"
					break
				}
			}
		}
		a.Registry.Genres = append(a.Registry.Genres, m)
	}
	a.EvidenceReused = len(reused)
	if len(a.Planned) > o.MaxProbes {
		a.Warnings = append(a.Warnings, fmt.Sprintf("probe plan exceeds per-run cap %d; live executes only first %d probes", o.MaxProbes, o.MaxProbes))
	}
	return a, a.Registry.Validate(c)
}

type ProbeFetcher interface {
	Fetch(context.Context, Probe, int) ProbeResult
}
type Provider struct {
	Key, CacheDir string
	Fresh         bool
	Timeout       time.Duration
}

func (p Provider) Fetch(ctx context.Context, probe Probe, sample int) ProbeResult {
	start := time.Now()
	out := ProbeResult{Probe: probe, Events: []lastfm.Event{}}
	client := lastfm.New(p.Key)
	client.CacheDir = p.CacheDir
	client.Fresh = p.Fresh
	if p.Timeout > 0 {
		client.Timeout = p.Timeout
	}
	info, err := client.GetInfo(ctx, probe.Tag)
	out.Info = info
	if err == nil && info != nil {
		active := info.Reach != nil && *info.Reach > 0 || info.Total != nil && *info.Total > 0 || info.Taggings != nil && *info.Taggings > 0
		if active {
			out.Tracks, err = client.GetTopTracks(ctx, probe.Tag, sample)
		}
	}
	if err != nil {
		if e, ok := err.(*lastfm.Error); ok {
			out.Error = e.Error()
		} else {
			out.Error = "probe canceled or failed"
		}
	}
	out.Events = client.Events
	out.ElapsedMS = float64(time.Since(start)) / float64(time.Millisecond)
	return out
}
func Execute(ctx context.Context, a *AuditResult, fetcher ProbeFetcher) {
	start := time.Now()
	defer func() { a.ElapsedMS = float64(time.Since(start)) / float64(time.Millisecond) }()
	if a.Options.DryRun || !a.Options.Live {
		a.Warnings = append(a.Warnings, "no live probes; planned tags have no newly collected evidence")
		return
	}
	probes := a.Planned
	if len(probes) > a.Options.MaxProbes {
		probes = probes[:a.Options.MaxProbes]
	}
	a.Results = make([]ProbeResult, len(probes))
	sem := make(chan struct{}, a.Options.Concurrency)
	var wg sync.WaitGroup
	for i, p := range probes {
		wg.Add(1)
		go func(i int, p Probe) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				a.Results[i] = ProbeResult{Probe: p, Error: "context canceled before probe"}
				return
			}
			a.Results[i] = fetcher.Fetch(ctx, p, a.Options.Sample)
		}(i, p)
	}
	wg.Wait()
	for _, result := range a.Results {
		calls := 0
		for _, e := range result.Events {
			if e.Cached {
				a.CacheHits++
			} else if e.HTTPAttempted {
				a.APICalls++
				calls++
				a.CacheMisses++
				a.PerEndpoint[e.Method]++
			}
		}
		if result.Error != "" {
			a.Warnings = append(a.Warnings, "partial probe failure: "+result.Probe.Tag+"; "+result.Error)
			continue
		}
		t := lastfm.AuditedTag{Name: result.Probe.Tag, Status: "candidate", Category: "genre", Info: result.Info, Samples: lastfm.Samples(result.Tracks), Notes: "New evidence collected; no automatic metadata-semantic validation."}
		if result.Info != nil {
			t.Reach = result.Info.Reach
			t.Taggings = result.Info.Taggings
		}
		evidence := EvidenceFrom(t, "current controlled probe", false)
		evidence.APICalls = calls
		pt := ProviderTag{t.Name, result.Probe.Kind, "candidate", "genre", t.Reach, evidence.Total, evidence}
		if len(t.Samples) == 0 {
			pt.Status = "unmapped"
			pt.Evidence.Warnings = append(pt.Evidence.Warnings, "empty retrieval")
		}
		for i, m := range a.Registry.Genres {
			if m.CanonicalGenreID == result.Probe.Canonical {
				if result.Probe.Kind == "orthographic_alias" {
					a.Registry.Genres[i].Aliases = append(a.Registry.Genres[i].Aliases, pt)
				} else {
					a.Registry.Genres[i].Exact = &pt
					a.Registry.Genres[i].Status = pt.Status
				}
				break
			}
		}
	}
}

// Only spelling transforms, never genre synonyms. Optional, bounded probes.
func Variants(name string) []string {
	exact := lastfm.Normalize(name)
	spaced := strings.ReplaceAll(exact, "-", " ")
	spaced = lastfm.Normalize(spaced)
	variants := []string{spaced, strings.ReplaceAll(spaced, " ", "-"), strings.ReplaceAll(spaced, " ", "")}
	seen := map[string]bool{exact: true}
	out := []string{}
	for _, v := range variants {
		if !seen[v] && Orthographic(v, exact) {
			out = append(out, v)
			seen[v] = true
		}
	}
	return out
}
