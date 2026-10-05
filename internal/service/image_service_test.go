package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"example.com/sync/internal/client"
	imageproc "example.com/sync/internal/image"
	"example.com/sync/internal/model"
)

func TestReadImageCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ReadImage(ctx, "test.jpg", strings.NewReader("ignored"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if _, err := NewImageService(nil, passthroughProcessor{}, time.Second).Analyze(ctx, model.UploadedImage{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

type analyzerFunc func(context.Context, []byte, string) (*model.ImageAnalysis, error)

func (f analyzerFunc) AnalyzeImage(ctx context.Context, image []byte, mime string) (*model.ImageAnalysis, error) {
	return f(ctx, image, mime)
}

func validAnalysis(t *testing.T) *model.ImageAnalysis {
	t.Helper()
	data, err := os.ReadFile("../model/testdata/analysis.json")
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := model.DecodeImageAnalysis(data)
	if err != nil {
		t.Fatal(err)
	}
	return analysis
}

func TestAnalyzeService(t *testing.T) {
	input := model.UploadedImage{ImageInfo: model.ImageInfo{ContentType: "image/png"}, Data: []byte("bytes")}
	for _, tc := range []struct {
		name         string
		result       *model.ImageAnalysis
		err, wantErr error
	}{
		{"success", validAnalysis(t), nil, nil},
		{"client failure", nil, client.ErrAIService, client.ErrAIService},
		{"invalid JSON", nil, client.ErrInvalidAIResponse, client.ErrInvalidAIResponse},
		{"nil result", nil, nil, client.ErrInvalidAIResponse},
		{"invalid values", &model.ImageAnalysis{}, nil, client.ErrInvalidAIResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := analyzerFunc(func(ctx context.Context, image []byte, mime string) (*model.ImageAnalysis, error) {
				if !bytes.Equal(image, input.Data) || mime != "image/png" {
					t.Fatal("image not forwarded")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("missing timeout")
				}
				return tc.result, tc.err
			})
			result, err := NewImageService(fake, passthroughProcessor{}, time.Second).Analyze(context.Background(), input)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error=%v want=%v", err, tc.wantErr)
			}
			if tc.wantErr == nil && result != tc.result {
				t.Fatal("result not forwarded")
			}
		})
	}
}

func TestAnalyzeServiceTimeout(t *testing.T) {
	fake := analyzerFunc(func(ctx context.Context, _ []byte, _ string) (*model.ImageAnalysis, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	_, err := NewImageService(fake, passthroughProcessor{}, time.Millisecond).Analyze(context.Background(), model.UploadedImage{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
}

func TestAnalyzePropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := analyzerFunc(func(callCtx context.Context, _ []byte, _ string) (*model.ImageAnalysis, error) {
		cancel()
		<-callCtx.Done()
		return nil, callCtx.Err()
	})
	_, err := NewImageService(fake, passthroughProcessor{}, time.Second).Analyze(ctx, model.UploadedImage{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestReadImageFailure(t *testing.T) {
	_, err := ReadImage(context.Background(), "test.jpg", failingReader{})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error=%v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestReadImageLimit(t *testing.T) {
	r := &countingReader{}
	_, err := ReadImage(context.Background(), "large.png", r)
	if !errors.Is(err, ErrImageTooLarge) || r.read != MaxImageSize+1 {
		t.Fatalf("err=%v bytes read=%d", err, r.read)
	}
}

type countingReader struct{ read int64 }

func (r *countingReader) Read(p []byte) (int, error) {
	clear(p)
	r.read += int64(len(p))
	return len(p), nil
}

func TestReadImageSanitizesFilename(t *testing.T) {
	// JPEG magic bytes are sufficient for this MIME sniffing test.
	image, err := ReadImage(context.Background(), `..\..\photo.jpg`, bytes.NewReader([]byte{0xff, 0xd8, 0xff, 0xe0}))
	if err != nil {
		t.Fatal(err)
	}
	if image.Filename != "photo.jpg" || image.ContentType != "image/jpeg" || image.Size != 4 || len(image.Data) != 4 {
		t.Fatalf("unexpected image: %+v", image)
	}
}

// Analyzer-error tests isolate service behavior; processor forwarding is tested separately.
type passthroughProcessor struct{}

func (passthroughProcessor) Process(ctx context.Context, data []byte, mime string) (*imageproc.ProcessedImage, error) {
	if len(data) == 0 {
		data = []byte("fake processed image")
	}
	return &imageproc.ProcessedImage{Data: data, MIMEType: mime}, nil
}

type processorFunc func(context.Context, []byte, string) (*imageproc.ProcessedImage, error)

func (f processorFunc) Process(ctx context.Context, b []byte, mime string) (*imageproc.ProcessedImage, error) {
	return f(ctx, b, mime)
}

func TestAnalyzeUsesProcessedImage(t *testing.T) {
	original := []byte("original-private-image")
	prepared := []byte("processed-metadata-free-image")
	processor := processorFunc(func(ctx context.Context, data []byte, mime string) (*imageproc.ProcessedImage, error) {
		if !bytes.Equal(data, original) || mime != "image/webp" {
			t.Fatal("wrong processor input")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing deadline")
		}
		return &imageproc.ProcessedImage{Data: prepared, MIMEType: "image/jpeg"}, nil
	})
	analyzer := analyzerFunc(func(ctx context.Context, data []byte, mime string) (*model.ImageAnalysis, error) {
		if !bytes.Equal(data, prepared) || mime != "image/jpeg" {
			t.Fatal("original bytes or MIME sent to AI")
		}
		return validAnalysis(t), nil
	})
	_, err := NewImageService(analyzer, processor, time.Second).Analyze(context.Background(), model.UploadedImage{ImageInfo: model.ImageInfo{ContentType: "image/webp"}, Data: original})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProcessingFailureSkipsAnalyzer(t *testing.T) {
	for _, want := range []error{imageproc.ErrDecodeFailed, imageproc.ErrDimensionsTooLarge, imageproc.ErrProcessingFailed, imageproc.ErrTooLarge, context.Canceled} {
		processor := processorFunc(func(context.Context, []byte, string) (*imageproc.ProcessedImage, error) { return nil, want })
		analyzer := analyzerFunc(func(context.Context, []byte, string) (*model.ImageAnalysis, error) {
			t.Fatal("analyzer called after processor failure")
			return nil, nil
		})
		_, err := NewImageService(analyzer, processor, time.Second).Analyze(context.Background(), model.UploadedImage{})
		if !errors.Is(err, want) {
			t.Fatalf("error=%v", err)
		}
	}
}

func TestProcessingTimeout(t *testing.T) {
	processor := processorFunc(func(ctx context.Context, _ []byte, _ string) (*imageproc.ProcessedImage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	analyzer := analyzerFunc(func(context.Context, []byte, string) (*model.ImageAnalysis, error) {
		t.Fatal("AI called after processing timeout")
		return nil, nil
	})
	_, err := NewImageService(analyzer, processor, time.Millisecond).Analyze(context.Background(), model.UploadedImage{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func TestMissingProcessedResult(t *testing.T) {
	processor := processorFunc(func(context.Context, []byte, string) (*imageproc.ProcessedImage, error) { return nil, nil })
	_, err := NewImageService(nil, processor, time.Second).Analyze(context.Background(), model.UploadedImage{})
	if !errors.Is(err, imageproc.ErrProcessingFailed) {
		t.Fatal(err)
	}
}
