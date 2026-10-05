package model

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestPlaylistValidation(t *testing.T) {
	for _, privacy := range []string{"", "private", "public", "unlisted"} {
		r := CreatePlaylistRequest{Title: " Test ", PrivacyStatus: privacy, Tracks: []PlaylistTrack{{VideoID: " short-id "}}}
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
		if r.Title != "Test" || r.Tracks[0].VideoID != "short-id" || (privacy == "" && r.PrivacyStatus != "private") {
			t.Fatal("normalization failed")
		}
	}
	for _, body := range []string{`null`, `{}`, `{"title":" ","tracks":[{"video_id":"A"}]}`, `{"title":"T","tracks":[]}`, `{"title":"T","tracks":[{"video_id":""}]}`, `{"title":"T","privacy_status":"invalid","tracks":[{"video_id":"A"}]}`, `{"title":"T","tracks":[{"video_id":"A"}],"extra":true}`, `{"title":"T","tracks":[{"video_id":"A"}]} {}`, fmt.Sprintf(`{"title":%q,"tracks":[{"video_id":"A"}]}`, strings.Repeat("한", 101)), fmt.Sprintf(`{"title":"T","description":%q,"tracks":[{"video_id":"A"}]}`, strings.Repeat("한", 4001))} {
		if _, err := DecodePlaylistRequest([]byte(body)); err == nil {
			t.Fatal("invalid playlist accepted")
		}
	}
	r := CreatePlaylistRequest{Title: "T", Tracks: make([]PlaylistTrack, 21)}
	if err := r.Validate(); !errors.Is(err, ErrPlaylistTooManyTracks) {
		t.Fatal("track cap missing")
	}
	for _, id := range []string{"a b", "\n", "https://youtube.com/watch?v=A", strings.Repeat("a", 129)} {
		r.Tracks = []PlaylistTrack{{VideoID: id}}
		if r.Validate() == nil {
			t.Fatal("unsafe id accepted")
		}
	}
}
