package preview

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func TestValidate(t *testing.T) {
	for _, size := range []image.Point{{320, 200}, {321, 200}, {320, 321}, {1, 1}} {
		var b bytes.Buffer
		if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, size.X, size.Y))); err != nil {
			t.Fatal(err)
		}
		valid := size.X <= 320 && size.Y <= 320
		if err := Validate(b.Bytes()); (err == nil) != valid {
			t.Fatalf("%v: %v", size, err)
		}
		if err := Validate(b.Bytes()[:len(b.Bytes())-5]); err == nil {
			t.Fatal("accepted truncated PNG")
		}
	}
	for _, data := range [][]byte{nil, []byte("<svg/>"), bytes.Repeat([]byte{'x'}, MaxBytes+1), []byte("\x89PNG\r\n\x1a\n")} {
		if err := Validate(data); err == nil {
			t.Fatal("accepted invalid bytes")
		}
	}
}
