package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const DefaultVertexModel = "gemini-3.8-flash"

type Config struct {
	RecommendationEngine                RecommendationEngine
	RecommendationDataMode              string
	RecommendationReplayPath            string
	YouTubeLiveSearchEnabled            bool
	YouTubeSearchMaxCalls               int
	YouTubeSearchCacheTTL               time.Duration
	RecommendationSafetyMargin          int
	GoogleOAuthClientID                 string `json:"-"`
	GoogleOAuthClientSecret             string `json:"-"`
	GoogleOAuthRedirectURL              string
	GoogleOAuthScope                    string
	OAuthCookieSecure                   bool
	OAuthForceConsent                   bool
	OAuthInvalid                        bool
	YouTubeAPIKey                       string `json:"-"`
	YouTubeRegion                       string
	YouTubeRelevanceLanguage            string
	RecommendationQueryMode             string
	RecommendationQueryLanguageKeywords bool
	RecommendationMixedKeyword          string
	RecommendationMinMediumEstablished  int
	RecommendationMinLanguageCoverage   int
	RecommendationRequireRelease        bool
	RecommendationAllowedRelease        []string
	YouTubeSearchMaxResults             int
	RecommendationRanker                string
	RecommendationCount                 int
	YouTubeSearchQueryCount             int
	RecommendationTimeout               time.Duration
	ImageMaxDimension                   int
	ImageHardMaxBytes                   int
	Port                                string
	AppEnv                              string
	GoogleCloudProject                  string
	GoogleCloudLocation                 string
	VertexModel                         string
	VertexTimeout                       time.Duration
	VertexThinkingLevel                 string
	VertexRetryAttempts                 int
	VertexRetryMode                     string
}

