package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"example.com/sync/internal/directmusic"
)

func recheckRecorded(root string) error {
	finalChecked := 0
	finalRejected := 0
	acceptanceChanged := 0
	changes := []map[string]any{}
	for _, photo := range []string{"still-life", "night-city"} {
		b, err := os.ReadFile(filepath.Join(root, "live-run", "per-photo", photo, "resolver-results.json"))
		if err != nil {
			return err
		}
		var rows []directmusic.Resolution
		if err = json.Unmarshal(b, &rows); err != nil {
			return err
		}
		for _, r := range rows {
			for _, c := range r.Checks {
				if c.Metadata == nil {
					continue
				}
				_, why := directmusic.CheckIdentity(r.Candidate, *c.Metadata, directmusic.DefaultConfig())
				if c.Accepted != (why == "") {
					acceptanceChanged++
				}
				if c.Reason != why {
					changes = append(changes, map[string]any{"photo": photo, "artist": r.Candidate.Artist, "title": r.Candidate.Title, "video_id": c.VideoID, "old_reason": c.Reason, "current_reason": why})
				}
			}
			if directmusic.IsResolved(r.Status) && r.Video != nil {
				finalChecked++
				if _, why := directmusic.CheckIdentity(r.Candidate, *r.Video, directmusic.DefaultConfig()); why != "" {
					finalRejected++
				}
			}
		}
	}
	target := filepath.Join(root, "current-policy-recheck.json")
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(map[string]any{"current_resolver_policy": directmusic.ResolverVersion, "live_executable_policy": "exact_metadata_v2", "external_api_calls": 0, "final_checked": finalChecked, "final_rejected": finalRejected, "checked_video_acceptance_changes": acceptanceChanged, "failure_reason_changes": changes, "scope": "post-build defensive narrowing: description-only mentions cannot override artist credit; unrelated version-labelled videos do not misclassify a title/artist failure as a version failure; no thresholds or prompt retuning"})
	if ce := f.Close(); err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	fmt.Printf("offline_current_policy_recheck final=%d rejected=%d acceptance_changes=%d reason_changes=%d external_calls=0\n", finalChecked, finalRejected, acceptanceChanged, len(changes))
	return nil
}
