package directe2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"example.com/sync/internal/client"
	"example.com/sync/internal/directmusic"
	imageproc "example.com/sync/internal/image"
	"example.com/sync/internal/model"
	"fmt"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type generator struct{ calls int }

func (g *generator) RecommendTracks(_ context.Context, _ []byte, _ string, max int) (*model.DirectMusicRecommendation, error) {
	g.calls++
	if max != 8 {
		return nil, fmt.Errorf("wrong candidate maximum")
	}
	r := &model.DirectMusicRecommendation{}
	for i := 0; i < 8; i++ {
		r.Tracks = append(r.Tracks, model.DirectTrack{Artist: fmt.Sprintf("Artist %d", i), Title: fmt.Sprintf("Song %d", i), FitScore: .8})
	}
	return r, nil
}

type search struct {
	calls  int
	empty  bool
	videos map[string]model.YouTubeVideo
}

func (s *search) SearchMusic(_ context.Context, q model.MusicSearchQuery) ([]model.YouTubeSearchResult, error) {
	s.calls++
	if s.empty {
		return []model.YouTubeSearchResult{}, nil
	}
	parts := strings.SplitN(q.Text, " Song ", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("broad query")
	}
	id := fmt.Sprint(s.calls)
	s.videos[id] = model.YouTubeVideo{VideoID: id, Title: parts[0] + " - Song " + parts[1], ChannelTitle: parts[0] + " - Topic", Public: true, Embeddable: true, CategoryID: "10", DurationSeconds: 240}
	return []model.YouTubeSearchResult{{VideoID: id}}, nil
}
func (s *search) GetVideos(_ context.Context, ids []string) ([]model.YouTubeVideo, error) {
	out := []model.YouTubeVideo{}
	for _, id := range ids {
		out = append(out, s.videos[id])
	}
	return out, nil
}

type processor struct{}

func (processor) Process(_ context.Context, b []byte, m string) (*imageproc.ProcessedImage, error) {
	return &imageproc.ProcessedImage{Data: b, MIMEType: m}, nil
}
func setup(t *testing.T) (*Runner, *generator, *search, model.UploadedImage) {
	t.Helper()
	c, _ := Config(5)
	g := &generator{}
	s := &search{videos: map[string]model.YouTubeVideo{}}
	cache, _ := directmusic.NewCache("")
	svc := &directmusic.Service{Generator: g, Resolver: &directmusic.Resolver{Client: s, Cache: cache, Config: c}, Config: c}
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	im := model.UploadedImage{ImageInfo: model.ImageInfo{ContentType: "image/png"}, Data: b.Bytes()}
	r := &Runner{Service: svc, Processor: processor{}, Budget: client.NewSearchBudget(9), Output: t.TempDir(), ImageID: "fixture", ImageHash: fmt.Sprintf("%x", sha256.Sum256(im.Data))}
	return r, g, s, im
}
func TestSingleRunEarlyStopCheckpointCacheAndAdapter(t *testing.T) {
	r, g, s, im := setup(t)
	out, e := r.Run(context.Background(), im)
	if e != nil || len(out.Tracks) != 5 || g.calls != 1 || s.calls != 5 {
		t.Fatal(out, e, g.calls, s.calls)
	}
	if out.RequestedCount != 5 || out.ReturnedCount != 5 || out.Partial {
		t.Fatal(out)
	}
	if _, e = r.Run(context.Background(), im); e != nil || g.calls != 1 || s.calls != 5 {
		t.Fatal("regenerated")
	}
	cp, e := Load(r.Output)
	if e != nil || len(cp.Tracks) != 5 || cp.Tracks[0].Artist == "" || cp.Tracks[0].Rank != 1 {
		t.Fatal(cp, e)
	}
	b, _ := json.Marshal(out)
	var legacy model.RecommendationResponse
	if json.Unmarshal(b, &legacy) != nil || len(legacy.Tracks) != 5 {
		t.Fatal("response contract")
	}
	// Second service shares verified positive cache, but revalidates current metadata.
	c := r.Service.Config
	resolver := &directmusic.Resolver{Client: s, Cache: r.Service.Resolver.Cache, Config: c}
	svc := &directmusic.Service{Generator: g, Resolver: resolver, Config: c}
	result, e := svc.Run(context.Background(), im.Data, im.ContentType)
	if e != nil || len(result.Final) != 5 || s.calls != 5 || result.Diagnostics.Calls.CacheHits != 5 {
		t.Fatal("positive cache not used first", e, result.Diagnostics)
	}
}
func TestPartialBudgetAndDefaults(t *testing.T) {
	if directmusic.DefaultConfig().FinalCount != 10 || directmusic.DefaultConfig().CandidateCount != 20 {
		t.Fatal("product defaults changed")
	}
	if _, e := Config(6); e == nil {
		t.Fatal("E2E cap missing")
	}
	r, _, s, im := setup(t)
	s.empty = true
	out, e := r.Run(context.Background(), im)
	if e != nil || len(out.Tracks) != 0 || !out.Partial || s.calls != 8 {
		t.Fatal(out, e, s.calls)
	}
	if _, e := Load(r.Output); e != nil {
		t.Fatal(e)
	}
	for _, n := range []int{0, 1, 3, 4, 5} {
		v := Adapt(&directmusic.Result{Final: make([]directmusic.VerifiedTrack, n)}, 5)
		if len(v.Tracks) != n || v.Partial != (n < 5) {
			t.Fatal(v)
		}
	}
}
func TestHTTPDirectUploadAndNoRepeat(t *testing.T) {
	r, g, s, im := setup(t)
	a := &App{Runner: r, Base: http.NotFoundHandler()}
	for i := 0; i < 2; i++ {
		var body bytes.Buffer
		m := multipart.NewWriter(&body)
		p, _ := m.CreateFormFile("image", "test.png")
		p.Write(im.Data)
		m.Close()
		req := httptest.NewRequest("POST", "/api/v1/recommend", &body)
		req.Header.Set("Content-Type", m.FormDataContentType())
		w := httptest.NewRecorder()
		a.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if g.calls != 1 || s.calls != 5 {
		t.Fatal("duplicate request spent providers")
	}
	req := httptest.NewRequest("POST", "/api/v1/recommend", strings.NewReader(`{"analysis":{}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != 415 {
		t.Fatal("silent legacy fallback")
	}
	// Unresolved IDs and public playlists are rejected before existing write service.
	for _, body := range []string{`{"title":"test","privacy_status":"public","tracks":[{"video_id":"x"}]}`, `{"title":"test","privacy_status":"private","tracks":[{"video_id":"unverified"}]}`} {
		req := httptest.NewRequest("POST", "/api/v1/playlists", strings.NewReader(body))
		w := httptest.NewRecorder()
		a.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
}
