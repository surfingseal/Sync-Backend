package directapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"example.com/sync/internal/auth"
	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
	"example.com/sync/internal/service"
)

func save(t *testing.T, s *Store, n int) *Response {
	t.Helper()
	r := Response{TargetTrackCount: 10, Partial: n < 10, Tracks: []Track{}}
	for i := 0; i < n; i++ {
		r.Tracks = append(r.Tracks, Track{Rank: i + 1, Artist: fmt.Sprint("artist", i), TrackTitle: "track", VideoID: fmt.Sprint("video", i), FitScore: .9})
	}
	out, err := s.Save(context.Background(), r, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestCheckpointLifecycle(t *testing.T) {
	s := NewStore()
	r := save(t, s, 3)
	r.Tracks[0].VideoID = "tamper"
	cp, err := s.Get(context.Background(), r.RecommendationID)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Response.Tracks[0].VideoID != "video0" || cp.Response.Tracks[2].Rank != 3 || cp.ResolverVersion == "" {
		t.Fatal(cp)
	}
	cp.Response.Tracks[0].VideoID = "tamper2"
	cp, _ = s.Get(context.Background(), r.RecommendationID)
	if cp.Response.Tracks[0].VideoID != "video0" {
		t.Fatal("mutable store")
	}
	if _, err = s.Get(context.Background(), "invalid"); !errors.Is(err, ErrInvalidID) {
		t.Fatal(err)
	}
	other := save(t, NewStore(), 1)
	if _, err = s.Get(context.Background(), other.RecommendationID); !errors.Is(err, ErrMissing) {
		t.Fatal(err)
	}
	s.now = func() time.Time { return cp.ExpiresAt }
	if _, err = s.Get(context.Background(), r.RecommendationID); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.Get(ctx, r.RecommendationID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type session struct{ err error }

func (s session) WithAuthenticatedClient(ctx context.Context, id string, f func(*http.Client) error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if id == "" {
		return auth.ErrSessionNotFound
	}
	if s.err != nil {
		return s.err
	}
	return f(&http.Client{})
}

type writer struct {
	mu     sync.Mutex
	calls  int
	ids    []string
	err    error
	failAt int
}

func (w *writer) CreatePlaylist(_ context.Context, r model.CreatePlaylistRequest) (*model.PlaylistResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	if w.err != nil {
		return nil, w.err
	}
	return &model.PlaylistResult{ID: "mock-playlist", Title: r.Title, PrivacyStatus: r.PrivacyStatus}, nil
}
func (w *writer) AddVideo(_ context.Context, _ string, id string, position int64) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ids = append(w.ids, id)
	if w.failAt == len(w.ids) {
		return "", &client.PlaylistError{Code: "VIDEO_NOT_FOUND"}
	}
	return "item-" + id, nil
}
func playlistFixture(t *testing.T, n int) (*Playlists, PlaylistRequest, *writer) {
	s := NewStore()
	r := save(t, s, n)
	w := &writer{}
	sessions := session{}
	creator := service.NewPlaylistService(sessions, func(context.Context, *http.Client) (client.PlaylistClient, error) { return w, nil }, time.Minute)
	return NewPlaylists(s, sessions, creator), PlaylistRequest{RecommendationID: r.RecommendationID, Title: " test "}, w
}
func TestPlaylistCheckpointRetryAndOrder(t *testing.T) {
	s, r, w := playlistFixture(t, 5)
	// Corrupt duplicate in an internal fixture: first appearance remains authoritative.
	s.Store.mu.Lock()
	cp := s.Store.entries[r.RecommendationID]
	cp.Response.Tracks[2].VideoID = "video0"
	s.Store.entries[r.RecommendationID] = cp
	s.Store.mu.Unlock()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := s.Create(context.Background(), "mock-session", r)
			if err != nil || out.AddedCount != 4 || out.Playlist.PrivacyStatus != "private" {
				t.Errorf("%+v %v", out, err)
			}
		}()
	}
	wg.Wait()
	if w.calls != 1 || fmt.Sprint(w.ids) != "[video0 video1 video3 video4]" {
		t.Fatalf("calls=%d order=%v", w.calls, w.ids)
	}
	r.Title = "different"
	if _, err := s.Create(context.Background(), "mock-session", r); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
func TestPlaylistFailureRetryBoundary(t *testing.T) {
	t.Run("partial replay", func(t *testing.T) {
		s, r, w := playlistFixture(t, 3)
		w.failAt = 2
		out, err := s.Create(context.Background(), "session", r)
		if err != nil || !out.Partial || out.AddedCount != 2 {
			t.Fatal(out, err)
		}
		out.Items[0].VideoID = "tamper"
		again, err := s.Create(context.Background(), "session", r)
		if err != nil || again.Items[0].VideoID != "video0" || w.calls != 1 || len(w.ids) != 3 {
			t.Fatal(again, err)
		}
	})
	t.Run("ambiguous write held", func(t *testing.T) {
		s, r, w := playlistFixture(t, 1)
		w.err = context.DeadlineExceeded
		_, err := s.Create(context.Background(), "session", r)
		if err == nil {
			t.Fatal("expected failure")
		}
		_, err = s.Create(context.Background(), "session", r)
		if !errors.Is(err, ErrUnknownWrite) || w.calls != 1 {
			t.Fatal(err, w.calls)
		}
	})
	t.Run("disconnected", func(t *testing.T) {
		s, r, w := playlistFixture(t, 1)
		s.Sessions = session{err: auth.ErrSessionNotFound}
		if _, err := s.Create(context.Background(), "cookie", r); !errors.Is(err, auth.ErrSessionNotFound) || w.calls != 0 {
			t.Fatal(err)
		}
		s.Sessions = session{}
		if _, err := s.Create(context.Background(), "cookie", r); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("no cookie", func(t *testing.T) {
		s, r, _ := playlistFixture(t, 1)
		if _, err := s.Create(context.Background(), "", r); !errors.Is(err, auth.ErrSessionNotFound) {
			t.Fatal(err)
		}
	})
	t.Run("empty", func(t *testing.T) {
		s, r, w := playlistFixture(t, 0)
		if _, err := s.Create(context.Background(), "cookie", r); !errors.Is(err, ErrEmpty) || w.calls != 0 {
			t.Fatal(err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		s, r, w := playlistFixture(t, 1)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := s.Create(ctx, "cookie", r); !errors.Is(err, context.Canceled) || w.calls != 0 {
			t.Fatal(err)
		}
	})
}
func TestStrictPlaylistContract(t *testing.T) {
	s := NewStore()
	r := save(t, s, 1)
	for _, extra := range []string{`,"tracks":[{"video_id":"client-modified"}]`, `,"privacy_status":"public"`, `,"verified":true`} {
		body := fmt.Sprintf(`{"recommendation_id":%q,"title":"test"%s}`, r.RecommendationID, extra)
		if _, err := DecodePlaylist([]byte(body)); err == nil {
			t.Fatal(body)
		}
	}
	body := fmt.Sprintf(`{"recommendation_id":%q,"title":"test"}`, r.RecommendationID)
	req, err := DecodePlaylist([]byte(body))
	if err != nil || req.PrivacyStatus != "private" {
		t.Fatal(req, err)
	}
	b, _ := json.Marshal(r)
	var fields map[string]any
	if err = json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["recommendation_id"]; !ok {
		t.Fatal(fields)
	}
}
