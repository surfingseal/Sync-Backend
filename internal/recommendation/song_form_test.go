package recommendation

import (
	"example.com/sync/internal/model"
	"testing"
)

func TestConservativeSongForms(t *testing.T) {
	for _, tc := range []struct{ title, reason string }{
		{"Citypop Backing Track", "backing_track"}, {"Piano Karaoke", "karaoke"}, {"Practice Track in A", "practice_track"}, {"Blues Jam Track", "jam_track"}, {"Guitar lesson", "tutorial"}, {"Full Album", "playlist_like"}, {"Summer Playlist", "playlist_like"}, {"60 minutes calm", "long_form_mix"}, {"Continuous Mix", "long_form_mix"}, {"Extended Mix", "long_form_mix"}, {"Instrumental Version", ""}, {"Official Lyrics Video", ""}, {"Official Remix", ""}, {"Official Album Audio", ""}, {"Album Version", ""},
	} {
		t.Run(tc.title, func(t *testing.T) {
			v := model.YouTubeVideo{Title: tc.title, Description: "Learn with our tutorial playlist backing track"}
			r := NonSongReasons(v)
			if tc.reason == "" && len(r) != 0 {
				t.Fatal(r)
			}
			if tc.reason != "" && (len(r) != 1 || r[0] != tc.reason) {
				t.Fatal(r)
			}
		})
	}
}
