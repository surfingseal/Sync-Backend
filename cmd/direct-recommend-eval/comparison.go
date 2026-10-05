package main

import (
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"example.com/sync/internal/directmusic"
	"example.com/sync/internal/lastfm"
	"example.com/sync/internal/lastfmeval"
	"example.com/sync/internal/music"
)

type reviewRow struct {
	PhotoID           string `json:"photo_id"`
	System            string `json:"system"`
	Rank              int    `json:"rank"`
	Artist            string `json:"artist"`
	Track             string `json:"track"`
	PhotoFit          string `json:"photo_fit"`
	WouldListen       string `json:"would_listen"`
	SongQuality       string `json:"song_quality"`
	PlaylistCoherence string `json:"playlist_coherence"`
	DiscoveryValue    string `json:"discovery_value"`
	Notes             string `json:"notes"`
}

// Only the comparison builder reads captured Last.fm research. No live Last.fm
// client is created, and this data never enters the direct service or resolver.
func comparison(out string, direct map[string]*directmusic.Result) error {
	catalog := music.CanonicalTaxonomy()
	registry, err := lastfmeval.LoadAutomaticRegistry("internal/music/lastfm_retrieval_registry.json", catalog)
	if err != nil {
		return err
	}
	vocab, err := lastfmeval.LoadVocabulary("internal/music/lastfm_tag_registry.json", "internal/music/lastfm_tag_mapping.json")
	if err != nil {
		return err
	}
	poolPath := "artifacts/lastfm-final-evidence-batch-20261004T144123Z/provider-observations.json"
	b, err := os.ReadFile(poolPath)
	if err != nil {
		return err
	}
	var pool []lastfmeval.TagEvidence
	if err = json.Unmarshal(b, &pool); err != nil {
		return err
	}
	captured := &lastfmeval.CapturedRetriever{Tracks: map[string][]lastfm.Track{}}
	for _, t := range pool {
		if t.TracksSuccess {
			captured.Tracks[lastfm.Normalize(t.Tag)] = t.Tracks
		}
	}
	policy := lastfmeval.DefaultPolicy()
	policy.PerRouteLimit = 20
	policy.TopN = 10
	policy.ArtistCap = 2
	comparisons := map[string]any{}
	rows := []reviewRow{}
	key := map[string]any{}
	for _, photo := range []struct{ id, analysis string }{{"still-life", "../benchmark-results/e2e/analyze.json"}, {"night-city", "../benchmark-results/live-v3-single/analysis.json"}} {
		a, _, err := lastfmeval.ReadAnalysis(photo.analysis)
		if err != nil {
			return err
		}
		profile, err := lastfmeval.Profile(a)
		if err != nil {
			return err
		}
		plan, err := lastfmeval.BuildAutomaticPlan(profile, vocab, policy, catalog, registry, false)
		if err != nil {
			return err
		}
		baseline, err := lastfmeval.Evaluate(context.Background(), plan, policy, captured)
		if err != nil {
			return err
		}
		baseline.DataMode = "offline captured provider samples of mixed vintage; not a fresh Last.fm call; no YouTube verification"
		if err = write(out, photo.id+"-lastfm-baseline.json", baseline); err != nil {
			return err
		}
		labels := []string{"X", "Y"}
		var coin [1]byte
		if _, err = rand.Read(coin[:]); err != nil {
			return err
		}
		if coin[0]&1 == 1 {
			labels[0], labels[1] = labels[1], labels[0]
		}
		key[photo.id] = map[string]string{labels[0]: "captured Last.fm baseline", labels[1]: "Gemini direct + YouTube identity verification"}
		for i, t := range baseline.Reranked {
			rows = append(rows, reviewRow{PhotoID: photo.id, System: labels[0], Rank: i + 1, Artist: t.Artist, Track: t.Title})
		}
		directTracks := []directmusic.VerifiedTrack{}
		if r := direct[photo.id]; r != nil {
			directTracks = r.Final
			for i, t := range r.Final {
				rows = append(rows, reviewRow{PhotoID: photo.id, System: labels[1], Rank: i + 1, Artist: t.Gemini.Artist, Track: t.Gemini.Title})
			}
		}
		comparisons[photo.id] = map[string]any{"saved_analysis": photo.analysis, "captured_evidence": poolPath, "captured_evidence_sha256": hash(b), "baseline": baseline.Reranked, "direct": directTracks, "limitations": []string{"baseline is offline mixed-vintage retrieval, not simultaneous live A/B", "baseline playback was not verified; direct identity is a metadata heuristic", "no automatic winner or human score"}}
	}
	if err = write(out, "comparison-lastfm.json", comparisons); err != nil {
		return err
	}
	if err = write(out, "blind-review-source-key.json", key); err != nil {
		return err
	}
	if err = write(out, "blind-review.json", rows); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(out, "blind-review.csv"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	w.Write([]string{"photo_id", "system", "rank", "artist", "track", "photo_fit_1_5", "would_listen_1_5", "song_quality_1_5", "playlist_coherence_1_5", "discovery_value_1_5", "notes"})
	for _, r := range rows {
		w.Write([]string{r.PhotoID, r.System, fmt.Sprint(r.Rank), r.Artist, r.Track, "", "", "", "", "", ""})
	}
	w.Flush()
	if err = w.Error(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
func summary(out, status string, results map[string]*directmusic.Result, warnings map[string]string) error {
	var s strings.Builder
	fmt.Fprintf(&s, "# Sync Gemini Direct — Resolver v2\n\nStatus: **%s**. Human quality review is pending. No playlist writes.\n\nImage → existing preprocessing → Gemini ordered artist/title candidates → conservative normalization → exact YouTube identity resolver → max 2 per artist → up to 10 verified tracks. Existing /recommend is unchanged.\n\nFit score is model relevance, not a probability. Resolver identity score is a metadata heuristic, not proof of audio identity or channel ownership. No broad search, substitute tracks, Last.fm live requests or retries. One token-only identity fallback may follow a primary identity miss.\n", status)
	for _, id := range []string{"still-life", "night-city"} {
		fmt.Fprintf(&s, "\n## %s\n\n", id)
		if reason := warnings[id]; reason != "" {
			fmt.Fprintf(&s, "Warning: %s\n\n", reason)
		}
		r := results[id]
		if r == nil {
			s.WriteString("Live result unavailable; no candidates fabricated.\n")
			continue
		}
		d := r.Diagnostics
		fmt.Fprintf(&s, "Generated %d; normalized %d; duplicates %d; blank %d. Attempted %d; verified %d; unresolved %d; unattempted %d. Final %d, unique artists %d, same-artist max %d.\n\nsearch.list %d; videos.list %d; cache hits %d / misses %d. Gemini %.1f ms; resolver %.1f ms; total %.1f ms.\n\n", d.Generated, d.Normalized, d.Duplicates, d.Blank, d.Attempted, d.Verified, d.Unresolved, d.Unattempted, d.FinalCount, d.UniqueArtists, d.SameArtistMax, d.Calls.SearchCalls, d.Calls.VideosCalls, d.Calls.CacheHits, d.Calls.CacheMisses, d.GeminiMS, d.ResolverMS, d.TotalMS)
		if d.SuccessRate != nil {
			fmt.Fprintf(&s, "Verification success %.1f%%; unresolved rate among attempted %.1f%%. Generated-not-verified %d includes unattempted/duplicates/blanks and is not a hallucination rate.\n\n", 100**d.SuccessRate, 100**d.UnresolvedRate, d.GeneratedNotVerified)
		}
		for i, t := range r.Final {
			fmt.Fprintf(&s, "%d. %s — %s ([YouTube](%s)); Gemini rank %d, fit %.2f, identity %.2f\n", i+1, t.Gemini.Artist, t.Gemini.Title, t.YouTubeURL, t.Gemini.GeminiRank, t.Gemini.FitScore, t.ResolutionEvidence.IdentityScore)
		}
	}
	s.WriteString("\n## Comparison and next decision\n\nLast.fm comparison uses the same saved-photo analyses and captured samples, with unchanged scoring/diversity. It is not a simultaneous live comparison; Last.fm playback is unverified. Blind review ratings remain empty. Share blind-review.csv without blind-review-source-key.json. Do not decide a winner or promote the engine before evaluating photo fit, listening intent and resolution errors.\n\n## Quota\n\nActual per-photo request counts above are authoritative. Maximum two photos × 20 searches; videos.list batches new IDs per search stage; previously checked IDs are not fetched again within that candidate. Positive cache hits revalidate one ID without search; negative entries expire after 10 minutes. No retry. Official search.list documentation currently describes a Search Queries bucket (100 calls/day, 1 unit/call); check the project's actual Cloud quota rather than assuming the historical 100 general units/search. [search.list](https://developers.google.com/youtube/v3/docs/search/list).\n")
	return os.WriteFile(filepath.Join(out, "summary.md"), []byte(s.String()), 0600)
}
