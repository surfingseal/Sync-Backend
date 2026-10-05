package handler

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"example.com/sync/internal/client"
	imageproc "example.com/sync/internal/image"
	"example.com/sync/internal/model"
	"example.com/sync/internal/service"
	"github.com/gin-gonic/gin"
)

// Allow multipart headers/boundaries in addition to an exact 10 MiB image.
const MaxAnalyzeRequestSize = service.MaxImageSize + 64*1024

type AnalyzeHandler struct{ images *service.ImageService }

func NewAnalyzeHandler(images *service.ImageService) *AnalyzeHandler {
	return &AnalyzeHandler{images: images}
}

func (h *AnalyzeHandler) Analyze(c *gin.Context) {
	start := time.Now()
	defer func() {
		log.Printf("analyze total_request_ms=%.2f http_status=%d", float64(time.Since(start))/float64(time.Millisecond), c.Writer.Status())
	}()
	if c.Request.ContentLength > MaxAnalyzeRequestSize {
		writeAnalyzeError(c, service.ErrImageTooLarge)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxAnalyzeRequestSize)
	defer c.Request.Body.Close()
	// MultipartReader streams parts without ParseMultipartForm's disk spill.
	reader, err := c.Request.MultipartReader()
	if err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "multipart/form-data request is required")
		return
	}
	var image model.UploadedImage
	found := false
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeAnalyzeError(c, err)
			return
		}
		if part.FormName() != "image" || part.FileName() == "" {
			_, err = io.Copy(io.Discard, part)
		} else {
			if found {
				writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "exactly one image file is allowed")
				return
			}
			found = true
			image, err = service.ReadImage(c.Request.Context(), part.FileName(), part)
		}
		if err != nil {
			writeAnalyzeError(c, err)
			return
		}
		if err := part.Close(); err != nil {
			writeAnalyzeError(c, err)
			return
		}
	}
	// Drain any epilogue as well, enforcing the cap for unknown-length bodies.
	if _, err := io.Copy(io.Discard, c.Request.Body); err != nil {
		writeAnalyzeError(c, err)
		return
	}
	if !found {
		writeError(c, http.StatusBadRequest, "IMAGE_REQUIRED", "image file is required")
		return
	}
	analysis, err := h.images.Analyze(c.Request.Context(), image)
	if err != nil {
		log.Printf("analyze image: %v", err)
		switch {
		case errors.Is(err, imageproc.ErrDecodeFailed):
			writeError(c, http.StatusBadRequest, "IMAGE_DECODE_FAILED", "failed to process image")
		case errors.Is(err, imageproc.ErrDimensionsTooLarge):
			writeError(c, http.StatusBadRequest, "IMAGE_DIMENSIONS_TOO_LARGE", "image dimensions are too large")
		case errors.Is(err, imageproc.ErrProcessingFailed):
			writeError(c, http.StatusInternalServerError, "IMAGE_PROCESSING_FAILED", "failed to process image")
		case errors.Is(err, imageproc.ErrTooLarge):
			writeError(c, http.StatusRequestEntityTooLarge, "IMAGE_TOO_LARGE", "processed image is too large")
		case errors.Is(err, context.DeadlineExceeded):
			writeError(c, http.StatusGatewayTimeout, "AI_TIMEOUT", "image analysis timed out")
		case errors.Is(err, client.ErrAIRateLimited):
			writeError(c, http.StatusServiceUnavailable, "AI_RATE_LIMITED", "image analysis service is temporarily busy")
		case errors.Is(err, client.ErrAIUnavailable):
			writeError(c, http.StatusServiceUnavailable, "AI_SERVICE_UNAVAILABLE", "image analysis service is temporarily unavailable")
		case errors.Is(err, client.ErrAIConfiguration):
			writeError(c, http.StatusInternalServerError, "AI_CONFIGURATION_ERROR", "image analysis service configuration error")
		case errors.Is(err, client.ErrInvalidAIResponse):
			writeError(c, http.StatusBadGateway, "INVALID_AI_RESPONSE", "invalid image analysis response")
		default:
			writeError(c, http.StatusBadGateway, "AI_SERVICE_ERROR", "failed to analyze image")
		}
		return
	}
	c.JSON(http.StatusOK, model.AnalyzeResponse{Image: image.ImageInfo, Analysis: analysis})
}

func writeAnalyzeError(c *gin.Context, err error) {
	var maxBytesErr *http.MaxBytesError
	switch {
	case errors.Is(err, service.ErrImageTooLarge) || errors.As(err, &maxBytesErr):
		writeError(c, http.StatusRequestEntityTooLarge, "IMAGE_TOO_LARGE", "image must be 10MB or smaller")
	case errors.Is(err, service.ErrUnsupportedImageType):
		writeError(c, http.StatusUnsupportedMediaType, "UNSUPPORTED_IMAGE_TYPE", "only JPEG, PNG and WebP images are supported")
	default:
		log.Printf("read image upload: %v", err)
		writeError(c, http.StatusBadRequest, "IMAGE_READ_ERROR", "could not read image upload")
	}
}

func writeError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, model.ErrorResponse{Error: model.APIError{Code: code, Message: message}})
}
