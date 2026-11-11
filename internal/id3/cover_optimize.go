package id3

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png" // регистрация PNG декодера
)

const (
	// DefaultCoverMaxDimension максимальное измерение (ширина или высота) APIC обложки по умолчанию.
	DefaultCoverMaxDimension = 1000

	// DefaultCoverQuality качество сжатия JPEG для APIC по умолчанию.
	DefaultCoverQuality = 85

	// MaxAPICSizeBytes определяет жесткий бюджет размера вшиваемого фрейма APIC (1 MiB).
	MaxAPICSizeBytes = 1024 * 1024

	// MaxCoverDimension определяет максимальное допустимое геометрическое измерение
	// (ширину или высоту) обложки до декодирования (8192 px). Защита от decompression bomb.
	MaxCoverDimension = 8192

	// MaxCoverPixels определяет предельное число пикселей изображения (36 мегапикселей, например 6000x6000px).
	MaxCoverPixels = 36_000_000

	// FastJPEGPassSizeBytes определяет размер (300 КиБ), до которого валидный JPEG с допустимыми
	// размерами не перекодируется повторно, сохраняя исходное качество.
	FastJPEGPassSizeBytes = 300 * 1024

	// MaxCoverDownloadBytes задает лимит сетевого ответа при загрузке обложки по HTTP (15 MiB).
	MaxCoverDownloadBytes = 15 * 1024 * 1024
)

var (
	// ErrOversizedDimension возвращается при превышении MaxCoverDimension по ширине или высоте.
	ErrOversizedDimension = errors.New("image dimension exceeds maximum limit")

	// ErrOversizedPixels возвращается при превышении MaxCoverPixels или переполнении площади.
	ErrOversizedPixels = errors.New("image pixel count exceeds maximum limit")
)

// ValidateCoverImage проверяет заголовок изображения (JPEG/PNG) без полного декодирования растра,
// контролирует допустимые геометрические размеры и суммарное число пикселей с защитой от переполнения.
func ValidateCoverImage(rawCover []byte) (*image.Config, string, error) {
	if len(rawCover) == 0 {
		return nil, "", errors.New("empty cover data")
	}

	cfg, format, err := image.DecodeConfig(bytes.NewReader(rawCover))
	if err != nil {
		return nil, "", fmt.Errorf("failed to decode image config: %w", err)
	}

	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, "", errors.New("invalid image dimensions: width and height must be positive")
	}

	if cfg.Width > MaxCoverDimension || cfg.Height > MaxCoverDimension {
		return nil, "", fmt.Errorf("%w: %dx%d exceeds %d", ErrOversizedDimension, cfg.Width, cfg.Height, MaxCoverDimension)
	}

	// Защита от переполнения целочисленного типа и проверка бюджета пикселей
	pixels := int64(cfg.Width) * int64(cfg.Height)
	if pixels <= 0 || pixels > MaxCoverPixels {
		return nil, "", fmt.Errorf("%w: %d exceeds %d", ErrOversizedPixels, pixels, MaxCoverPixels)
	}

	return &cfg, format, nil
}

