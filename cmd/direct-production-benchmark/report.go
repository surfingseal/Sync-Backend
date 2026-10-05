package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"example.com/sync/internal/directbench"
	"example.com/sync/internal/directmusic"
)

type Regression struct {
	Winners            int              `json:"recorded_winners"`
	Accepted           int              `json:"current_accepted"`
	Rejected           int              `json:"conservative_rejected"`
	KnownFailures      int              `json:"known_failure_cases"`
	StructuralFailures int              `json:"structural_failures"`
	Notes              []string         `json:"notes"`
	Cases              []RegressionCase `json:"cases"`
}
type RegressionCase struct {
	Source   string `json:"source"`
	Artist   string `json:"artist"`
	Title    string `json:"title"`
	VideoID  string `json:"video_id,omitempty"`
	Accepted bool   `json:"current_accepted"`
	Reason   string `json:"reason"`
}

func recordedRegression() (Regression, error) {
	r := Regression{Notes: []string{"Winning metadata fully replayed; losing v1 metadata is incomplete, not fabricated", "Conservative version/title rejection is reported, not repaired with substitutes"}}
	roots := []string{"artifacts/gemini-direct-v1-20261004T152933Z/per-photo", "artifacts/gemini-direct-resolver-v2-20261004T155634Z/live-run/per-photo"}
	for _, root := range roots {
		for _, photo := range []string{"still-life", "night-city"} {
			b, e := os.ReadFile(filepath.Join(root, photo, "resolver-results.json"))
			if e != nil {
				return r, e
			}
			var rows []directmusic.Resolution
			if e = json.Unmarshal(b, &rows); e != nil {
				return r, e
			}
			for _, row := range rows {
				if row.Status == "UNRESOLVED" && root == roots[0] {
					r.KnownFailures++
					r.Cases = append(r.Cases, RegressionCase{Source: root, Artist: row.Candidate.Artist, Title: row.Candidate.Title, Reason: "historical unresolved; incomplete losing metadata not promoted"})
				}
				if (row.Status == "RESOLVED" || directmusic.IsResolved(row.Status)) && row.Video != nil {
					r.Winners++
					_, reason := directmusic.CheckIdentity(row.Candidate, *row.Video, directmusic.DefaultConfig())
					r.Cases = append(r.Cases, RegressionCase{root, row.Candidate.Artist, row.Candidate.Title, row.Video.VideoID, reason == "", reason})
					if reason == "" {
						r.Accepted++
					} else {
						r.Rejected++
					}
				}
			}
		}
	}
	if r.Winners != 40 || r.KnownFailures != 4 {
		r.StructuralFailures++
	}
	return r, nil
}
func number(v *float64, percent bool) string {
	if v == nil {
		return "N/A (no live denominator)"
	}
	if percent {
		return fmt.Sprintf("%.2f%%", 100**v)
	}
	return fmt.Sprintf("%.2f", *v)
}
func report(out string, d directbench.Dataset, a directbench.Aggregate, p directbench.Decision) error {
	var s strings.Builder
	fmt.Fprintf(&s, "# Direct production technical benchmark\n\nStatus: **%s**\n\nUnique real images: %d; duplicate entries removed: %d; executed live images: %d. No image downloads or synthetic benchmark photos. Human review is excluded by product decision and is not a promotion gate.\n\n", p.Status, len(d.Images), d.Duplicates, a.Images)
	for _, row := range []struct {
		name    string
		v       *float64
		percent bool
	}{{"10-track completion", a.CompleteRate, true}, {">=8 tracks", a.EightRate, true}, {"Average final tracks", a.AvgFinal, false}, {"Median final tracks", a.MedianFinal, false}, {"Verification among attempted", a.Verification, true}, {"Unresolved among attempted", a.Unresolved, true}, {"API failure among attempted", a.APIError, true}, {"Search calls/image", a.AvgSearch, false}, {"videos.list calls/image", a.AvgVideos, false}, {"YouTube calls/image", a.AvgHTTP, false}, {"YouTube calls/final track", a.CallsPerTrack, false}, {"Pipeline p50 (ms)", a.P50, false}, {"Pipeline p90 (ms)", a.P90, false}, {"Pipeline p95 (ms)", a.P95, false}} {
		fmt.Fprintf(&s, "- %s: %s\n", row.name, number(row.v, row.percent))
	}
	fmt.Fprintf(&s, "\nSafety: wrong-identity false positives %d; fixture failures %d; diversity violations %d; broad-search violations %d; substitute violations %d; API-failure-as-valid %d; resource bounds violations %d.\n\nQuota: primary %d, fallback %d, search.list %d, videos.list %d, total HTTP %d. Cloud remaining is unknown. Playlist writes/OAuth mutations/Last.fm calls: zero.\n\n", a.Safety.FalsePositives, a.Safety.FixtureFailures, a.Safety.Diversity, a.Safety.Broad, a.Safety.Substitute, a.Safety.APIValid, a.Safety.Bounds, a.PrimaryCalls, a.FallbackCalls, a.SearchCalls, a.VideosCalls, a.SearchCalls+a.VideosCalls)
	for _, g := range p.Gates {
		fmt.Fprintf(&s, "- Gate %s: %s (%s)\n", g.Name, g.Status, g.Reason)
	}
	fmt.Fprintf(&s, "\nReasons: %v\n\n", p.Reasons)
	s.WriteString("Endpoint compatibility: production POST /recommend still accepts JSON ImageAnalysis and preferences. Direct requires raw image context, which this request does not carry. RECOMMENDATION_ENGINE defaults to legacy; gemini_direct is parsed but startup explicitly rejects pending technical readiness and a backward-compatible image-input adapter. No silent legacy fallback, deployment switch or public schema change. Production adapter is deferred unless READY_FOR_PRIMARY.\n\nCold cache means a new empty in-memory cache for every image; warm measurements cannot qualify primary readiness. API failures never become tracks; no replacements, broad discovery or random fill. Missing execution/data does not become a zero-latency success. Nearest-rank latency quantiles; no fit-score means or language metrics in promotion.\n\nNext step: complete missing cold-cache live samples under an explicit quota budget after provider availability is confirmed. Inspect failed technical gates before proposing any prompt/threshold change. No automatic follow-up run or tuning.\n")
	return os.WriteFile(filepath.Join(out, "summary.md"), []byte(s.String()), 0600)
}
