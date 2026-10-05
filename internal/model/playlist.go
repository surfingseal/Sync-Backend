package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxPlaylistTracks = 20
const MaxPlaylistTitle = 100
const MaxPlaylistDescription = 4000

var ErrPlaylistTooManyTracks = errors.New("too many playlist tracks")
var ErrInvalidPlaylist = errors.New("invalid playlist request")

type PlaylistTrack struct {
	VideoID string `json:"video_id"`
}
type CreatePlaylistRequest struct {
	Title         string          `json:"title"`
	Description   string          `json:"description"`
	PrivacyStatus string          `json:"privacy_status"`
	Tracks        []PlaylistTrack `json:"tracks"`
}
type PlaylistResult struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	PrivacyStatus string `json:"privacy_status"`
	URL           string `json:"url"`
}
type PlaylistItemResult struct {
	VideoID        string `json:"video_id"`
	Status         string `json:"status"`
	PlaylistItemID string `json:"playlist_item_id,omitempty"`
	ErrorCode      string `json:"error_code,omitempty"`
	Attempted      bool   `json:"attempted"`
}
type CreatePlaylistResponse struct {
	Playlist       PlaylistResult       `json:"playlist"`
	SubmittedCount int                  `json:"submitted_count"`
	RequestedCount int                  `json:"requested_count"`
	AddedCount     int                  `json:"added_count"`
	FailedCount    int                  `json:"failed_count"`
	Partial        bool                 `json:"partial"`
	Items          []PlaylistItemResult `json:"items"`
}

func DecodePlaylistRequest(data []byte) (*CreatePlaylistRequest, error) {
	if !utf8.Valid(data) {
		return nil, ErrInvalidPlaylist
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request CreatePlaylistRequest
	if err := decoder.Decode(&request); err != nil {
		return nil, ErrInvalidPlaylist
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, ErrInvalidPlaylist
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return &request, nil
}
func (r *CreatePlaylistRequest) Validate() error {
	r.Title = strings.TrimSpace(r.Title)
	r.Description = strings.TrimSpace(r.Description)
	if r.Title == "" || !utf8.ValidString(r.Title) || utf8.RuneCountInString(r.Title) > MaxPlaylistTitle || !utf8.ValidString(r.Description) || utf8.RuneCountInString(r.Description) > MaxPlaylistDescription {
		return ErrInvalidPlaylist
	}
	if strings.ContainsAny(r.Title, "\x00\r\n") || strings.ContainsRune(r.Description, '\x00') {
		return ErrInvalidPlaylist
	}
	if r.PrivacyStatus == "" {
		r.PrivacyStatus = "private"
	}
	if r.PrivacyStatus != "private" && r.PrivacyStatus != "public" && r.PrivacyStatus != "unlisted" {
		return ErrInvalidPlaylist
	}
	if len(r.Tracks) > MaxPlaylistTracks {
		return ErrPlaylistTooManyTracks
	}
	if len(r.Tracks) == 0 {
		return ErrInvalidPlaylist
	}
	for i := range r.Tracks {
		id := strings.TrimSpace(r.Tracks[i].VideoID)
		r.Tracks[i].VideoID = id
		// No fixed 11-character assumption; YouTube is the authority on existence.
		if strings.HasPrefix(id, "fixture_") || id == "" || len(id) > 128 || !utf8.ValidString(id) || strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 || strings.ContainsAny(id, "/?#&=\\") {
			return ErrInvalidPlaylist
		}
	}
	return nil
}
