package recommendation

import (
	"example.com/sync/internal/model"
	"testing"
)

func TestSongFormAliasesAndSourcePreservation(t *testing.T) {
	for _, title := range []string{"backing track", "Backing Track", "BackingTrack", "backing-track", "BACKINGTRACK", "backing_track", "backing_tracks", "accompaniment track", "minus-one", "CITY POP Jam C Major 105bpm All Instruments version BackingTrack"} {
		v := model.YouTubeVideo{Title: title, LicensedContent: true}
		r := SongFormReasons(v)
		if len(r) == 0 || r[0] != "non_song_form.backing_track" || VocalKind(v) != "instrumental" || ReleaseConfidence(v).Confidence != "HIGH" || v.Title != title {
			t.Fatal(title, r)
		}
	}
	for _, title := range []string{"karaoke", "KARAOKE", "カラオケ", "ｶﾗｵｹ", "가라오케", "노래방", "【カラオケ】真夜中のドア～Stay With Me/松原みき"} {
		v := model.YouTubeVideo{Title: title}
		r := SongFormReasons(v)
		if len(r) == 0 || r[0] != "non_song_form.karaoke" || VocalKind(v) != "instrumental" {
			t.Fatal(title, r)
		}
	}
	for _, title := range []string{"Official Instrumental", "Piano Instrumental", "Instrumental Version", "piano", "orchestral", "acoustic", "Backing You Up", "Backingtrackers", "Karaokeish", "Official Remix", "Official Lyrics", "Official Album Audio"} {
		if r := SongFormReasons(model.YouTubeVideo{Title: title}); len(r) > 0 {
			t.Fatal("false positive", title, r)
		}
	}
	for _, tc := range []struct{ title, reason string }{{"practice backing", "practice_track"}, {"rehearsal track", "practice_track"}, {"JamTrack", "jam_track"}, {"play along tutorial", "tutorial"}} {
		r := SongFormReasons(model.YouTubeVideo{Title: tc.title})
		if len(r) == 0 || r[0] != "non_song_form."+tc.reason {
			t.Fatal(r)
		}
	}
}
func TestAudioTextRetrievalEvidenceSeparated(t *testing.T) {
	cases := []struct {
		v                      model.YouTubeVideo
		genre, source          string
		coverage               bool
		audio, text, retrieval string
	}{
		{model.YouTubeVideo{DefaultLanguage: "en"}, "city-pop", "", false, "unknown", "en", "unknown"},
		{model.YouTubeVideo{DefaultAudioLanguage: "ko", DefaultLanguage: "en"}, "city-pop", "", true, "ko", "en", "unknown"},
		{model.YouTubeVideo{Title: "BackingTrack", DefaultAudioLanguage: "en"}, "city-pop", "", false, "en", "unknown", "unknown"},
		{model.YouTubeVideo{Title: "Official Instrumental", DefaultAudioLanguage: "en"}, "city-pop", "", false, "en", "unknown", "unknown"},
		{model.YouTubeVideo{Title: "【カラオケ】Track", DefaultAudioLanguage: "en"}, "city-pop", "", false, "en", "unknown", "unknown"},
		{model.YouTubeVideo{}, "k-indie", "ko", false, "unknown", "unknown", "ko"},
		{model.YouTubeVideo{Title: "A plain Latin title"}, "city-pop", "en", false, "unknown", "unknown", "en"},
	}
	for _, tc := range cases {
		r := LanguageAffinity(tc.v, tc.genre, tc.source)
		if r.CoverageEligible != tc.coverage || r.AudioLanguage.Value != tc.audio || r.TextLanguage.Value != tc.text || r.RetrievalAffinity.Value != tc.retrieval {
			t.Fatal(r)
		}
		if !tc.coverage && r.CoverageLanguage != "unknown" {
			t.Fatal("vocal language forced", r)
		}
	}
}
