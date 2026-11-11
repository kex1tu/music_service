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
		t.Errorf("expected original bytes on decode failure for small data")
	}
	if mime != "image/jpeg" {
		t.Errorf("expected fallback mime image/jpeg, got %s", mime)
	}
}

func TestOptimizeCoverForID3_RejectHugeCorruptInput(t *testing.T) {
	// Создаем срез размером 2 МБ (> MaxAPICSizeBytes), не являющийся валидным изображением
	hugeCorrupt := make([]byte, 2*1024*1024)
	copy(hugeCorrupt, "corrupt_stream_larger_than_max_apic_budget")
	res, mime := OptimizeCoverForID3(hugeCorrupt, 1000, 85)
	if res != nil || mime != "" {
		t.Errorf("expected nil and empty mime for huge corrupt input exceeding budget, got len=%d mime=%s", len(res), mime)
	}
}

func TestOptimizeCoverForID3_SmallJPEGPassThrough(t *testing.T) {
	// Создаем небольшой валидный JPEG (200x200) весом менее 300 КБ
	raw := generateTestImage(200, 200, false)
	if len(raw) > FastJPEGPassSizeBytes {
		t.Fatalf("test image unexpectedly large: %d", len(raw))
	}

	optimized, mime := OptimizeCoverForID3(raw, 1000, 85)
	if mime != "image/jpeg" {
		t.Errorf("expected mime image/jpeg, got %s", mime)
	}
	// Проверяем, что исходные байты возвращены без лишнего перекодирования
	if !bytes.Equal(optimized, raw) {
		t.Errorf("expected small valid JPEG to pass through without re-encoding")
	}
}

func createJPEGWithDimensions(width, height uint16) []byte {
	base := generateTestImage(10, 10, false)
	data := make([]byte, len(base))
	copy(data, base)
	for i := 0; i < len(data)-8; i++ {
		if data[i] == 0xFF && data[i+1] == 0xC0 {
			data[i+5] = byte(height >> 8)
			data[i+6] = byte(height & 0xFF)
			data[i+7] = byte(width >> 8)
			data[i+8] = byte(width & 0xFF)
			break
		}
	}
	return data
}

func TestOptimizeCoverForID3_DecompressionBombLimits(t *testing.T) {
	// 1. Создаем JPEG с гигантскими объявленными измерениями (50000x50000)
	hugeDimJPEG := createJPEGWithDimensions(50000, 50000)

	res, mime := OptimizeCoverForID3(hugeDimJPEG, 1000, 85)
	if res != nil || mime != "" {
		t.Errorf("expected nil for huge dimension JPEG bomb, got %v, %s", res, mime)
	}

	_, _, err := ValidateCoverImage(hugeDimJPEG)
	if err == nil {
		t.Errorf("expected ValidateCoverImage to reject 50000x50000 image, got nil")
	}

	// 2. Изображение с допустимыми измерениями по отдельности, но с превышением суммарных пикселей (7000x7000 = 49Mpx > 36Mpx)
	hugePixelsJPEG := createJPEGWithDimensions(7000, 7000)

	resPixels, mimePixels := OptimizeCoverForID3(hugePixelsJPEG, 1000, 85)
	if resPixels != nil || mimePixels != "" {
		t.Errorf("expected nil for huge pixel count JPEG, got %v, %s", resPixels, mimePixels)
	}

	_, _, pErr := ValidateCoverImage(hugePixelsJPEG)
	if pErr == nil {
		t.Errorf("expected ValidateCoverImage to reject 7000x7000 pixel bomb, got nil")
	}
}

func TestOptimizeCoverForID3_EnforcesMaxAPICSizeBytes(t *testing.T) {
	// Создаем тестовое изображение 1500x1500
	raw := generateTestImage(1500, 1500, false)
	optimized, mime := OptimizeCoverForID3(raw, 1000, 85)
	if mime != "image/jpeg" {
		t.Fatalf("expected mime image/jpeg, got %s", mime)
	}
	if len(optimized) == 0 {
		t.Fatalf("optimized image is empty")
	}
	if len(optimized) > MaxAPICSizeBytes {
		t.Errorf("optimized APIC size %d exceeds hard budget %d", len(optimized), MaxAPICSizeBytes)
	}

	// Проверяем, что результат валиден и декодируем
	cfg, _, err := image.DecodeConfig(bytes.NewReader(optimized))
	if err != nil {
		t.Fatalf("failed to decode result config: %v", err)
	}
	if cfg.Width > 1000 || cfg.Height > 1000 {
		t.Errorf("expected dimensions <= 1000, got %dx%d", cfg.Width, cfg.Height)
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
