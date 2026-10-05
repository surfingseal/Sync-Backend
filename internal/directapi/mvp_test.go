package directapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"bytes"
	"example.com/sync/internal/directmusic"
	imageproc "example.com/sync/internal/image"
	"example.com/sync/internal/model"
	"image"
	"image/png"
)

func policyPool(langs ...model.LyricLanguage) []directmusic.VerifiedTrack {
	out := []directmusic.VerifiedTrack{}
	for i, l := range langs {
		out = append(out, directmusic.VerifiedTrack{Gemini: model.DirectTrack{Artist: fmt.Sprint("artist", i), Title: fmt.Sprint("song", i), FitScore: 1 - float64(i)/100, LyricLanguage: l}, VideoID: fmt.Sprint("video", i), VideoTitle: fmt.Sprint("video title", i)})
	}
	return out
}
func TestMVPLanguageSelection(t *testing.T) {
	for _, tc := range []struct {
		name          string
		langs         []model.LyricLanguage
		count, korean int
	}{
		{"reserve-two-low-ranked", []model.LyricLanguage{model.LyricEN, model.LyricEN, model.LyricEN, model.LyricEN, model.LyricEN, model.LyricKO, model.LyricKOEN}, 5, 2},
		{"three-korean", []model.LyricLanguage{model.LyricKO, model.LyricKOEN, model.LyricKO, model.LyricEN, model.LyricInstrumental}, 5, 3},
		{"instrumental-not-korean", []model.LyricLanguage{model.LyricKO, model.LyricInstrumental, model.LyricEN}, 3, 1},
		{"unknown-rejected", []model.LyricLanguage{model.LyricKO, model.LyricKOEN, model.LyricUnknown, model.LyricEN}, 3, 2},
		{"foreign-language-rejected", []model.LyricLanguage{model.LyricKO, model.LyricKOEN, "ja", "es", model.LyricEN}, 3, 2},
		{"four-partial-no-padding", []model.LyricLanguage{model.LyricKO, model.LyricKOEN, model.LyricEN, model.LyricInstrumental}, 4, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected, _ := selectMVP(policyPool(tc.langs...))
			ko := 0
			for _, tr := range selected {
				if tr.Gemini.LyricLanguage.KoreanEligible() {
					ko++
				}
				if !tr.Gemini.LyricLanguage.Allowed() {
					t.Fatal("unknown accepted")
				}
			}
			if len(selected) != tc.count || ko != tc.korean {
				t.Fatal(len(selected), ko)
			}
		})
	}
}
func TestMVPDiversityDuplicatesAndNationality(t *testing.T) {
	pool := policyPool(model.LyricKO, model.LyricKOEN, model.LyricKO, model.LyricEN, model.LyricEN, model.LyricEN, model.LyricEN)
	for i := 0; i < 3; i++ {
		pool[i].Gemini.Artist = "same"
	}
	pool[3].Gemini.Artist = "IU"
	pool[4].Gemini.Artist = "overseas artist"
	pool[4].Gemini.LyricLanguage = model.LyricKO
	pool = append(pool, pool[0])
	selected, _ := selectMVP(pool)
	counts := map[string]int{}
	ids := map[string]bool{}
	for _, tr := range selected {
		counts[tr.Gemini.Artist]++
		if ids[tr.VideoID] {
			t.Fatal("duplicate")
		}
		ids[tr.VideoID] = true
		if tr.Gemini.Artist == "IU" && tr.Gemini.LyricLanguage.KoreanEligible() {
			t.Fatal("nationality inferred")
		}
	}
	if counts["same"] > 2 || len(selected) != 5 {
		t.Fatal(counts)
	}
	if !model.LyricKOEN.KoreanEligible() || model.LyricInstrumental.KoreanEligible() || model.LyricEN.KoreanEligible() {
		t.Fatal("language taxonomy")
	}
}

type mvpFakeRunner struct{ result *directmusic.Result }

func (r mvpFakeRunner) Run(context.Context, []byte, string) (*directmusic.Result, error) {
	return r.result, nil
}

type mvpFakeArtwork struct {
	calls   atomic.Int32
	active  atomic.Int32
	maximum atomic.Int32
	fail    bool
}

