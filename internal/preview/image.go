// Package preview validates the small, untrusted images returned by desktops.
package preview

import (
	"bytes"
	"fmt"
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
