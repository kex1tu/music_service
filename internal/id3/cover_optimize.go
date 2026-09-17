package id3

import (
	"bytes"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png" // регистрация PNG декодера
)

// OptimizeCoverForID3 оптимизирует изображение обложки в памяти перед вшиванием во фрейм APIC ID3v2.4.
// Уменьшает большие изображения до maxDimension (по умолчанию 1000px) и сжимает в JPEG с качеством quality (по умолчанию 85).
// Если изображение уже меньше maxDimension и весит до 300 КБ, возвращает исходные байты без лишнего рекодинга.
// При ошибках декодирования возвращает исходный срез байтов (fallback).
func OptimizeCoverForID3(rawCover []byte, maxDimension int, quality int) ([]byte, string) {
	if len(rawCover) == 0 {
		return nil, ""
	}
	if maxDimension <= 0 {
		maxDimension = 1000
	}
	if quality <= 0 || quality > 100 {
		quality = 85
	}

	// Если уже небольшой JPEG (до 300 КБ), проверяем размер без полного декодирования
	if len(rawCover) <= 300*1024 && bytes.HasPrefix(rawCover, []byte("\xFF\xD8\xFF")) {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(rawCover))
		if err == nil && cfg.Width > 0 && cfg.Height > 0 && cfg.Width <= maxDimension && cfg.Height <= maxDimension {
			return rawCover, "image/jpeg"
		}
	}

	src, _, err := image.Decode(bytes.NewReader(rawCover))
	if err != nil {
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

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
		return rawCover, "image/jpeg"
	}

	return buf.Bytes(), "image/jpeg"
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
