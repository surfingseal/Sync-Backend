package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"example.com/sync/internal/directmusic"
)

func v2Report(out string, status string, results map[string]*directmusic.Result) error {
	f, err := os.OpenFile(filepath.Join(out, "direct-review.csv"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	w.Write([]string{"photo_id", "rank", "artist", "track", "photo_fit_1_5", "would_listen_1_5", "playlist_coherence_1_5", "notes"})
	counts := map[string]map[string]int{}
	timing := map[string]any{}
	total := directmusic.CallStats{}
	for _, id := range []string{"still-life", "night-city"} {
		r := results[id]
		if r == nil {
			continue
		}
		distribution := map[string]int{}
		for i, t := range r.Final {
			w.Write([]string{id, fmt.Sprint(i + 1), t.Gemini.Artist, t.Gemini.Title, "", "", "", ""})
			distribution[t.ResolutionEvidence.Officiality]++
		}
		counts[id] = distribution
		d := r.Diagnostics
		timing[id] = map[string]any{"diagnostics": d, "calls_per_final_track": ratio(d.Calls.SearchCalls+d.Calls.VideosCalls, d.FinalCount), "new_gemini_output_not_same_as_v1": true}
		total.SearchCalls += d.Calls.SearchCalls
		total.VideosCalls += d.Calls.VideosCalls
		total.PrimaryCalls += d.Calls.PrimaryCalls
		total.FallbackCalls += d.Calls.FallbackCalls
		total.CacheHits += d.Calls.CacheHits
		total.CacheMisses += d.Calls.CacheMisses
		total.SearchMS += d.Calls.SearchMS
		total.PrimaryMS += d.Calls.PrimaryMS
		total.FallbackMS += d.Calls.FallbackMS
		total.MetadataMS += d.Calls.MetadataMS
		total.PositiveMigrations += d.Calls.PositiveMigrations
	}
	w.Flush()
	if err = w.Error(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = write(out, "quota-timing.json", map[string]any{"per_photo": timing, "total_calls": total, "cloud_remaining": "unknown", "cache_revalidation_excludes_search_when_successful": true, "playlist_writes": 0, "lastfm_calls": 0}); err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(out, "accepted-v1-regression.json"))
	if err != nil {
		return err
	}
	var regression map[string]any
	if err = json.Unmarshal(b, &regression); err != nil {
		return err
	}
	var s strings.Builder
	s.WriteString("\n## Resolver v2 evidence and regression\n\nPrompt/model/fit-score/order/diversity remain unchanged. V1 candidate replay is separated from new live Gemini generation. Artist/title/version evidence is separate from officiality and playability. Named artist/Topic/VEVO channels are metadata heuristics, not OAC/ownership attestation. Policy B requires Topic/VEVO/licensed evidence only for offline analysis; it is not a production gate.\n\n")
	fmt.Fprintf(&s, "Recorded winners: same ID accepted %v / 20; rejected %v; no alternate IDs were searched in replay. Inspect accepted-v1-regression.json for each reason. Losing v1 snippets lack full release/playability metadata, so ADOY text identity improvement is not falsely reported as a full offline playback resolution. Colde aliases are unsupported by default without explicit provider proof. Corcovado credit relaxation requires strong release evidence. Iron & Wine needs a controlled same-track token query.\n\n", regression["same_id_accepted"], regression["same_id_rejected"])
	for _, id := range []string{"still-life", "night-city"} {
		if r := results[id]; r != nil {
			d := r.Diagnostics
			fmt.Fprintf(&s, "%s: primary %d / fallback %d / metadata %d; cache %d hit, %d miss; primary %.1fms, fallback %.1fms, metadata %.1fms. Final officiality metadata distribution: %v. API failures %d (separate from identity/version/no-candidate failures).\n\n", id, d.Calls.PrimaryCalls, d.Calls.FallbackCalls, d.Calls.VideosCalls, d.Calls.CacheHits, d.Calls.CacheMisses, d.Calls.PrimaryMS, d.Calls.FallbackMS, d.Calls.MetadataMS, counts[id], d.APIFailures)
		}
	}
	s.WriteString("Human review is PENDING. blind-review.csv hides source labels; do not share blind-review-source-key.json. direct-review.csv supports Direct-only evaluation. No language/country ratio is collected as a quality metric. Last.fm comparison remains offline mixed-vintage, not controlled simultaneous A/B. Production promotion requires human quality, identity precision/recall and latency/quota tradeoff assessment. No live result-driven retuning or extra Gemini run.\n")
	fmt.Fprintf(&s, "\nPromotion status: %s.\n", status)
	f, err = os.OpenFile(filepath.Join(out, "summary.md"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(s.String())
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}
func ratio(n, d int) *float64 {
	if d == 0 {
		return nil
	}
	v := float64(n) / float64(d)
	return &v
}
