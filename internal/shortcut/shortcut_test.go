package shortcut

import (
	"fmt"
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

func TestSearchFoldersResolve(t *testing.T) {
	folders := SearchFolders()
	if len(folders) != 6 {
		t.Fatalf("got %d folders: %q", len(folders), folders)
	}
	// The taskbar folder, last, only exists once something has been pinned.
	for _, folder := range folders[:5] {
		if info, err := os.Stat(folder); err != nil || !info.IsDir() {
			t.Errorf("%q: %v", folder, err)
		}
	}
}

func TestFindMatchesMarkerOnly(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(nested, "Mine.lnk")
	for path, id := range map[string]string{mine: "vlc", filepath.Join(dir, "Other.lnk"): "gimp"} {
		if err := Create(path, Link{Target: os.Args[0], Description: Marker(id)}); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(dir, "broken.lnk"), []byte("not a shortcut"), 0o666)
	got := Find("vlc", []string{dir, filepath.Join(dir, "missing")})
	if len(got) != 1 || !strings.EqualFold(got[0], mine) {
		t.Fatalf("got %q", got)
	}
}

// growStack calls fn from a chosen depth. A new goroutine has a small stack, so
// the calls fn makes will outgrow it and move it at different points.
//
//go:noinline
func growStack(depth int, fn func()) {
	var pad [64]byte
	if depth > 0 {
		growStack(depth-1, fn)
		pad[0]++
		return
	}
	fn()
}

// COM writes into buffers through raw addresses. If one of those buffers lived on a
// goroutine's stack and the stack moved first, COM would write to freed memory, so
// this reads a shortcut from stacks in many different states.
func TestReadSurvivesStackMoves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "App.lnk")
	want := Link{Target: os.Args[0], Arguments: "run app --ttl 7d --keep-prefs", Description: Marker("app"), Icon: filepath.Join(dir, "app.ico")}
	if err := Create(path, want); err != nil {
		t.Fatal(err)
	}
	for i := range 600 {
		done := make(chan error, 1)
		go growStack(i%400, func() {
			link, err := Read(path)
			if err == nil && (link.Arguments != want.Arguments || link.Description != want.Description || !strings.EqualFold(link.Icon, want.Icon)) {
				err = fmt.Errorf("read %+v", link)
			}
			done <- err
		})
		if err := <-done; err != nil {
			t.Fatalf("attempt %d: %v", i, err)
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
