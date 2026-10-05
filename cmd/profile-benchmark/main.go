// Offline audit/consistency harness. It never constructs an external API client.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
	"example.com/sync/internal/music"
)

type savedInput struct {
	ID       string          `json:"id"`
	Analysis json.RawMessage `json:"analysis"`
}
type repeatedInput struct {
	PhotoID  string            `json:"photo_id"`
	Analyses []json.RawMessage `json:"analyses"`
}
type audit struct {
	PhotoID string            `json:"photo_id"`
	Intent  model.QueryIntent `json:"intent"`
	Queries []string          `json:"queries"`
}
type variation struct {
	Samples  int     `json:"samples"`
	StdDev   float64 `json:"stddev"`
	Variance float64 `json:"variance"`
}
type consistency struct {
	PhotoID              string               `json:"photo_id"`
	PairCount            int                  `json:"pair_count"`
	PrimaryMoodAgreement float64              `json:"primary_mood_agreement"`
	SecondaryMoodJaccard float64              `json:"secondary_mood_jaccard"`
	GenreTop3Jaccard     float64              `json:"genre_top3_jaccard"`
	GenreFamilyAgreement float64              `json:"primary_genre_family_agreement"`
	TempoAgreement       float64              `json:"tempo_agreement"`
	MusicEnergyMAE       float64              `json:"music_energy_pairwise_mae"`
	MoodEnergyMAE        float64              `json:"mood_energy_pairwise_mae"`
	ValenceMAE           float64              `json:"valence_pairwise_mae"`
	GenreScoreVariation  map[string]variation `json:"genre_score_variation"`
	ConfidenceVariation  map[string]variation `json:"confidence_variation"`
}

