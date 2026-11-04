package extract

import (
	"archive/zip"
	"hash/crc32"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func makeZip(t *testing.T, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	for name, content := range entries {
		e, err := w.CreateRaw(&zip.FileHeader{
			Name: name, Method: zip.Store, CRC32: crc32.ChecksumIEEE([]byte(content)),
			CompressedSize64: uint64(len(content)), UncompressedSize64: uint64(len(content)),
		})
		if err != nil {
			t.Fatal(err)
		}
		e.Write([]byte(content))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestZipExtracts(t *testing.T) {
	src := makeZip(t, map[string]string{"app/a.exe": "A", `app\sub\b.txt`: "B", "app/empty/": ""})
	dst := t.TempDir()
	if err := Zip(src, dst); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{`app\a.exe`: "A", `app\sub\b.txt`: "B"} {
		got, err := os.ReadFile(filepath.Join(dst, path))
		if err != nil || string(got) != want {
			t.Errorf("%s: %q, %v", path, got, err)
		}
	}
	if info, err := os.Stat(filepath.Join(dst, "app", "empty")); err != nil || !info.IsDir() {
		t.Errorf("empty folder: %v", err)
	}
}

func TestZipRejectsEscapes(t *testing.T) {
	for _, name := range []string{"../evil.exe", `..\evil.exe`, "a/../../evil.exe", `C:\evil.exe`, "C:/evil.exe", "/evil.exe",
		`\evil.exe`, `\\host\share\evil.exe`, "a.exe:stream", "CON", "x/NUL.txt", ".. /evil.exe", "a./b"} {
		parent := t.TempDir()
		dst := filepath.Join(parent, "dst")
		os.Mkdir(dst, 0o755)
		err := Zip(makeZip(t, map[string]string{name: "x"}), dst)
		if err == nil {
			t.Errorf("%q: extracted", name)
		}
		if _, statErr := os.Stat(filepath.Join(parent, "evil.exe")); statErr == nil {
			t.Errorf("%q: escaped the destination", name)
		}
	}
}

func TestSymlinkEntryRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.zip")
	f, _ := os.Create(path)
	w := zip.NewWriter(f)
	h := &zip.FileHeader{Name: "link"}
	h.SetMode(fs.ModeSymlink | 0o777)
	e, err := w.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	e.Write([]byte(`C:\Windows`))
	w.Close()
	f.Close()
	if err := Zip(path, t.TempDir()); err == nil {
		t.Fatal("symlink entry extracted")
	}
}

func TestNotAnArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x")
	os.WriteFile(path, []byte("not an archive"), 0o666)
	if err := Zip(path, t.TempDir()); err == nil {
		t.Error("Zip accepted junk")
	}
	if err := SevenZip(path, t.TempDir()); err == nil {
		t.Error("SevenZip accepted junk")
	}
}

// testdata/sample.7z holds top/a.txt ("hello 7z\n") and top/sub/b.txt ("nested\n").
func TestSevenZip(t *testing.T) {
	dst := t.TempDir()
	if err := SevenZip(filepath.Join("testdata", "sample.7z"), dst); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{`top\a.txt`: "hello 7z\n", `top\sub\b.txt`: "nested\n"} {
		got, err := os.ReadFile(filepath.Join(dst, path))
		if err != nil || string(got) != want {
			t.Errorf("%s: %q, %v", path, got, err)
		}
	}
}
