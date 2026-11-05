// Package extract unpacks untrusted archives. Any entry that would land outside
// the destination fails the whole extraction.
package extract

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"

	"github.com/Dryd33n/ephget/internal/paths"
)

// entry is what the two archive formats have in common.
type entry struct {
	name string
	mode fs.FileMode
	open func() (io.ReadCloser, error)
}

// Zip extracts the zip archive at src into dst.
func Zip(src, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()
	for _, f := range r.File {
		if err := write(dst, entry{f.Name, f.Mode(), f.Open}); err != nil {
			return err
		}
	}
	return nil
}

// SevenZip extracts the 7z archive at src into dst. Entries are read in archive
// order, which is what keeps solid archives from being decompressed repeatedly.
func SevenZip(src, dst string) error {
	r, err := sevenzip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("open 7z: %w", err)
	}
	defer r.Close()
	for _, f := range r.File {
		if err := write(dst, entry{f.Name, f.Mode(), f.Open}); err != nil {
			return err
		}
	}
	return nil
}

func write(dst string, e entry) error {
	name := strings.ReplaceAll(e.name, `\`, "/")
	if !paths.SafeRelative(name) {
		return fmt.Errorf("archive entry %q is outside the app folder", e.name)
	}
	if e.mode&(fs.ModeSymlink|fs.ModeDevice|fs.ModeNamedPipe|fs.ModeSocket|fs.ModeCharDevice) != 0 {
		return fmt.Errorf("archive entry %q is not a regular file", e.name)
	}
	target := filepath.Join(dst, filepath.FromSlash(name))
	if e.mode.IsDir() || strings.HasSuffix(name, "/") {
		return os.MkdirAll(target, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	in, err := e.open()
	if err != nil {
		return fmt.Errorf("read %q: %w", e.name, err)
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("extract %q: %w", e.name, err)
	}
	return out.Close()
}