func (a *mvpFakeArtwork) Resolve(ctx context.Context, _, _ string) (*model.AlbumArtwork, error) {
	a.calls.Add(1)
	n := a.active.Add(1)
	defer a.active.Add(-1)
	for {
		old := a.maximum.Load()
		if old >= n || a.maximum.CompareAndSwap(old, n) {
			break
		}
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Millisecond):
	}
	if a.fail {
		return nil, fmt.Errorf("provider failed")
	}
	return &model.AlbumArtwork{AlbumTitle: "Album", URL: "https://is1-ssl.mzstatic.com/test.jpg", Source: "itunes"}, nil
}
func mvpResult() *directmusic.Result {
	r := &directmusic.Result{Generated: &model.DirectMusicRecommendation{Scene: model.DirectScene{Description: "가을 밤 불꽃놀이 중인 공원"}, Playlist: model.DirectPlaylist{Title: "Autumn Fireworks"}}}
	r.Final = policyPool(model.LyricEN, model.LyricEN, model.LyricEN, model.LyricEN, model.LyricKO, model.LyricKOEN, model.LyricInstrumental, model.LyricUnknown)
	for _, tr := range r.Final {
		r.Normalized = append(r.Normalized, tr.Gemini)
		r.Resolutions = append(r.Resolutions, directmusic.Resolution{Candidate: tr.Gemini, Status: "RESOLVED", Video: &model.YouTubeVideo{VideoID: tr.VideoID, Title: tr.VideoTitle, ThumbnailURL: "https://i.ytimg.com/test.jpg"}})
	}
	return r
}
func TestMVPContractArtworkAndDefensiveCheckpoint(t *testing.T) {
	for _, fail := range []bool{false, true} {
		art := &mvpFakeArtwork{fail: fail}
		result := mvpResult()
		processor, _ := imageproc.NewProcessor(1920, imageproc.DefaultHardMaxBytes)
		s := &Recommender{Processor: processor, NewRunner: func() Runner { return mvpFakeRunner{result} }, Store: NewStore(), Artwork: art}
		var b bytes.Buffer
		png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1)))
		response, err := s.Recommend(context.Background(), model.UploadedImage{ImageInfo: model.ImageInfo{ContentType: "image/png"}, Data: b.Bytes()})
		if err != nil {
			t.Fatal(err)
		}
		if response.TargetTrackCount != 5 || response.Partial || len(response.Tracks) != 5 || !response.LanguagePolicy.Satisfied || response.LanguagePolicy.ActualKoreanTracks != 2 {
			t.Fatal(response)
		}
		if art.calls.Load() != 5 || art.maximum.Load() > 3 {
			t.Fatal("unbounded enrichment")
		}
		for i, tr := range response.Tracks {
			if tr.Rank != i+1 || tr.YouTubeThumbnailURL != tr.ThumbnailURL || (tr.AlbumArtworkURL == nil) != fail {
				t.Fatal(tr)
			}
		}
		cp, _ := s.Store.Get(context.Background(), response.RecommendationID)
		if cp.Metrics.ArtworkLookups != 5 || cp.Metrics.TotalMS < cp.Metrics.ArtworkMS {
			t.Fatal(cp.Metrics)
		}
		data, _ := json.Marshal(response)
		var wire map[string]any
		json.Unmarshal(data, &wire)
		if wire["scene"] == nil || wire["playlist"] == nil || wire["language_policy"] == nil {
			t.Fatal(string(data))
		}
		for _, key := range []string{"rank", "artist", "track_title", "lyric_language", "korean_eligible", "album_title", "album_artwork_url", "video_id", "youtube_title", "youtube_thumbnail_url", "thumbnail_url", "fit_score"} {
			if _, ok := wire["tracks"].([]any)[0].(map[string]any)[key]; !ok {
				t.Fatal("missing", key)
			}
		}
		response.Scene.Description = "changed"
		if response.Tracks[0].AlbumTitle != nil {
			*response.Tracks[0].AlbumTitle = "changed"
		}
		again, _ := s.Store.Get(context.Background(), response.RecommendationID)
		if again.Response.Scene.Description != cp.Response.Scene.Description || (again.Response.Tracks[0].AlbumTitle != nil && *again.Response.Tracks[0].AlbumTitle == "changed") {
			t.Fatal("checkpoint mutated")
		}
	}
}

func TestSyntheticMVPFixtureIsExplicitAndUsable(t *testing.T) {
	result, err := LoadFixture("testdata/mvp-contract")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Final) != 8 || !model.ValidDirectScene(result.Generated.Scene.Description) {
		t.Fatal("invalid fixture")
	}
	selected, _ := selectMVP(result.Final)
	if len(selected) != 5 {
		t.Fatal(len(selected))
	}
	for _, tr := range selected {
		if tr.VideoID == "" {
			t.Fatal("fixture metadata not parsed")
		}
	}
}

