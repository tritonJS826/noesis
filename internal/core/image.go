package core

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"

	// Image decoders used by the vision transcode step. The standard library
	// covers PNG/JPEG/GIF; the rest are pure-Go decoders.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	xdraw "golang.org/x/image/draw"

	_ "github.com/gen2brain/gav1d/avif" // AVIF (pure-Go dav1d port)
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// ── Why images are transcoded before they reach the vision model ─────────────
//
// Ollama's multimodal API takes a bare base64 image with no MIME type, so the
// server sniffs the bytes to decide whether the payload is a supported image.
// Formats it does not recognise — notably AVIF — are classified as
// "application/octet-stream" and rejected with:
//
//	invalid image: expected image mime type, got "application/octet-stream"
//
// To keep sending images regardless of their source format, anything that is
// not already PNG/JPEG is decoded and re-encoded to PNG, entirely in pure Go
// (no cgo, no ffmpeg/ImageMagick dependency):
//
//	png, jpeg, gif : Go standard library
//	webp, bmp, tiff: golang.org/x/image
//	avif           : github.com/gen2brain/gav1d (pure-Go port of dav1d)
//
// Independently of the format, an image is also downscaled so its longest side
// never exceeds maxVisionImageDim. Vision models resample every input to a
// small fixed size (typically ≤896px) before running, so a multi-megapixel
// photo cannot produce a more detailed description — it only inflates the
// base64 payload and the vision-token count, which is what made a handful of
// very large images stall the extraction run until the request timed out.
//
// When Ollama eventually supports AVIF (and other) images natively, the
// transcode step becomes unnecessary: `visionImage` can simply return the
// original bytes for every raster format and the gav1d / x-image decoder
// imports can be dropped. The downscaling should stay.
// ─────────────────────────────────────────────────────────────────────────────

const (
	// maxVisionImageDim is the longest side (in pixels) an image may have when
	// it is handed to the vision model.
	maxVisionImageDim = 1120

	// maxVisionImagePixels rejects decompression bombs before they are decoded
	// into memory: 64 megapixels is far above any real note attachment.
	maxVisionImagePixels = 64 << 20
)

// visionImage returns the bytes to hand to the vision model. PNG and JPEG that
// are already small enough are passed through untouched because Ollama
// recognises them; any other raster image, or any image larger than
// maxVisionImageDim, is decoded and re-encoded to PNG. converted reports
// whether re-encoding took place.
func visionImage(ctx context.Context, name, mimeType string, data []byte) (out []byte, converted bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	native := mimeType == "image/png" || mimeType == "image/jpeg"

	// Read only the header first: it gives the dimensions without decoding the
	// whole image. Formats Go cannot parse fall through to the full decoder
	// below, which reports a proper error.
	if cfg, _, cfgErr := image.DecodeConfig(bytes.NewReader(data)); cfgErr == nil {
		if int64(cfg.Width)*int64(cfg.Height) > maxVisionImagePixels {
			return nil, false, fmt.Errorf("%s: image too large to process (%dx%d)", name, cfg.Width, cfg.Height)
		}
		if native && cfg.Width <= maxVisionImageDim && cfg.Height <= maxVisionImageDim {
			return data, false, nil
		}
	}

	img, format, decErr := image.Decode(bytes.NewReader(data))
	if decErr != nil {
		if native {
			// Let Ollama try the original bytes for a PNG/JPEG we could not
			// decode ourselves (e.g. an unusual or truncated encoding).
			return data, false, nil
		}
		return nil, false, fmt.Errorf("cannot decode %s image (%s): %w", name, mimeType, decErr)
	}

	if b := img.Bounds(); b.Dx() > maxVisionImageDim || b.Dy() > maxVisionImageDim {
		img = downscaleImage(img, maxVisionImageDim)
		converted = true
	} else if format == "png" || format == "jpeg" {
		// Native image that the header check let through: keep the original.
		return data, false, nil
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, false, fmt.Errorf("re-encode %s (%s) as PNG: %w", name, format, err)
	}
	return buf.Bytes(), true, nil
}

// downscaleImage returns img scaled so its longest side is at most max,
// preserving the aspect ratio. The source image is never scaled up.
func downscaleImage(img image.Image, max int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return img
	}
	nw, nh := fitWithin(w, h, max)
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	return dst
}

// fitWithin returns the largest w×h box with the same aspect ratio as the
// inputs whose longest side is max, rounding to whole pixels (never below 1).
func fitWithin(w, h, max int) (int, int) {
	if w <= 0 || h <= 0 {
		return max, max
	}
	if w >= h {
		nh := h * max / w
		if nh < 1 {
			nh = 1
		}
		return max, nh
	}
	nw := w * max / h
	if nw < 1 {
		nw = 1
	}
	return nw, max
}