func Load() (Config, error) {
	engine, err := ParseRecommendationEngine(os.Getenv("RECOMMENDATION_ENGINE"))
	if err != nil {
		return Config{}, err
	}
	cfg := Config{RecommendationEngine: engine, RecommendationMinMediumEstablished: 6, RecommendationMinLanguageCoverage: 2, RecommendationRequireRelease: true, RecommendationAllowedRelease: []string{"HIGH", "MEDIUM"}, RecommendationMixedKeyword: "song", YouTubeSearchMaxResults: 50, RecommendationQueryLanguageKeywords: true, RecommendationQueryMode: "deterministic", RecommendationRanker: "v2", YouTubeRegion: "KR", YouTubeRelevanceLanguage: "ko", RecommendationCount: 10, YouTubeSearchQueryCount: 2, RecommendationTimeout: 15 * time.Second, ImageMaxDimension: 1920, ImageHardMaxBytes: 6291456, Port: "8080", AppEnv: "development", GoogleCloudLocation: "global", VertexModel: DefaultVertexModel, VertexTimeout: 20 * time.Second, VertexThinkingLevel: "MEDIUM", VertexRetryAttempts: 3, VertexRetryMode: "sdk"}
	if port, exists := os.LookupEnv("PORT"); exists {
		cfg.Port = port
	}
	if env := strings.TrimSpace(os.Getenv("APP_ENV")); env != "" {
		cfg.AppEnv = env
	}
	cfg.RecommendationDataMode = "fixture"
	cfg.YouTubeSearchMaxCalls = 10
	cfg.YouTubeSearchCacheTTL = 5 * time.Minute
	cfg.RecommendationSafetyMargin = 3
	if cfg.AppEnv == "production" {
		cfg.RecommendationDataMode = "live"
		cfg.YouTubeLiveSearchEnabled = true
	}
	if mode := os.Getenv("RECOMMENDATION_DATA_MODE"); mode != "" {
		cfg.RecommendationDataMode = mode
	}
	switch cfg.RecommendationDataMode {
	case "fixture", "replay", "live":
	default:
		return Config{}, fmt.Errorf("invalid RECOMMENDATION_DATA_MODE")
	}
	cfg.RecommendationReplayPath = os.Getenv("RECOMMENDATION_REPLAY_PATH")
	if cfg.RecommendationDataMode == "replay" && cfg.RecommendationReplayPath == "" {
		return Config{}, fmt.Errorf("RECOMMENDATION_REPLAY_PATH is required")
	}
	if raw := os.Getenv("YOUTUBE_LIVE_SEARCH_ENABLED"); raw != "" {
		v, e := strconv.ParseBool(raw)
		if e != nil {
			return Config{}, fmt.Errorf("invalid YOUTUBE_LIVE_SEARCH_ENABLED")
		}
		cfg.YouTubeLiveSearchEnabled = v
	}
	for _, setting := range []struct {
		name     string
		target   *int
		min, max int
	}{
		{"YOUTUBE_SEARCH_MAX_CALLS_PER_RUN", &cfg.YouTubeSearchMaxCalls, 0, 1000}, {"RECOMMENDATION_ADAPTIVE_SAFETY_MARGIN", &cfg.RecommendationSafetyMargin, 0, 10}} {
		if raw := os.Getenv(setting.name); raw != "" {
			n, e := strconv.Atoi(raw)
			if e != nil || n < setting.min || n > setting.max {
				return Config{}, fmt.Errorf("invalid %s", setting.name)
			}
			*setting.target = n
		}
	}
	if raw := os.Getenv("YOUTUBE_SEARCH_CACHE_TTL_SECONDS"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 3600 {
			return Config{}, fmt.Errorf("invalid YOUTUBE_SEARCH_CACHE_TTL_SECONDS")
		}
		cfg.YouTubeSearchCacheTTL = time.Duration(n) * time.Second
	}
	port, err := strconv.Atoi(cfg.Port)
	if err != nil || port < 1 || port > 65535 {
		return Config{}, fmt.Errorf("PORT must be an integer between 1 and 65535")
	}
	cfg.GoogleCloudProject = strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_PROJECT"))
	if cfg.GoogleCloudProject == "" {
		return Config{}, fmt.Errorf("GOOGLE_CLOUD_PROJECT is required")
	}
	if location := strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_LOCATION")); location != "" {
		cfg.GoogleCloudLocation = location
	}
	if name := strings.TrimSpace(os.Getenv("VERTEX_MODEL")); name != "" {
		cfg.VertexModel = name
	}
	if value := strings.TrimSpace(os.Getenv("VERTEX_THINKING_LEVEL")); value != "" {
		cfg.VertexThinkingLevel = strings.ToUpper(value)
	}
	if err := ValidateThinking(cfg.VertexModel, cfg.VertexThinkingLevel); err != nil {
		return Config{}, err
	}
	if value := os.Getenv("VERTEX_RETRY_ATTEMPTS"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 3 {
			return Config{}, fmt.Errorf("VERTEX_RETRY_ATTEMPTS must be 1 to 3")
		}
		cfg.VertexRetryAttempts = n
	}
	if value := os.Getenv("VERTEX_RETRY_MODE"); value != "" {
		cfg.VertexRetryMode = value
	}
	if cfg.VertexRetryMode != "sdk" && cfg.VertexRetryMode != "budget" {
		return Config{}, fmt.Errorf("VERTEX_RETRY_MODE must be sdk or budget")
	}
	if cfg.VertexRetryMode == "budget" && cfg.VertexRetryAttempts > 2 {
		return Config{}, fmt.Errorf("budget retry supports at most 2 attempts")
	}
	if raw, exists := os.LookupEnv("VERTEX_TIMEOUT_SECONDS"); exists {
		seconds, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || seconds <= 0 || seconds > int64((1<<63-1)/time.Second) {
			return Config{}, fmt.Errorf("VERTEX_TIMEOUT_SECONDS must be a positive integer within the duration range")
		}
		cfg.VertexTimeout = time.Duration(seconds) * time.Second
	}
	for _, setting := range []struct {
		name    string
		value   *int
		maximum int
	}{
		{"IMAGE_MAX_DIMENSION", &cfg.ImageMaxDimension, 10000},
		{"IMAGE_HARD_MAX_BYTES", &cfg.ImageHardMaxBytes, 6291456},
	} {
		if raw, exists := os.LookupEnv(setting.name); exists {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > setting.maximum {
				return Config{}, fmt.Errorf("%s must be an integer between 1 and %d", setting.name, setting.maximum)
			}
			*setting.value = value
		}
	}
	if mode := os.Getenv("RECOMMENDATION_QUERY_MODE"); mode != "" {
		cfg.RecommendationQueryMode = mode
	}
	if raw, exists := os.LookupEnv("RECOMMENDATION_QUERY_LANGUAGE_KEYWORDS"); exists {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("RECOMMENDATION_QUERY_LANGUAGE_KEYWORDS must be a boolean")
		}
		cfg.RecommendationQueryLanguageKeywords = value
	}
	if cfg.RecommendationQueryMode != "gemini" && cfg.RecommendationQueryMode != "deterministic" {
		return Config{}, fmt.Errorf("RECOMMENDATION_QUERY_MODE must be gemini or deterministic")
	}
	if mode := os.Getenv("RECOMMENDATION_RANKER"); mode != "" {
		cfg.RecommendationRanker = mode
	}
	if cfg.RecommendationRanker != "legacy" && cfg.RecommendationRanker != "v2" {
		return Config{}, fmt.Errorf("RECOMMENDATION_RANKER must be legacy or v2")
	}
	cfg.YouTubeAPIKey = strings.TrimSpace(os.Getenv("YOUTUBE_API_KEY"))
	if value := strings.TrimSpace(os.Getenv("YOUTUBE_REGION")); value != "" {
		cfg.YouTubeRegion = strings.ToUpper(value)
	}
	if len(cfg.YouTubeRegion) != 2 || cfg.YouTubeRegion[0] < 'A' || cfg.YouTubeRegion[0] > 'Z' || cfg.YouTubeRegion[1] < 'A' || cfg.YouTubeRegion[1] > 'Z' {
		return Config{}, fmt.Errorf("YOUTUBE_REGION must be a two-letter country code")
	}
	if value := strings.TrimSpace(os.Getenv("YOUTUBE_RELEVANCE_LANGUAGE")); value != "" {
		cfg.YouTubeRelevanceLanguage = value
	}
	if len(cfg.YouTubeRelevanceLanguage) != 2 || cfg.YouTubeRelevanceLanguage[0] < 'a' || cfg.YouTubeRelevanceLanguage[0] > 'z' || cfg.YouTubeRelevanceLanguage[1] < 'a' || cfg.YouTubeRelevanceLanguage[1] > 'z' {
		return Config{}, fmt.Errorf("YOUTUBE_RELEVANCE_LANGUAGE must be a lowercase two-letter language code")
	}
	for _, setting := range []struct {
		name             string
		target           *int
		minimum, maximum int
	}{
		{"RECOMMENDATION_COUNT", &cfg.RecommendationCount, 5, 20},
		{"RECOMMENDATION_MIN_MEDIUM_ESTABLISHED", &cfg.RecommendationMinMediumEstablished, 0, 20},
		{"RECOMMENDATION_MIN_LANGUAGE_COVERAGE_PER_PREFERENCE", &cfg.RecommendationMinLanguageCoverage, 0, 5},
		{"YOUTUBE_SEARCH_MAX_RESULTS", &cfg.YouTubeSearchMaxResults, 1, 50},
		{"YOUTUBE_SEARCH_QUERY_COUNT", &cfg.YouTubeSearchQueryCount, 1, 3},
	} {
		if raw, exists := os.LookupEnv(setting.name); exists {
			value, err := strconv.Atoi(raw)
			if err != nil || value < setting.minimum || value > setting.maximum {
				return Config{}, fmt.Errorf("%s is outside the supported range", setting.name)
			}
			*setting.target = value
		}
	}
	if value := strings.TrimSpace(os.Getenv("RECOMMENDATION_MIXED_QUERY_KEYWORD")); value != "" {
		cfg.RecommendationMixedKeyword = value
	}
	if cfg.RecommendationMixedKeyword != "song" && cfg.RecommendationMixedKeyword != "music" {
		return Config{}, fmt.Errorf("RECOMMENDATION_MIXED_QUERY_KEYWORD must be song or music")
	}
	if value := strings.TrimSpace(os.Getenv("RECOMMENDATION_Q1_ORDER")); value != "" && value != "relevance" {
		return Config{}, fmt.Errorf("RECOMMENDATION_Q1_ORDER must be relevance")
	}
	if value := strings.TrimSpace(os.Getenv("RECOMMENDATION_Q2_POPULARITY_ORDER")); value != "" && value != "viewCount" {
		return Config{}, fmt.Errorf("RECOMMENDATION_Q2_POPULARITY_ORDER must be viewCount")
	}
	if value := os.Getenv("RECOMMENDATION_REQUIRE_RELEASE_CONFIDENCE"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("invalid RECOMMENDATION_REQUIRE_RELEASE_CONFIDENCE")
		}
		cfg.RecommendationRequireRelease = parsed
	}
	if value, exists := os.LookupEnv("RECOMMENDATION_ALLOWED_RELEASE_CONFIDENCE"); exists {
		cfg.RecommendationAllowedRelease = []string{}
		seen := map[string]bool{}
		for _, raw := range strings.Split(value, ",") {
			level := strings.TrimSpace(raw)
			if level != "HIGH" && level != "MEDIUM" && level != "LOW" && level != "UNKNOWN" {
				return Config{}, fmt.Errorf("invalid RECOMMENDATION_ALLOWED_RELEASE_CONFIDENCE")
			}
			if !seen[level] {
				cfg.RecommendationAllowedRelease = append(cfg.RecommendationAllowedRelease, level)
				seen[level] = true
			}
		}
	}
	cfg.GoogleOAuthClientID = strings.TrimSpace(os.Getenv("GOOGLE_OAUTH_CLIENT_ID"))
	cfg.GoogleOAuthClientSecret = strings.TrimSpace(os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET"))
	cfg.GoogleOAuthRedirectURL = "http://localhost:8080/api/v1/auth/google/callback"
	if value := strings.TrimSpace(os.Getenv("GOOGLE_OAUTH_REDIRECT_URL")); value != "" {
		cfg.GoogleOAuthRedirectURL = value
	}
	cfg.GoogleOAuthScope = "https://www.googleapis.com/auth/youtube"
	if value := strings.TrimSpace(os.Getenv("GOOGLE_OAUTH_SCOPE")); value != "" {
		cfg.GoogleOAuthScope = value
	}
	cfg.OAuthCookieSecure = cfg.AppEnv == "production"
	cfg.OAuthForceConsent = cfg.AppEnv != "production"
	for _, setting := range []struct {
		name   string
		target *bool
	}{
		{"OAUTH_COOKIE_SECURE", &cfg.OAuthCookieSecure}, {"OAUTH_FORCE_CONSENT", &cfg.OAuthForceConsent},
	} {
		if raw, exists := os.LookupEnv(setting.name); exists {
			value, err := strconv.ParseBool(raw)
			if err != nil {
				cfg.OAuthInvalid = true
			} else {
				*setting.target = value
			}
		}
	}
	if cfg.AppEnv == "production" && !cfg.OAuthCookieSecure {
		cfg.OAuthInvalid = true
	}
	return cfg, nil
}

// Safe formatting prevents accidental API-key logging via common fmt verbs.
func (c Config) String() string {
	return fmt.Sprintf("Config{port=%s env=%s project=%s location=%s model=%s youtube_configured=%t}", c.Port, c.AppEnv, c.GoogleCloudProject, c.GoogleCloudLocation, c.VertexModel, c.YouTubeAPIKey != "")
}
func (c Config) GoString() string { return c.String() }

func ValidateThinking(model, level string) error {
	switch level {
	case "LOW", "MEDIUM", "HIGH", "MINIMAL":
	default:
		return fmt.Errorf("invalid VERTEX_THINKING_LEVEL")
	}
	if strings.HasPrefix(model, "gemini-3.8-flash") && level == "MINIMAL" {
		return fmt.Errorf("gemini-3.8-flash does not support MINIMAL thinking")
	}
	return nil
}
