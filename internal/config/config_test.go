package config

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "")
	t.Setenv("VERTEX_MODEL", "")
	t.Setenv("VERTEX_TIMEOUT_SECONDS", "20")
	if err := os.Unsetenv("VERTEX_TIMEOUT_SECONDS"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORT", "8080")
	if err := os.Unsetenv("PORT"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_ENV", "")
	cfg, err := Load()
	if err != nil || cfg.Port != "8080" || cfg.AppEnv != "development" || cfg.VertexModel != DefaultVertexModel || cfg.GoogleCloudLocation != "global" || cfg.VertexTimeout != 20*time.Second {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
}

func TestLoadCustomEnvironment(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("APP_ENV", "production")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "us-central1")
	t.Setenv("VERTEX_MODEL", "custom-model")
	t.Setenv("VERTEX_TIMEOUT_SECONDS", "12")
	cfg, err := Load()
	if err != nil || cfg.Port != "9090" || cfg.AppEnv != "production" || cfg.VertexModel != "custom-model" || cfg.GoogleCloudLocation != "us-central1" || cfg.VertexTimeout != 12*time.Second {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
}

func TestMissingProject(t *testing.T) {
	t.Setenv("PORT", "8080")
	t.Setenv("GOOGLE_CLOUD_PROJECT", " ")
	if _, err := Load(); err == nil || err.Error() != "GOOGLE_CLOUD_PROJECT is required" {
		t.Fatalf("error=%v", err)
	}
}

func TestLoadInvalidPort(t *testing.T) {
	for _, port := range []string{"", "abc", "0", "-1", "65536", "8080:80"} {
		t.Run(port, func(t *testing.T) {
			t.Setenv("PORT", port)
			if _, err := Load(); err == nil {
				t.Fatal("invalid port accepted")
			}
		})
	}
}

func TestInvalidVertexTimeout(t *testing.T) {
	t.Setenv("PORT", "8080")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	for _, value := range []string{"", "0", "-1", "1.5", "abc", "9223372036854775807"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("VERTEX_TIMEOUT_SECONDS", value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid timeout accepted")
			}
		})
	}
}

func TestImageSettings(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("PORT", "8080")
	t.Setenv("IMAGE_MAX_DIMENSION", "1600")
	t.Setenv("IMAGE_HARD_MAX_BYTES", "4194304")
	cfg, err := Load()
	if err != nil || cfg.ImageMaxDimension != 1600 || cfg.ImageHardMaxBytes != 4194304 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	for _, setting := range []struct{ name, value string }{{"IMAGE_MAX_DIMENSION", "0"}, {"IMAGE_MAX_DIMENSION", "10001"}, {"IMAGE_MAX_DIMENSION", "bad"}, {"IMAGE_HARD_MAX_BYTES", "0"}, {"IMAGE_HARD_MAX_BYTES", "6291457"}} {
		t.Run(setting.name+setting.value, func(t *testing.T) {
			t.Setenv(setting.name, setting.value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid image setting accepted")
			}
		})
	}
}

func TestYouTubeConfig(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("PORT", "8080")
	t.Setenv("YOUTUBE_API_KEY", "unit-key-must-not-leak")
	t.Setenv("YOUTUBE_REGION", "us")
	t.Setenv("YOUTUBE_RELEVANCE_LANGUAGE", "en")
	t.Setenv("RECOMMENDATION_COUNT", "20")
	t.Setenv("YOUTUBE_SEARCH_QUERY_COUNT", "3")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.YouTubeRegion != "US" || cfg.YouTubeRelevanceLanguage != "en" || cfg.RecommendationCount != 20 || cfg.YouTubeSearchQueryCount != 3 {
		t.Fatal("custom settings not loaded")
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", cfg, cfg), "unit-key-must-not-leak") {
		t.Fatal("API key exposed by config formatting")
	}
	for _, setting := range []struct{ name, value string }{{"RECOMMENDATION_COUNT", "4"}, {"RECOMMENDATION_COUNT", "21"}, {"YOUTUBE_SEARCH_QUERY_COUNT", "0"}, {"YOUTUBE_SEARCH_QUERY_COUNT", "4"}, {"YOUTUBE_REGION", "KOR"}, {"YOUTUBE_RELEVANCE_LANGUAGE", "bad"}} {
		t.Run(setting.name+setting.value, func(t *testing.T) {
			t.Setenv(setting.name, setting.value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid setting accepted")
			}
		})
	}
}

