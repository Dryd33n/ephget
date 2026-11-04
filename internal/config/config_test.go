package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	got, err := Load(path)
	if err != nil || got != Default() {
		t.Fatalf("missing file: %+v, %v", got, err)
	}
	if got.TTL != "7d" || !got.KeepPrefs || !got.StartMenu || got.ShortcutDir != "" {
		t.Fatalf("wrong defaults: %+v", got)
	}
	want := Config{TTL: "1h", KeepPrefs: false, ShortcutDir: `D:\Apps`, StartMenu: false}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(path); err != nil || got != want {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
}

func TestLoadPartialAndInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"ttl":"2d"}`), 0o666)
	got, err := Load(path)
	if err != nil || got.TTL != "2d" || !got.KeepPrefs || !got.StartMenu {
		t.Fatalf("partial: %+v, %v", got, err)
	}
	for _, bad := range []string{`{"ttl":"5m"}`, `not json`} {
		os.WriteFile(path, []byte(bad), 0o666)
		if _, err := Load(path); err == nil {
			t.Errorf("Load(%s) succeeded", bad)
		}
	}
}
