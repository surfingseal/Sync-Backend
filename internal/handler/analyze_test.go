package handler_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"

	"example.com/sync/internal/client"
	"example.com/sync/internal/config"
	"example.com/sync/internal/handler"
	imageproc "example.com/sync/internal/image"
	"example.com/sync/internal/model"
	"example.com/sync/internal/router"
	"example.com/sync/internal/service"
	"github.com/gin-gonic/gin"
)

func imageFixtures(t *testing.T) ([]byte, []byte, []byte) {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 1, 1))
	im.Set(0, 0, color.RGBA{R: 200, A: 255})
	var jpg, pngFile bytes.Buffer
	if err := jpeg.Encode(&jpg, im, nil); err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(&pngFile, im); err != nil {
		t.Fatal(err)
	}
	// A small WebP fixture; no additional image encoder dependency is needed.
	webp, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	return jpg.Bytes(), pngFile.Bytes(), webp
}

func uploadRequest(t *testing.T, field, filename, claimedType string, data []byte, duplicate bool) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if field != "" {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="`+field+`"; filename="`+filename+`"`)
		header.Set("Content-Type", claimedType)
		part, err := w.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
		if duplicate {
			part, err = w.CreateFormFile("image", "second.png")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write(data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/analyze", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func TestAnalyzeUploads(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	jpg, pngFile, webp := imageFixtures(t)
	exact := make([]byte, service.MaxImageSize)
	copy(exact, pngFile)
	tooLarge := append(append([]byte(nil), exact...), 0)
	r := newTestRouter(t, nil, nil)
	for _, tc := range []struct {
		name, field, filename, claimedType string
		data                               []byte
		status                             int
		code, contentType                  string
		unknownLength, duplicate           bool
	}{
		{name: "JPEG", field: "image", filename: "sample.jpg", data: jpg, status: 200, contentType: "image/jpeg"},
		{name: "PNG ignores extension and claimed MIME", field: "image", filename: "sample.txt", claimedType: "text/plain", data: pngFile, status: 200, contentType: "image/png"},
		{name: "WebP", field: "image", filename: "sample.webp", data: webp, status: 200, contentType: "image/webp"},
		{name: "missing", status: 400, code: "IMAGE_REQUIRED"},
		{name: "damaged JPEG", field: "image", filename: "broken.jpg", data: jpg[:50], status: 400, code: "IMAGE_DECODE_FAILED"},
		{name: "wrong field", field: "photo", filename: "sample.jpg", data: jpg, status: 400, code: "IMAGE_REQUIRED"},
		{name: "text disguised as JPEG", field: "image", filename: "fake.jpg", claimedType: "image/jpeg", data: []byte("plain text"), status: 415, code: "UNSUPPORTED_IMAGE_TYPE"},
		{name: "empty file", field: "image", filename: "empty.png", status: 415, code: "UNSUPPORTED_IMAGE_TYPE"},
		{name: "exact 10MiB", field: "image", filename: "boundary.png", data: exact, status: 200, contentType: "image/png"},
		{name: "10MiB plus one", field: "image", filename: "big.png", data: tooLarge, status: 413, code: "IMAGE_TOO_LARGE"},
		{name: "unknown length oversized", field: "image", filename: "big.png", data: tooLarge, status: 413, code: "IMAGE_TOO_LARGE", unknownLength: true},
		{name: "duplicate images", field: "image", filename: "sample.png", data: pngFile, status: 400, code: "INVALID_REQUEST", duplicate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := uploadRequest(t, tc.field, tc.filename, tc.claimedType, tc.data, tc.duplicate)
			if tc.unknownLength {
				req.ContentLength = -1
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.status == 200 {
				var response model.AnalyzeResponse
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.Image.ContentType != tc.contentType || response.Image.Size != int64(len(tc.data)) || response.Image.Filename != tc.filename {
					t.Fatalf("incorrect metadata: %+v", response.Image)
				}
				if response.Analysis.Scene.Category != "ocean" || response.Analysis.Scene.TimeOfDay != "sunset" || len(response.Analysis.Mood.Tags) != 4 {
					t.Fatalf("incorrect fake analysis: %+v", response.Analysis)
				}
				if bytes.Contains(w.Body.Bytes(), []byte(`"Data"`)) || bytes.Contains(w.Body.Bytes(), []byte(`"data"`)) {
					t.Fatal("raw bytes leaked")
				}
			} else {
				assertError(t, w, tc.code)
			}
		})
	}
	files, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatal("upload wrote temporary files")
	}
}

func assertError(t *testing.T, w *httptest.ResponseRecorder, code string) {
	t.Helper()
	var response model.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != code || response.Error.Message == "" {
		t.Fatalf("unexpected error: %s", w.Body.String())
	}
}

func TestAnalyzeMalformedRequest(t *testing.T) {
	r := newTestRouter(t, nil, nil)
	for _, tc := range []struct{ contentType, body, code string }{
		{"application/json", `{}`, "INVALID_REQUEST"},
		{"multipart/form-data; boundary=test", "--test\r\nmalformed header\r\n", "IMAGE_READ_ERROR"},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/analyze", bytes.NewBufferString(tc.body))
		req.Header.Set("Content-Type", tc.contentType)
		r.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("status=%d", w.Code)
		}
		assertError(t, w, tc.code)
	}
}

func TestAnalyzeTruncatedFile(t *testing.T) {
	jpg, _, _ := imageFixtures(t)
	req := uploadRequest(t, "image", "sample.jpg", "image/jpeg", jpg, false)
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	start := bytes.Index(body, jpg)
	if start < 0 {
		t.Fatal("fixture missing from request")
	}
	req.Body = io.NopCloser(bytes.NewReader(body[:start+len(jpg)/2]))
	req.ContentLength = -1
	w := httptest.NewRecorder()
	newTestRouter(t, nil, nil).ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	assertError(t, w, "IMAGE_READ_ERROR")
}

func TestAnalyzeRequestCap(t *testing.T) {
	_, pngFile, _ := imageFixtures(t)
	r := newTestRouter(t, nil, nil)
	for _, unknownLength := range []bool{false, true} {
		req := uploadRequest(t, "image", "sample.png", "image/png", pngFile, false)
		original := req.Body
		req.Body = io.NopCloser(io.MultiReader(original, io.LimitReader(zeroReader{}, handler.MaxAnalyzeRequestSize+1)))
		req.ContentLength += handler.MaxAnalyzeRequestSize + 1
		if unknownLength {
			req.ContentLength = -1
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 413 {
			t.Fatalf("unknownLength=%v status=%d body=%s", unknownLength, w.Code, w.Body.String())
		}
		assertError(t, w, "IMAGE_TOO_LARGE")
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// All handler tests inject an analyzer without any external requests.
type fakeAnalyzer struct {
	result *model.ImageAnalysis
	err    error
}

func (f fakeAnalyzer) AnalyzeImage(context.Context, []byte, string) (*model.ImageAnalysis, error) {
	return f.result, f.err
}

func newTestRouter(t *testing.T, result *model.ImageAnalysis, analyzerErr error) *gin.Engine {
	t.Helper()
	if result == nil && analyzerErr == nil {
		data, err := os.ReadFile("../model/testdata/analysis.json")
		if err != nil {
			t.Fatal(err)
		}
		result, err = model.DecodeImageAnalysis(data)
		if err != nil {
			t.Fatal(err)
		}
	}
	processor, err := imageproc.NewProcessor(imageproc.DefaultMaxDimension, imageproc.DefaultHardMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return router.New(config.Config{AppEnv: "production"}, service.NewImageService(fakeAnalyzer{result, analyzerErr}, processor, time.Second), nil)
}

func TestAnalyzeAIErrors(t *testing.T) {
	jpg, _, _ := imageFixtures(t)
	for _, tc := range []struct {
		name   string
		err    error
		result *model.ImageAnalysis
		status int
		code   string
	}{
		{name: "client error", err: errors.New("private provider details"), status: 502, code: "AI_SERVICE_ERROR"},
		{name: "timeout", err: context.DeadlineExceeded, status: 504, code: "AI_TIMEOUT"},
		{name: "invalid JSON", err: client.ErrInvalidAIResponse, status: 502, code: "INVALID_AI_RESPONSE"},
		{name: "rate limited", err: client.ErrAIRateLimited, status: 503, code: "AI_RATE_LIMITED"},
		{name: "unavailable", err: client.ErrAIUnavailable, status: 503, code: "AI_SERVICE_UNAVAILABLE"},
		{name: "configuration", err: client.ErrAIConfiguration, status: 500, code: "AI_CONFIGURATION_ERROR"},
		{name: "decode", err: imageproc.ErrDecodeFailed, status: 400, code: "IMAGE_DECODE_FAILED"},
		{name: "dimensions", err: imageproc.ErrDimensionsTooLarge, status: 400, code: "IMAGE_DIMENSIONS_TOO_LARGE"},
		{name: "processing", err: imageproc.ErrProcessingFailed, status: 500, code: "IMAGE_PROCESSING_FAILED"},
		{name: "processed size", err: imageproc.ErrTooLarge, status: 413, code: "IMAGE_TOO_LARGE"},
		{name: "invalid result", result: &model.ImageAnalysis{}, status: 502, code: "INVALID_AI_RESPONSE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			newTestRouter(t, tc.result, tc.err).ServeHTTP(w, uploadRequest(t, "image", "photo.jpg", "image/jpeg", jpg, false))
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			assertError(t, w, tc.code)
			if strings.Contains(w.Body.String(), "private provider details") {
				t.Fatal("internal details leaked")
			}
		})
	}
}
