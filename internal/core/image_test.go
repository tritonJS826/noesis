package core

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	avifenc "github.com/gen2brain/gav1d/avif"
)

var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

func sampleImage() image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x * 60), G: uint8(y * 60), B: 200, A: 255})
		}
	}
	return img
}

func TestVisionImagePassesThroughPNGAndJPEG(t *testing.T) {
	ctx := context.Background()

	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, sampleImage()); err != nil {
		t.Fatal(err)
	}
	out, converted, err := visionImage(ctx, "a.png", "image/png", pngBuf.Bytes())
	if err != nil {
		t.Fatalf("png: %v", err)
	}
	if converted {
		t.Fatal("png should not be re-encoded")
	}
	if !bytes.Equal(out, pngBuf.Bytes()) {
		t.Fatal("png bytes changed")
	}

	var jpgBuf bytes.Buffer
	if err := jpeg.Encode(&jpgBuf, sampleImage(), nil); err != nil {
		t.Fatal(err)
	}
	out, converted, err = visionImage(ctx, "a.jpg", "image/jpeg", jpgBuf.Bytes())
	if err != nil {
		t.Fatalf("jpeg: %v", err)
	}
	if converted || !bytes.Equal(out, jpgBuf.Bytes()) {
		t.Fatal("jpeg should pass through unchanged")
	}
}

// AVIF is the format Ollama rejects; transcoding to PNG must work in pure Go,
// without ffmpeg or ImageMagick.
func TestVisionImageConvertsAVIF(t *testing.T) {
	var avifBuf bytes.Buffer
	if err := avifenc.Encode(&avifBuf, sampleImage()); err != nil {
		t.Fatalf("encode avif: %v", err)
	}

	out, converted, err := visionImage(context.Background(), "IMG_0001.avif", "image/avif", avifBuf.Bytes())
	if err != nil {
		t.Fatalf("visionImage: %v", err)
	}
	if !converted {
		t.Fatal("avif should be re-encoded to png")
	}
	if !bytes.HasPrefix(out, pngMagic) {
		t.Fatalf("output is not a PNG (first bytes %x)", out[:min(8, len(out))])
	}
	if _, _, err := image.Decode(bytes.NewReader(out)); err != nil {
		t.Fatalf("converted PNG does not decode: %v", err)
	}
}

func TestVisionImageRejectsUndecodableData(t *testing.T) {
	if _, _, err := visionImage(context.Background(), "broken.avif", "image/avif", []byte("not an image")); err == nil {
		t.Fatal("expected an error for undecodable data")
	}
}

// A multi-megapixel photo must be downscaled before it reaches the vision
// model; otherwise its token count explodes and the request stalls.
func TestVisionImageDownscalesOversizedImage(t *testing.T) {
	big := image.NewRGBA(image.Rect(0, 0, 3000, 2000))
	for y := 0; y < 2000; y++ {
		for x := 0; x < 3000; x++ {
			big.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, big); err != nil {
		t.Fatal(err)
	}

	out, converted, err := visionImage(context.Background(), "big.png", "image/png", buf.Bytes())
	if err != nil {
		t.Fatalf("visionImage: %v", err)
	}
	if !converted {
		t.Fatal("oversized png should be re-encoded")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("downscaled output does not decode: %v", err)
	}
	if cfg.Width != maxVisionImageDim {
		t.Fatalf("longest side = %d, want %d", cfg.Width, maxVisionImageDim)
	}
	if cfg.Height > maxVisionImageDim {
		t.Fatalf("height = %d, want <= %d", cfg.Height, maxVisionImageDim)
	}
}
