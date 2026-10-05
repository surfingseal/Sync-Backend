package router

import (
	"example.com/sync/internal/apidocs"
	"example.com/sync/internal/auth"
	"example.com/sync/internal/directapi"
	"example.com/sync/internal/mobileauth"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	"example.com/sync/internal/handler"
	"example.com/sync/internal/model"
	"example.com/sync/internal/service"
	"github.com/gin-gonic/gin"
)

func New(cfg config.Config, images *service.ImageService, recommendations *service.RecommendationService, oauthServices ...*auth.GoogleOAuthService) *gin.Engine {
	var oauth *auth.GoogleOAuthService
	if len(oauthServices) > 0 {
		oauth = oauthServices[0]
	}
	return newRouter(cfg, images, recommendations, oauth, nil, nil)
}

// NewWithDirect is explicit local/test dependency injection. The normal server
// still calls New, defaults to legacy, and keeps ValidateServerEngine intact.
func NewWithDirect(cfg config.Config, images *service.ImageService, recommendations *service.RecommendationService, oauth *auth.GoogleOAuthService, direct *directapi.Recommender, playlists *directapi.Playlists) *gin.Engine {
	if cfg.AppEnv != "development" || cfg.RecommendationEngine != config.DirectEngine {
		direct = nil
		playlists = nil
	}
	return newRouter(cfg, images, recommendations, oauth, direct, playlists)
}
func newRouter(cfg config.Config, images *service.ImageService, recommendations *service.RecommendationService, oauth *auth.GoogleOAuthService, direct *directapi.Recommender, playlists *directapi.Playlists) *gin.Engine {
	gin.SetMode(gin.DebugMode)
	if cfg.AppEnv == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	if cfg.AppEnv == "development" {
		r.Use(gin.LoggerWithFormatter(func(p gin.LogFormatterParams) string {
			// Never log raw query strings, headers, cookies, or panic values.
			return fmt.Sprintf("[GIN] %s | %d | %s | %s %s\n", p.TimeStamp.Format(time.RFC3339), p.StatusCode, p.Latency, p.Method, p.Request.URL.Path)
		}))
	}
	r.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		log.Print("HTTP handler panic recovered")
		c.AbortWithStatusJSON(http.StatusInternalServerError, model.ErrorResponse{
			Error: model.APIError{Code: "INTERNAL_ERROR", Message: "internal server error"},
		})
	}))
	// No reverse proxy is configured at this stage.
	_ = r.SetTrustedProxies(nil)
	r.GET("/health", handler.Health)
	apidocs.Register(r)
	mobile := mobileauth.New(mobileauth.Settings{BaseURL: cfg.MobileAppLinkBaseURL, PackageName: cfg.AndroidPackageName, SigningSHA256: cfg.AndroidAppSigningSHA256, OAuthRedirectURL: cfg.GoogleOAuthRedirectURL}, oauth, mobileauth.NewMemoryStore(mobileauth.DefaultCapacity))
	googleHandler := handler.NewGoogleOAuthHandler(oauth, mobile)
	mobileHandler := handler.NewMobileAuthHandler(mobile, googleHandler)
	authentication := handler.Authenticate(mobile, oauth)
	r.GET("/.well-known/assetlinks.json", mobileHandler.AssetLinks)
	r.GET("/auth/android/complete", mobileHandler.Completion)
	r.POST("/api/v1/auth/google/mobile/start", mobileHandler.Start)
	r.GET("/api/v1/auth/google/mobile/authorize", mobileHandler.Authorize)
	r.POST("/api/v1/auth/mobile/exchange", mobileHandler.Exchange)
	r.DELETE("/api/v1/auth/mobile/session", authentication, mobileHandler.Logout)
	registerV1(r.Group("/api/v1"), handler.NewAnalyzeHandler(images), handler.NewRecommendHandler(recommendations, cfg.RecommendationCount), googleHandler, handler.NewPlaylistHandler(service.NewPlaylistService(oauth, client.NewYouTubePlaylistClient, service.DefaultPlaylistTimeout), cfg.GoogleOAuthRedirectURL, playlists), authentication)
	r.POST("/api/v1/recommend/direct", handler.NewDirectHandler(direct).Recommend)
	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, model.ErrorResponse{
			Error: model.APIError{Code: "NOT_FOUND", Message: "route not found"},
		})
	})
	return r
}

func registerV1(v1 *gin.RouterGroup, analyze *handler.AnalyzeHandler, recommend *handler.RecommendHandler, oauth *handler.GoogleOAuthHandler, playlist *handler.PlaylistHandler, authentication gin.HandlerFunc) {
	// Add implemented handlers here when the corresponding features are ready:
	v1.POST("/analyze", analyze.Analyze)
	v1.POST("/recommend", recommend.Recommend)
	google := v1.Group("/auth/google")
	google.GET("", oauth.Start)
	google.GET("/callback", oauth.Callback)
	google.GET("/status", authentication, oauth.Status)
	google.DELETE("", authentication, oauth.Disconnect)
	v1.POST("/playlists", authentication, playlist.Create)
}
