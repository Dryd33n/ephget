package index

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"testing"
)

func gz(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return buf.Bytes()
}

func TestParse(t *testing.T) {
	idx, err := Parse(gz(t, map[string]any{"schema": 1, "packages": []any{map[string]any{"id": "vlc", "installers": []any{
		map[string]any{"arch": "x64", "rename": []string{"a*.exe", "a.exe"}, "extract_dir": nil},
	}}}}))
	if err != nil {
		t.Fatal(err)
	}
	if p := idx.Find("VLC"); p == nil || p.Installers[0].Rename[1] != "a.exe" {
		t.Fatalf("%+v", p)
	}
	if idx.Find("nope") != nil {
		t.Fatal("found a missing app")
	}
}

func TestParseRefusesOtherSchemasAndDamage(t *testing.T) {
	good := gz(t, map[string]any{"schema": 1, "packages": []any{}})
	cases := map[string][]byte{
		"schema 2":      gz(t, map[string]any{"schema": 2, "packages": "a different shape"}),
		"schema 0":      gz(t, map[string]any{"schema": 0}),
		"no schema":     gz(t, map[string]any{"packages": []any{}}),
		"not gzip":      []byte("hello"),
		"truncated":     good[:len(good)-6],
		"not an object": gz(t, []int{1}),
	}
	for name, data := range cases {
		if _, err := Parse(data); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestSelectInstaller(t *testing.T) {
	both := Package{ID: "a", Installers: []Installer{{Arch: "x64"}, {Arch: "arm64"}}}
	x64Only := Package{ID: "b", Installers: []Installer{{Arch: "x64"}}}
	anyOnly := Package{ID: "c", Installers: []Installer{{Arch: "any"}}}
	armOnly := Package{ID: "d", Installers: []Installer{{Arch: "arm64"}}}
	cases := []struct {
		pkg           Package
		machine, want string
	}{
		{both, "x64", "x64"}, {both, "arm64", "arm64"}, {x64Only, "arm64", "x64"},
		{anyOnly, "x64", "any"}, {anyOnly, "arm64", "any"}, {armOnly, "arm64", "arm64"},
	}
	for _, c := range cases {
		got, err := c.pkg.SelectInstaller(c.machine)
		if err != nil || got.Arch != c.want {
			t.Errorf("%s on %s: %+v, %v", c.pkg.ID, c.machine, got, err)
		}
	}
	if _, err := armOnly.SelectInstaller("x64"); !errors.Is(err, ErrNoInstaller) {
		t.Fatal(err)
	}
}

func TestSearch(t *testing.T) {
	idx := &Index{Packages: []Package{
		{ID: "vlc", Name: "VLC media player", Description: "A multimedia player."},
		{ID: "mpv", Name: "mpv", Description: "Plays video, like vlc does."},
		{ID: "notepadplusplus", Name: "Notepad++", Description: "Text editor."},
		{ID: "wiztree", Name: "WizTree", Description: "Disk usage analyzer."},
		{ID: "windirstat", Name: "WinDirStat", Description: "Disk usage statistics."},
	}}
	ids := func(q string) []string {
		var out []string
		for _, p := range idx.Search(q) {
			out = append(out, p.ID)
		}
		return out
	}
	cases := []struct {
		query string
		first string
		count int
	}{
		{"vlc", "vlc", 2},        // ID beats a mention in a description
		{"VLC", "vlc", 2},        // ignores case
		{"media", "vlc", 1},      // name
		{"vcl", "vlc", 1},        // swapped letters
		{"wiztre", "wiztree", 1}, // typo
		{"npp", "notepadplusplus", 1},
		{"disk usage", "windirstat", 2}, // every word must match
	}
	for _, c := range cases {
		got := ids(c.query)
		if len(got) != c.count || got[0] != c.first {
			t.Errorf("Search(%q) = %v", c.query, got)
		}
	}
	if got := ids("disk zzzz"); len(got) != 0 {
		t.Errorf("one unmatched word still matched: %v", got)
	}
	if got := ids("  "); len(got) != 0 {
		t.Errorf("empty query matched: %v", got)
	}
}

func TestEditDistance(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"vcl", "vlc", 1}, {"abc", "abc", 0}, {"abc", "abd", 1}, {"abc", "abcd", 1}, {"abc", "xyz", 3}} {
		if got := editDistance(c.a, c.b, 3); got != c.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
