package client

import (
	"context"
	"errors"

	"example.com/sync/internal/model"
)

type ImageAnalyzer interface {
	AnalyzeImage(ctx context.Context, image []byte, mimeType string) (*model.ImageAnalysis, error)
}

var (
	ErrAIService         = errors.New("AI service failed")
	ErrAIRateLimited     = errors.New("AI rate limited")
	ErrAIUnavailable     = errors.New("AI service unavailable")
	ErrAIConfiguration   = errors.New("AI configuration error")
	ErrInvalidAIResponse = errors.New("invalid AI response")
)
