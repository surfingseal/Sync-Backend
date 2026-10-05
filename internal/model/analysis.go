package model

type ImageAnalysis struct {
	SchemaVersion string              `json:"schema_version,omitempty"`
	Scene         SceneAnalysis       `json:"scene"`
	Visual        VisualAnalysis      `json:"visual"`
	Mood          MoodAnalysis        `json:"mood"`
	MusicProfile  MusicProfile        `json:"music_profile"`
	Confidence    *AnalysisConfidence `json:"confidence,omitempty"`
}

type SceneAnalysis struct {
	Category    string `json:"category"`
	Description string `json:"description"`
	TimeOfDay   string `json:"time_of_day"`
	Weather     string `json:"weather"`
}

type VisualAnalysis struct {
	Brightness       float64  `json:"brightness"`
	ColorTemperature string   `json:"color_temperature"`
	DominantColors   []string `json:"dominant_colors"`
	Motion           string   `json:"motion"`
	Contrast         string   `json:"contrast,omitempty"`
	Saturation       string   `json:"saturation,omitempty"`
}

type MoodAnalysis struct {
	Primary   string   `json:"primary,omitempty"`
	Secondary []string `json:"secondary,omitempty"`
	Tags      []string `json:"tags"`
	Energy    float64  `json:"energy"`
	Valence   float64  `json:"valence"`
}

type MusicProfile struct {
	Tempo                  string           `json:"tempo"`
	Energy                 float64          `json:"energy"`
	VocalPreference        string           `json:"vocal_preference,omitempty"`
	Genres                 []string         `json:"genres"`
	GenreCandidates        []GenreCandidate `json:"genre_candidates,omitempty"`
	InstrumentalPreference *float64         `json:"instrumental_preference,omitempty"`
}

// Scores/confidence are model-reported relevance signals, not calibrated probabilities.
type GenreCandidate struct {
	Category string  `json:"category"`
	Name     string  `json:"name"`
	Score    float64 `json:"score"`
	RawLabel string  `json:"raw_label,omitempty"`
}
type AnalysisConfidence struct {
	Mood  float64 `json:"mood"`
	Genre float64 `json:"genre"`
	Tempo float64 `json:"tempo"`
}

type AnalyzeResponse struct {
	Image    ImageInfo      `json:"image"`
	Analysis *ImageAnalysis `json:"analysis"`
}
