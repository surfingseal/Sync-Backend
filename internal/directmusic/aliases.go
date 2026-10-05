package directmusic

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// LoadTitleAliases is operator-supplied release evidence, not an inferred
// translation table. Empty by default. Entries bind one video and channel.
func LoadTitleAliases(path string) ([]TitleAlias, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read title alias evidence")
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 128*1024))
	decoder.DisallowUnknownFields()
	var entries []TitleAlias
	if err = decoder.Decode(&entries); err != nil {
		return nil, fmt.Errorf("invalid title alias evidence")
	}
	if decoder.Decode(new(any)) != io.EOF || len(entries) > 50 {
		return nil, fmt.Errorf("invalid title alias evidence")
	}
	for _, e := range entries {
		if Normalize(e.Artist) == "" || Normalize(e.CanonicalTitle) == "" || Normalize(e.ProviderTitle) == "" || e.VideoID == "" || e.ChannelID == "" || !strings.HasPrefix(e.EvidenceURL, "https://") || Normalize(e.EvidenceText) == "" {
			return nil, fmt.Errorf("incomplete provider release evidence")
		}
	}
	return entries, nil
}
func (r *Resolver) candidateCacheKey(artist, title string) string {
	base := cacheKey(artist, title, r.Config)
	if len(r.Aliases) == 0 {
		return base
	}
	b, _ := json.Marshal(r.Aliases)
	return fmt.Sprintf("%s:%x", base, sha256.Sum256(b))
}
