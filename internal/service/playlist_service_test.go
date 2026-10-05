package service

import (
	"context"
	"errors"
	"example.com/sync/internal/auth"
	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"
)

type fakePlaylistSession struct{ err error }

func (f fakePlaylistSession) WithAuthenticatedClient(ctx context.Context, id string, fn func(*http.Client) error) error {
	if f.err != nil {
		return f.err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(&http.Client{})
}

type fakePlaylistClient struct {
	created     int
	privacy     string
	ids         []string
	positions   []int64
	fail        map[string]error
	createError error
	cancel      context.CancelFunc
}

func (f *fakePlaylistClient) CreatePlaylist(ctx context.Context, r model.CreatePlaylistRequest) (*model.PlaylistResult, error) {
	f.created++
	f.privacy = r.PrivacyStatus
	if f.createError != nil {
		return nil, f.createError
	}
	return &model.PlaylistResult{ID: "PL-test", Title: r.Title, PrivacyStatus: r.PrivacyStatus, URL: client.PlaylistURL("PL-test")}, nil
}
func (f *fakePlaylistClient) AddVideo(ctx context.Context, playlist, id string, position int64) (string, error) {
	f.ids = append(f.ids, id)
	f.positions = append(f.positions, position)
	if f.cancel != nil {
		f.cancel()
	}
	if err := f.fail[id]; err != nil {
		return "", err
	}
	return "item-" + id, nil
}
func playlistRequest(ids ...string) model.CreatePlaylistRequest {
	r := model.CreatePlaylistRequest{Title: " Sync Test "}
	for _, id := range ids {
		r.Tracks = append(r.Tracks, model.PlaylistTrack{VideoID: id})
	}
	return r
}
func playlistService(fake *fakePlaylistClient, sessionError error) *PlaylistService {
	return NewPlaylistService(fakePlaylistSession{sessionError}, func(context.Context, *http.Client) (client.PlaylistClient, error) { return fake, nil }, time.Second)
}
func TestPlaylistSuccessOrderAndDedup(t *testing.T) {
	for _, count := range []int{1, 10, 20} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			ids := []string{}
			for i := 0; i < count; i++ {
				ids = append(ids, fmt.Sprintf("video-%d", i))
			}
			f := &fakePlaylistClient{}
			response, err := playlistService(f, nil).Create(context.Background(), "session", playlistRequest(ids...))
			if err != nil {
				t.Fatal(err)
			}
			if response.AddedCount != count || response.FailedCount != 0 || response.Partial || f.privacy != "private" || !reflect.DeepEqual(f.ids, ids) {
				t.Fatal("wrong playlist result or order")
			}
			for i, p := range f.positions {
				if p != int64(i) {
					t.Fatal("wrong position")
				}
			}
		})
	}
	f := &fakePlaylistClient{}
	response, err := playlistService(f, nil).Create(context.Background(), "session", playlistRequest("A", "B", "A", "C"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.ids, []string{"A", "B", "C"}) || response.RequestedCount != 3 || response.SubmittedCount != 4 {
		t.Fatal("duplicate removal failed")
	}
}
func TestPlaylistPartialFailures(t *testing.T) {
	for _, failedID := range []string{"A", "B"} {
		t.Run(failedID, func(t *testing.T) {
			f := &fakePlaylistClient{fail: map[string]error{failedID: &client.PlaylistError{Code: "VIDEO_NOT_FOUND"}}}
			r, err := playlistService(f, nil).Create(context.Background(), "session", playlistRequest("A", "B", "C"))
			if err != nil {
				t.Fatal(err)
			}
			if r.Playlist.ID != "PL-test" || r.AddedCount != 2 || r.FailedCount != 1 || !r.Partial || len(f.ids) != 3 {
				t.Fatal("partial results lost")
			}
			if !reflect.DeepEqual(f.positions, []int64{0, map[string]int64{"A": 0, "B": 1}[failedID], 1}) {
				t.Fatal("position did not advance by acknowledged success")
			}
		})
	}
}
func TestPlaylistStopsOnFatalFailures(t *testing.T) {
	for _, code := range []string{"YOUTUBE_QUOTA_EXCEEDED", "YOUTUBE_AUTH_EXPIRED", "PLAYLIST_NOT_FOUND", "VIDEO_ADD_RESULT_UNKNOWN", "YOUTUBE_SERVICE_UNAVAILABLE"} {
		t.Run(code, func(t *testing.T) {
			f := &fakePlaylistClient{fail: map[string]error{"B": &client.PlaylistError{Code: code}}}
			r, err := playlistService(f, nil).Create(context.Background(), "session", playlistRequest("A", "B", "C", "D"))
			if err != nil {
				t.Fatal(err)
			}
			if r.AddedCount != 1 || r.FailedCount != 3 || len(f.ids) != 2 || r.Items[2].Attempted || r.Items[2].ErrorCode != code {
				t.Fatal("fatal failure did not stop writes")
			}
		})
	}
}
func TestPlaylistCreateFailureAndAuth(t *testing.T) {
	f := &fakePlaylistClient{createError: &client.PlaylistError{Code: "PLAYLIST_CREATE_FAILED"}}
	if _, err := playlistService(f, nil).Create(context.Background(), "session", playlistRequest("A")); err == nil || len(f.ids) != 0 {
		t.Fatal("items added after create failure")
	}
	f = &fakePlaylistClient{}
	if _, err := playlistService(f, nil).Create(context.Background(), "", playlistRequest("A")); !errors.Is(err, auth.ErrSessionNotFound) || f.created != 0 {
		t.Fatal("missing session accepted")
	}
	for _, failure := range []error{auth.ErrSessionNotFound, auth.ErrAuthExpired} {
		if _, err := playlistService(f, failure).Create(context.Background(), "session", playlistRequest("A")); !errors.Is(err, failure) || f.created != 0 {
			t.Fatal("auth failure ignored")
		}
	}
}
func TestPlaylistCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakePlaylistClient{}
	if _, err := playlistService(f, nil).Create(ctx, "session", playlistRequest("A")); !errors.Is(err, context.Canceled) || f.created != 0 {
		t.Fatal("pre-write cancellation ignored")
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	f = &fakePlaylistClient{cancel: cancel}
	response, err := playlistService(f, nil).Create(ctx, "session", playlistRequest("A", "B", "C"))
	if err != nil {
		t.Fatal(err)
	}
	if response.AddedCount != 1 || response.FailedCount != 2 || len(f.ids) != 1 || response.Items[1].Attempted || response.Items[1].ErrorCode != "REQUEST_CANCELED" {
		t.Fatal("created playlist lost after cancellation")
	}
}