// OptimizeCoverForID3 оптимизирует изображение обложки в памяти перед вшиванием во фрейм APIC ID3v2.4.
// Уменьшает большие изображения до maxDimension (по умолчанию 1000px) и сжимает в JPEG с качеством quality (по умолчанию 85).
// Если изображение уже меньше maxDimension и весит до 300 КБ, возвращает исходные байты без лишнего рекодинга.
// Гарантирует соблюдение жесткого лимита MaxAPICSizeBytes (1 MiB).
// При поврежденных данных, неподдерживаемом формате или ошибках декодирования возврат исходного среза
// допускается только для компактных данных в пределах жесткого бюджета MaxAPICSizeBytes;
// чрезмерно большие срезы (> MaxAPICSizeBytes) и декомпрессионные бомбы отсекаются с возвратом (nil, "").
func OptimizeCoverForID3(rawCover []byte, maxDimension int, quality int) ([]byte, string) {
	if len(rawCover) == 0 {
		return nil, ""
	}
	if maxDimension <= 0 {
		maxDimension = DefaultCoverMaxDimension
	}
	if quality <= 0 || quality > 100 {
		quality = DefaultCoverQuality
	}

	cfg, format, err := ValidateCoverImage(rawCover)
	if err != nil {
		// Если данные превышают жесткий бюджет фрейма APIC или обнаружена декомпрессионная бомба,
		// возврат среза строго запрещен (возвращаем nil, "")
		if len(rawCover) > MaxAPICSizeBytes || errors.Is(err, ErrOversizedDimension) || errors.Is(err, ErrOversizedPixels) {
			return nil, ""
		}
		// Для компактных поврежденных/неподдерживаемых срезов (в пределах бюджета)
		mime := "image/jpeg"
		if bytes.HasPrefix(rawCover, []byte("\x89PNG")) {
			mime = "image/png"
		}
		return rawCover, mime
	}

	// Быстрый путь для небольших JPEG (до 300 КБ и в пределах MaxAPICSizeBytes),
	// если размеры уже не превышают maxDimension. Сохраняем исходное качество без рекодинга.
	if format == "jpeg" && len(rawCover) <= FastJPEGPassSizeBytes && len(rawCover) <= MaxAPICSizeBytes {
		if cfg.Width <= maxDimension && cfg.Height <= maxDimension {
			return rawCover, "image/jpeg"
		}
	}

	src, _, err := image.Decode(bytes.NewReader(rawCover))
	if err != nil {
		if len(rawCover) > MaxAPICSizeBytes {
			return nil, ""
		}
		mime := "image/jpeg"
		if bytes.HasPrefix(rawCover, []byte("\x89PNG")) {
			mime = "image/png"
		}
		return rawCover, mime
	}

	bounds := src.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	if w <= 0 || h <= 0 {
		if len(rawCover) > MaxAPICSizeBytes {
			return nil, ""
		}
		return rawCover, "image/jpeg"
	}

	targetW := w
	targetH := h

	if w > maxDimension || h > maxDimension {
		if w >= h {
			targetW = maxDimension
			targetH = int(float64(h) * float64(maxDimension) / float64(w))
		} else {
			targetH = maxDimension
			targetW = int(float64(w) * float64(maxDimension) / float64(h))
		}
		if targetW < 1 {
			targetW = 1
		}
		if targetH < 1 {
			targetH = 1
		}
	}

	var dst *image.RGBA
	if targetW != w || targetH != h {
		dst = resizeBilinear(src, targetW, targetH)
	} else {
		dst = image.NewRGBA(image.Rect(0, 0, targetW, targetH))
		draw.Draw(dst, dst.Bounds(), src, bounds.Min, draw.Src)
	}

	// Кодирование с соблюдением жесткого бюджета MaxAPICSizeBytes
	currentQuality := quality
	currentDst := dst

	for attempts := 0; attempts < 5; attempts++ {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, currentDst, &jpeg.Options{Quality: currentQuality}); err != nil {
			if len(rawCover) <= MaxAPICSizeBytes {
				return rawCover, "image/jpeg"
			}
			return nil, ""
		}

		encoded := buf.Bytes()
		if len(encoded) <= MaxAPICSizeBytes {
			return encoded, "image/jpeg"
		}

		// Если размер превышает MaxAPICSizeBytes, пробуем понизить качество, затем уменьшить размеры
		if currentQuality > 50 {
			currentQuality -= 15
		} else {
			curBounds := currentDst.Bounds()
			newW := curBounds.Dx() * 3 / 4
			newH := curBounds.Dy() * 3 / 4
			if newW < 100 || newH < 100 {
				break
			}
			currentDst = resizeBilinear(currentDst, newW, newH)
			currentQuality = 70
		}
	}

	return nil, ""
}

// resizeBilinear выполняет гладкое билинейное масштабирование изображения в чистом Go без cgo и сторонних библиотек.
func resizeBilinear(src image.Image, targetW, targetH int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	bounds := src.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()

	xRatio := float64(srcW-1) / float64(targetW)
	yRatio := float64(srcH-1) / float64(targetH)
	if targetW <= 1 {
		xRatio = 0
	}
	if targetH <= 1 {
		yRatio = 0
	}

	for y := 0; y < targetH; y++ {
		srcY := float64(y) * yRatio
		yLow := int(srcY)
		yHigh := yLow + 1
		if yHigh >= srcH {
			yHigh = srcH - 1
		}
		yWeight := srcY - float64(yLow)

		for x := 0; x < targetW; x++ {
			srcX := float64(x) * xRatio
			xLow := int(srcX)
			xHigh := xLow + 1
			if xHigh >= srcW {
				xHigh = srcW - 1
			}
			xWeight := srcX - float64(xLow)

			r1, g1, b1, a1 := src.At(bounds.Min.X+xLow, bounds.Min.Y+yLow).RGBA()
			r2, g2, b2, a2 := src.At(bounds.Min.X+xHigh, bounds.Min.Y+yLow).RGBA()
			r3, g3, b3, a3 := src.At(bounds.Min.X+xLow, bounds.Min.Y+yHigh).RGBA()
			r4, g4, b4, a4 := src.At(bounds.Min.X+xHigh, bounds.Min.Y+yHigh).RGBA()

			topR := float64(r1)*(1.0-xWeight) + float64(r2)*xWeight
			topG := float64(g1)*(1.0-xWeight) + float64(g2)*xWeight
			topB := float64(b1)*(1.0-xWeight) + float64(b2)*xWeight
			topA := float64(a1)*(1.0-xWeight) + float64(a2)*xWeight

			botR := float64(r3)*(1.0-xWeight) + float64(r4)*xWeight
			botG := float64(g3)*(1.0-xWeight) + float64(g4)*xWeight
			botB := float64(b3)*(1.0-xWeight) + float64(b4)*xWeight
			botA := float64(a3)*(1.0-xWeight) + float64(a4)*xWeight

			r := uint8((topR*(1.0-yWeight) + botR*yWeight) / 257)
			g := uint8((topG*(1.0-yWeight) + botG*yWeight) / 257)
			b := uint8((topB*(1.0-yWeight) + botB*yWeight) / 257)
			a := uint8((topA*(1.0-yWeight) + botA*yWeight) / 257)

			offset := dst.PixOffset(x, y)
			dst.Pix[offset] = r
			dst.Pix[offset+1] = g
			dst.Pix[offset+2] = b
			dst.Pix[offset+3] = a
		}
	}
	return dst
}
