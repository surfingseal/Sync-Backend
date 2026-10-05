package directapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"example.com/sync/internal/directmusic"
	"example.com/sync/internal/model"
)

// LoadFixture reads a completed checkpoint, never calls providers. It is only
// for offline contract simulation, not analysis of the newly submitted image.
func LoadFixture(dir string) (*directmusic.Result, error) {
	read := func(name string, out any) error {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		return json.Unmarshal(data, out)
	}
	var generated model.DirectMusicRecommendation
	var resolutions []directmusic.Resolution
	if err := read("gemini-candidates.json", &generated); err != nil {
		return nil, err
	}
	if err := read("resolver-results.json", &resolutions); err != nil {
		return nil, err
	}
	normalized, _, _ := directmusic.NormalizeCandidates(generated.Tracks)
	r := &directmusic.Result{Generated: &generated, Normalized: normalized, Resolutions: resolutions, Final: []directmusic.VerifiedTrack{}}
	seen := map[string]bool{}
	for _, rr := range resolutions {
		if directmusic.IsResolved(rr.Status) && rr.Video != nil && rr.Evidence != nil && !seen[rr.Video.VideoID] {
			v := rr.Video
			seen[v.VideoID] = true
			r.Final = append(r.Final, directmusic.VerifiedTrack{Gemini: rr.Candidate, VideoID: v.VideoID, VideoTitle: v.Title, Channel: v.ChannelTitle, DurationSeconds: v.DurationSeconds, ResolutionEvidence: *rr.Evidence})
		}
	}
	return r, nil
}

type FixtureRunner struct{ Result *directmusic.Result }

func (r FixtureRunner) Run(ctx context.Context, _ []byte, _ string) (*directmusic.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := clone(*r.Result)
	return &out, nil
}