func TestOAuthConfig(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("PORT", "8080")
	t.Setenv("APP_ENV", "development")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "test-oauth-id")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "test-oauth-secret")
	t.Setenv("GOOGLE_OAUTH_REDIRECT_URL", "")
	t.Setenv("GOOGLE_OAUTH_SCOPE", "")
	for _, name := range []string{"OAUTH_COOKIE_SECURE", "OAUTH_FORCE_CONSENT"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := Load()
	if err != nil || cfg.OAuthCookieSecure || !cfg.OAuthForceConsent || cfg.GoogleOAuthRedirectURL != "http://localhost:8080/api/v1/auth/google/callback" || cfg.GoogleOAuthScope != "https://www.googleapis.com/auth/youtube" {
		t.Fatal("wrong development OAuth defaults")
	}
	for _, value := range []string{fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg)} {
		if strings.Contains(value, "test-oauth-secret") || strings.Contains(value, "test-oauth-id") {
			t.Fatal("OAuth credentials leaked in config formatting")
		}
	}
	t.Setenv("APP_ENV", "production")
	cfg, err = Load()
	if err != nil || !cfg.OAuthCookieSecure || cfg.OAuthForceConsent {
		t.Fatal("wrong production OAuth defaults")
	}
	t.Setenv("OAUTH_COOKIE_SECURE", "false")
	cfg, err = Load()
	if err != nil || !cfg.OAuthInvalid {
		t.Fatal("insecure production cookie accepted")
	}
	t.Setenv("OAUTH_COOKIE_SECURE", "not-a-boolean")
	cfg, err = Load()
	if err != nil || !cfg.OAuthInvalid {
		t.Fatal("invalid OAuth flag must disable only OAuth")
	}
}

