// Package preview validates the small, untrusted images returned by desktops.
package preview

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
)

const MaxBytes = 256 << 10

// Validate bounds both encoded bytes and decoded dimensions before decoding.
// Only PNG is accepted, never SVG or an arbitrary container Content-Type.
func Validate(data []byte) error {
	if len(data) > MaxBytes || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		return fmt.Errorf("invalid preview bytes")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Width > 320 || cfg.Height < 1 || cfg.Height > 320 {
		return fmt.Errorf("invalid preview dimensions")
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("invalid preview: %w", err)
	}
	return nil
}

// MaxScreenBytes bounds encoded native desktop images before any decode.
const MaxScreenBytes = 8 << 20

func decodeScreen(data []byte) (image.Image, error) {
	if len(data) > MaxScreenBytes {
		return nil, fmt.Errorf("screen too large")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Width > 2048 || cfg.Height < 1 || cfg.Height > 2048 {
		return nil, fmt.Errorf("invalid screen dimensions")
	}
	return png.Decode(bytes.NewReader(data))
}

// Screen re-encodes untrusted PNGs, retaining pixels only.
func Screen(data []byte) ([]byte, error) {
	im, err := decodeScreen(data)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := png.Encode(&out, im); err != nil {
		return nil, err
	}
	if out.Len() > MaxScreenBytes {
		return nil, fmt.Errorf("screen too large")
	}
	return out.Bytes(), nil
}

// Thumbnail scales the native desktop to the console's existing bounded size.
func Thumbnail(data []byte) ([]byte, error) {
	im, err := decodeScreen(data)
	if err != nil {
		return nil, err
	}
	bounds := im.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width > 320 || height > 320 {
		if width >= height {
			height = max(1, height*320/width)
			width = 320
		} else {
			width = max(1, width*320/height)
			height = 320
		}
	}
	thumb := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			thumb.Set(x, y, im.At(bounds.Min.X+x*bounds.Dx()/width, bounds.Min.Y+y*bounds.Dy()/height))
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, thumb); err != nil {
		return nil, err
	}
	if err := Validate(out.Bytes()); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