func equal(a, b string) float64 {
	if a == b {
		return 1
	}
	return 0
}
func jaccard(a, b []string) float64 {
	union := map[string]bool{}
	left := map[string]bool{}
	for _, s := range a {
		union[s] = true
		left[s] = true
	}
	intersection := map[string]bool{}
	for _, s := range b {
		union[s] = true
		if left[s] {
			intersection[s] = true
		}
	}
	if len(union) == 0 {
		return 1
	}
	return float64(len(intersection)) / float64(len(union))
}
func spread(values []float64) variation {
	mean := 0.0
	for _, v := range values {
		mean += v
	}
	mean /= float64(len(values))
	variance := 0.0
	for _, v := range values {
		variance += (v - mean) * (v - mean)
	}
	return variation{len(values), math.Sqrt(variance / float64(len(values))), variance / float64(len(values))}
}
func compare(photo string, analyses []model.ImageAnalysis) (consistency, error) {
	r := consistency{PhotoID: photo, GenreScoreVariation: map[string]variation{}, ConfidenceVariation: map[string]variation{}}
	if len(analyses) < 2 || len(analyses) > 3 {
		return r, fmt.Errorf("each photo requires 2 or 3 analyses")
	}
	families := []string{}
	genreSets := [][]string{}
	scores := map[string][]float64{}
	confidences := map[string][]float64{}
	for _, a := range analyses {
		if err := a.Validate(); err != nil {
			return r, err
		}
		if a.SchemaVersion != "2" && a.SchemaVersion != "3" {
			return r, fmt.Errorf("enhanced repeat required")
		}
		family := a.MusicProfile.GenreCandidates[0]
		for _, g := range a.MusicProfile.GenreCandidates {
			if g.Score > family.Score {
				family = g
			}
		}
		families = append(families, family.Category)
		names := []string{}
		for _, g := range a.MusicProfile.GenreCandidates {
			names = append(names, g.Name)
			scores[g.Name] = append(scores[g.Name], g.Score)
		}
		genreSets = append(genreSets, names)
		if a.Confidence != nil {
			confidences["mood"] = append(confidences["mood"], a.Confidence.Mood)
			confidences["genre"] = append(confidences["genre"], a.Confidence.Genre)
			confidences["tempo"] = append(confidences["tempo"], a.Confidence.Tempo)
		}
	}
	for i := 0; i < len(analyses); i++ {
		for j := i + 1; j < len(analyses); j++ {
			a, b := analyses[i], analyses[j]
			r.PairCount++
			r.PrimaryMoodAgreement += equal(a.Mood.Primary, b.Mood.Primary)
			r.SecondaryMoodJaccard += jaccard(a.Mood.Secondary, b.Mood.Secondary)
			r.GenreTop3Jaccard += jaccard(genreSets[i], genreSets[j])
			r.GenreFamilyAgreement += equal(families[i], families[j])
			r.TempoAgreement += equal(a.MusicProfile.Tempo, b.MusicProfile.Tempo)
			r.MusicEnergyMAE += math.Abs(a.MusicProfile.Energy - b.MusicProfile.Energy)
			r.MoodEnergyMAE += math.Abs(a.Mood.Energy - b.Mood.Energy)
			r.ValenceMAE += math.Abs(a.Mood.Valence - b.Mood.Valence)
		}
	}
	for _, value := range []*float64{&r.PrimaryMoodAgreement, &r.SecondaryMoodJaccard, &r.GenreTop3Jaccard, &r.GenreFamilyAgreement, &r.TempoAgreement, &r.MusicEnergyMAE, &r.MoodEnergyMAE, &r.ValenceMAE} {
		*value /= float64(r.PairCount)
	}
	for name, values := range scores {
		r.GenreScoreVariation[name] = spread(values)
	}
	for name, values := range confidences {
		r.ConfidenceVariation[name] = spread(values)
	}
	return r, nil
}
func save(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	dataset := flag.String("dataset", "", "legacy/enhanced saved analyses (offline)")
	repeats := flag.String("repeats", "", "saved enhanced repeats [{photo_id,analyses:[...2..3]}] (offline)")
	out := flag.String("out", "", "new output directory")
	flag.Parse()
	if (*dataset == "") == (*repeats == "") || *out == "" {
		return fmt.Errorf("provide exactly one of dataset/repeats and out")
	}
	if _, err := os.Stat(*out); !os.IsNotExist(err) {
		return fmt.Errorf("refusing existing/unavailable output")
	}
	var records []audit
	var results []consistency
	prefs := model.MusicPreferences{Languages: []string{"ko", "en"}, Count: 10}
	if *dataset != "" {
		raw, err := os.ReadFile(*dataset)
		if err != nil {
			return err
		}
		var inputs []savedInput
		if json.Unmarshal(raw, &inputs) != nil || len(inputs) < 12 {
			return fmt.Errorf("at least 12 saved analyses required")
		}
		seen := map[string]bool{}
		for _, item := range inputs {
			if item.ID == "" || seen[item.ID] {
				return fmt.Errorf("invalid or duplicate photo ID")
			}
			seen[item.ID] = true
			a, err := model.DecodeImageAnalysis(item.Analysis)
			if err != nil {
				return fmt.Errorf("invalid analysis %s", item.ID)
			}
			intent, err := model.AdaptQueryIntent(*a, prefs)
			if err != nil {
				return err
			}
			queries, err := (client.DeterministicQueryBuilder{}).GenerateMusicQueries(context.Background(), *a, prefs, 2)
			if err != nil {
				return err
			}
			records = append(records, audit{item.ID, intent, queries})
		}
	} else {
		raw, err := os.ReadFile(*repeats)
		if err != nil {
			return err
		}
		var inputs []repeatedInput
		if json.Unmarshal(raw, &inputs) != nil || len(inputs) == 0 {
			return fmt.Errorf("invalid repeat manifest")
		}
		seen := map[string]bool{}
		for _, item := range inputs {
			if item.PhotoID == "" || seen[item.PhotoID] {
				return fmt.Errorf("invalid or duplicate photo ID")
			}
			seen[item.PhotoID] = true
			analyses := []model.ImageAnalysis{}
			for _, b := range item.Analyses {
				a, err := model.DecodeImageAnalysis(b)
				if err != nil {
					return err
				}
				analyses = append(analyses, *a)
			}
			result, err := compare(item.PhotoID, analyses)
			if err != nil {
				return err
			}
			results = append(results, result)
		}
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(*out, 0700); err != nil {
		return err
	}
	metadata := map[string]any{"actual_api_calls": 0, "vertex": 0, "youtube": 0, "user_oauth": 0, "playlist": 0, "taxonomy_category_count": len(music.Categories()), "taxonomy_genre_count": len(music.Genres()), "legacy_adaptations": len(records), "repeat_photo_count": len(results), "human_scores_entered": false, "note": "Legacy projection does not create model scores/confidence. Pairwise MAE is repeat disagreement, not ground-truth accuracy; score variation reports only observed labels, no zero imputation."}
	if err := save(filepath.Join(*out, "summary.json"), metadata); err != nil {
		return err
	}
	if len(records) > 0 {
		if err := save(filepath.Join(*out, "profiles.json"), records); err != nil {
			return err
		}
		schema, err := client.AnalysisSchema()
		if err != nil {
			return err
		}
		if err := save(filepath.Join(*out, "enhanced-schema.json"), schema); err != nil {
			return err
		}
		var b strings.Builder
		b.WriteString("# Saved Analysis → Controlled Query Audit\n\nOffline legacy projection only; not new Gemini analysis or recommendation results. Human ratings are blank.\n")
		for _, r := range records {
			fmt.Fprintf(&b, "\n## %s\n\nMood: %s / %s\n\n", r.PhotoID, r.Intent.PrimaryMood, strings.Join(r.Intent.SecondaryMoods, ", "))
			for i, q := range r.Queries {
				fmt.Fprintf(&b, "%d. %s\n", i+1, q)
			}
			b.WriteString("\nQuery usefulness: __ / 5\n\nComment: __\n")
		}
		if err := os.WriteFile(filepath.Join(*out, "review.md"), []byte(b.String()), 0600); err != nil {
			return err
		}
	} else {
		if err := save(filepath.Join(*out, "consistency.json"), results); err != nil {
			return err
		}
	}
	fmt.Printf("offline legacy_profiles=%d repeat_photos=%d actual_api_calls=0\n", len(records), len(results))
	return nil
}
