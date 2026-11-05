package reap

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Ephget/ephget/internal/lockfile"
	"github.com/Ephget/ephget/internal/paths"
	"github.com/Ephget/ephget/internal/shortcut"
	"github.com/Ephget/ephget/internal/state"
)

type fixture struct {
	layout paths.Layout
	store  state.Store
	stubs  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	layout := paths.Layout{Home: t.TempDir()}
	t.Setenv("EPHGET_HOME", layout.Home) // keeps scans out of the real folders and registry
	return &fixture{layout: layout, store: state.Store{Path: layout.State()}, stubs: t.TempDir()}
}

func (f *fixture) stub(t *testing.T, folder, id string) string {
	t.Helper()
	path := filepath.Join(folder, id+".lnk")
	link := shortcut.Link{Target: os.Args[0], Arguments: "run " + id, Description: shortcut.Marker(id)}
	if err := shortcut.Create(path, link); err != nil {
		t.Fatal(err)
	}
	return path
}

func (f *fixture) entry(t *testing.T, id string, shortcuts ...string) {
	t.Helper()
	err := f.store.Update(func(st state.State) error {
		st[id] = &state.Entry{TTL: "7d", KeepPrefs: true, Shortcuts: shortcuts}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) installed(t *testing.T, id, version string) {
	t.Helper()
	dir := filepath.Join(f.layout.Home, paths.AppRel(id, version))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.exe"), []byte("EXE"), 0o666); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) hasEntry(t *testing.T, id string) bool {
	t.Helper()
	st, err := f.store.Read()
	if err != nil {
		t.Fatal(err)
	}
	return st[id] != nil
}

func jsonString(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

// lockApp holds an app's fetch lock until the returned function is called.
func lockApp(t *testing.T, f *fixture, id string) func() {
	t.Helper()
	lock, err := lockfile.Acquire(f.layout.FetchLock(id))
	if err != nil {
		t.Fatal(err)
	}
	return lock.Release
}

func gone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("%s still exists", path)
	}
}

func TestRemoveDeletesStubsFilesAndState(t *testing.T) {
	f := newFixture(t)
	second := t.TempDir()
	a, b := f.stub(t, f.stubs, "tool"), f.stub(t, second, "tool")
	f.entry(t, "tool", a, b)
	f.installed(t, "tool", "1.0")

	r, err := Remove(f.layout, "tool", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Deleted) != 2 || len(r.Undeleted) != 0 || !r.HadFiles {
		t.Fatalf("%+v", r)
	}
	gone(t, a)
	gone(t, b)
	gone(t, f.layout.AppRoot("tool"))
	gone(t, f.layout.Removing("tool"))
	if f.hasEntry(t, "tool") {
		t.Error("state entry survived")
	}
}

// A renamed folder can't hide older versions: the whole app goes.
func TestRemoveTakesEveryVersion(t *testing.T) {
	f := newFixture(t)
	f.entry(t, "tool")
	f.installed(t, "tool", "1.0")
	f.installed(t, "tool", "2.0")
	if _, err := Remove(f.layout, "tool", nil); err != nil {
		t.Fatal(err)
	}
	gone(t, f.layout.AppRoot("tool"))
}

// An app that was never downloaded, or whose stubs are already gone, still has to
// lose its entry. This is the case a failed rename must not mistake for "running".
func TestRemoveUncachedAppWithMissingStubs(t *testing.T) {
	f := newFixture(t)
	f.entry(t, "tool", filepath.Join(f.stubs, "deleted.lnk"))
	r, err := Remove(f.layout, "tool", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.HadFiles || len(r.Deleted) != 0 {
		t.Fatalf("%+v", r)
	}
	if f.hasEntry(t, "tool") {
		t.Error("state entry survived")
	}
}

func TestRemoveFindsMovedStubByMarker(t *testing.T) {
	f := newFixture(t)
	nested := filepath.Join(f.stubs, "games", "old")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	moved := f.stub(t, nested, "tool")
	other := f.stub(t, f.stubs, "other")
	f.entry(t, "tool", filepath.Join(f.stubs, "where-it-was.lnk"))

	r, err := Remove(f.layout, "tool", []string{f.stubs})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Deleted) != 1 {
		t.Fatalf("%+v", r)
	}
	gone(t, moved)
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another app's stub was deleted: %v", err)
	}
}

func TestRemoveLeavesAForeignShortcutAtARecordedPath(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.stubs, "Tool.lnk")
	if err := shortcut.Create(path, shortcut.Link{Target: os.Args[0], Description: "theirs"}); err != nil {
		t.Fatal(err)
	}
	f.entry(t, "tool", path)
	r, err := Remove(f.layout, "tool", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Deleted) != 0 {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("foreign shortcut deleted: %v", err)
	}
}

func TestRemoveWorksWithoutStateEntry(t *testing.T) {
	f := newFixture(t)
	stub := f.stub(t, f.stubs, "tool")
	f.installed(t, "tool", "1.0")
	if _, err := Remove(f.layout, "TOOL", []string{f.stubs}); err != nil {
		t.Fatal(err)
	}
	gone(t, stub)
	gone(t, f.layout.AppRoot("tool"))
}

func TestRemoveFinishesAnEarlierInterruptedRemoval(t *testing.T) {
	f := newFixture(t)
	trash := f.layout.Removing("tool")
	if err := os.MkdirAll(trash, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(f.layout, "tool", nil); err != nil {
		t.Fatal(err)
	}
	gone(t, trash)
}

func TestRemoveUnknownApp(t *testing.T) {
	f := newFixture(t)
	if _, err := Remove(f.layout, "nope", []string{f.stubs}); !errors.Is(err, ErrNotAdded) {
		t.Fatalf("got %v", err)
	}
}

func TestRemoveRejectsUnsafeID(t *testing.T) {
	f := newFixture(t)
	f.installed(t, "tool", "1.0")
	for _, id := range []string{`..\apps`, "..", `tool\1.0`, "NUL"} {
		if _, err := Remove(f.layout, id, nil); err == nil || errors.Is(err, ErrNotAdded) {
			t.Errorf("%q: got %v", id, err)
		}
	}
	if _, err := os.Stat(f.layout.AppRoot("tool")); err != nil {
		t.Errorf("app folder touched: %v", err)
	}
}

func TestRemoveRefusesDuringADownload(t *testing.T) {
	f := newFixture(t)
	f.entry(t, "tool")
	f.installed(t, "tool", "1.0")
	if err := os.MkdirAll(f.layout.Tmp(), 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := lockfile.Acquire(f.layout.FetchLock("tool"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err := Remove(f.layout, "tool", nil); !errors.Is(err, ErrDownloading) {
		t.Fatalf("got %v", err)
	}
	if !f.hasEntry(t, "tool") {
		t.Error("state entry dropped")
	}
	if _, err := os.Stat(f.layout.AppRoot("tool")); err != nil {
		t.Errorf("app folder touched: %v", err)
	}
}
