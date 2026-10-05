package recommendation

import (
	"example.com/sync/internal/model"
	"math"
	"strings"
)

// Metadata evidence is a heuristic, not audio classification. Unknown stays eligible in mixed mode.
func VocalKind(v model.YouTubeVideo) string {
	if nonSongInstrumental(v) {
		return "instrumental"
	}
	text := strings.ToLower(v.Title)
	for _, s := range []string{"instrumental", "karaoke", "backing track", "piano solo"} {
		if songPhrase(text, s) {
			return "instrumental"
		}
	}
	for _, s := range []string{"lyrics", "lyric video", "official mv", "official music video", "vocal"} {
		if songPhrase(text, s) {
			return "vocal"
		}
	}
	return "unknown"
}

const VocalMixScoreTolerance = .08

func SelectVocalMix(pool []RankedCandidate, p model.MusicPreferences) []RankedCandidate {
	count := min(p.Count, len(pool))
	out := []RankedCandidate{}
	remaining := append([]RankedCandidate{}, pool...)
	mode := p.EffectiveVocalMode()
	vocal, instrumental := 0, 0
	for len(out) < count && len(remaining) > 0 {
		index := 0
		want := ""
		switch mode {
		case "instrumental-only", "instrumental-first":
			want = "instrumental"
		case "vocal-first":
			want = "vocal"
		case "mixed":
			if vocal < int(math.Ceil(float64(len(out)+1)*.7)) {
				want = "vocal"
			} else if instrumental < int(math.Floor(float64(len(out)+1)*.3)) {
				want = "instrumental"
			}
		}
		if want != "" {
			for i, item := range remaining {
				if VocalKind(item.Video) == want && (mode == "instrumental-only" || item.Track.MatchScore >= remaining[0].Track.MatchScore-VocalMixScoreTolerance) {
					index = i
					break
				}
			}
		}
		chosen := remaining[index]
		remaining = append(remaining[:index], remaining[index+1:]...)
		kind := VocalKind(chosen.Video)
		if mode == "instrumental-only" && kind != "instrumental" {
			continue
		}
		if kind == "vocal" {
			vocal++
		}
		if kind == "instrumental" {
			instrumental++
		}
		out = append(out, chosen)
	}
	return out
}
