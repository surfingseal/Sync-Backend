package lastfmeval

import (
	"context"
	"encoding/json"
	"example.com/sync/internal/lastfm"
	"fmt"
	"os"
)

// CapturedRetriever is explicitly offline. Never uses credentials/network and
// never represents prior provider observations as a new live run/cache hit.
type CapturedRetriever struct{ Tracks map[string][]lastfm.Track }

func LoadCaptured(auditPath, retrievalPath string) (*CapturedRetriever, error) {
	c := &CapturedRetriever{Tracks: map[string][]lastfm.Track{}}
	if auditPath != "" {
		b, e := os.ReadFile(auditPath)
		if e != nil {
			return nil, e
		}
		var a lastfm.Audit
		if e = json.Unmarshal(b, &a); e != nil {
			return nil, e
		}
		for _, t := range a.Tags {
			if len(t.Samples) > 0 {
				tracks := []lastfm.Track{}
				for _, s := range t.Samples {
					tracks = append(tracks, s.Track)
				}
				c.Tracks[lastfm.Normalize(t.Name)] = tracks
			}
		}
	}
	if retrievalPath != "" {
		b, e := os.ReadFile(retrievalPath)
		if e != nil {
			return nil, e
		}
		var a Evaluation
		if e = json.Unmarshal(b, &a); e != nil {
			return nil, e
		}
		groups := map[string][]lastfm.Track{}
		for _, r := range a.Raw {
			key := lastfm.Normalize(r.Evidence.LastFMTag)
			groups[key] = append(groups[key], r.Track)
		}
		for tag, tracks := range groups {
			c.Tracks[tag] = tracks
		}
	}
	return c, nil
}
func (c *CapturedRetriever) Fetch(ctx context.Context, tag string, limit int) (FetchResult, error) {
	if ctx.Err() != nil {
		return FetchResult{}, ctx.Err()
	}
	tracks, ok := c.Tracks[lastfm.Normalize(tag)]
	if !ok {
		return FetchResult{}, fmt.Errorf("captured route unavailable")
	}
	if len(tracks) < limit {
		return FetchResult{}, fmt.Errorf("captured sample below requested limit")
	}
	return FetchResult{Tracks: append([]lastfm.Track{}, tracks[:limit]...)}, nil
}
