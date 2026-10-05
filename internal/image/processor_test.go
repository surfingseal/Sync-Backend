package image

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	stdimage "image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"net/http"
	"testing"
	"time"
)

func processor(t testing.TB, dimension, hard int) *Processor {
	t.Helper()
	p, err := NewProcessor(dimension, hard)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func encoded(t testing.TB, w, h int, format string) []byte {
	t.Helper()
	im := stdimage.NewRGBA(stdimage.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), uint8((x + y) / 2), 255})
		}
	}
	var b bytes.Buffer
	var err error
	if format == "png" {
		err = png.Encode(&b, im)
	} else {
		err = jpeg.Encode(&b, im, &jpeg.Options{Quality: 95})
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func assertProcessed(t *testing.T, p *ProcessedImage, w, h int, mime string) {
	t.Helper()
	if p.Width != w || p.Height != h || p.MIMEType != mime || p.ProcessedSize != len(p.Data) || len(p.Data) > DefaultHardMaxBytes || !p.Reencoded {
		t.Fatalf("unexpected result dimensions=%dx%d MIME=%s bytes=%d", p.Width, p.Height, p.MIMEType, len(p.Data))
	}
	cfg, _, err := stdimage.DecodeConfig(bytes.NewReader(p.Data))
	if err != nil || cfg.Width != w || cfg.Height != h || http.DetectContentType(p.Data) != mime {
		t.Fatalf("invalid encoded result: %v", err)
	}
}
func TestProcessDimensions(t *testing.T) {
	for _, tc := range []struct {
		name             string
		w, h, outW, outH int
	}{{"small", 160, 120, 160, 120}, {"large", 4032, 3024, 1920, 1440}, {"portrait", 2400, 4000, 1152, 1920}, {"already portrait", 1080, 1920, 1080, 1920}, {"12MP", 4000, 3000, 1920, 1440}} {
		t.Run(tc.name, func(t *testing.T) {
			data := encoded(t, tc.w, tc.h, "jpeg")
			p, err := processor(t, 1920, DefaultHardMaxBytes).Process(context.Background(), data, "image/jpeg")
			if err != nil {
				t.Fatal(err)
			}
			assertProcessed(t, p, tc.outW, tc.outH, "image/jpeg")
			if p.Resized != (tc.w != tc.outW || tc.h != tc.outH) || p.OriginalSize != len(data) {
				t.Fatal("incorrect flags")
			}
		})
	}
}
func withOrientation(data []byte, orient uint16) []byte {
	payload := []byte{'E', 'x', 'i', 'f', 0, 0, 'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 1, 3, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	binary.LittleEndian.PutUint16(payload[24:26], orient)
	payload = append(payload, []byte("private-GPS-camera-timestamp")...)
	result := append([]byte{}, data[:2]...)
	result = append(result, 0xff, 0xe1, byte((len(payload)+2)>>8), byte(len(payload)+2))
	result = append(result, payload...)
	return append(result, data[2:]...)
}
func TestEXIFOrientationsAndMetadata(t *testing.T) {
	im := stdimage.NewRGBA(stdimage.Rect(0, 0, 80, 60))
	colors := []color.RGBA{{220, 20, 20, 255}, {20, 220, 20, 255}, {20, 20, 220, 255}, {220, 220, 20, 255}}
	for y := 0; y < 60; y++ {
		for x := 0; x < 80; x++ {
			index := 0
			if x >= 40 {
				index++
			}
			if y >= 30 {
				index += 2
			}
			im.SetRGBA(x, y, colors[index])
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, im, &jpeg.Options{Quality: 100})
	// Expected top-left, top-right, bottom-left, bottom-right after EXIF transform.
	expected := [][]int{{0, 1, 2, 3}, {1, 0, 3, 2}, {3, 2, 1, 0}, {2, 3, 0, 1}, {0, 2, 1, 3}, {2, 0, 3, 1}, {3, 1, 2, 0}, {1, 3, 0, 2}}
	for orientation := 1; orientation <= 8; orientation++ {
		data := withOrientation(buf.Bytes(), uint16(orientation))
		p, err := processor(t, 1920, DefaultHardMaxBytes).Process(context.Background(), data, "image/jpeg")
		if err != nil {
			t.Fatal(err)
		}
		w, h := 80, 60
		if orientation >= 5 {
			w, h = h, w
		}
		assertProcessed(t, p, w, h, "image/jpeg")
		out, err := jpeg.Decode(bytes.NewReader(p.Data))
		if err != nil {
			t.Fatal(err)
		}
		positions := [][2]int{{10, 10}, {w - 11, 10}, {10, h - 11}, {w - 11, h - 11}}
		for corner, pos := range positions {
			c := color.RGBAModel.Convert(out.At(pos[0], pos[1])).(color.RGBA)
			best, distance := 0, int64(1<<60)
			for i, want := range colors {
				r, g, b := int64(c.R)-int64(want.R), int64(c.G)-int64(want.G), int64(c.B)-int64(want.B)
				d := r*r + g*g + b*b
				if d < distance {
					best, distance = i, d
				}
			}
			if best != expected[orientation-1][corner] {
				t.Fatalf("orientation %d corner %d: got %d", orientation, corner, best)
			}
		}
		if bytes.Contains(p.Data, []byte("Exif")) || bytes.Contains(p.Data, []byte("private-GPS")) {
			t.Fatal("EXIF leaked")
		}
	}
}
func TestPNGTransparencyAndMetadata(t *testing.T) {
	im := stdimage.NewNRGBA(stdimage.Rect(0, 0, 16, 12))
	im.SetNRGBA(4, 4, color.NRGBA{200, 30, 50, 90})
	var b bytes.Buffer
	png.Encode(&b, im)
	chunk := append([]byte("tEXt"), []byte("Comment\x00private-camera-info")...)
	var encodedChunk bytes.Buffer
	binary.Write(&encodedChunk, binary.BigEndian, uint32(len(chunk)-4))
	encodedChunk.Write(chunk)
	binary.Write(&encodedChunk, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	data := append(append(append([]byte{}, b.Bytes()[:33]...), encodedChunk.Bytes()...), b.Bytes()[33:]...)
	p, err := processor(t, 1920, DefaultHardMaxBytes).Process(context.Background(), data, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	assertProcessed(t, p, 16, 12, "image/png")
	out, err := png.Decode(bytes.NewReader(p.Data))
	if err != nil {
		t.Fatal(err)
	}
	if color.NRGBAModel.Convert(out.At(4, 4)).(color.NRGBA) != im.NRGBAAt(4, 4) {
		t.Fatal("transparency changed")
	}
	if bytes.Contains(p.Data, []byte("private-camera")) {
		t.Fatal("PNG metadata leaked")
	}
}
func TestWebP(t *testing.T) {
	data, _ := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	p, err := processor(t, 1920, DefaultHardMaxBytes).Process(context.Background(), data, "image/webp")
	if err != nil {
		t.Fatal(err)
	}
	assertProcessed(t, p, 1, 1, "image/jpeg")
}
func pngHeader(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	binary.Write(&b, binary.BigEndian, uint32(13))
	chunk := make([]byte, 17)
	copy(chunk, "IHDR")
	binary.BigEndian.PutUint32(chunk[4:8], w)
	binary.BigEndian.PutUint32(chunk[8:12], h)
	chunk[12] = 8
	chunk[13] = 2
	b.Write(chunk)
	binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}
func TestRejectedImages(t *testing.T) {
	p := processor(t, 1920, DefaultHardMaxBytes)
	for _, tc := range []struct {
		data []byte
		mime string
		err  error
	}{{[]byte{255, 216, 255, 224}, "image/jpeg", ErrDecodeFailed}, {encoded(t, 5, 5, "png"), "image/jpeg", ErrDecodeFailed}, {[]byte("text"), "text/plain", ErrDecodeFailed}, {pngHeader(10001, 1), "image/png", ErrDimensionsTooLarge}, {pngHeader(10000, 5000), "image/png", ErrDimensionsTooLarge}} {
		if _, err := p.Process(context.Background(), tc.data, tc.mime); !errors.Is(err, tc.err) {
			t.Fatalf("err=%v want=%v", err, tc.err)
		}
	}
}
func TestCancellation(t *testing.T) {
	p := processor(t, 1920, DefaultHardMaxBytes)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Process(ctx, nil, "image/jpeg"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	p.gate <- struct{}{}
	data := encoded(t, 5, 5, "jpeg")
	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := p.Process(ctx, data, "image/jpeg"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	<-p.gate
	b := &limitedBuffer{ctx: ctx, limit: 100}
	if _, err := b.Write([]byte("a")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func TestHardLimitFallback(t *testing.T) {
	im := stdimage.NewRGBA(stdimage.Rect(0, 0, 1700, 1200))
	r := rand.New(rand.NewSource(1))
	r.Read(im.Pix)
	for i := 3; i < len(im.Pix); i += 4 {
		im.Pix[i] = 255
	}
	var b bytes.Buffer
	png.Encode(&b, im)
	p, err := processor(t, 1920, 4*1024*1024).Process(context.Background(), b.Bytes(), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Resized || p.Width >= 1700 || len(p.Data) > 4*1024*1024 {
		t.Fatal("hard-limit resize not applied")
	}
	if _, err := processor(t, 1920, 1).Process(context.Background(), b.Bytes(), "image/png"); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
}
func BenchmarkImageProcessor4000x3000JPEG(b *testing.B) {
	input := encoded(b, 4000, 3000, "jpeg")
	p := processor(b, 1920, DefaultHardMaxBytes)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := p.Process(context.Background(), input, "image/jpeg"); err != nil {
			b.Fatal(err)
		}
	}
}
