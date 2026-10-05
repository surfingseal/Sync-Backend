package directmusic

import (
	"regexp"
	"strings"
	"unicode"

	"example.com/sync/internal/model"
	"example.com/sync/internal/recommendation"
	"golang.org/x/text/unicode/norm"
)

// TitleAlias binds a provider-proven spelling to a specific video and channel.
// No translation, fuzzy distance, or LLM-produced alias is inferred at runtime.
type TitleAlias struct {
	Artist         string `json:"artist"`
	CanonicalTitle string `json:"canonical_title"`
	ProviderTitle  string `json:"provider_title"`
	VideoID        string `json:"video_id"`
	ChannelID      string `json:"channel_id"`
	EvidenceURL    string `json:"evidence_url"`
	EvidenceText   string `json:"evidence_text"`
}

var leadingBareLabel = regexp.MustCompile(`(?i)^\s*(?:official music video|official video|official audio|lyric video|visualizer)\s*[:\-]?\s+`)
var releaseYear = regexp.MustCompile(`\s*\((?:19|20)[0-9]{2}\)\s*$`)
var harmlessHQ = regexp.MustCompile(`(?i)\s+(?:hq|hd|4k)$`)

var leadingLabel = regexp.MustCompile(`(?i)^\s*[\[(](?:mv|official video|official music video|official audio|audio|lyric video|lyrics|visualizer)[\])]\s*`)
var trailingLabel = regexp.MustCompile(`(?i)\s*[\[(](?:official video|official music video|music video|official audio|audio|lyrics? video|lyrics|visualizer|mv)[\])]\s*$`)
var trailingBareLabel = regexp.MustCompile(`(?i)\s+(?:official music video|music video|official video|official audio|lyrics? video|visualizer|mv)$`)
var creditSeparators = regexp.MustCompile(`(?i)\s*(?:,|&|\s+and\s+|\s+feat\.?\s+|\s+ft\.?\s+|\s+featuring\s+)\s*`)
var delimiter = regexp.MustCompile(`\s+[-–—]\s+`)

