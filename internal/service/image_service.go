package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"example.com/sync/internal/client"
	imageproc "example.com/sync/internal/image"
	"example.com/sync/internal/model"
)

// MaxImageSize is 10 MiB; a multipart request has a separate overhead allowance.
const MaxImageSize int64 = 10 * 1024 * 1024

var (
	ErrImageTooLarge        = errors.New("image too large")
	ErrUnsupportedImageType = errors.New("unsupported image type")
)

// ReadImage bounds memory usage and ignores the client-supplied MIME type.
func ReadImage(ctx context.Context, filename string, reader io.Reader) (model.UploadedImage, error) {
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: reader}, MaxImageSize+1))
	if err != nil {
		return model.UploadedImage{}, fmt.Errorf("read image: %w", err)
	}
	if int64(len(data)) > MaxImageSize {
		return model.UploadedImage{}, ErrImageTooLarge
	}
	contentType := http.DetectContentType(data)
	switch contentType {
	case "image/jpeg", "image/png", "image/webp":
	default:
		return model.UploadedImage{}, ErrUnsupportedImageType
	}
	// Treat the name as display metadata only, never as a filesystem path.
	filename = path.Base(strings.ReplaceAll(filename, "\\", "/"))
	return model.UploadedImage{
		ImageInfo: model.ImageInfo{Filename: filename, ContentType: contentType, Size: int64(len(data))},
		Data:      data,
	}, nil
}

type ImageService struct {
	analyzer  client.ImageAnalyzer
	processor imageproc.ImageProcessor
	timeout   time.Duration
}

func NewImageService(analyzer client.ImageAnalyzer, processor imageproc.ImageProcessor, timeout time.Duration) *ImageService {
	return &ImageService{analyzer: analyzer, processor: processor, timeout: timeout}
}

func (s *ImageService) Analyze(ctx context.Context, image model.UploadedImage) (*model.ImageAnalysis, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	processed, err := s.processor.Process(callCtx, image.Data, image.ContentType)
	if err != nil {
		return nil, err
	}
	if err := callCtx.Err(); err != nil {
		return nil, err
	}
	if processed == nil || len(processed.Data) == 0 {
		return nil, imageproc.ErrProcessingFailed
	}
	analysis, err := s.analyzer.AnalyzeImage(callCtx, processed.Data, processed.MIMEType)
	if err != nil {
		if callCtx.Err() != nil {
			return nil, callCtx.Err()
		}
		return nil, err
	}
	if err := callCtx.Err(); err != nil {
		return nil, err
	}
	if err := analysis.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", client.ErrInvalidAIResponse, err)
	}
	return analysis, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
