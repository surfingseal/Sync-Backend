package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"time"

	"example.com/sync/internal/client"
	"example.com/sync/internal/directapi"
	"example.com/sync/internal/directmusic"
	imageprocessing "example.com/sync/internal/image"
	"example.com/sync/internal/model"
	"example.com/sync/internal/service"
	"github.com/gin-gonic/gin"
)

const MaxDirectRequestSize = service.MaxImageSize + 64*1024

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type DirectHandler struct{ recommender *directapi.Recommender }

func NewDirectHandler(s *directapi.Recommender) *DirectHandler { return &DirectHandler{s} }
func (h *DirectHandler) Recommend(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if h.recommender == nil {
		writeDirectError(c, directapi.ErrUnavailable)
		return
	}
	kind, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || kind != "multipart/form-data" {
		writeError(c, 415, "UNSUPPORTED_CONTENT_TYPE", "multipart/form-data is required")
		return
	}
	if c.Request.ContentLength > MaxDirectRequestSize {
		writeDirectError(c, service.ErrImageTooLarge)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxDirectRequestSize)
	defer c.Request.Body.Close()
	reader, err := c.Request.MultipartReader()
	if err != nil {
		writeError(c, 400, "INVALID_REQUEST", "invalid multipart request")
		return
	}
	var uploaded *model.UploadedImage
	seenRequestID := false
	for {
		part, e := reader.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			writeDirectUploadError(c, e)
			return
		}
		switch part.FormName() {
		case "image":
			if uploaded != nil || part.FileName() == "" {
				part.Close()
				writeError(c, 400, "INVALID_REQUEST", "exactly one image file is required")
				return
			}
			data, e := io.ReadAll(io.LimitReader(part, service.MaxImageSize+1))
			part.Close()
			if e != nil {
				writeDirectUploadError(c, e)
				return
			}
			if len(data) == 0 {
				writeError(c, 400, "INVALID_IMAGE", "image file is empty")
				return
			}
			img, e := service.ReadImage(c.Request.Context(), part.FileName(), bytes.NewReader(data))
			if e != nil {
				writeDirectUploadError(c, e)
				return
			}
			uploaded = &img
		case "request_id":
			data, e := io.ReadAll(io.LimitReader(part, 65))
			part.Close()
			if e != nil {
				writeDirectUploadError(c, e)
				return
			}
			if seenRequestID || !requestIDPattern.Match(data) {
				writeError(c, 400, "INVALID_REQUEST", "invalid request_id")
				return
			}
			seenRequestID = true
			c.Header("X-Request-ID", string(data))
		default:
			part.Close()
			writeError(c, 400, "INVALID_REQUEST", "unknown multipart field")
			return
		}
	}
	// Drain trailing bytes as well: MaxBytesReader limits the whole request.
	if _, err := io.Copy(io.Discard, c.Request.Body); err != nil {
		writeDirectUploadError(c, err)
		return
	}
	if uploaded == nil {
		writeError(c, 400, "IMAGE_REQUIRED", "image file is required")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 120*time.Second)
	defer cancel()
	response, err := h.recommender.Recommend(ctx, *uploaded)
	if err != nil {
		writeDirectError(c, err)
		return
	}
	c.JSON(200, response)
}
func writeDirectError(c *gin.Context, err error) {
	var large *http.MaxBytesError
	switch {
	case errors.As(err, &large), errors.Is(err, service.ErrImageTooLarge), errors.Is(err, imageprocessing.ErrTooLarge):
		writeError(c, 413, "IMAGE_TOO_LARGE", "image must be 10MB or smaller")
	case errors.Is(err, service.ErrUnsupportedImageType):
		writeError(c, 415, "UNSUPPORTED_IMAGE_TYPE", "only JPEG, PNG and WebP images are supported")
	case errors.Is(err, imageprocessing.ErrDecodeFailed), errors.Is(err, imageprocessing.ErrDimensionsTooLarge):
		writeError(c, 400, "INVALID_IMAGE", "invalid image")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, 504, "RECOMMENDATION_TIMEOUT", "recommendation timed out")
	case errors.Is(err, context.Canceled):
		writeError(c, 408, "REQUEST_CANCELED", "request was canceled")
	case errors.Is(err, directapi.ErrUnavailable):
		writeError(c, 503, "DIRECT_RECOMMENDATION_UNAVAILABLE", "Direct recommendation is not enabled")
	case errors.Is(err, directapi.ErrCapacity):
		writeError(c, 503, "CHECKPOINT_CAPACITY_EXCEEDED", "recommendation storage is temporarily unavailable")
	case errors.Is(err, client.ErrYouTubeQuota):
		writeError(c, 503, "YOUTUBE_QUOTA_EXCEEDED", "music search is temporarily unavailable")
	case errors.Is(err, directmusic.ErrSearchBudget):
		writeError(c, 503, "YOUTUBE_SEARCH_BUDGET_EXCEEDED", "music search budget exceeded")
	case errors.Is(err, imageprocessing.ErrProcessingFailed):
		writeError(c, 500, "INTERNAL_ERROR", "internal server error")
	case errors.Is(err, directapi.ErrInvalidResult), errors.Is(err, client.ErrInvalidAIResponse):
		writeError(c, 502, "INVALID_RECOMMENDATION_RESPONSE", "invalid recommendation response")
	default:
		writeError(c, 502, "DIRECT_RECOMMENDATION_FAILED", "failed to recommend music")
	}
}

func writeDirectUploadError(c *gin.Context, err error) {
	var large *http.MaxBytesError
	if errors.As(err, &large) || errors.Is(err, service.ErrImageTooLarge) || errors.Is(err, service.ErrUnsupportedImageType) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeDirectError(c, err)
		return
	}
	writeError(c, 400, "INVALID_REQUEST", "invalid multipart request")
}
