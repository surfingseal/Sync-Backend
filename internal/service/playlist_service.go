package service

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"example.com/sync/internal/auth"
	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
)

const DefaultPlaylistTimeout = 120 * time.Second

type PlaylistSessionProvider interface {
	WithAuthenticatedClient(context.Context, string, func(*http.Client) error) error
}
type PlaylistClientFactory func(context.Context, *http.Client) (client.PlaylistClient, error)
type PlaylistService struct {
	sessions PlaylistSessionProvider
	factory  PlaylistClientFactory
	timeout  time.Duration
}

func NewPlaylistService(sessions PlaylistSessionProvider, factory PlaylistClientFactory, timeout time.Duration) *PlaylistService {
	if timeout <= 0 {
		timeout = DefaultPlaylistTimeout
	}
	return &PlaylistService{sessions, factory, timeout}
}
func (s *PlaylistService) Create(ctx context.Context, session string, request model.CreatePlaylistRequest) (*model.CreatePlaylistResponse, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if session == "" {
		return nil, auth.ErrSessionNotFound
	}
	if s.sessions == nil || s.factory == nil {
		return nil, auth.ErrConfiguration
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	ids := make([]string, 0, len(request.Tracks))
	seen := make(map[string]bool)
	for _, track := range request.Tracks {
		if !seen[track.VideoID] {
			seen[track.VideoID] = true
			ids = append(ids, track.VideoID)
		}
	}
	var response *model.CreatePlaylistResponse
	err := s.sessions.WithAuthenticatedClient(ctx, session, func(httpClient *http.Client) error {
		api, err := s.factory(ctx, httpClient)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		playlist, err := api.CreatePlaylist(ctx, request)
		if err != nil {
			return err
		}
		if playlist == nil || playlist.ID == "" {
			return &client.PlaylistError{Code: "PLAYLIST_CREATE_RESULT_UNKNOWN"}
		}
		response = &model.CreatePlaylistResponse{Playlist: *playlist, SubmittedCount: len(request.Tracks), RequestedCount: len(ids), Items: make([]model.PlaylistItemResult, 0, len(ids))}
		stopCode := ""
		for _, id := range ids {
			item := model.PlaylistItemResult{VideoID: id, Status: "failed"}
			if stopCode == "" && ctx.Err() != nil {
				stopCode = client.PlaylistErrorCode(ctx.Err())
			}
			if stopCode != "" {
				item.ErrorCode = stopCode
			} else {
				item.Attempted = true
				itemID, err := api.AddVideo(ctx, playlist.ID, id, int64(response.AddedCount))
				if err == nil && itemID == "" {
					err = &client.PlaylistError{Code: "VIDEO_ADD_RESULT_UNKNOWN"}
				}
				if err == nil {
					item.Status = "added"
					item.PlaylistItemID = itemID
					response.AddedCount++
				} else {
					item.ErrorCode = client.PlaylistErrorCode(err)
					log.Printf("playlist item failed code=%s", item.ErrorCode)
					switch item.ErrorCode {
					case "YOUTUBE_QUOTA_EXCEEDED", "YOUTUBE_AUTH_EXPIRED", "PLAYLIST_NOT_FOUND", "YOUTUBE_SERVICE_UNAVAILABLE", "VIDEO_ADD_RESULT_UNKNOWN", "PLAYLIST_TIMEOUT", "REQUEST_CANCELED":
						stopCode = item.ErrorCode
					}
				}
			}
			if item.Status != "added" {
				response.FailedCount++
			}
			response.Items = append(response.Items, item)
		}
		response.Partial = response.FailedCount > 0
		log.Printf("playlist operation submitted=%d unique=%d added=%d failed=%d", response.SubmittedCount, response.RequestedCount, response.AddedCount, response.FailedCount)
		return nil
	})
	// Preserve the known created playlist even if a session wrapper reports a later error.
	if response != nil {
		return response, nil
	}
	if errors.Is(err, auth.ErrSessionNotFound) {
		return nil, auth.ErrSessionNotFound
	}
	return response, err
}
