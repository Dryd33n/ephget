package shortcut

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateAndReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ephgetw.exe")
	os.WriteFile(target, nil, 0o666)
	path := filepath.Join(dir, "VLC media player.lnk")
	want := Link{Target: target, Arguments: "run vlc --ttl 7d --keep-prefs", Description: Marker("vlc"), WorkingDir: dir}
	if err := Create(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(got.Target, target) || got.Arguments != want.Arguments || got.Description != "ephget:vlc" {
		t.Fatalf("%+v", got)
	}
	if _, err := Read(filepath.Join(dir, "missing.lnk")); err == nil {
		t.Fatal("read a missing shortcut")
	}
}

func TestKnownFolders(t *testing.T) {
	for name, get := range map[string]func() (string, error){"Desktop": Desktop, "Programs": Programs} {
		path, err := get()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("%s %q: %v", name, path, err)
		}
	}
}

func TestFileName(t *testing.T) {
	for in, want := range map[string]string{
		"VLC media player": "VLC media player.lnk",
		`a\b:c`:            "a_b_c.lnk",
		"Tool v2.":         "Tool v2.lnk",
		"...":              "id.lnk",
		"NUL":              "id.lnk",
		"":                 "id.lnk",
	} {
		if got := FileName(in, "id"); got != want {
			t.Errorf("FileName(%q) = %q, want %q", in, got, want)
		}
	}
}
