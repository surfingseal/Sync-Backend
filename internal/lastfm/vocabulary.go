package lastfm

import (
	"crypto/sha256"
	"golang.org/x/text/unicode/norm"
	"strings"
	"time"
	"unicode"
)

func Normalize(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(norm.NFKC.String(s)), " "))
}
func Canonical(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, norm.NFKC.String(s))), "-")
}

type Seed struct{ Category, Family string }

var seeds = map[string]Seed{}

func init() {
	groups := []struct{ category, family, names string }{
		{"genre", "pop", "pop"}, {"subgenre", "pop", "dream pop|synthpop|synth-pop|indie pop|electropop|chamber pop"},
		{"genre", "rock", "rock|alternative|indie rock"}, {"subgenre", "rock", "shoegaze|post-rock|psychedelic rock|alternative rock|progressive rock|punk|post-punk"},
		{"genre", "electronic", "electronic|electronica"}, {"subgenre", "electronic", "chillwave|idm|downtempo|trip-hop|synthwave"},
		{"genre", "hip-hop-rap", "hip-hop|hip hop|rap"}, {"genre", "rnb-soul", "rnb|r&b|soul"}, {"genre", "jazz", "jazz"}, {"subgenre", "jazz", "nu jazz|smooth jazz|bebop"},
		{"genre", "classical", "classical"}, {"genre", "folk-acoustic", "folk"}, {"style", "folk-acoustic", "acoustic|singer-songwriter"},
		{"genre", "metal", "metal"}, {"subgenre", "metal", "heavy metal|black metal|death metal|progressive metal|doom metal"},
		{"genre", "ambient", "ambient"}, {"subgenre", "ambient", "dark ambient"}, {"genre", "dance", "dance"}, {"subgenre", "dance", "house|techno|trance|disco|edm|drum and bass|dubstep"},
		{"genre", "soundtrack-cinematic", "soundtrack|soundtracks|film score"}, {"style", "experimental", "experimental|avant-garde"},
		{"genre", "country", "country"}, {"genre", "reggae", "reggae"}, {"subgenre", "reggae", "dub|ska"},
		{"genre", "latin", "latin music|salsa|bossa nova|reggaeton"}, {"genre", "world-regional", "world music|world|afrobeat"},
		{"mood", "", "dreamy|melancholic|sad|happy|uplifting|mellow|calm|dark|romantic|nostalgic|energetic|peaceful|aggressive|atmospheric"},
		{"era", "", "80s|90s|70s|60s|00s|2000s|2010s|2020s"}, {"instrumentation", "", "piano|guitar|violin|orchestral"},
		{"vocal_attribute", "", "female vocalists|male vocalists|female vocals|male vocals|instrumental"},
		{"geography", "", "british|american|japanese|german|swedish|latin|french"}, {"language", "", "english|spanish|portuguese"},
		{"activity_context", "", "sleep|study|workout|relax|chillout"}, {"personal/noisy", "", "seen live|favorites|favourites|awesome|my music|owned|songs i love|love|beautiful|best|albums i own|all time favourites"},
	}
	for _, g := range groups {
		for _, name := range strings.Split(g.names, "|") {
			seeds[name] = Seed{g.category, g.family}
		}
	}
}
func Classify(name string) (Seed, string) {
	if s, ok := seeds[Normalize(name)]; ok {
		if s.Category == "personal/noisy" {
			return s, "rejected"
		}
		if s.Category == "genre" || s.Category == "subgenre" || s.Category == "style" || s.Category == "mood" {
			return s, "candidate"
		}
		return s, "unknown"
	}
	return Seed{"unknown", ""}, "unknown"
}

type Sample struct {
	Rank      int   `json:"retrieval_rank"`
	Track     Track `json:"track"`
	Duplicate bool  `json:"duplicate"`
}
type AuditedTag struct {
	Name             string   `json:"name"`
	Canonical        string   `json:"canonical"`
	Rank             *int     `json:"rank"`
	Count            *Number  `json:"count"`
	Reach            *Number  `json:"reach"`
	Taggings         *Number  `json:"taggings"`
	Info             *TagInfo `json:"info,omitempty"`
	Category         string   `json:"category"`
	Family           string   `json:"family,omitempty"`
	Status           string   `json:"status"`
	Origin           string   `json:"origin"`
	Notes            string   `json:"notes"`
	Samples          []Sample `json:"samples,omitempty"`
	SamplingWarnings []string `json:"sampling_warnings,omitempty"`
	Reviewed         bool     `json:"semantic_reviewed"`
}

func AuditGlobal(tags []Tag) []AuditedTag {
	out := []AuditedTag{}
	seen := map[string]bool{}
	canon := map[string]string{}
	for i, t := range tags {
		n := Normalize(t.Name)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		s, status := Classify(n)
		rank := i + 1
		slug := Canonical(n)
		if old, ok := canon[slug]; ok && old != n {
			hash := sha256.Sum256([]byte(n))
			slug += "-" + fmtHex(hash[:3])
		}
		canon[slug] = n
		out = append(out, AuditedTag{Name: t.Name, Canonical: slug, Rank: &rank, Count: t.Count, Reach: t.Reach, Category: s.Category, Family: s.Family, Status: status, Origin: "global", Notes: "Seed category is provisional; status is not semantic certification."})
	}
	return out
}
func fmtHex(b []byte) string {
	const h = "0123456789abcdef"
	out := []byte{}
	for _, v := range b {
		out = append(out, h[v>>4], h[v&15])
	}
	return string(out)
}
func Samples(tracks []Track) []Sample {
	out := []Sample{}
	seen := map[string]bool{}
	for i, t := range tracks {
		key := Normalize(t.Artist.Name) + "\x00" + Normalize(t.Name)
		out = append(out, Sample{i + 1, t, seen[key]})
		seen[key] = true
	}
	return out
}

type RegistryTag struct {
	Canonical string  `json:"canonical"`
	Tag       string  `json:"lastfm_tag"`
	Category  string  `json:"category"`
	Family    string  `json:"family,omitempty"`
	Status    string  `json:"status"`
	Rank      *int    `json:"rank"`
	Reach     *Number `json:"reach"`
	Taggings  *Number `json:"taggings"`
	Evidence  string  `json:"validation_scope"`
}
type Registry struct {
	Version     int           `json:"version"`
	GeneratedAt time.Time     `json:"generated_at"`
	Tags        []RegistryTag `json:"tags"`
}

func GenerateRegistry(tags []AuditedTag) Registry {
	r := Registry{1, time.Now().UTC(), []RegistryTag{}}
	for _, t := range tags {
		if t.Status == "validated" && !t.Reviewed {
			t.Status = "candidate"
		}
		if t.Status == "candidate" || t.Status == "validated" {
			evidence := "candidate until usage/retrieval and explicit sample semantic review pass"
			if t.Status == "validated" {
				evidence = "Usage and Top Tracks metadata reviewed; audio mood and recommendation quality not certified. " + t.Notes
			}
			r.Tags = append(r.Tags, RegistryTag{t.Canonical, t.Name, t.Category, t.Family, t.Status, t.Rank, t.Reach, t.Taggings, evidence})
		}
	}
	return r
}

type TagMapping struct {
	Canonical string   `json:"canonical"`
	Type      string   `json:"type"`
	Primary   string   `json:"primary"`
	Aliases   []string `json:"aliases"`
}
