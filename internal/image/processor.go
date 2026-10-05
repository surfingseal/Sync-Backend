// Package image prepares metadata-free images for atmosphere analysis.
package image

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	stdimage "image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"
)

const (
	DefaultMaxDimension = 1920
	DefaultHardMaxBytes = 6 * 1024 * 1024
	targetMaxBytes      = 2621440
	maxSourceDimension  = 10000
	maxSourcePixels     = 40000000
	maxInputBytes       = 10 * 1024 * 1024
)

var (
	ErrDecodeFailed       = errors.New("image decode failed")
	ErrDimensionsTooLarge = errors.New("image dimensions too large")
	ErrProcessingFailed   = errors.New("image processing failed")
	ErrTooLarge           = errors.New("processed image too large")
)

type ProcessedImage struct {
	Data                         []byte
	MIMEType                     string
	Width, Height                int
	OriginalSize, ProcessedSize  int
	Resized, Reencoded           bool
	DecodeMS, ResizeMS, EncodeMS float64
}

type ImageProcessor interface {
	Process(context.Context, []byte, string) (*ProcessedImage, error)
}

type Processor struct {
	maxDimension, hardMaxBytes int
	// Serialize raster allocation per shared processor. Waiting is cancellable.
	gate chan struct{}
}

func NewProcessor(maxDimension, hardMaxBytes int) (*Processor, error) {
	if maxDimension < 1 || maxDimension > maxSourceDimension || hardMaxBytes < 1 || hardMaxBytes > DefaultHardMaxBytes {
		return nil, fmt.Errorf("invalid image processor limits")
	}
	return &Processor{maxDimension: maxDimension, hardMaxBytes: hardMaxBytes, gate: make(chan struct{}, 1)}, nil
}

func (p *Processor) Process(ctx context.Context, input []byte, mimeType string) (*ProcessedImage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch mimeType {
	case "image/jpeg", "image/png", "image/webp":
	default:
		return nil, ErrDecodeFailed
	}
	if len(input) > maxInputBytes {
		return nil, ErrTooLarge
	}
	if http.DetectContentType(input) != mimeType {
		return nil, ErrDecodeFailed
	}
	cfg, format, err := stdimage.DecodeConfig(contextReader{ctx, bytes.NewReader(input)})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrDecodeFailed
	}
	if "image/"+format != mimeType {
		return nil, ErrDecodeFailed
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxSourceDimension || cfg.Height > maxSourceDimension || cfg.Width > maxSourcePixels/cfg.Height {
		return nil, ErrDimensionsTooLarge
	}
	select {
	case p.gate <- struct{}{}:
		defer func() { <-p.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	start := time.Now()
	// imaging provides tested EXIF orientation 1-8 handling. Other formats don't
	// need JPEG EXIF scanning. No EXIF contents are logged or copied to output.
	var decoded stdimage.Image
	decoded, err = imaging.Decode(contextReader{ctx, bytes.NewReader(input)}, imaging.AutoOrientation(mimeType == "image/jpeg"))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, ErrDecodeFailed
	}
	decodeMS := float64(time.Since(start)) / float64(time.Millisecond)
	resizeMS, encodeMS := 0.0, 0.0
	original := decoded.Bounds().Size()
	outputMIME := "image/jpeg"
	if mimeType == "image/png" {
		outputMIME = "image/png"
	}
	// Bound fallback work: requested maximum, then 1600px and 1280px.
	limits := []int{p.maxDimension}
	for _, limit := range []int{1600, 1280} {
		if limit < limits[len(limits)-1] {
			limits = append(limits, limit)
		}
	}
	for _, limit := range limits {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		w, h := fit(original.X, original.Y, limit)
		raster := decoded
		if w != original.X || h != original.Y {
			// Reuse the EXIF library's separable Lanczos resize with smaller
			// intermediate buffers than x/image/draw's float64 kernel scaler.
			resizeStart := time.Now()
			raster = imaging.Resize(decoded, w, h, imaging.Lanczos)
			resizeMS += float64(time.Since(resizeStart)) / float64(time.Millisecond)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if outputMIME == "image/jpeg" {
			// Opaque NRGBA and RGBA have identical pixel layout. A zero-copy view
			// lets the standard JPEG encoder avoid per-pixel interface allocations.
			if nrgba, ok := raster.(*stdimage.NRGBA); ok && nrgba.Opaque() {
				raster = &stdimage.RGBA{Pix: nrgba.Pix, Stride: nrgba.Stride, Rect: nrgba.Rect}
			}
			if opaque, ok := raster.(interface{ Opaque() bool }); !ok || !opaque.Opaque() {
				dst := stdimage.NewRGBA(stdimage.Rect(0, 0, w, h))
				draw.Draw(dst, dst.Bounds(), stdimage.NewUniform(color.White), stdimage.Point{}, draw.Src)
				draw.Draw(dst, dst.Bounds(), raster, raster.Bounds().Min, draw.Over)
				raster = dst
			}
		}
		qualities := []int{85}
		if outputMIME == "image/jpeg" {
			qualities = []int{85, 80, 75}
		}
		for _, quality := range qualities {
			encodeStart := time.Now()
			buffer := &limitedBuffer{ctx: ctx, limit: p.hardMaxBytes}
			if outputMIME == "image/png" {
				err = png.Encode(buffer, raster)
			} else {
				err = jpeg.Encode(buffer, raster, &jpeg.Options{Quality: quality})
			}
			encodeMS += float64(time.Since(encodeStart)) / float64(time.Millisecond)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err != nil && !errors.Is(err, ErrTooLarge) {
				return nil, ErrProcessingFailed
			}
			if err == nil {
				if outputMIME == "image/jpeg" && buffer.Len() > targetMaxBytes && quality > 75 {
					continue
				}
				result := &ProcessedImage{Data: buffer.Bytes(), MIMEType: outputMIME, Width: w, Height: h, OriginalSize: len(input), ProcessedSize: buffer.Len(), Resized: w != original.X || h != original.Y, Reencoded: true, DecodeMS: decodeMS, ResizeMS: resizeMS, EncodeMS: encodeMS}
				log.Printf("image processing input_mime=%s output_mime=%s original_width=%d original_height=%d output_width=%d output_height=%d original_bytes=%d processed_bytes=%d resized=%t reencoded=true image_decode_ms=%.2f image_resize_ms=%.2f image_encode_ms=%.2f duration=%s", mimeType, outputMIME, cfg.Width, cfg.Height, w, h, len(input), result.ProcessedSize, result.Resized, decodeMS, resizeMS, encodeMS, time.Since(start))
				return result, nil
			}
		}
	}
	return nil, ErrTooLarge
}

func fit(w, h, limit int) (int, int) {
	if w <= limit && h <= limit {
		return w, h
	}
	if w >= h {
		return limit, max(1, h*limit/w)
	}
	return max(1, w*limit/h), limit
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(b)
}

type limitedBuffer struct {
	buffer bytes.Buffer
	ctx    context.Context
	limit  int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if len(data) > b.limit-b.Len() {
		return 0, ErrTooLarge
	}
	return b.buffer.Write(data)
}

func (b *limitedBuffer) Len() int      { return b.buffer.Len() }
func (b *limitedBuffer) Bytes() []byte { return b.buffer.Bytes() }
