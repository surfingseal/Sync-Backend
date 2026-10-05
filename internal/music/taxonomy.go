// Package music owns the controlled vocabulary and search-token mappings.
package music

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

//go:embed taxonomy.json
var taxonomyJSON []byte

type Genre struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	QueryTokens string   `json:"query_tokens"`
	Aliases     []string `json:"aliases"`
	Category    string   `json:"-"`
}
type Category struct {
	Slug        string  `json:"slug"`
	DisplayName string  `json:"display_name"`
	Genres      []Genre `json:"genres"`
}
type Vocabulary struct {
	Categories  []Category        `json:"categories"`
	Moods       []string          `json:"moods"`
	MoodAliases map[string]string `json:"mood_aliases"`
}

var vocabulary = load()

func load() Vocabulary {
	var v Vocabulary
	if err := json.Unmarshal(taxonomyJSON, &v); err != nil {
		panic("invalid embedded music taxonomy")
	}
	if err := Validate(v); err != nil {
		panic(err)
	}
	return v
}
func Normalize(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, s)), " ")
}
func Validate(v Vocabulary) error {
	slugs, names, categories, aliases := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]string{}
	categoryNames := map[string]bool{}
	for _, c := range v.Categories {
		if c.Slug == "" || c.DisplayName == "" || categories[c.Slug] || categoryNames[strings.ToLower(c.DisplayName)] {
			return fmt.Errorf("invalid or duplicate category")
		}
		categories[c.Slug] = true
		categoryNames[strings.ToLower(c.DisplayName)] = true
		for _, g := range c.Genres {
			if g.Name == "" || g.DisplayName == "" || slugs[g.Name] || names[strings.ToLower(g.DisplayName)] {
				return fmt.Errorf("invalid or duplicate genre")
			}
			slugs[g.Name] = true
			names[strings.ToLower(g.DisplayName)] = true
			if c.Slug != "other" && strings.TrimSpace(g.QueryTokens) == "" {
				return fmt.Errorf("missing genre query tokens")
			}
			if c.Slug == "other" && (g.Name != "other" && g.Name != "unknown" || g.QueryTokens != "") {
				return fmt.Errorf("unknown must not inject query tokens")
			}
			for _, alias := range append([]string{g.Name, g.DisplayName, g.QueryTokens}, g.Aliases...) {
				key := Normalize(alias)
				if key == "" {
					continue
				}
				if prior, ok := aliases[key]; ok && prior != g.Name {
					return fmt.Errorf("genre alias collision: %s", key)
				}
				aliases[key] = g.Name
			}
		}
	}
	if !slugs["other"] || !slugs["unknown"] {
		return fmt.Errorf("missing other/unknown genres")
	}
	moods := map[string]bool{}
	for _, m := range v.Moods {
		if m == "" || moods[m] {
			return fmt.Errorf("duplicate/empty mood")
		}
		moods[m] = true
	}
	for alias, target := range v.MoodAliases {
		if !moods[target] || moods[alias] {
			return fmt.Errorf("invalid mood alias")
		}
	}
	return nil
}
func Genres() []Genre {
	var result []Genre
	for _, c := range vocabulary.Categories {
		for _, g := range c.Genres {
			g.Category = c.Slug
			g.Aliases = append([]string(nil), g.Aliases...)
			result = append(result, g)
		}
	}
	return result
}
func Categories() []string {
	var result []string
	for _, c := range vocabulary.Categories {
		result = append(result, c.Slug)
	}
	return result
}
func GenreNames() []string {
	var result []string
	for _, g := range Genres() {
		result = append(result, g.Name)
	}
	return result
}

// Lookup is strictly canonical for model output validation.
func Lookup(slug string) (Genre, bool) {
	for _, g := range Genres() {
		if g.Name == slug {
			return g, true
		}
	}
	return Genre{}, false
}

// Resolve accepts only curated aliases; unrecognized labels become diagnostic other.
func Resolve(label string) (Genre, bool) {
	key := Normalize(label)
	if key != "" {
		for _, g := range Genres() {
			for _, s := range append([]string{g.Name, g.DisplayName, g.QueryTokens}, g.Aliases...) {
				if Normalize(s) == key {
					return g, true
				}
			}
		}
	}
	g, _ := Lookup("other")
	return g, false
}
func Moods() []string { return append([]string(nil), vocabulary.Moods...) }
func IsMood(s string) bool {
	for _, m := range vocabulary.Moods {
		if s == m {
			return true
		}
	}
	return false
}
func ResolveMood(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if IsMood(s) {
		return s, true
	}
	if m, ok := vocabulary.MoodAliases[s]; ok {
		return m, true
	}
	return "", false
}
