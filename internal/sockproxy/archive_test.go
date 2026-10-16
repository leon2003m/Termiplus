package sockproxy

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"
)

type entry struct {
	h    tar.Header
	body string
}

func mkTar(t *testing.T, entries ...entry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := e.h
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(tw, e.body)
	}
	_ = tw.Close()
	return &buf
}

func reg(name, body string) entry { return entry{tar.Header{Name: name, Typeflag: tar.TypeReg}, body} }

func readTar(t *testing.T, b []byte) []*tar.Header {
	t.Helper()
	var out []*tar.Header
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, h)
	}
}

func TestFilterHomeTarPassesContent(t *testing.T) {
	in := mkTar(t,
		entry{tar.Header{Name: "dir/", Typeflag: tar.TypeDir, Mode: 0o755}, ""},
		reg("dir/a.txt", "hello"),
		entry{tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/hostname"}, ""},
		entry{tar.Header{Name: "hard", Typeflag: tar.TypeLink, Linkname: "dir/a.txt"}, ""},
		entry{tar.Header{Name: "suid", Typeflag: tar.TypeReg, Mode: 0o4755, PAXRecords: map[string]string{"SCHILY.xattr.security.capability": "x"}}, "#!"},
	)
	var out bytes.Buffer
	if err := filterHomeTar(in, &out); err != nil {
		t.Fatal(err)
	}
	hs := readTar(t, out.Bytes())
	if len(hs) != 5 || hs[0].Name != "dir" || hs[1].Name != "dir/a.txt" || hs[2].Linkname != "/etc/hostname" {
		t.Fatalf("got %+v", hs)
	}
	if hs[4].Mode&0o6000 != 0 || hs[4].PAXRecords["SCHILY.xattr.security.capability"] != "" {
		t.Fatalf("setuid or xattrs passed: mode %o pax %v", hs[4].Mode, hs[4].PAXRecords)
	}
}

func TestFilterHomeTarRefuses(t *testing.T) {
	cases := map[string]*bytes.Buffer{
		"absolute":      mkTar(t, reg("/etc/passwd", "x")),
		"dotdot":        mkTar(t, reg("../x", "x")),
		"inner dotdot":  mkTar(t, reg("a/../../x", "x")),
		"dot":           mkTar(t, reg("./x", "x")),
		"double slash":  mkTar(t, reg("a//b", "x")),
		"backslash":     mkTar(t, reg(`a\b`, "x")),
		"device":        mkTar(t, entry{tar.Header{Name: "dev", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3}, ""}),
		"fifo":          mkTar(t, entry{tar.Header{Name: "fifo", Typeflag: tar.TypeFifo}, ""}),
		"below symlink": mkTar(t, entry{tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "/etc"}, ""}, reg("l/passwd", "x")),
		"deep symlink":  mkTar(t, entry{tar.Header{Name: "a/l", Typeflag: tar.TypeSymlink, Linkname: "/"}, ""}, reg("a/l/b/c", "x")),
		"hard link out": mkTar(t, entry{tar.Header{Name: "h", Typeflag: tar.TypeLink, Linkname: "../../etc/shadow"}, ""}),
		"hard link abs": mkTar(t, entry{tar.Header{Name: "h", Typeflag: tar.TypeLink, Linkname: "/etc/shadow"}, ""}),
		"empty symlink": mkTar(t, entry{tar.Header{Name: "s", Typeflag: tar.TypeSymlink}, ""}),
		"not a tar":     bytes.NewBufferString(strings.Repeat("garbage", 200)),
	}
	for name, in := range cases {
		var out bytes.Buffer
		err := filterHomeTar(in, &out)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if _, ok := err.(*denial); !ok {
			t.Errorf("%s: want a denial, got %T %v", name, err, err)
		}
	}
}
