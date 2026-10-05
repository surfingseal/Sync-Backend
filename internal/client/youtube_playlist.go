package client

import (
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"example.com/sync/internal/auth"
	"example.com/sync/internal/model"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

type PlaylistClient interface {
	CreatePlaylist(context.Context, model.CreatePlaylistRequest) (*model.PlaylistResult, error)
	AddVideo(context.Context, string, string, int64) (string, error)
}
type PlaylistError struct{ Code string }

func (e *PlaylistError) Error() string { return e.Code }
func PlaylistErrorCode(err error) string {
	var problem *PlaylistError
	if errors.As(err, &problem) {
		return problem.Code
	}
	if errors.Is(err, auth.ErrAuthExpired) {
		return "YOUTUBE_AUTH_EXPIRED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "PLAYLIST_TIMEOUT"
	}
	if errors.Is(err, context.Canceled) {
		return "REQUEST_CANCELED"
	}
	return "VIDEO_ADD_FAILED"
}

type YouTubePlaylistClient struct{ api *youtube.Service }

func NewYouTubePlaylistClient(ctx context.Context, httpClient *http.Client) (PlaylistClient, error) {
	if httpClient == nil {
		return nil, &PlaylistError{Code: "YOUTUBE_AUTH_EXPIRED"}
	}
	api, err := youtube.NewService(ctx, option.WithHTTPClient(httpClient), option.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		return nil, &PlaylistError{Code: "PLAYLIST_CREATE_FAILED"}
	}
	return &YouTubePlaylistClient{api}, nil
}
func (y *YouTubePlaylistClient) CreatePlaylist(ctx context.Context, r model.CreatePlaylistRequest) (*model.PlaylistResult, error) {
	start := time.Now()
	result, err := y.api.Playlists.Insert([]string{"snippet", "status"}, &youtube.Playlist{Snippet: &youtube.PlaylistSnippet{Title: r.Title, Description: r.Description}, Status: &youtube.PlaylistStatus{PrivacyStatus: r.PrivacyStatus}}).Context(ctx).Do()
	log.Printf("youtube method=playlists.insert latency=%s success=%t", time.Since(start), err == nil)
	if err != nil {
		return nil, mapPlaylistError(err, true)
	}
	if result == nil || result.Id == "" || result.Snippet == nil || result.Status == nil {
		return nil, &PlaylistError{Code: "PLAYLIST_CREATE_RESULT_UNKNOWN"}
	}
	return &model.PlaylistResult{ID: result.Id, Title: result.Snippet.Title, PrivacyStatus: result.Status.PrivacyStatus, URL: PlaylistURL(result.Id)}, nil
}
func (y *YouTubePlaylistClient) AddVideo(ctx context.Context, playlistID, videoID string, position int64) (string, error) {
	start := time.Now()
	result, err := y.api.PlaylistItems.Insert([]string{"snippet"}, &youtube.PlaylistItem{Snippet: &youtube.PlaylistItemSnippet{PlaylistId: playlistID, Position: position, ForceSendFields: []string{"Position"}, ResourceId: &youtube.ResourceId{Kind: "youtube#video", VideoId: videoID}}}).Context(ctx).Do()
	log.Printf("youtube method=playlistItems.insert latency=%s success=%t", time.Since(start), err == nil)
	if err != nil {
		return "", mapPlaylistError(err, false)
	}
	if result == nil || result.Id == "" {
		return "", &PlaylistError{Code: "VIDEO_ADD_RESULT_UNKNOWN"}
	}
	return result.Id, nil
}
func PlaylistURL(id string) string {
	return "https://www.youtube.com/playlist?" + url.Values{"list": []string{id}}.Encode()
}
func mapPlaylistError(err error, creating bool) error {
	if errors.Is(err, auth.ErrAuthExpired) || errors.Is(err, auth.ErrSessionNotFound) {
		return &PlaylistError{Code: "YOUTUBE_AUTH_EXPIRED"}
	}
	var response *googleapi.Error
	if errors.As(err, &response) {
		for _, item := range response.Errors {
			switch item.Reason {
			case "quotaExceeded", "dailyLimitExceeded":
				return &PlaylistError{Code: "YOUTUBE_QUOTA_EXCEEDED"}
			case "videoNotFound":
				return &PlaylistError{Code: "VIDEO_NOT_FOUND"}
			case "playlistNotFound":
				return &PlaylistError{Code: "PLAYLIST_NOT_FOUND"}
			case "authError", "invalidCredentials":
				return &PlaylistError{Code: "YOUTUBE_AUTH_EXPIRED"}
			}
		}
		if response.Code == 401 {
			return &PlaylistError{Code: "YOUTUBE_AUTH_EXPIRED"}
		}
		if response.Code == 403 && !creating {
			return &PlaylistError{Code: "VIDEO_ADD_FORBIDDEN"}
		}
		// Even a 5xx write response can be ambiguous. Do not retry or continue.
		if response.Code >= 500 {
			return &PlaylistError{Code: map[bool]string{true: "PLAYLIST_CREATE_RESULT_UNKNOWN", false: "VIDEO_ADD_RESULT_UNKNOWN"}[creating]}
		}
		if response.Code == 429 {
			return &PlaylistError{Code: "YOUTUBE_SERVICE_UNAVAILABLE"}
		}
		if creating {
			return &PlaylistError{Code: "PLAYLIST_CREATE_FAILED"}
		}
		return &PlaylistError{Code: "VIDEO_ADD_FAILED"}
	}
	// Transport failures/timeouts may occur AFTER the write was committed.
	return &PlaylistError{Code: map[bool]string{true: "PLAYLIST_CREATE_RESULT_UNKNOWN", false: "VIDEO_ADD_RESULT_UNKNOWN"}[creating]}
}
