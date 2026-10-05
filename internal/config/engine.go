package config

import (
	"fmt"
	"strings"
)

type RecommendationEngine string

const (
	LegacyEngine RecommendationEngine = "legacy"
	DirectEngine RecommendationEngine = "gemini_direct"
)

func ParseRecommendationEngine(value string) (RecommendationEngine, error) {
	v := RecommendationEngine(strings.TrimSpace(value))
	if v == "" {
		v = LegacyEngine
	}
	if v != LegacyEngine && v != DirectEngine {
		return "", fmt.Errorf("RECOMMENDATION_ENGINE must be legacy or gemini_direct")
	}
	return v, nil
}

// Engine selection is explicit; the default remains legacy. Runtime prerequisites
// are checked separately before the server binds a port.
func ValidateServerEngine(engine RecommendationEngine) error {
	if engine == "" || engine == LegacyEngine || engine == DirectEngine {
		return nil
	}
	return fmt.Errorf("invalid recommendation engine")
}

// Direct uses its own multipart route, never the legacy JSON input contract.
// Fail closed rather than serving fixtures or silently activating without auth.
func ValidateDirectStartup(c Config) error {
	if err := ValidateServerEngine(c.RecommendationEngine); err != nil {
		return err
	}
	if c.RecommendationEngine != DirectEngine {
		return nil
	}
	if c.RecommendationDataMode != "live" || !c.YouTubeLiveSearchEnabled {
		return fmt.Errorf("gemini_direct requires live recommendation data and enabled YouTube search")
	}
	if strings.TrimSpace(c.YouTubeAPIKey) == "" {
		return fmt.Errorf("gemini_direct requires YOUTUBE_API_KEY")
	}
	if strings.TrimSpace(c.GoogleCloudProject) == "" || strings.TrimSpace(c.GoogleCloudLocation) == "" || strings.TrimSpace(c.VertexModel) == "" || c.VertexTimeout <= 0 {
		return fmt.Errorf("gemini_direct requires valid Vertex configuration")
	}
	if c.YouTubeSearchMaxCalls < 1 {
		return fmt.Errorf("gemini_direct requires a positive YouTube search budget")
	}
	return ValidateThinking(c.VertexModel, c.VertexThinkingLevel)
}
