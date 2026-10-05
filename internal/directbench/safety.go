package directbench

import (
	"example.com/sync/internal/directmusic"
	"example.com/sync/internal/model"
)

type Fixture struct {
	Name     string                   `json:"name"`
	Track    model.DirectTrack        `json:"requested_track"`
	Video    model.YouTubeVideo       `json:"metadata"`
	Aliases  []directmusic.TitleAlias `json:"aliases,omitempty"`
	Expected bool                     `json:"expected_accept"`
}
type FixtureResult struct {
	Name     string `json:"name"`
	Expected bool   `json:"expected_accept"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason"`
	Passed   bool   `json:"passed"`
}

func AdversarialFixtures() []Fixture {
	t := model.DirectTrack{Artist: "Artist A", Title: "Song X"}
	base := model.YouTubeVideo{VideoID: "fixture-video", ChannelID: "fixture-channel", Title: "Artist A - Song X", ChannelTitle: "Artist A - Topic", CategoryID: "10", DurationSeconds: 240, Public: true, Embeddable: true, LicensedContent: true}
	fixtures := []Fixture{}
	add := func(name, title, channel string, want bool) {
		v := base
		v.Title = title
		v.ChannelTitle = channel
		fixtures = append(fixtures, Fixture{name, t, v, nil, want})
	}
	add("mv_prefix", "[MV] Artist A - Song X", "Artist A - Topic", true)
	add("official_prefix", "Official Video Artist A - Song X", "Artist A - Topic", true)
	add("wrong_artist_same_title", "Artist B - Song X", "Artist B - Topic", false)
	add("similar_title", "Artist A - Song X Part Two", "Artist A - Topic", false)
	add("wrong_cover", "Artist B - Song X (Artist A Cover)", "Artist B", false)
	for _, marker := range []string{"karaoke", "backing track", "live", "remix", "acoustic version", "demo", "slowed + reverb", "sped up", "reaction", "fan edit", "tribute", "compilation"} {
		add(marker, "Artist A - Song X ("+marker+")", "Artist A - Topic", false)
	}
	v := base
	v.Title = "Artist B - Song X"
	v.ChannelTitle = "Artist B - Topic"
	v.Description = "Song originally by Artist A"
	fixtures = append(fixtures, Fixture{"artist_only_description", t, v, nil, false})
	collab := t
	collab.Artist = "Artist A feat. Artist C"
	v = base
	v.Title = "Artist A feat. Artist B - Song X"
	fixtures = append(fixtures, Fixture{"featured_mismatch", collab, v, nil, false})
	collab.Artist = "Artist A, Artist C & Artist D"
	v.Title = "Artist A - Song X"
	fixtures = append(fixtures, Fixture{"insufficient_collaborative_credit", collab, v, nil, false})
	v.Title = "Artist A & Artist C - Song X"
	fixtures = append(fixtures, Fixture{"supported_partial_collaborative_credit", collab, v, nil, true})
	v = base
	v.Title = "Artist A - 노래"
	fixtures = append(fixtures, Fixture{"localized_unsupported", t, v, nil, false})
	aliases := []directmusic.TitleAlias{{Artist: t.Artist, CanonicalTitle: t.Title, ProviderTitle: "노래", VideoID: v.VideoID, ChannelID: v.ChannelID, EvidenceURL: "https://provider.example/fixture", EvidenceText: "explicit alias in artificial regression evidence"}}
	fixtures = append(fixtures, Fixture{"explicit_alias", t, v, aliases, true})
	v = base
	v.ChannelTitle = "Ordinary uploader"
	v.LicensedContent = false
	fixtures = append(fixtures, Fixture{"ordinary_exact_identity", t, v, nil, true})
	add("topic_exact_identity", base.Title, base.ChannelTitle, true)
	return fixtures
}
func RunFixtures() ([]FixtureResult, Safety) {
	rows := []FixtureResult{}
	s := Safety{}
	for _, f := range AdversarialFixtures() {
		_, reason := directmusic.CheckIdentityWithAliases(f.Track, f.Video, directmusic.DefaultConfig(), f.Aliases)
		accepted := reason == ""
		passed := accepted == f.Expected
		rows = append(rows, FixtureResult{f.Name, f.Expected, accepted, reason, passed})
		if !passed {
			s.FixtureFailures++
		}
		if accepted && !f.Expected {
			s.FalsePositives++
		}
	}
	return rows, s
}

// AuditResult enforces provenance and resource invariants without semantic music
// quality scoring, subjective labels or language/country metrics.
func AuditResult(r *directmusic.Result) Safety {
	s := Safety{}
	origins := map[string]bool{}
	for _, t := range r.Normalized {
		origins[directmusic.NormalizeKey(t.Artist)+"\x00"+directmusic.NormalizeKey(t.Title)] = true
	}
	artists := map[string]int{}
	verified := map[string]bool{}
	for _, rr := range r.Resolutions {
		primary, fallback := 0, 0
		for i, q := range rr.Searches {
			if q.Kind != "primary" && q.Kind != "fallback" {
				s.Bounds++
				s.Broad++
			}
			if q.Kind == "primary" {
				primary++
			} else if q.Kind == "fallback" {
				fallback++
				if i == 0 || rr.Searches[i-1].Kind != "primary" || rr.Searches[i-1].AcceptedCount > 0 {
					s.Bounds++
				}
			}
			if q.Calls != 1 {
				s.Bounds++
			}
			tokens := directmusic.IdentityTokenQuery(rr.Candidate)
			if q.Kind == "fallback" && q.Query != tokens {
				s.Broad++
			}
			if q.Kind == "primary" && q.Query != directmusic.IdentityPrimaryQuery(rr.Candidate) {
				s.Broad++
			}
		}
		if primary > 1 || fallback > 1 {
			s.Bounds++
		}
		if rr.Status == "API_FAILURE" && rr.Video != nil {
			s.APIValid++
		}
		if directmusic.IsResolved(rr.Status) && rr.Video != nil {
			verified[rr.Video.VideoID+"\x00"+directmusic.NormalizeKey(rr.Candidate.Artist)+"\x00"+directmusic.NormalizeKey(rr.Candidate.Title)] = true
		}
	}
	for _, t := range r.Final {
		key := directmusic.NormalizeKey(t.Gemini.Artist) + "\x00" + directmusic.NormalizeKey(t.Gemini.Title)
		if !origins[key] || !verified[t.VideoID+"\x00"+key] {
			s.Substitute++
		}
		artists[directmusic.NormalizeKey(t.Gemini.Artist)]++
	}
	for _, count := range artists {
		if count > 2 {
			s.Diversity++
		}
	}
	if len(r.Final) > 10 {
		s.Bounds++
	}
	return s
}
