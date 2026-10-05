package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"example.com/sync/internal/directmusic"
	"example.com/sync/internal/model"
)

const v1EvidenceRoot = "artifacts/gemini-direct-v1-20261004T152933Z"

type replayDecision struct {
	Photo    string               `json:"photo_id"`
	Artist   string               `json:"artist"`
	Title    string               `json:"title"`
	VideoID  string               `json:"video_id,omitempty"`
	V1Status string               `json:"v1_status"`
	V1Reason string               `json:"v1_reason,omitempty"`
	V2Status string               `json:"v2_status"`
	V2Reason string               `json:"v2_reason,omitempty"`
	Evidence directmusic.Evidence `json:"evidence"`
	Scope    string               `json:"scope"`
}

// Replay never constructs a provider client. The v1 full metadata of each winner
// is usable, but loser checks contain only title/channel snippets. Missing fields
// are not filled with guessed duration, licensing or playability.
func offlineReplay(out string, cfg directmusic.Config) error {
	failures := []directmusic.Resolution{}
	checks := []replayDecision{}
	accepted := []replayDecision{}
	distributions := map[string]int{}
	strongCounts := map[string]int{}
	identityCounts := map[string]int{}
	for _, photo := range []string{"still-life", "night-city"} {
		b, err := os.ReadFile(filepath.Join(v1EvidenceRoot, "per-photo", photo, "resolver-results.json"))
		if err != nil {
			return err
		}
		var rows []directmusic.Resolution
		if err = json.Unmarshal(b, &rows); err != nil {
			return err
		}
		for _, r := range rows {
			if r.Status == "UNRESOLVED" {
				failures = append(failures, r)
				if len(r.Checks) == 0 {
					checks = append(checks, replayDecision{Photo: photo, Artist: r.Candidate.Artist, Title: r.Candidate.Title, V1Status: r.Status, V1Reason: r.Reason, V2Status: "UNRESOLVED_NO_CANDIDATE", V2Reason: "controlled_token_fallback_requires_live_search", Scope: "zero recorded candidates; no new search in replay"})
				}
				for _, v := range r.Checks {
					snippet := model.YouTubeVideo{VideoID: v.VideoID, Title: v.Title, ChannelTitle: v.Channel}
					e, reason := directmusic.CheckTextIdentity(r.Candidate, snippet, nil)
					status := "UNRESOLVED_IDENTITY"
					if reason == "" {
						status = "TEXT_IDENTITY_MATCH_REQUIRES_METADATA"
					}
					if reason == "UNREQUESTED_VERSION" {
						status = "UNRESOLVED_VERSION"
					}
					checks = append(checks, replayDecision{photo, r.Candidate.Artist, r.Candidate.Title, v.VideoID, r.Status, v.Reason, status, reason, e, "snippet-only: no recorded losing video duration, licensed flag, region, channel ID or full description; cannot claim playable resolution"})
				}
			}
			if r.Status == "RESOLVED" && r.Video != nil {
				e, reason := directmusic.CheckIdentity(r.Candidate, *r.Video, cfg)
				status := directmusic.ResolvedStatus(e)
				if reason != "" {
					status = "UNRESOLVED_IDENTITY"
					if reason == "UNREQUESTED_VERSION" {
						status = "UNRESOLVED_VERSION"
					}
				} else {
					identityCounts[photo]++
					if directmusic.StrongOfficialEvidence(e) {
						strongCounts[photo]++
					}
				}
				accepted = append(accepted, replayDecision{photo, r.Candidate.Artist, r.Candidate.Title, r.Video.VideoID, r.Status, "", status, reason, e, "full recorded winning video metadata; same ID revalidation, no alternate-ID search"})
				distributions[e.Officiality]++
			}
		}
	}
	regression := 0
	for _, x := range accepted {
		if !directmusic.IsResolved(x.V2Status) {
			regression++
		}
	}
	for name, v := range map[string]any{
		"v1-known-failures.json":      failures,
		"offline-v2-replay.json":      map[string]any{"source": v1EvidenceRoot, "external_api_calls": 0, "decisions": checks, "notes": []string{"No fake metadata backfill", "Corcovado relaxation still needs full release evidence", "WA-R-R localized relation remains unsupported: no provider alias proof in recorded data", "Naked as We Came primary had no candidates; second token search needs live validation"}},
		"accepted-v1-regression.json": map[string]any{"total": len(accepted), "same_id_accepted": len(accepted) - regression, "same_id_rejected": regression, "alternative_ids_selected": 0, "alternative_ids_not_evaluated": true, "decisions": accepted},
		"officiality-analysis.json":   map[string]any{"v1_final_metadata_categories": distributions, "v2_identity_only_counts": identityCounts, "v2_stronger_evidence_counts": strongCounts, "strong_policy_categories": []string{"topic", "vevo", "licensed"}, "official_artist_name_is_not_oac_attestation": true, "policy_B_experiment_only": true},
		"resolver-policy.json":        map[string]any{"version": directmusic.ResolverVersion, "gemini_prompt_unchanged": true, "prompt_version": "direct_music_prompt_v1", "title_prefixes": "bounded whitelist only; raw metadata preserved", "credit_relaxation": "exact title boundary + main credited artist + at least two overlapping credits + >=50% coverage + Topic/VEVO/licensed metadata", "localized_alias": "empty default; explicit provider evidence must bind canonical/provider title, video ID and channel ID", "primary_max": 1, "fallback_max": 1, "fallback": "artist/title tokens only; only after successful primary with no accepted identity; never after API failure", "max_search_calls_per_photo": cfg.MaxSearchCalls, "language_policy": "none", "ordinary_channels": "acceptable lexical identity; never labelled guaranteed official", "version_policy": "remix/live/cover/acoustic/demo/version/edit preserved and rejected unless requested", "negative_cache": "v2 policy key, never use v1 negatives", "positive_cache": "v1 positives may migrate after v2 metadata revalidation", "replay_external_calls": 0},
	} {
		if err := write(out, name, v); err != nil {
			return err
		}
	}
	fmt.Printf("offline_replay accepted=%d preserved=%d regressions=%d failures=%d external_calls=0\n", len(accepted), len(accepted)-regression, regression, len(failures))
	return nil
}
