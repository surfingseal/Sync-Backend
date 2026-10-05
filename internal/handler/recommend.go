package handler

import (
	"context"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"

	"example.com/sync/internal/client"
	"example.com/sync/internal/model"
	"example.com/sync/internal/service"
	"github.com/gin-gonic/gin"
)

const MaxRecommendRequestSize int64 = 64 * 1024

type RecommendHandler struct {
	recommendations *service.RecommendationService
	defaultCount    int
}

func NewRecommendHandler(recommendations *service.RecommendationService, count int) *RecommendHandler {
	return &RecommendHandler{recommendations: recommendations, defaultCount: count}
}
func (h *RecommendHandler) Recommend(c *gin.Context) {
	contentType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeError(c, 415, "UNSUPPORTED_CONTENT_TYPE", "application/json is required")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxRecommendRequestSize)
	defer c.Request.Body.Close()
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(c, 413, "REQUEST_TOO_LARGE", "recommendation request is too large")
		} else {
			writeError(c, 400, "INVALID_REQUEST", "invalid recommendation request")
		}
		return
	}
	request, err := model.DecodeRecommendationRequest(data, h.defaultCount)
	if err != nil {
		writeError(c, 400, "INVALID_REQUEST", "invalid recommendation request")
		return
	}
	if h.recommendations == nil {
		writeError(c, 503, "MUSIC_SEARCH_UNAVAILABLE", "music search is unavailable")
		return
	}
	result, err := h.recommendations.Recommend(c.Request.Context(), *request)
	if err != nil {
		log.Printf("recommendation failed cause_type=%T", err)
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			writeError(c, 504, "RECOMMENDATION_TIMEOUT", "music recommendation timed out")
		case errors.Is(err, client.ErrSearchBudget):
			writeError(c, 503, "YOUTUBE_SEARCH_BUDGET_EXCEEDED", "music search budget is exhausted")
		case errors.Is(err, client.ErrLiveSearchDisabled):
			writeError(c, 503, "YOUTUBE_LIVE_SEARCH_DISABLED", "live music search is disabled")
		case errors.Is(err, client.ErrYouTubeQuota):
			writeError(c, 503, "YOUTUBE_QUOTA_EXCEEDED", "music search is temporarily unavailable")
		case errors.Is(err, client.ErrMusicNotConfigured):
			writeError(c, 503, "MUSIC_SEARCH_UNAVAILABLE", "music search is unavailable")
		default:
			writeError(c, 502, "MUSIC_SEARCH_ERROR", "failed to search music")
		}
		return
	}
	c.JSON(200, result)
}
