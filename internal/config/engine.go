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

// Until a cold-cache readiness gate passes, reject direct startup explicitly.
// The current JSON ImageAnalysis request contains no raw image for Direct.
func ValidateServerEngine(engine RecommendationEngine) error {
	if engine == "" || engine == LegacyEngine {
		return nil
	}
	if engine == DirectEngine {
		return fmt.Errorf("gemini_direct production integration is not enabled: technical benchmark readiness and backward-compatible image input contract are required; use RECOMMENDATION_ENGINE=legacy")
	}
	return fmt.Errorf("invalid recommendation engine")
}
