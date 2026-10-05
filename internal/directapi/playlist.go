package directapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"unicode/utf8"

	"example.com/sync/internal/auth"
	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
	"example.com/sync/internal/service"
)

var (
	ErrConflict     = errors.New("playlist retry payload conflicts")
	ErrUnknownWrite = errors.New("previous playlist result unknown")
	ErrEmpty        = errors.New("checkpoint has no verified tracks")
)

type PlaylistRequest struct {
	RecommendationID string `json:"recommendation_id"`
	Title            string `json:"title"`
	Description      string `json:"description,omitempty"`
	PrivacyStatus    string `json:"privacy_status,omitempty"`
}

func DecodePlaylist(data []byte) (*PlaylistRequest, error) {
	if !utf8.Valid(data) {
		return nil, model.ErrInvalidPlaylist
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var r PlaylistRequest
	if err := d.Decode(&r); err != nil {
		return nil, model.ErrInvalidPlaylist
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, model.ErrInvalidPlaylist
	}
	if !validID.MatchString(r.RecommendationID) {
		return nil, ErrInvalidID
	}
	// Reuse current title/description validation, restrict the new MVP contract to private.
	check := model.CreatePlaylistRequest{Title: r.Title, Description: r.Description, PrivacyStatus: r.PrivacyStatus, Tracks: []model.PlaylistTrack{{VideoID: "validation-only"}}}
	if err := check.Validate(); err != nil {
		return nil, err
	}
	if check.PrivacyStatus != "private" {
		return nil, model.ErrInvalidPlaylist
	}
	r.Title, r.Description, r.PrivacyStatus = check.Title, check.Description, check.PrivacyStatus
	return &r, nil
}

type playlistRun struct {
	fingerprint [32]byte
	done        chan struct{}
	result      *model.CreatePlaylistResponse
	err         error
}

// The key is session + recommendation, so retries cannot produce another
// playlist by varying a client-supplied idempotency key. Markers are not evicted
// during this process: capacity rejection is safer than repeating an old write.
type Playlists struct {
	Store    *Store
	Sessions service.PlaylistSessionProvider
	Creator  *service.PlaylistService
	mu       sync.Mutex
	runs     map[[32]byte]*playlistRun
}

func NewPlaylists(store *Store, sessions service.PlaylistSessionProvider, creator *service.PlaylistService) *Playlists {
	return &Playlists{Store: store, Sessions: sessions, Creator: creator, runs: make(map[[32]byte]*playlistRun)}
}
func (s *Playlists) Create(ctx context.Context, session string, r PlaylistRequest) (*model.CreatePlaylistResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, service.DefaultPlaylistTimeout)
	defer cancel()
	if session == "" {
		return nil, auth.ErrSessionNotFound
	}
	if s == nil || s.Store == nil || s.Creator == nil || s.Sessions == nil {
		return nil, ErrUnavailable
	}
	encoded, _ := json.Marshal(r)
	normalized, err := DecodePlaylist(encoded)
	if err != nil {
		return nil, err
	}
	r = *normalized
	cp, err := s.Store.Get(ctx, r.RecommendationID)
	if err != nil {
		return nil, err
	}
	if len(cp.Response.Tracks) == 0 {
		return nil, ErrEmpty
	}
	// Authenticate even a replay; an opaque session cookie alone is not proof.
	if err := s.Sessions.WithAuthenticatedClient(ctx, session, func(*http.Client) error { return nil }); err != nil {
		return nil, err
	}
	key := sha256.Sum256([]byte(session + "\x00" + r.RecommendationID))
	data, _ := json.Marshal(r)
	fingerprint := sha256.Sum256(data)
	s.mu.Lock()
	if previous := s.runs[key]; previous != nil {
		s.mu.Unlock()
		if previous.fingerprint != fingerprint {
			return nil, ErrConflict
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-previous.done:
		}
		if previous.result != nil {
			out := clone(*previous.result)
			return &out, nil
		}
		return nil, previous.err
	}
	if len(s.runs) >= MaxCheckpoints {
		s.mu.Unlock()
		return nil, ErrCapacity
	}
	run := &playlistRun{fingerprint: fingerprint, done: make(chan struct{})}
	s.runs[key] = run
	s.mu.Unlock()
	req := model.CreatePlaylistRequest{Title: r.Title, Description: r.Description, PrivacyStatus: r.PrivacyStatus, Tracks: []model.PlaylistTrack{}}
	seen := map[string]bool{}
	for _, t := range cp.Response.Tracks {
		if !seen[t.VideoID] {
			seen[t.VideoID] = true
			req.Tracks = append(req.Tracks, model.PlaylistTrack{VideoID: t.VideoID})
		}
	}
	response, err := s.Creator.Create(ctx, session, req)
	s.mu.Lock()
	defer s.mu.Unlock()
	if response != nil {
		copy := clone(*response)
		run.result = &copy
	} else {
		// Auth/config errors are known pre-write failures; allow reconnect + retry.
		// All other failures are held conservatively, even if the provider might
		// have rejected before creation. Never resubmit an ambiguous POST.
		if errors.Is(err, auth.ErrSessionNotFound) || errors.Is(err, auth.ErrAuthExpired) || errors.Is(err, auth.ErrConfiguration) {
			delete(s.runs, key)
			run.err = err
		} else {
			run.err = ErrUnknownWrite
		}
	}
	close(run.done)
	if response == nil && err == nil {
		return nil, &client.PlaylistError{Code: "PLAYLIST_CREATE_RESULT_UNKNOWN"}
	}
	return response, err
}
