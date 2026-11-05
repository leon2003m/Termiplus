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

func TestNativeScreenAndThumbnail(t *testing.T) {
	for _, size := range []image.Point{{1280, 800}, {800, 1280}, {1, 2048}, {2049, 1}} {
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, size.X, size.Y))); err != nil {
			t.Fatal(err)
		}
		normalized, err := Screen(append(encoded.Bytes(), []byte("<script>bad</script>")...))
		if size.X > 2048 {
			if err == nil {
				t.Fatal("oversized dimensions accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(normalized, []byte("<script>")) {
			t.Fatal("trailing data retained")
		}
		thumb, err := Thumbnail(normalized)
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate(thumb); err != nil {
			t.Fatal(err)
		}
		cfg, _ := png.DecodeConfig(bytes.NewReader(thumb))
		if size.X == 1280 && (cfg.Width != 320 || cfg.Height != 200) {
			t.Fatalf("wrong aspect ratio: %v", cfg)
		}
	}
	for _, invalid := range [][]byte{[]byte("<html>"), bytes.Repeat([]byte{0}, MaxScreenBytes+1), nil} {
		if _, err := Screen(invalid); err == nil {
			t.Fatal("invalid screen accepted")
		}
		if _, err := Thumbnail(invalid); err == nil {
			t.Fatal("invalid thumbnail accepted")
		}
	}
}

func TestScreenEncodingStopsAtByteLimit(t *testing.T) {
	var out screenBuffer
	if _, err := out.Write(make([]byte, MaxScreenBytes-1)); err != nil {
		t.Fatal(err)
	}
	if n, err := out.Write([]byte{1, 2}); err == nil || n != 0 || out.Len() != MaxScreenBytes-1 {
		t.Fatal("oversized encoded chunk was retained")
	}
	if _, err := out.Write([]byte{1}); err != nil || out.Len() != MaxScreenBytes {
		t.Fatal("exact byte limit rejected")
	}
	if n, err := out.Write([]byte{1}); err == nil || n != 0 || out.Len() != MaxScreenBytes {
		t.Fatal("encoder exceeded the byte limit")
	}
}
