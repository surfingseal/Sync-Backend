package directe2e

import (
	"context"
	"encoding/json"
	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
	"example.com/sync/internal/service"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type sessions struct{}

func (sessions) WithAuthenticatedClient(ctx context.Context, _ string, f func(*http.Client) error) error {
	return f(&http.Client{})
}

type playlist struct {
	creates int
	ids     []string
}

func (p *playlist) CreatePlaylist(_ context.Context, r model.CreatePlaylistRequest) (*model.PlaylistResult, error) {
	p.creates++
	return &model.PlaylistResult{ID: "provider-playlist", Title: r.Title, PrivacyStatus: r.PrivacyStatus}, nil
}
func (p *playlist) AddVideo(_ context.Context, _ string, id string, _ int64) (string, error) {
	p.ids = append(p.ids, id)
	return "provider-item-" + id, nil
}
func TestPlaylistCheckpointOnlyAndNoRepeatedWrites(t *testing.T) {
	r, _, _, im := setup(t)
	r.Run(context.Background(), im)
	p := &playlist{}
	svc := service.NewPlaylistService(sessions{}, func(context.Context, *http.Client) (client.PlaylistClient, error) { return p, nil }, service.DefaultPlaylistTimeout)
	base := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var input model.CreatePlaylistRequest
		json.NewDecoder(req.Body).Decode(&input)
		v, e := svc.Create(req.Context(), "fake-unit-session", input)
		if e != nil {
			t.Error(e)
			return
		}
		respond(w, 201, v)
	})
	app := &App{Runner: r, Base: base}
	request := model.CreatePlaylistRequest{Title: "unit", PrivacyStatus: "private"}
	for _, tr := range r.Checkpoint.Tracks {
		request.Tracks = append(request.Tracks, model.PlaylistTrack{VideoID: tr.VideoID})
	}
	b, _ := json.Marshal(request)
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/playlists", strings.NewReader(string(b)))
		app.ServeHTTP(w, req)
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if p.creates != 1 || len(p.ids) != 5 {
		t.Fatal("write retried", p)
	}
	for i, id := range p.ids {
		if id != r.Checkpoint.Tracks[i].VideoID {
			t.Fatal("unverified insertion")
		}
	}
}
