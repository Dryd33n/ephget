package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomicReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json")
	for _, content := range []string{"one", "two"} {
		if err := WriteAtomic(path, []byte(content)); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != content {
			t.Fatalf("got %q, %v; want %q", got, err, content)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestClearMarkOfTheWeb(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "sub", "a.exe")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	stream := file + ":Zone.Identifier"
	if err := os.WriteFile(stream, []byte("[ZoneTransfer]\r\nZoneId=3\r\n"), 0o666); err != nil {
		t.Skipf("file system has no alternate streams: %v", err)
	}
	if err := ClearMarkOfTheWeb(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stream); err == nil {
		t.Fatal("Zone.Identifier still present")
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "x" {
		t.Fatalf("file damaged: %q, %v", got, err)
	}
}