func TestMVPCustomArtistLimitIsPreserved(t *testing.T) {
	pool := policyPool(model.LyricKO, model.LyricKOEN, model.LyricKO, model.LyricEN, model.LyricEN)
	pool[0].Gemini.Artist = "same"
	pool[1].Gemini.Artist = "same"
	selected, _ := selectMVPWithArtistLimit(pool, 1)
	seen := map[string]bool{}
	ko := 0
	for _, tr := range selected {
		if seen[tr.Gemini.Artist] {
			t.Fatal("custom artist limit bypassed")
		}
		seen[tr.Gemini.Artist] = true
		if tr.Gemini.LyricLanguage.KoreanEligible() {
			ko++
		}
	}
	if ko != 2 || len(selected) != 4 {
		t.Fatal(len(selected), ko)
	}
}

func TestMVPPartialLanguageResponseAndCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name          string
		langs         []model.LyricLanguage
		count, korean int
		partial       bool
	}{
		{"A", []model.LyricLanguage{model.LyricKO, model.LyricKOEN, model.LyricEN, model.LyricEN, model.LyricEN}, 5, 2, false},
		{"B", []model.LyricLanguage{model.LyricKO, model.LyricEN, model.LyricEN, model.LyricEN, model.LyricEN}, 5, 1, true},
		{"C", []model.LyricLanguage{model.LyricKO, model.LyricEN, model.LyricEN, model.LyricEN}, 4, 1, true},
		{"D", []model.LyricLanguage{model.LyricEN, model.LyricEN, model.LyricInstrumental}, 3, 0, true},
		{"E", nil, 0, 0, true},
		{"F-and-H", []model.LyricLanguage{model.LyricEN, model.LyricEN, model.LyricEN, model.LyricEN, model.LyricKO, model.LyricKOEN, model.LyricKO}, 5, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := mvpResult()
			result.Final = policyPool(tc.langs...)
			result.Resolutions = nil
			for _, tr := range result.Final {
				result.Resolutions = append(result.Resolutions, directmusic.Resolution{Candidate: tr.Gemini, Status: "RESOLVED_ACCEPTABLE", Video: &model.YouTubeVideo{VideoID: tr.VideoID, Title: tr.VideoTitle}})
			}
			// An unresolved generated candidate must never become padding.
			result.Resolutions = append(result.Resolutions, directmusic.Resolution{Candidate: model.DirectTrack{Artist: "unresolved", Title: "missing", LyricLanguage: model.LyricKO}, Status: "UNRESOLVED_IDENTITY"})
			processor, _ := imageproc.NewProcessor(1920, imageproc.DefaultHardMaxBytes)
			art := &mvpFakeArtwork{fail: true}
			store := NewStore()
			s := &Recommender{Processor: processor, NewRunner: func() Runner { return mvpFakeRunner{result} }, Store: store, Artwork: art}
			var b bytes.Buffer
			png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1)))
			response, err := s.Recommend(context.Background(), model.UploadedImage{ImageInfo: model.ImageInfo{ContentType: "image/png"}, Data: b.Bytes()})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Tracks) != tc.count || response.Partial != tc.partial || response.LanguagePolicy.ActualKoreanTracks != tc.korean || response.LanguagePolicy.Satisfied != (tc.korean >= 2) {
				t.Fatal(response)
			}
			actual := 0
			for i, tr := range response.Tracks {
				if tr.Artist == "unresolved" || tr.Rank != i+1 {
					t.Fatal(tr)
				}
				if tr.KoreanEligible {
					actual++
				}
				if i > 0 && response.Tracks[i-1].FitScore < tr.FitScore {
					t.Fatal("fit order")
				}
			}
			if actual != tc.korean || int(art.calls.Load()) != tc.count {
				t.Fatal("final count/enrichment mismatch")
			}
			cp, err := store.Get(context.Background(), response.RecommendationID)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := json.Marshal(response)
			got, _ := json.Marshal(cp.Response)
			if string(want) != string(got) {
				t.Fatal("checkpoint differs from returned tracks")
			}
			if tc.name == "F-and-H" && cp.Metrics.AvailableKoreanCandidates != 3 {
				t.Fatal("pool and final Korean counts conflated")
			}
		})
	}
}
