package id3

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"testing"
)

func generateTestImage(width, height int, isPNG bool) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	// Fill with gradient pattern
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8((x * 255) / width),
				G: uint8((y * 255) / height),
				B: 128,
				A: 255,
			})
		}
	}

	var buf bytes.Buffer
	if isPNG {
		_ = png.Encode(&buf, img)
	} else {
		_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95})
	}
	return buf.Bytes()
}

func TestOptimizeCoverForID3_LargeImageDownscaling(t *testing.T) {
	// Generate a 2000x2000 JPEG
	raw := generateTestImage(2000, 2000, false)
	origLen := len(raw)

	optimized, mime := OptimizeCoverForID3(raw, 800, 80)
	if mime != "image/jpeg" {
		t.Fatalf("expected mime image/jpeg, got %s", mime)
	}
	if len(optimized) == 0 {
		t.Fatalf("optimized image is empty")
	}
	if len(optimized) >= origLen {
		t.Errorf("expected size reduction, orig=%d, opt=%d", origLen, len(optimized))
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(optimized))
	if err != nil {
		t.Fatalf("failed to decode optimized image config: %v", err)
	}
	if cfg.Width != 800 || cfg.Height != 800 {
		t.Errorf("expected dimensions 800x800, got %dx%d", cfg.Width, cfg.Height)
	}
}

func TestOptimizeCoverForID3_PNGConversion(t *testing.T) {
	// Generate a 1200x800 PNG
	raw := generateTestImage(1200, 800, true)

	optimized, mime := OptimizeCoverForID3(raw, 1000, 85)
	if mime != "image/jpeg" {
		t.Fatalf("expected conversion to image/jpeg, got %s", mime)
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(optimized))
	if err != nil {
		t.Fatalf("failed to decode converted image config: %v", err)
	}
	// Aspect ratio 1200:800 -> 3:2. Width should scale to 1000, height to ~666
	if cfg.Width != 1000 || cfg.Height < 660 || cfg.Height > 670 {
		t.Errorf("unexpected scaled dimensions for 3:2 image: %dx%d", cfg.Width, cfg.Height)
	}
}

func TestOptimizeCoverForID3_FallbackOnCorrupt(t *testing.T) {
	corruptData := []byte("not an image at all")
	res, mime := OptimizeCoverForID3(corruptData, 1000, 85)
	if !bytes.Equal(res, corruptData) {
		t.Errorf("expected original bytes on decode failure")
	}
	if mime != "image/jpeg" {
		t.Errorf("expected fallback mime image/jpeg, got %s", mime)
	}
}

func TestOptimizeCoverForID3_EmptyInput(t *testing.T) {
	res, mime := OptimizeCoverForID3(nil, 1000, 85)
	if res != nil || mime != "" {
		t.Errorf("expected nil result for empty input")
	}
}

func TestResizeBilinear_SmallCases(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 10, 10))
	draw.Draw(src, src.Bounds(), &image.Uniform{C: color.RGBA{R: 255, G: 0, B: 0, A: 255}}, image.Point{}, draw.Src)

	dst := resizeBilinear(src, 1, 1)
	if dst.Bounds().Dx() != 1 || dst.Bounds().Dy() != 1 {
		t.Errorf("expected 1x1, got %dx%d", dst.Bounds().Dx(), dst.Bounds().Dy())
	}
}
