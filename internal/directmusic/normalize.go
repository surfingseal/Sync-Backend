package directmusic

import (
	"encoding/json"
	"strings"
	"unicode"

	"example.com/sync/internal/model"
	"golang.org/x/text/unicode/norm"
)

func Normalize(s string) string    { return strings.Join(strings.Fields(norm.NFKC.String(s)), " ") }
func NormalizeKey(s string) string { return strings.ToLower(Normalize(s)) }
func NormalizeCandidates(input []model.DirectTrack) (out []model.DirectTrack, duplicates, blank int) {
	out = make([]model.DirectTrack, 0, len(input))
	seen := map[string]bool{}
	for i, t := range input {
		t.Artist = Normalize(t.Artist)
		t.Title = Normalize(t.Title)
		t.Reason = Normalize(t.Reason)
		t.GeminiRank = i + 1
		if t.Artist == "" || t.Title == "" {
			blank++
			continue
		}
		pair, _ := json.Marshal([]string{NormalizeKey(t.Artist), NormalizeKey(t.Title)})
		key := string(pair)
		if seen[key] {
			duplicates++
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return
}

// Match keys collapse punctuation only for video identity, never for dedupe.
func matchKey(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, norm.NFKC.String(s))), " ")
}
func phrase(hay, needle string) bool {
	return needle != "" && strings.Contains(" "+hay+" ", " "+needle+" ")
}
