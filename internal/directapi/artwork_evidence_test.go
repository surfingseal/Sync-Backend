package directapi

import (
	"context"
	"errors"
	"example.com/sync/internal/model"
	"testing"
)

type evidenceArtwork struct {
	evidence string
	fail     bool
	match    bool
}

func (a *evidenceArtwork) Resolve(context.Context, string, string) (*model.AlbumArtwork, error) {
	return nil, errors.New("expected verified evidence path")
}
func (a *evidenceArtwork) ResolveWithArtistEvidence(_ context.Context, _, _, e string) (*model.AlbumArtwork, error) {
	a.evidence = e
	if a.fail {
		return nil, errors.New("provider failure")
	}
	if !a.match {
		return nil, nil
	}
	return &model.AlbumArtwork{AlbumTitle: "Album", URL: "https://is1-ssl.mzstatic.com/image.jpg", Source: "itunes"}, nil
}
func TestArtworkEnrichmentUsesVerifiedEvidenceAndKeepsTrack(t *testing.T) {
	for _, tc := range []struct {
		name        string
		fail, match bool
	}{{"match", false, true}, {"empty", false, false}, {"error", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			a := &evidenceArtwork{fail: tc.fail, match: tc.match}
			s := &Recommender{Artwork: a}
			tracks := []Track{{Artist: "백예린", TrackTitle: "Bye bye my blue", VideoID: "video", YouTubeThumbnailURL: "https://thumbnail"}}
			title := `Yerin Baek(백예린) "Bye bye my blue" M/V`
			m := RecommendationMetrics{}
			s.enrichArtworkWithVideos(context.Background(), tracks, &m, map[string]*model.YouTubeVideo{"video": {Title: title}})
			if a.evidence != title || len(tracks) != 1 || tracks[0].YouTubeThumbnailURL != "https://thumbnail" || (tracks[0].AlbumArtworkURL != nil) != tc.match {
				t.Fatalf("invalid enrichment %+v", tracks)
			}
		})
	}
}
