package reap

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dryd33n/ephget/internal/paths"
	"github.com/Dryd33n/ephget/internal/state"
)

var now = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// cachedEntry records an installed app with one live stub, closed `age` ago.
func (f *fixture) cachedEntry(t *testing.T, id, ttl string, keep bool, age time.Duration, persist ...string) (stub string) {
	t.Helper()
	f.installed(t, id, "1.0")
	stub = f.stub(t, f.stubs, id)
	closed := now.Add(-age)
	err := f.store.Update(func(st state.State) error {
		st[id] = &state.Entry{Version: "1.0", Path: paths.AppRel(id, "1.0"), Exe: "app.exe", TTL: ttl, KeepPrefs: keep,
			LastClosed: &closed, Shortcuts: []string{stub}, Persist: persist}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return stub
}

func (f *fixture) get(t *testing.T, id string) *state.Entry {
	t.Helper()
	st, err := f.store.Read()
	if err != nil {
		t.Fatal(err)
	}
	return st[id]
}

func (f *fixture) appFile(id, name string) string {
	return filepath.Join(f.layout.Home, paths.AppRel(id, "1.0"), name)
}

func present(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("%s is missing: %v", path, err)
	}
}

func TestPassKeepsAnAppInsideItsTTL(t *testing.T) {
	f := newFixture(t)
	f.cachedEntry(t, "tool", "1h", true, 59*time.Minute)
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	if !f.get(t, "tool").Cached(f.layout.Home) {
		t.Fatal("app removed before its TTL")
	}
}

func TestPassExpiresAnAppAndKeepsItsStub(t *testing.T) {
	f := newFixture(t)
	stub := f.cachedEntry(t, "tool", "1h", true, 61*time.Minute)
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	gone(t, f.layout.AppRoot("tool"))
	gone(t, f.layout.Removing("tool"))
	present(t, stub)
	e := f.get(t, "tool")
	if e == nil || e.Path != "" || e.TTL != "1h" || len(e.Shortcuts) != 1 {
		t.Fatalf("entry after expiry: %+v", e)
	}
}

func TestPassFallsBackToLastLaunched(t *testing.T) {
	f := newFixture(t)
	f.cachedEntry(t, "tool", "1h", true, 0)
	launched := now.Add(-2 * time.Hour)
	f.store.Update(func(st state.State) error {
		st["tool"].LastClosed, st["tool"].LastLaunched = nil, &launched
		return nil
	})
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	gone(t, f.layout.AppRoot("tool"))
}

// A failed launch leaves no timestamps, and that copy mustn't stay forever.
func TestPassCountsANeverOpenedAppFromItsDownload(t *testing.T) {
	f := newFixture(t)
	f.cachedEntry(t, "tool", "1h", true, 0)
	f.store.Update(func(st state.State) error {
		st["tool"].LastClosed = nil
		return nil
	})
	root := filepath.Join(f.layout.Home, paths.AppRel("tool", "1.0"))
	downloaded := now.Add(-30 * time.Minute)
	if err := os.Chtimes(root, downloaded, downloaded); err != nil {
		t.Fatal(err)
	}
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	present(t, root)
	if err := Pass(f.layout, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	gone(t, root)
}

func TestPassSavesPersistItemsWithKeepPrefs(t *testing.T) {
	f := newFixture(t)
	f.cachedEntry(t, "tool", "1h", true, 2*time.Hour, `conf\settings.ini`, "data", "never-created", `..\escape`)
	os.MkdirAll(filepath.Dir(f.appFile("tool", `conf\settings.ini`)), 0o755)
	os.WriteFile(f.appFile("tool", `conf\settings.ini`), []byte("mine"), 0o666)
	os.MkdirAll(f.appFile("tool", "data"), 0o755)
	os.WriteFile(f.appFile("tool", `data\saved.txt`), []byte("save"), 0o666)
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	gone(t, f.layout.AppRoot("tool"))
	got, err := os.ReadFile(filepath.Join(f.layout.Prefs("tool"), `conf\settings.ini`))
	if err != nil || string(got) != "mine" {
		t.Errorf("settings file: %q, %v", got, err)
	}
	present(t, filepath.Join(f.layout.Prefs("tool"), `data\saved.txt`))
	gone(t, filepath.Join(f.layout.Home, "prefs", "escape"))
}

func TestPassDropsSettingsWithoutKeepPrefs(t *testing.T) {
	f := newFixture(t)
	f.cachedEntry(t, "tool", "1h", false, 2*time.Hour, "settings.ini")
	os.WriteFile(f.appFile("tool", "settings.ini"), []byte("mine"), 0o666)
	saved := f.layout.Prefs("tool")
	os.MkdirAll(saved, 0o755)
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	gone(t, f.layout.AppRoot("tool"))
	gone(t, saved)
}

func TestPassTreatsDeletedStubsAsRemovalWhateverTheTTL(t *testing.T) {
	f := newFixture(t)
	stub := f.cachedEntry(t, "tool", "30d", true, 0, "settings.ini")
	os.WriteFile(f.appFile("tool", "settings.ini"), []byte("mine"), 0o666)
	if err := os.Remove(stub); err != nil {
		t.Fatal(err)
	}
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	gone(t, f.layout.AppRoot("tool"))
	if f.get(t, "tool") != nil {
		t.Fatal("state entry survived")
	}
	// The stub may only have moved somewhere a scan can't see, so settings stay.
	got, err := os.ReadFile(filepath.Join(f.layout.Prefs("tool"), "settings.ini"))
	if err != nil || string(got) != "mine" {
		t.Fatalf("settings lost: %q, %v", got, err)
	}
	// And `remove` still clears them, even with nothing else of the app left.
	if _, err := Remove(f.layout, "tool", nil); err != nil {
		t.Fatal(err)
	}
	gone(t, f.layout.Prefs("tool"))
}

func TestPassDropsSettingsOfDeletedStubsWithoutKeepPrefs(t *testing.T) {
	f := newFixture(t)
	stub := f.cachedEntry(t, "tool", "30d", false, 0, "settings.ini")
	os.WriteFile(f.appFile("tool", "settings.ini"), []byte("mine"), 0o666)
	os.Remove(stub)
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	gone(t, f.layout.Prefs("tool"))
}

func TestFootprintFollowsTheToggle(t *testing.T) {
	for _, keep := range []bool{true, false} {
		f := newFixture(t)
		t.Setenv("EPHGET_HOME", f.layout.Home) // leaves the real registry alone
		appData := t.TempDir()
		t.Setenv("APPDATA", appData)
		settings := filepath.Join(appData, "tool")
		os.MkdirAll(settings, 0o755)
		f.cachedEntry(t, "tool", "1h", keep, 2*time.Hour)
		f.store.Update(func(st state.State) error {
			st["tool"].Footprint = []string{`%APPDATA%\tool`}
			return nil
		})
		if err := Pass(f.layout, now); err != nil {
			t.Fatal(err)
		}
		_, err := os.Stat(settings)
		if keep && err != nil {
			t.Errorf("keep on: settings deleted: %v", err)
		}
		if !keep && err == nil {
			t.Error("keep off: settings survived")
		}
	}
}

func TestDeletedStubsDropTheIconButExpiryKeepsIt(t *testing.T) {
	f := newFixture(t)
	f.cachedEntry(t, "tool", "1h", true, 2*time.Hour)
	icon := f.layout.Icon("tool")
	os.MkdirAll(filepath.Dir(icon), 0o755)
	os.WriteFile(icon, []byte("ICO"), 0o666)
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	present(t, icon)
	stub := f.get(t, "tool").Shortcuts[0]
	os.Remove(stub)
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	gone(t, icon)
}

// Deleting one of an app's two stubs doesn't remove it.
func TestPassKeepsAppWhileAnyStubRemains(t *testing.T) {
	f := newFixture(t)
	second := filepath.Join(t.TempDir(), "other.lnk")
	stub := f.cachedEntry(t, "tool", "30d", true, 0)
	f.store.Update(func(st state.State) error {
		st["tool"].Shortcuts = append(st["tool"].Shortcuts, second)
		return nil
	})
	if err := os.WriteFile(second, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	os.Remove(stub)
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	if !f.get(t, "tool").Cached(f.layout.Home) {
		t.Fatal("app removed while a stub remained")
	}
}

// The spec's open problem: an uncached app whose stubs are gone has no folder to
// rename, which must not read as "running".
func TestPassDropsUncachedAppWithDeletedStubs(t *testing.T) {
	f := newFixture(t)
	f.entry(t, "tool", filepath.Join(f.stubs, "deleted.lnk"))
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	if f.get(t, "tool") != nil {
		t.Fatal("state entry survived")
	}
}

func TestPassWritesNothingForAnUncachedStub(t *testing.T) {
	f := newFixture(t)
	f.entry(t, "tool", f.stub(t, f.stubs, "tool"))
	before, err := os.Stat(f.layout.State())
	if err != nil {
		t.Fatal(err)
	}
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(f.layout.State())
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("an idle pass wrote state.json")
	}
}

func TestPassTreatsAnOpenFileAsRunning(t *testing.T) {
	f := newFixture(t)
	f.cachedEntry(t, "tool", "1h", true, 2*time.Hour)
	// An open file makes the folder's rename fail, as a running exe does.
	held, err := os.Open(f.appFile("tool", "app.exe"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	e := f.get(t, "tool")
	if !e.Cached(f.layout.Home) {
		t.Fatal("an open app was removed")
	}
	if e.LastClosed == nil || !e.LastClosed.Equal(now) {
		t.Fatalf("clock not restarted: %+v", e.LastClosed)
	}
}

// Windows Defender often holds new files for a moment, which isn't an open app.
func TestPassWaitsOutABriefHoldOnTheFolder(t *testing.T) {
	f := newFixture(t)
	f.cachedEntry(t, "tool", "1h", true, 2*time.Hour)
	held, err := os.Open(f.appFile("tool", "app.exe"))
	if err != nil {
		t.Fatal(err)
	}
	released := time.AfterFunc(300*time.Millisecond, func() { held.Close() })
	defer released.Stop()
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	gone(t, f.layout.AppRoot("tool"))
	if e := f.get(t, "tool"); e == nil || e.Path != "" {
		t.Fatalf("%+v", e)
	}
}

func TestPassSkipsAnAppThatIsDownloading(t *testing.T) {
	f := newFixture(t)
	f.cachedEntry(t, "tool", "1h", true, 2*time.Hour)
	os.MkdirAll(f.layout.Tmp(), 0o755)
	held := lockApp(t, f, "tool")
	defer held()
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	if !f.get(t, "tool").Cached(f.layout.Home) {
		t.Fatal("removed during a download")
	}
}

func TestPassFinishesInterruptedDeletions(t *testing.T) {
	f := newFixture(t)
	trash := f.layout.Removing("tool")
	os.MkdirAll(trash, 0o755)
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	gone(t, trash)
}

func TestPassIgnoresUnsafeIDsInState(t *testing.T) {
	f := newFixture(t)
	f.installed(t, "tool", "1.0")
	f.entry(t, `..\apps\tool`)
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	present(t, f.layout.AppRoot("tool"))
}

func TestPassKeepsAppWithAnInvalidTTL(t *testing.T) {
	f := newFixture(t)
	f.cachedEntry(t, "tool", "soon", true, 1000*time.Hour)
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	present(t, f.layout.AppRoot("tool"))
}

func TestPassRelearnsAMovedStubByScanning(t *testing.T) {
	f := newFixture(t)
	t.Setenv("EPHGET_HOME", f.layout.Home)
	scanned := t.TempDir()
	if err := os.WriteFile(f.layout.Config(), []byte(`{"ttl":"7d","keep_prefs":true,"shortcut_dir":`+jsonString(scanned)+`,"start_menu":false}`), 0o666); err != nil {
		t.Fatal(err)
	}
	moved := f.stub(t, scanned, "tool")
	f.cachedEntry(t, "tool", "30d", true, 0)
	f.store.Update(func(st state.State) error {
		st["tool"].Shortcuts = []string{filepath.Join(f.stubs, "was-here.lnk")}
		return nil
	})
	if err := Pass(f.layout, now); err != nil {
		t.Fatal(err)
	}
	e := f.get(t, "tool")
	if e == nil || !e.Cached(f.layout.Home) || len(e.Shortcuts) != 1 || e.Shortcuts[0] != moved {
		t.Fatalf("%+v", e)
	}
}
