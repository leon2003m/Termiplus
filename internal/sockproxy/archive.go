package sockproxy

import (
	"archive/tar"
	"errors"
	"io"
	"path"
	"strings"
)

// Home archives. The gateway reads a home as a tar from an archive helper
// (GET /containers/ot-a-.../archive?path=/home/user) for downloads, snapshots
// and archiving departed users, and writes one back (PUT, same path) to
// restore it. Docker extracts inside the helper's filesystem view, so nothing
// lands outside the container; on top of that, every uploaded tar is rewritten
// here entry by entry and only plain content passes.

// filterHomeTar copies the tar stream in to out, refusing anything but
// regular files, directories, symlinks and hard links with clean relative
// names. Extended attributes (file capabilities, ACLs) are dropped, as are
// setuid/setgid bits. A refused entry ends the stream with a denial; entries
// before it have already been passed on.
func filterHomeTar(in io.Reader, out io.Writer) error {
	tr := tar.NewReader(in)
	tw := tar.NewWriter(out)
	symlinks := map[string]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return tw.Close()
		}
		if err != nil {
			return deny("archive: %v", err)
		}
		name, ok := cleanTarName(h.Name)
		if !ok {
			return deny("archive entry %q not allowed", h.Name)
		}
		// Nothing may be written through a symlink that came earlier in the
		// same archive.
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			if symlinks[dir] {
				return deny("archive entry %q is below a symlink", h.Name)
			}
		}
		clean := &tar.Header{
			Name:    name,
			Mode:    h.Mode &^ 0o6000, // no setuid/setgid
			Uid:     h.Uid,
			Gid:     h.Gid,
			ModTime: h.ModTime,
			Format:  tar.FormatPAX,
		}
		switch h.Typeflag {
		case tar.TypeReg:
			clean.Typeflag, clean.Size = tar.TypeReg, h.Size
		case tar.TypeDir:
			clean.Typeflag = tar.TypeDir
		case tar.TypeSymlink:
			if h.Linkname == "" || strings.ContainsRune(h.Linkname, 0) {
				return deny("archive symlink %q not allowed", h.Name)
			}
			clean.Typeflag, clean.Linkname = tar.TypeSymlink, h.Linkname
			symlinks[name] = true
		case tar.TypeLink:
			target, ok := cleanTarName(h.Linkname)
			if !ok {
				return deny("archive hard link %q not allowed", h.Name)
			}
			clean.Typeflag, clean.Linkname = tar.TypeLink, target
		default:
			return deny("archive entry %q: type %q not allowed", h.Name, h.Typeflag)
		}
		if err := tw.WriteHeader(clean); err != nil {
			return err
		}
		if clean.Typeflag == tar.TypeReg {
			if _, err := io.Copy(tw, tr); err != nil {
				return err
			}
		}
	}
}

// cleanTarName accepts relative names without "..", "." or empty elements
// (a trailing slash on directories is fine) and returns them without it.
func cleanTarName(name string) (string, bool) {
	name = strings.TrimSuffix(name, "/")
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) || strings.Contains(name, "\\") || path.Clean(name) != name {
		return "", false
	}
	for _, el := range strings.Split(name, "/") {
		if el == ".." || el == "." {
			return "", false
		}
	}
	return name, true
}
