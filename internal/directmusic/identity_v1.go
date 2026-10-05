package directmusic

import (
	"example.com/sync/internal/model"
	"example.com/sync/internal/recommendation"
	"strings"
)

// Identity is a conservative metadata heuristic, not an audio fingerprint or
// independent proof of copyright ownership / official artist identity.
func CheckIdentityV1(t model.DirectTrack, v model.YouTubeVideo, c Config) (Evidence, string) {
	e := Evidence{Signals: []string{}, Caveat: "metadata identity heuristic; audio and channel ownership not independently verified"}
	if !v.Public || !v.Embeddable || v.Live {
		return e, "UNPLAYABLE_OR_LIVE"
	}
	if v.CategoryID != "10" {
		return e, "NON_MUSIC_CATEGORY"
	}
	if v.DurationSeconds < c.MinDuration || v.DurationSeconds > c.MaxDuration {
		return e, "DURATION"
	}
	for _, b := range v.BlockedRegions {
		if strings.EqualFold(b, c.Region) {
			return e, "REGION_BLOCKED"
		}
	}
	if len(v.AllowedRegions) > 0 {
		ok := false
		for _, a := range v.AllowedRegions {
			ok = ok || strings.EqualFold(a, c.Region)
		}
		if !ok {
			return e, "REGION_NOT_ALLOWED"
		}
	}
	artist, title := matchKey(t.Artist), matchKey(t.Title)
	vt, channel := matchKey(v.Title), matchKey(v.ChannelTitle)
	if artist == "" || title == "" {
		return e, "EMPTY_IDENTITY"
	}
	// Exact prefix and whole phrase prevent matches to medleys, reaction videos or
	// unrelated titles that merely mention the desired song.
	prefix := vt == title || strings.HasPrefix(vt, title+" ") || vt == artist+" "+title || strings.HasPrefix(vt, artist+" "+title+" ")
	if !prefix || !phrase(vt, title) {
		return e, "TITLE_MISMATCH"
	}
	vevoChannel := strings.ReplaceAll(channel, " ", "") == strings.ReplaceAll(artist, " ", "")+"vevo"
	if !phrase(vt, artist) && !phrase(channel, artist) && !vevoChannel {
		return e, "ARTIST_MISMATCH"
	}
	for _, word := range []string{"karaoke", "backing track", "cover", "tribute", "reaction", "live", "remix", "slowed", "reverb", "sped up", "nightcore", "fan upload", "fan made", "remaster", "remastered", "demo"} {
		if phrase(vt, word) && !phrase(title, word) && !phrase(artist, word) {
			return e, "UNREQUESTED_VERSION"
		}
	}
	if len(recommendation.NonSongReasons(v)) > 0 {
		return e, "NON_SONG"
	}
	if reason := recommendation.ExclusionReason(v); reason != "" {
		return e, "GENERATED_OR_TRANSFORMED_AUDIO"
	}
	e.IdentityScore = .75
	e.Signals = append(e.Signals, "title_prefix_and_artist_phrase_match")
	if channel == artist || channel == artist+" topic" || channel == artist+" official" {
		e.IdentityScore += .1
		e.Signals = append(e.Signals, "artist_channel_or_topic_metadata")
	}
	if strings.ReplaceAll(channel, " ", "") == strings.ReplaceAll(artist, " ", "")+"vevo" {
		e.IdentityScore += .1
		e.Signals = append(e.Signals, "vevo_named_channel")
	}
	if phrase(vt, "official") {
		e.IdentityScore += .05
		e.Signals = append(e.Signals, "official_title_label")
	}
	if v.LicensedContent {
		e.IdentityScore += .1
		e.Signals = append(e.Signals, "licensed_content")
	}
	if e.IdentityScore > 1 {
		e.IdentityScore = 1
	}
	return e, ""
}
