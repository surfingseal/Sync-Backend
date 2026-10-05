package recommendation

import (
	"example.com/sync/internal/model"
	"strings"
)

// Metadata hints only. These never participate in filtering or ranking.
type ContentDiagnostic struct {
	Category   string   `json:"category"`
	Categories []string `json:"categories"`
	Evidence   []string `json:"evidence"`
	AILabeled  bool     `json:"ai_labeled"`
}

func DiagnoseContent(v model.YouTubeVideo) ContentDiagnostic {
	r := ContentDiagnostic{Category: "unknown", Categories: []string{}, Evidence: []string{}}
	text := v.Title + " " + v.ChannelTitle + " " + v.Description
	add := func(category string, aliases ...string) {
		for _, a := range aliases {
			if songPhrase(text, a) {
				r.Categories = append(r.Categories, category)
				r.Evidence = append(r.Evidence, category+": "+a)
				return
			}
		}
	}
	for _, a := range []string{"Suno", "Udio", "AI Worship", "AI Song", "AI Music", "Generated with AI"} {
		if songPhrase(text, a) {
			r.AILabeled = true
			r.Evidence = append(r.Evidence, "AI label: "+a)
		}
	}
	if songPhrase(text, "worship") {
		r.Evidence = append(r.Evidence, "worship label (not alone proof of AI)")
	}
	if r.AILabeled && songPhrase(text, "worship") {
		r.Categories = append(r.Categories, "worship_generated")
	}
	add("type_beat", "type beat", "free beat", "instrumental beat", "beat for sale")
	// "prod." alone is a diagnostic cue, not a declaration of a beat or exclusion.
	if strings.Contains(strings.ToLower(v.Title), "prod.") {
		r.Evidence = append(r.Evidence, "production credit: prod.")
	}
	add("street_performance", "singing to strangers", "sings a korean indie song to strangers", "street performance", "sings to strangers")
	add("cover_performance", "cover")
	add("live_performance", "live performance", "live version", "official live", "live at")
	add("reaction", "reaction", "reacts to")
	if len(r.Categories) == 0 && !r.AILabeled && len(NonSongReasons(v)) == 0 && (songPhrase(v.Title, "official audio") || strings.Contains(strings.ToLower(v.Description), "provided to youtube by")) {
		r.Categories = append(r.Categories, "normal_track")
		r.Evidence = append(r.Evidence, "normal release metadata heuristic")
	}
	if len(r.Categories) > 0 {
		r.Category = r.Categories[0]
	}
	return r
}
