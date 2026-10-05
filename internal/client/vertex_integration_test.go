package client_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	imageproc "example.com/sync/internal/image"
	"example.com/sync/internal/model"
	"example.com/sync/internal/router"
	"example.com/sync/internal/service"
)

func TestVertexIntegration(t *testing.T) {
	if os.Getenv("VERTEX_INTEGRATION_TEST") != "1" {
		t.Skip("set VERTEX_INTEGRATION_TEST=1 to explicitly call Vertex AI")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	analyzer, err := client.NewVertexImageAnalyzer(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	filename := os.Getenv("VERTEX_TEST_IMAGE")
	if filename == "" {
		t.Fatal("VERTEX_TEST_IMAGE is required")
	}
	file, err := os.Open(filename)
	if err != nil {
		t.Fatal("integration image could not be opened")
	}
	defer file.Close()
	image, err := service.ReadImage(context.Background(), file.Name(), file)
	if err != nil {
		t.Fatal(err)
	}
	processor, err := imageproc.NewProcessor(cfg.ImageMaxDimension, cfg.ImageHardMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.NewImageService(analyzer, processor, cfg.VertexTimeout).Analyze(context.Background(), image)
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestVertexUnknownModelIntegration(t *testing.T) {
	if os.Getenv("VERTEX_INTEGRATION_TEST") != "1" {
		t.Skip("explicit Vertex integration opt-in required")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.VertexModel = "sync-nonexistent-model"
	analyzer, err := client.NewVertexImageAnalyzer(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	filename := os.Getenv("VERTEX_TEST_IMAGE")
	if filename == "" {
		t.Fatal("VERTEX_TEST_IMAGE is required")
	}
	file, err := os.Open(filename)
	if err != nil {
		t.Fatal("integration image could not be opened")
	}
	defer file.Close()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image", filepath.Base(filename))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(part, file); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/analyze", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	processor, err := imageproc.NewProcessor(cfg.ImageMaxDimension, cfg.ImageHardMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	router.New(cfg, service.NewImageService(analyzer, processor, cfg.VertexTimeout), nil).ServeHTTP(w, req)
	var response model.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 500 || response.Error.Code != "AI_CONFIGURATION_ERROR" {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
}