func NormalizeVideoTitle(s string) string {
	s = Normalize(s)
	for {
		old := s
		s = leadingLabel.ReplaceAllString(s, "")
		s = leadingBareLabel.ReplaceAllString(s, "")
		s = trailingLabel.ReplaceAllString(s, "")
		s = trailingBareLabel.ReplaceAllString(s, "")
		s = Normalize(s)
		if old == s {
			return s
		}
	}
}
func artistKey(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, norm.NFKD.String(s))
	return matchKey(s)
}
func ArtistCredits(s string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, part := range creditSeparators.Split(s, -1) {
		k := artistKey(part)
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// Officiality classifies observable metadata only. YouTube videos.list does not
// attest Official Artist Channel status or ownership, so named channels are hints.
func ClassifyOfficiality(t model.DirectTrack, v model.YouTubeVideo) (string, string) {
	a, ch := artistKey(t.Artist), artistKey(v.ChannelTitle)
	if ch == a+" topic" {
		return "topic", "named_channel_heuristic"
	}
	if strings.ReplaceAll(ch, " ", "") == strings.ReplaceAll(a, " ", "")+"vevo" {
		return "vevo", "named_channel_heuristic"
	}
	if v.LicensedContent {
		return "licensed", "provider_licensed_flag_not_owner_proof"
	}
	if ch == a || ch == a+" official" {
		return "official_artist", "name_consistency_only_not_oac_verified"
	}
	if ch != "" {
		return "ordinary_channel", "no_stronger_evidence"
	}
	return "unknown", "missing_channel_metadata"
}
func StrongOfficialEvidence(e Evidence) bool {
	return e.Officiality == "topic" || e.Officiality == "vevo" || e.Officiality == "licensed"
}
func CheckIdentity(t model.DirectTrack, v model.YouTubeVideo, c Config) (Evidence, string) {
	return CheckIdentityWithAliases(t, v, c, nil)
}
func CheckIdentityWithAliases(t model.DirectTrack, v model.YouTubeVideo, c Config, aliases []TitleAlias) (Evidence, string) {
	e, reason := CheckTextIdentity(t, v, aliases)
	e.Playable = playable(v, c)
	if reason != "" {
		return e, reason
	}
	if !e.Playable {
		return e, "UNPLAYABLE"
	}
	if len(recommendation.NonSongReasons(v)) > 0 {
		return e, "NON_SONG"
	}
	if recommendation.ExclusionReason(v) != "" {
		return e, "GENERATED_OR_TRANSFORMED_AUDIO"
	}
	return e, ""
}
func playable(v model.YouTubeVideo, c Config) bool {
	if !v.Public || !v.Embeddable || v.Live || v.CategoryID != "10" || v.DurationSeconds < c.MinDuration || v.DurationSeconds > c.MaxDuration {
		return false
	}
	for _, x := range v.BlockedRegions {
		if strings.EqualFold(x, c.Region) {
			return false
		}
	}
	if len(v.AllowedRegions) > 0 {
		for _, x := range v.AllowedRegions {
			if strings.EqualFold(x, c.Region) {
				return true
			}
		}
		return false
	}
	return true
}

// CheckTextIdentity may be replayed on a recorded snippet. Its Playable field
// remains false unless the full metadata check runs; snippets are never promoted
// to final tracks. Missing release metadata prevents relaxed credit/alias matches.
func CheckTextIdentity(t model.DirectTrack, v model.YouTubeVideo, aliases []TitleAlias) (Evidence, string) {
	e := Evidence{Signals: []string{}, ArtistMatch: "none", TitleMatch: "none", VersionMatch: "compatible", Caveat: "metadata heuristic; no audio fingerprint or channel ownership attestation"}
	e.Officiality, e.OfficialityConfidence = ClassifyOfficiality(t, v)
	raw := NormalizeVideoTitle(v.Title)
	vt, title := matchKey(raw), matchKey(t.Title)
	a, ch := artistKey(t.Artist), artistKey(v.ChannelTitle)
	if a == "" || title == "" {
		return e, "EMPTY_IDENTITY"
	}
	if raw != Normalize(v.Title) {
		e.Signals = append(e.Signals, "known_metadata_label_removed_for_comparison")
	}
	versionMismatch := false
	for _, marker := range []string{"remix", "live", "cover", "acoustic", "demo", "version", "edit", "remaster", "remastered", "karaoke", "backing track", "tribute", "reaction", "slowed", "reverb", "sped up", "nightcore"} {
		if phrase(vt, marker) && !phrase(title, marker) && !phrase(matchKey(t.Artist), marker) {
			e.VersionMatch = "unrequested_" + strings.ReplaceAll(marker, " ", "_")
			versionMismatch = true
		}
	}
	candidateArtistPrefix := artistKey(raw)
	artistExact := phrase(candidateArtistPrefix, a) || ch == a || ch == a+" topic" || ch == a+" official" || strings.ReplaceAll(ch, " ", "") == strings.ReplaceAll(a, " ", "")+"vevo"
	comparisonTitle := func(s string) string {
		return matchKey(harmlessHQ.ReplaceAllString(releaseYear.ReplaceAllString(s, ""), ""))
	}
	titlePrefix := comparisonTitle(raw) == title || comparisonTitle(raw) == matchKey(t.Artist)+" "+title
	// A different credited artist before a delimiter must not be accepted merely
	// because it mentions the requested artist in its title or channel.
	parts := delimiter.Split(raw, 2)
	if len(parts) == 2 {
		titlePrefix = comparisonTitle(parts[1]) == title
		left := artistKey(parts[0])
		artistExact = left == a

	}
	if titlePrefix {
		e.TitleMatch = "exact_title_boundary"
	}
	if artistExact {
		e.ArtistMatch = "exact_credit_or_artist_channel"
	}
	if !artistExact && titlePrefix {
		credits := ArtistCredits(t.Artist)
		if len(credits) > 1 {
			visible := ArtistCredits(raw)
			if len(parts) == 2 {
				visible = ArtistCredits(parts[0])
			}
			// Credit overlap must come from credited title/channel fields, not from
			// arbitrary description mentions of an original artist in a cover upload.
			observed := artistKey(v.ChannelTitle)
			if len(parts) == 2 {
				observed = artistKey(parts[0])
			}
			overlap := 0
			for _, credit := range credits {
				for _, p := range visible {
					if credit == p || phrase(observed, credit) {
						overlap++
						break
					}
				}
			}
			main := phrase(observed, credits[0])
			if main && overlap >= 2 && overlap*2 >= len(credits) && StrongOfficialEvidence(e) {
				artistExact = true
				e.ArtistMatch = "collaborative_main_and_credit_overlap"
				e.Signals = append(e.Signals, "credit_relaxation_requires_main_artist_two_credits_and_release_evidence")
			}
		}
	}
	if !titlePrefix && artistExact {
		for _, alias := range aliases {
			if NormalizeKey(alias.Artist) != NormalizeKey(t.Artist) || matchKey(alias.CanonicalTitle) != title || alias.VideoID == "" || alias.ChannelID == "" || alias.EvidenceURL == "" || alias.EvidenceText == "" || v.VideoID != alias.VideoID || v.ChannelID != alias.ChannelID || !StrongOfficialEvidence(e) {
				continue
			}
			provider := matchKey(alias.ProviderTitle)
			right := vt
			if len(parts) == 2 {
				right = matchKey(parts[1])
			}
			if right == provider {
				titlePrefix = true
				e.TitleMatch = "explicit_provider_release_alias"
				e.Signals = append(e.Signals, "video_and_channel_bound_title_alias")
				break
			}
		}
	}
	if !titlePrefix {
		return e, "TITLE_MISMATCH"
	}
	if !artistExact {
		return e, "ARTIST_MISMATCH"
	}
	if versionMismatch {
		return e, "UNREQUESTED_VERSION"
	}
	e.IdentityConfidence = .9
	if e.ArtistMatch == "collaborative_main_and_credit_overlap" {
		e.IdentityConfidence = .85
	}
	if e.TitleMatch == "explicit_provider_release_alias" {
		e.IdentityConfidence = .85
	}
	e.IdentityScore = e.IdentityConfidence
	e.Signals = append(e.Signals, "artist_and_title_evidence_separate", "version_markers_preserved")
	return e, ""
}
func ResolvedStatus(e Evidence) string {
	if StrongOfficialEvidence(e) {
		return "RESOLVED_STRONG"
	}
	return "RESOLVED_ACCEPTABLE"
}
func IsResolved(s string) bool {
	return s == "RESOLVED" || s == "RESOLVED_STRONG" || s == "RESOLVED_ACCEPTABLE"
}
