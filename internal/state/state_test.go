package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConcurrentUpdatesAllLand(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "state.json")}
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := range 40 {
		wg.Go(func() {
			errs <- s.Update(func(st State) error {
				st[fmt.Sprintf("app%d", i)] = &Entry{TTL: "7d"}
				return nil
			})
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	st, err := s.Read()
	if err != nil || len(st) != 40 {
		t.Fatalf("%d entries, %v", len(st), err)
	}
}

func TestUpdateErrorSavesNothing(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "state.json")}
	wantErr := fmt.Errorf("no")
	err := s.Update(func(st State) error {
		st["a"] = &Entry{}
		return wantErr
	})
	if err != wantErr {
		t.Fatal(err)
	}
	if st, _ := s.Read(); len(st) != 0 {
		t.Fatalf("saved %v", st)
	}
}

func TestCorruptFileIsSetAside(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "state.json")}
	os.WriteFile(s.Path, []byte("{broken"), 0o666)
	st, err := s.Read()
	if err != nil || len(st) != 0 {
		t.Fatalf("%v, %v", st, err)
	}
	if _, err := os.Stat(s.Path + ".corrupt"); err != nil {
		t.Fatal(err)
	}
}

func TestRoundTrip(t *testing.T) {
	now := time.Date(2026, 11, 4, 1, 5, 0, 0, time.UTC)
	s := Store{Path: filepath.Join(t.TempDir(), "state.json")}
	err := s.Update(func(st State) error {
		st["vlc"] = &Entry{Bucket: "Extras", Version: "3", Path: `apps\vlc\3`, Exe: "vlc.exe", LastLaunched: &now, TTL: "7d", KeepPrefs: true}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Read()
	if err != nil || st["vlc"].LastClosed != nil || !st["vlc"].LastLaunched.Equal(now) {
		t.Fatalf("%+v, %v", st["vlc"], err)
	}
}

func TestCached(t *testing.T) {
	home := t.TempDir()
	e := &Entry{Path: `apps\a\1`, Exe: `bin\a.exe`}
	if e.Cached(home) {
		t.Fatal("cached before the file exists")
	}
	exe := filepath.Join(home, e.Path, e.Exe)
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, nil, 0o666)
	if !e.Cached(home) {
		t.Fatal("not cached with the file present")
	}
	var missing *Entry
	if missing.Cached(home) || (&Entry{TTL: "7d"}).Cached(home) {
		t.Fatal("stub reported as cached")
	}
}

func TestAddShortcutIgnoresCase(t *testing.T) {
	e := &Entry{}
	e.AddShortcut(`C:\A.lnk`)
	e.AddShortcut(`c:\a.LNK`)
	e.AddShortcut(`C:\B.lnk`)
	if len(e.Shortcuts) != 2 {
		t.Fatal(e.Shortcuts)
	}
}

func TestTrackShortcutReplacesAMovedStub(t *testing.T) {
	dir := t.TempDir()
	old, kept, moved := filepath.Join(dir, "old.lnk"), filepath.Join(dir, "kept.lnk"), filepath.Join(dir, "moved.lnk")
	for _, path := range []string{kept, moved} {
		if err := os.WriteFile(path, nil, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	e := &Entry{Shortcuts: []string{old, kept}}
	e.TrackShortcut(moved)
	e.TrackShortcut(strings.ToUpper(moved)) // the same stub, however it's spelled
	if len(e.Shortcuts) != 2 || e.Shortcuts[0] != kept || e.Shortcuts[1] != moved {
		t.Fatalf("%q", e.Shortcuts)
	}
}
