package lastfmeval

import (
	"encoding/json"
	"example.com/sync/internal/lastfm"
	"example.com/sync/internal/lastfmmapping"
	"example.com/sync/internal/music"
	"fmt"
	"os"
	"sort"
)

func readJSON(path string, out any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}
func eventTime(events []lastfm.Event, tag, method string) string {
	at := ""
	for _, e := range events {
		if lastfm.Normalize(e.Tag) == lastfm.Normalize(tag) && e.Method == method && e.Success {
			at = e.At.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
		}
	}
	return at
}

// Source observations stay distinct. New track observations may reuse older info,
// with both timestamps preserved; no reach or sample aggregation across spellings.
func LoadEligibilityEvidence(auditPath, specificityPath, retrievalPath string, c music.CanonicalCatalog) ([]TagEvidence, error) {
	var prior lastfm.Audit
	if e := readJSON(auditPath, &prior); e != nil {
		return nil, e
	}
	pool := map[string]TagEvidence{}
	for _, t := range prior.Tags {
		infoAt := eventTime(prior.Events, t.Name, "tag.getInfo")
		tracksAt := eventTime(prior.Events, t.Name, "tag.getTopTracks")
		e := TagEvidence{Tag: t.Name, Category: t.Category, Info: t.Info, Tracks: []lastfm.Track{}, InfoSuccess: t.Info != nil && infoAt != "", TracksSuccess: tracksAt != "", SampleRequested: prior.Config.SampleLimit, Source: auditPath, RecordedAt: tracksAt, InfoRecordedAt: infoAt, Reused: true, ExplicitRejected: t.Status == "rejected", HumanReview: "not_performed", SemanticReview: "not_performed"}
		if t.Reviewed {
			e.SemanticReview = "metadata_reviewed_no_audio_certification"
		}
		for _, s := range t.Samples {
			e.Tracks = append(e.Tracks, s.Track)
		}
		pool[lastfm.Normalize(t.Name)] = e
	}
	if retrievalPath != "" {
		var previous Evaluation
		if e := readJSON(retrievalPath, &previous); e != nil {
			return nil, e
		}
		for _, r := range previous.RouteResults {
			key := lastfm.Normalize(r.Route.LastFMTag)
			t, ok := pool[key]
			if !ok {
				continue
			}
			at := eventTime(r.Events, t.Tag, "tag.getTopTracks")
			if at == "" || r.Error != "" {
				continue
			}
			tracks := []lastfm.Track{}
			for _, raw := range previous.Raw {
				if lastfm.Normalize(raw.Evidence.LastFMTag) == key {
					tracks = append(tracks, raw.Track)
				}
			}
			t.Tracks = tracks
			t.TracksSuccess = true
			t.SampleRequested = previous.Policy.PerRouteLimit
			t.RecordedAt = at
			t.Source = retrievalPath + " (tracks); " + auditPath + " (info)"
			pool[key] = t
		}
	}
	if specificityPath != "" {
		var recent lastfmmapping.AuditResult
		if e := readJSON(specificityPath, &recent); e != nil {
			return nil, e
		}
		for _, r := range recent.Results {
			key := lastfm.Normalize(r.Probe.Tag)
			previous := pool[key]
			category := previous.Category
			if category == "" {
				category = "subgenre"
			}
			pool[key] = TagEvidence{Tag: r.Probe.Tag, Category: category, Info: r.Info, Tracks: r.Tracks, InfoSuccess: eventTime(r.Events, r.Probe.Tag, "tag.getInfo") != "", TracksSuccess: eventTime(r.Events, r.Probe.Tag, "tag.getTopTracks") != "", SampleRequested: recent.Options.Sample, Source: specificityPath, RecordedAt: eventTime(r.Events, r.Probe.Tag, "tag.getTopTracks"), InfoRecordedAt: eventTime(r.Events, r.Probe.Tag, "tag.getInfo"), Reused: true, SemanticReview: "not_performed", HumanReview: "not_performed", ExplicitRejected: previous.ExplicitRejected}
		}
	}
	for _, g := range c.Genres {
		if g.Family == "other" {
			continue
		}
		for _, tag := range append([]string{g.Name}, g.Aliases...) {
			key := lastfm.Normalize(tag)
			if _, ok := pool[key]; !ok {
				pool[key] = TagEvidence{Tag: lastfm.Normalize(tag), Category: "genre", Source: "canonical candidate only; no provider observation", Tracks: []lastfm.Track{}, Reused: false}
			}
		}
	}
	keys := []string{}
	for k := range pool {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []TagEvidence{}
	for _, k := range keys {
		out = append(out, pool[k])
	}
	return out, nil
}

type EvidenceNeed struct {
	Tag           string `json:"lastfm_tag"`
	Priority      int    `json:"priority"`
	NeedInfo      bool   `json:"get_info_needed"`
	NeedTracks    bool   `json:"get_top_tracks_needed"`
	ExpectedCalls int    `json:"planned_http_calls"`
	Reason        string `json:"reason"`
}

func MissingEvidencePlan(c music.CanonicalCatalog, r AutomaticRegistry, evidence []TagEvidence, profile MusicRetrievalProfile) []EvidenceNeed {
	priority := map[string]int{}
	add := func(tag string, p int) {
		key := lastfm.Normalize(tag)
		old, ok := priority[key]
		if !ok || p < old {
			priority[key] = p
		}
	}
	recent := map[string]bool{}
	for _, x := range profile.Genres {
		if g, ok := c.Resolve(x.Name); ok {
			recent[g.ID] = true
		}
	}
	for _, m := range r.Genres {
		for _, t := range m.Tags {
			p := 4
			if recent[m.Canonical] {
				p = 1
			}
			add(t.Tag, p)
		}
	}
	for _, f := range r.Families {
		if f.Primary != nil {
			add(f.Primary.Tag, 2)
		}
	}
	plan := []EvidenceNeed{}
	for _, e := range evidence {
		key := lastfm.Normalize(e.Tag)
		p, ok := priority[key]
		if !ok || e.ExplicitRejected {
			continue
		}
		d := DecideEligibility(e)
		if d.Status != "insufficient_evidence" {
			continue
		}
		info := !e.InfoSuccess || e.Info == nil || e.Info.Reach == nil
		tracks := !e.TracksSuccess || e.SampleRequested < 20
		// Successful enough-sized but small/noisy pools are not refetched to chase thresholds.
		if !info && !tracks {
			continue
		}
		calls := 0
		if info {
			calls++
		}
		if tracks {
			calls++
		}
		plan = append(plan, EvidenceNeed{e.Tag, p, info, tracks, calls, fmt.Sprintf("policy-v1 missing observations: %v", d.Reasons)})
	}
	sort.Slice(plan, func(i, j int) bool {
		if plan[i].Priority != plan[j].Priority {
			return plan[i].Priority < plan[j].Priority
		}
		return plan[i].Tag < plan[j].Tag
	})
	return plan
}
