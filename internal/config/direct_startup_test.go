package config

import (
	"testing"
	"time"
)

func TestDirectStartupPrerequisites(t *testing.T) {
	valid := Config{AppEnv: "production", RecommendationEngine: DirectEngine, RecommendationDataMode: "live", YouTubeLiveSearchEnabled: true, YouTubeAPIKey: "test-key", YouTubeSearchMaxCalls: 10, GoogleCloudProject: "test-project", GoogleCloudLocation: "global", VertexModel: DefaultVertexModel, VertexThinkingLevel: "MEDIUM", VertexTimeout: time.Minute}
	if err := ValidateDirectStartup(valid); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.YouTubeAPIKey = "" },
		func(c *Config) { c.RecommendationDataMode = "fixture" },
		func(c *Config) { c.RecommendationDataMode = "replay" },
		func(c *Config) { c.YouTubeLiveSearchEnabled = false },
		func(c *Config) { c.GoogleCloudProject = "" },
		func(c *Config) { c.VertexTimeout = 0 },
		func(c *Config) { c.YouTubeSearchMaxCalls = 0 },
		func(c *Config) { c.VertexThinkingLevel = "invalid" },
		func(c *Config) { c.RecommendationEngine = "invalid" },
	} {
		c := valid
		change(&c)
		if ValidateDirectStartup(c) == nil {
			t.Fatalf("incomplete configuration accepted: %s", c)
		}
	}
	if err := ValidateDirectStartup(Config{RecommendationEngine: LegacyEngine}); err != nil {
		t.Fatal(err)
	}
}
