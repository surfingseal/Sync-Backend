package directmusic

import (
	"regexp"
	"strings"
	"unicode"

	"example.com/sync/internal/model"
)

var parentheticalCredit = regexp.MustCompile(`^([^()]+)\s*\(([^()]+)\)$`)
var leadingParentheticalCredit = regexp.MustCompile(`^([^()]+\([^()]+\))\s+(.+)$`)
var quotedCredit = regexp.MustCompile(`^(.+?)\s+["“‘'](.+)["”’']$`)
var collaborationMarker = regexp.MustCompile(`(?i)(?:\b(?:feat|ft|featuring)\.?\s|\s[x×&+,]\s)`)

// Explicit credit boundaries are required; arbitrary artist/title mentions are
// never considered credits. All strings here are comparison copies only.
func splitMusicCredit(raw, requestedArtist string) (credit, title string, explicit bool) {
	if parts := delimiter.Split(raw, 2); len(parts) == 2 {
		return parts[0], parts[1], true
	}
	if parts := leadingParentheticalCredit.FindStringSubmatch(raw); len(parts) == 3 {
		return parts[1], parts[2], true
	}
	if parts := quotedCredit.FindStringSubmatch(raw); len(parts) == 3 {
		return parts[1], parts[2], true
	}
	// Legacy unseparated exact artist + title formatting; not a substring match.
	fields := strings.Fields(raw)
	for i := 1; i < len(fields); i++ {
		if artistKey(strings.Join(fields[:i], " ")) == artistKey(requestedArtist) {
			return strings.Join(fields[:i], " "), strings.Join(fields[i:], " "), true
		}
	}
	return "", raw, false
}

func creditedArtistMatch(requested, credit string) bool {
	if artistKey(requested) == artistKey(credit) {
		return true
	}
	// Do not reduce guest/collaboration credits to a solo artist.
	if collaborationMarker.MatchString(credit) || len(ArtistCredits(credit)) != 1 {
		return false
	}
	parts := parentheticalCredit.FindStringSubmatch(credit)
	if len(parts) != 3 || collaborationMarker.MatchString(parts[1]) || collaborationMarker.MatchString(parts[2]) {
		return false
	}
	// Bounded Latin/Hangul bilingual notation only. This is a provider credit
	// heuristic, not an inferred alias table or artist nationality assertion.
	if !((hangulOnly(parts[1]) && latinOnly(parts[2])) || (latinOnly(parts[1]) && hangulOnly(parts[2]))) {
		return false
	}
	key := artistKey(requested)
	return key == artistKey(parts[1]) || key == artistKey(parts[2])
}
func hangulOnly(s string) bool { return scriptOnly(s, unicode.Hangul) }
func latinOnly(s string) bool  { return scriptOnly(s, unicode.Latin) }
func scriptOnly(s string, script *unicode.RangeTable) bool {
	found := false
	for _, r := range s {
		if unicode.IsLetter(r) {
			if !unicode.Is(script, r) {
				return false
			}
			found = true
		}
	}
	return found
}
func artistChannelMatch(artist, channel string) bool {
	a, ch := artistKey(artist), artistKey(channel)
	return ch == a || ch == a+" topic" || ch == a+" official" ||
		strings.ReplaceAll(ch, " ", "") == strings.ReplaceAll(a, " ", "")+"vevo"
}
func localizedTitleMatch(segment, requested string) bool {
	parts := parentheticalCredit.FindStringSubmatch(segment)
	return len(parts) == 3 && matchKey(parts[2]) == matchKey(requested)
}

// Self-reported release metadata is supporting evidence, never an artist/title
// replacement. Statistics and default audio language have no role here.
func releaseMetadataEvidence(t model.DirectTrack, v model.YouTubeVideo) bool {
	if v.LicensedContent {
		return true
	}
	d := matchKey(v.Description)
	if !phrase(d, artistKey(t.Artist)) || !phrase(d, matchKey(t.Title)) {
		return false
	}
	return strings.Contains(v.Description, "ⓒ") || strings.Contains(v.Description, "©") ||
		phrase(d, "copyright") || phrase(d, "copyrights") || phrase(d, "all rights reserved") ||
		phrase(d, "provided to youtube by") || distributionChannelEvidence(v)
}
func copyrightChannelEvidence(v model.YouTubeVideo) bool {
	ch := artistKey(v.ChannelTitle)
	if ch == "" {
		return false
	}
	for _, line := range strings.Split(v.Description, "\n") {
		lower := strings.ToLower(line)
		if (strings.Contains(line, "ⓒ") || strings.Contains(line, "©") || strings.Contains(lower, "copyright")) && phrase(matchKey(line), ch) {
			return true
		}
	}
	return false
}

// Explicit distributor statements must name this channel, not merely claim
// "official" somewhere in a title. Such statements remain self-reported hints.
func distributionChannelEvidence(v model.YouTubeVideo) bool {
	channel := v.ChannelTitle
	if parts := parentheticalCredit.FindStringSubmatch(Normalize(channel)); len(parts) == 3 {
		channel = parts[1]
	}
	ch := artistKey(channel)
	if ch == "" {
		return false
	}
	for _, line := range strings.Split(v.Description, "\n") {
		key := matchKey(line)
		if phrase(key, ch) && phrase(key, "official channel for the mv") {
			return true
		}
	}
	return false
}