func TestThinkingCombinations(t *testing.T) {
	if ValidateThinking("gemini-3.8-flash", "MINIMAL") == nil {
		t.Fatal("unsupported thinking accepted")
	}
	for _, level := range []string{"LOW", "MEDIUM", "HIGH"} {
		if err := ValidateThinking("gemini-3.8-flash", level); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateThinking("gemini-3.5-flash-lite", "MINIMAL"); err != nil {
		t.Fatal(err)
	}
	if ValidateThinking("gemini-3.8-flash", "FAST") == nil {
		t.Fatal("unknown thinking accepted")
	}
}

func TestRecommendationModes(t *testing.T) {
	t.Setenv("PORT", "8080")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("RECOMMENDATION_QUERY_MODE", "")
	t.Setenv("RECOMMENDATION_RANKER", "")
	c, err := Load()
	if err != nil || c.RecommendationQueryMode != "deterministic" || c.RecommendationRanker != "v2" {
		t.Fatal(c, err)
	}
	t.Setenv("RECOMMENDATION_QUERY_MODE", "deterministic")
	t.Setenv("RECOMMENDATION_RANKER", "legacy")
	if _, err = Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RECOMMENDATION_QUERY_MODE", "invalid")
	if _, err = Load(); err == nil {
		t.Fatal("invalid query mode accepted")
	}
	t.Setenv("RECOMMENDATION_QUERY_MODE", "gemini")
	t.Setenv("RECOMMENDATION_RANKER", "invalid")
	if _, err = Load(); err == nil {
		t.Fatal("invalid ranker accepted")
	}
}

func TestQueryLanguageKeywordOption(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("RECOMMENDATION_QUERY_LANGUAGE_KEYWORDS", "false")
	cfg, err := Load()
	if err != nil || cfg.RecommendationQueryLanguageKeywords {
		t.Fatal(cfg, err)
	}
	t.Setenv("RECOMMENDATION_QUERY_LANGUAGE_KEYWORDS", "true")
	cfg, err = Load()
	if err != nil || !cfg.RecommendationQueryLanguageKeywords {
		t.Fatal(cfg, err)
	}
	t.Setenv("RECOMMENDATION_QUERY_LANGUAGE_KEYWORDS", "invalid")
	if _, err := Load(); err == nil {
		t.Fatal("invalid bool accepted")
	}
}

func TestOfflineRetrievalDefaults(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("APP_ENV", "development")
	for _, key := range []string{"RECOMMENDATION_DATA_MODE", "YOUTUBE_LIVE_SEARCH_ENABLED", "YOUTUBE_SEARCH_MAX_CALLS_PER_RUN", "YOUTUBE_SEARCH_CACHE_TTL_SECONDS", "RECOMMENDATION_ADAPTIVE_SAFETY_MARGIN", "RECOMMENDATION_REPLAY_PATH"} {
		t.Setenv(key, "")
	}
	cfg, err := Load()
	if err != nil || cfg.RecommendationDataMode != "fixture" || cfg.YouTubeLiveSearchEnabled || cfg.YouTubeSearchMaxCalls != 10 {
		t.Fatal(cfg, err)
	}
	t.Setenv("APP_ENV", "production")
	cfg, err = Load()
	if err != nil || cfg.RecommendationDataMode != "live" || !cfg.YouTubeLiveSearchEnabled {
		t.Fatal(cfg, err)
	}
	t.Setenv("RECOMMENDATION_DATA_MODE", "replay")
	if _, err = Load(); err == nil {
		t.Fatal("missing replay path")
	}
	t.Setenv("RECOMMENDATION_REPLAY_PATH", "local.json")
	t.Setenv("YOUTUBE_SEARCH_CACHE_TTL_SECONDS", "86400")
	if _, err = Load(); err == nil {
		t.Fatal("long TTL accepted")
	}
}

func TestRetrievalV2Config(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("PORT", "8080")
	t.Setenv("YOUTUBE_SEARCH_MAX_RESULTS", "50")
	t.Setenv("RECOMMENDATION_MIXED_QUERY_KEYWORD", "song")
	cfg, err := Load()
	if err != nil || cfg.YouTubeSearchMaxResults != 50 || cfg.RecommendationMixedKeyword != "song" {
		t.Fatal(err)
	}
	t.Setenv("YOUTUBE_SEARCH_MAX_RESULTS", "51")
	if _, err := Load(); err == nil {
		t.Fatal("51 accepted")
	}
	t.Setenv("YOUTUBE_SEARCH_MAX_RESULTS", "0")
	if _, err := Load(); err == nil {
		t.Fatal("0 accepted")
	}
	t.Setenv("YOUTUBE_SEARCH_MAX_RESULTS", "50")
	t.Setenv("RECOMMENDATION_MIXED_QUERY_KEYWORD", "playlist")
	if _, err := Load(); err == nil {
		t.Fatal("invalid keyword")
	}
}

func TestNeutralRetrievalSettings(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("PORT", "8080")
	cfg, err := Load()
	if err != nil || !cfg.RecommendationRequireRelease || cfg.RecommendationMinMediumEstablished != 6 || cfg.RecommendationMinLanguageCoverage != 2 {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, value string }{{"RECOMMENDATION_MIN_LANGUAGE_COVERAGE_PER_PREFERENCE", "6"}, {"RECOMMENDATION_MIN_MEDIUM_ESTABLISHED", "-1"}, {"RECOMMENDATION_REQUIRE_RELEASE_CONFIDENCE", "invalid"}, {"RECOMMENDATION_ALLOWED_RELEASE_CONFIDENCE", "OFFICIAL"}, {"RECOMMENDATION_Q1_ORDER", "viewCount"}, {"RECOMMENDATION_Q2_POPULARITY_ORDER", "relevance"}} {
		t.Run(tc.key, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid neutral setting accepted")
			}
		})
	}
}
