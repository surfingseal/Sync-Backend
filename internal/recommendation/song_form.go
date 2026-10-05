package recommendation

import (
	"example.com/sync/internal/model"
	"golang.org/x/text/unicode/norm"
	"strings"
	"unicode"
)

// Comparison-only NFKC/camel normalization. Source metadata is never modified.
// Aliases match whole concepts; "backing" alone and arbitrary substrings do not.
func songNormalize(text string) string {
	runes := []rune(norm.NFKC.String(text))
	var b strings.Builder
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) && (unicode.IsLower(runes[i-1]) || (unicode.IsUpper(runes[i-1]) && i+1 < len(runes) && unicode.IsLower(runes[i+1]))) {
			b.WriteRune(' ')
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
func songPhrase(text, alias string) bool {
	value := songNormalize(alias)
	return value != "" && strings.Contains(" "+songNormalize(text)+" ", " "+value+" ")
}

var songConcepts = []struct {
	reason  string
	aliases []string
}{
	{"backing_track", []string{"backing track", "backing tracks", "backingtrack", "backingtracks", "accompaniment track", "minus one"}},
	{"karaoke", []string{"karaoke", "カラオケ", "가라오케", "노래방"}},
	{"practice_track", []string{"practice track", "practice backing", "rehearsal track"}},
	{"jam_track", []string{"jam track", "jamtrack"}},
	{"tutorial", []string{"tutorial", "lesson", "play along tutorial"}},
	{"playlist_like", []string{"playlist", "compilation", "full album"}},
	{"long_form_mix", []string{"1 hour", "1hr", "60 minutes", "extended mix", "continuous mix", "nonstop", "loop"}},
}

// Use title only: descriptions often advertise unrelated practice/playlist links.
func NonSongReasons(v model.YouTubeVideo) []string {
	reasons := []string{}
	for _, concept := range songConcepts {
		for _, alias := range concept.aliases {
			if songPhrase(v.Title, alias) {
				reasons = append(reasons, concept.reason)
				break
			}
		}
	}
	return reasons
}
func SongFormReasons(v model.YouTubeVideo) []string {
	out := []string{}
	for _, reason := range NonSongReasons(v) {
		out = append(out, "non_song_form."+reason)
	}
	return out
}
func nonSongInstrumental(v model.YouTubeVideo) bool {
	for _, reason := range NonSongReasons(v) {
		switch reason {
		case "backing_track", "karaoke", "practice_track", "jam_track":
			return true
		}
	}
	return false
}
