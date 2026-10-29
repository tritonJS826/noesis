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
// When Ollama eventually supports AVIF (and other) images natively, this whole
// step becomes unnecessary: `visionImage` can simply return the original bytes
// for every raster format (exactly what it already does for PNG/JPEG) and the
// gav1d / x-image decoder imports can be dropped.
// ─────────────────────────────────────────────────────────────────────────────

// visionImage returns the bytes to hand to the vision model. PNG and JPEG are
// passed through untouched because Ollama already recognises them; any other
// raster image is decoded and re-encoded to PNG so it can be identified.
// converted reports whether re-encoding took place.
func visionImage(ctx context.Context, name, mimeType string, data []byte) (out []byte, converted bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	// Formats Ollama understands natively: keep the original bytes.
	switch mimeType {
	case "image/png", "image/jpeg":
		return data, false, nil
	}

	img, format, decErr := image.Decode(bytes.NewReader(data))
	if decErr != nil {
		return nil, false, fmt.Errorf("cannot decode %s image (%s): %w", name, mimeType, decErr)
	}
	if format == "png" || format == "jpeg" {
		return data, false, nil
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, false, fmt.Errorf("re-encode %s (%s) as PNG: %w", name, format, err)
	}
	return buf.Bytes(), true, nil
}
