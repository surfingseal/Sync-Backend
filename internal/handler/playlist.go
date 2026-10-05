package handler

import (
	"context"
	"encoding/json"
	"errors"
	"example.com/sync/internal/directapi"
	"io"
	"mime"
	"net/http"
	"net/url"

	"example.com/sync/internal/auth"
	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
	"example.com/sync/internal/service"
	"github.com/gin-gonic/gin"
)

const MaxPlaylistRequestSize int64 = 32 * 1024

type PlaylistHandler struct {
	playlists   *service.PlaylistService
	origin      string
	checkpoints *directapi.Playlists
}

func NewPlaylistHandler(playlists *service.PlaylistService, redirectURL string, checkpoints ...*directapi.Playlists) *PlaylistHandler {
	parsed, _ := url.Parse(redirectURL)
	origin := ""
	if parsed != nil && parsed.Host != "" {
		origin = parsed.Scheme + "://" + parsed.Host
	}
	h := &PlaylistHandler{playlists: playlists, origin: origin}
	if len(checkpoints) > 0 {
		h.checkpoints = checkpoints[0]
	}
	return h
}
func (h *PlaylistHandler) Create(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	// Cookies authorize a write. Reject explicit cross-origin browser requests.
	if c.GetHeader("Sec-Fetch-Site") == "cross-site" || c.GetHeader("Origin") != "" && c.GetHeader("Origin") != h.origin {
		writeError(c, 403, "INVALID_REQUEST_ORIGIN", "same-origin request is required")
		return
	}
	kind, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || kind != "application/json" {
		writeError(c, 415, "UNSUPPORTED_CONTENT_TYPE", "application/json is required")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxPlaylistRequestSize)
	defer c.Request.Body.Close()
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			writeError(c, 413, "REQUEST_TOO_LARGE", "playlist request is too large")
		} else {
			writeError(c, 400, "INVALID_REQUEST", "invalid playlist request")
		}
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) == nil {
		if _, ok := fields["recommendation_id"]; ok {
			h.createCheckpoint(c, data)
			return
		}
	}
	request, err := model.DecodePlaylistRequest(data)
	if err != nil {
		if errors.Is(err, model.ErrPlaylistTooManyTracks) {
			writeError(c, 400, "PLAYLIST_TOO_MANY_TRACKS", "playlist must contain 20 tracks or fewer")
		} else {
			writeError(c, 400, "INVALID_REQUEST", "invalid playlist request")
		}
		return
	}
	session, _ := c.Cookie("sync_session")
	if session == "" {
		writeError(c, 401, "YOUTUBE_NOT_CONNECTED", "YouTube account is not connected")
		return
	}
	if h.playlists == nil {
		writeError(c, 500, "OAUTH_CONFIGURATION_ERROR", "Google OAuth is not configured")
		return
	}
	response, err := h.playlists.Create(c.Request.Context(), session, *request)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrSessionNotFound):
			writeError(c, 401, "YOUTUBE_NOT_CONNECTED", "YouTube account is not connected")
		case errors.Is(err, auth.ErrAuthExpired):
			writeError(c, 401, "YOUTUBE_AUTH_EXPIRED", "reconnect your YouTube account")
		case errors.Is(err, auth.ErrConfiguration):
			writeError(c, 500, "OAUTH_CONFIGURATION_ERROR", "Google OAuth is not configured")
		case errors.Is(err, context.DeadlineExceeded):
			writeError(c, 504, "PLAYLIST_TIMEOUT", "playlist request timed out")
		case errors.Is(err, context.Canceled):
			writeError(c, 408, "REQUEST_CANCELED", "playlist request was canceled")
		default:
			code := client.PlaylistErrorCode(err)
			status := 502
			message := "failed to create playlist"
			switch code {
			case "YOUTUBE_QUOTA_EXCEEDED":
				status = 503
				message = "YouTube service quota is temporarily unavailable"
			case "YOUTUBE_AUTH_EXPIRED":
				status = 401
				message = "reconnect your YouTube account"
			case "PLAYLIST_CREATE_RESULT_UNKNOWN":
				message = "playlist creation could not be confirmed; check YouTube before retrying"
			case "YOUTUBE_SERVICE_UNAVAILABLE":
				status = 503
				message = "YouTube service is temporarily unavailable"
			default:
				code = "PLAYLIST_CREATE_FAILED"
			}
			writeError(c, status, code, message)
		}
		return
	}
	c.JSON(http.StatusCreated, response)
}

func (h *PlaylistHandler) createCheckpoint(c *gin.Context, data []byte) {
	request, err := directapi.DecodePlaylist(data)
	if err != nil {
		writeCheckpointError(c, err)
		return
	}
	session, _ := c.Cookie("sync_session")
	if session == "" {
		writeError(c, 401, "YOUTUBE_NOT_CONNECTED", "YouTube account is not connected")
		return
	}
	if h.checkpoints == nil {
		writeDirectError(c, directapi.ErrUnavailable)
		return
	}
	response, err := h.checkpoints.Create(c.Request.Context(), session, *request)
	if err != nil {
		writeCheckpointError(c, err)
		return
	}
	c.JSON(http.StatusCreated, response)
}
func writeCheckpointError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, directapi.ErrInvalidID):
		writeError(c, 400, "INVALID_RECOMMENDATION_ID", "invalid recommendation_id")
	case errors.Is(err, directapi.ErrMissing):
		writeError(c, 404, "RECOMMENDATION_NOT_FOUND", "recommendation not found")
	case errors.Is(err, directapi.ErrExpired):
		writeError(c, 410, "RECOMMENDATION_EXPIRED", "recommendation has expired")
	case errors.Is(err, directapi.ErrEmpty):
		writeError(c, 409, "NO_VERIFIED_TRACKS", "recommendation contains no verified tracks")
	case errors.Is(err, directapi.ErrConflict):
		writeError(c, 409, "PLAYLIST_REQUEST_CONFLICT", "retry must use the original playlist request")
	case errors.Is(err, directapi.ErrUnknownWrite):
		writeError(c, 502, "PLAYLIST_CREATE_RESULT_UNKNOWN", "check YouTube before retrying; previous write was not confirmed")
	case errors.Is(err, model.ErrInvalidPlaylist):
		writeError(c, 400, "INVALID_REQUEST", "invalid checkpoint playlist request")
	case errors.Is(err, auth.ErrSessionNotFound):
		writeError(c, 401, "YOUTUBE_NOT_CONNECTED", "YouTube account is not connected")
	case errors.Is(err, auth.ErrAuthExpired):
		writeError(c, 401, "YOUTUBE_AUTH_EXPIRED", "reconnect your YouTube account")
	case errors.Is(err, auth.ErrConfiguration):
		writeError(c, 500, "OAUTH_CONFIGURATION_ERROR", "Google OAuth is not configured")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, 504, "PLAYLIST_TIMEOUT", "playlist request timed out")
	case errors.Is(err, context.Canceled):
		writeError(c, 408, "REQUEST_CANCELED", "playlist request was canceled")
	case errors.Is(err, directapi.ErrCapacity), errors.Is(err, directapi.ErrUnavailable):
		writeDirectError(c, err)
	default:
		code := client.PlaylistErrorCode(err)
		status := 502
		if code == "YOUTUBE_QUOTA_EXCEEDED" {
			status = 503
		}
		if code == "YOUTUBE_AUTH_EXPIRED" {
			status = 401
		}
		writeError(c, status, code, "playlist creation failed; do not automatically retry an unconfirmed write")
	}
}
