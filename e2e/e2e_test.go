// Package e2e drives the real executables through search, add and a real click on
// the stub. The "app" is internal/testapp, never anything from the catalog.
package e2e

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ephget/ephget/internal/index"
	"github.com/Ephget/ephget/internal/shortcut"
	"github.com/Ephget/ephget/internal/state"
)

type env struct {
	t         *testing.T
	bin       string
	home      string
	stubs     string
	logFile   string
	requests  atomic.Int32
	server    *httptest.Server
	zipData   []byte
	appDirRel string
}

func build(t *testing.T, out string, ldflags string, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", out, pkg)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, output)
	}
}

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, bin: t.TempDir(), home: t.TempDir(), stubs: t.TempDir(), logFile: filepath.Join(t.TempDir(), "log.txt")}
	build(t, filepath.Join(e.bin, "ephget.exe"), "-s -w", "github.com/Ephget/ephget/cmd/ephget")
	build(t, filepath.Join(e.bin, "ephgetw.exe"), "-s -w -H=windowsgui", "github.com/Ephget/ephget/cmd/ephgetw")
	appExe := filepath.Join(t.TempDir(), "testapp.exe")
	build(t, appExe, "-s -w -H=windowsgui", "github.com/Ephget/ephget/internal/testapp")

	content, err := os.ReadFile(appExe)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create("testapp-1.0/testapp-1.0.exe")
	f.Write(content)
	zw.Close()
	e.zipData = buf.Bytes()

	e.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.requests.Add(1)
		w.Write(e.zipData)
	}))
	t.Cleanup(e.server.Close)
	hash := sha256.Sum256(e.zipData)
	e.writeIndex(hex.EncodeToString(hash[:]))

	t.Setenv("EPHGET_HOME", e.home)
	t.Setenv("TESTAPP_LOG", e.logFile)
	t.Setenv("TESTAPP_MILLIS", "300")
	return e
}

// writeIndex puts a fresh index in place, so no command goes to the real URL.
func (e *env) writeIndex(hash string) {
	e.t.Helper()
	pkg := index.Package{
		ID: "testapp", Name: "Test App", Bucket: "Extras", Version: "1.0", Description: "A stand-in for a real app.",
		Installers: []index.Installer{{
			Arch: "x64", Type: "zip", URL: e.server.URL + "/testapp.zip", SHA256: hash, Exe: "testapp.exe",
			ExtractDir: "testapp-1.0", Rename: []string{"testapp-*.exe", "testapp.exe"},
		}},
	}
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	json.NewEncoder(w).Encode(map[string]any{"schema": 1, "generated": "now", "packages": []index.Package{pkg}})
	w.Close()
	if err := os.WriteFile(filepath.Join(e.home, "index.json.gz"), buf.Bytes(), 0o666); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) ephget(args ...string) (string, error) {
	cmd := exec.Command(filepath.Join(e.bin, "ephget.exe"), args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (e *env) state() *state.Entry {
	e.t.Helper()
	st, err := state.Store{Path: filepath.Join(e.home, "state.json")}.Read()
	if err != nil {
		e.t.Fatal(err)
	}
	return st["testapp"]
}

// click opens the stub the way Explorer does, through the shell.
func (e *env) click(path string) {
	e.t.Helper()
	if err := exec.Command("cmd", "/c", "start", "", path).Run(); err != nil {
		e.t.Fatalf("click: %v", err)
	}
}

func (e *env) appStarts() int {
	data, _ := os.ReadFile(e.logFile)
	return strings.Count(string(data), "start ")
}

// waitClosed waits until the launcher has recorded a close at or after since.
func (e *env) waitClosed(since time.Time) *state.Entry {
	e.t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if s := e.state(); s != nil && s.LastClosed != nil && !s.LastClosed.Before(since.Truncate(time.Second)) &&
			s.LastLaunched != nil && !s.LastClosed.Before(*s.LastLaunched) {
			return s
		}
		time.Sleep(100 * time.Millisecond)
	}
	data, _ := os.ReadFile(e.logFile)
	e.t.Fatalf("app never recorded as closed; state %+v; log:\n%s", e.state(), data)
	return nil
}

func TestSearchAddClickThenOfflineClick(t *testing.T) {
	e := setup(t)

	out, err := e.ephget("search", "stand-in")
	if err != nil || !strings.Contains(out, "testapp") || !strings.Contains(out, "1.0") {
		t.Fatalf("search: %v\n%s", err, out)
	}

	out, err = e.ephget("add", "testapp", "--ttl", "1h", "--no-keep-prefs", "--dir", e.stubs, "--no-start-menu")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	stub := filepath.Join(e.stubs, "Test App.lnk")
	link, err := shortcut.Read(stub)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(link.Target, filepath.Join(e.bin, "ephgetw.exe")) ||
		link.Arguments != "run testapp --ttl 1h --no-keep-prefs" || link.Description != "ephget:testapp" {
		t.Fatalf("stub: %+v", link)
	}
	if e.requests.Load() != 0 {
		t.Fatal("add downloaded something")
	}
	if s := e.state(); s == nil || s.TTL != "1h" || s.KeepPrefs || s.Cached(e.home) || len(s.Shortcuts) != 1 {
		t.Fatalf("state after add: %+v", s)
	}
	out, err = e.ephget("list")
	if err != nil || !strings.Contains(out, "stub") {
		t.Fatalf("list: %v\n%s", err, out)
	}

	// First click: downloads in its own console, then runs the app.
	first := time.Now()
	e.click(stub)
	s := e.waitClosed(first)
	if e.requests.Load() != 1 || e.appStarts() != 1 {
		t.Fatalf("first click: %d requests, %d starts", e.requests.Load(), e.appStarts())
	}
	if s.Version != "1.0" || s.Path != `apps\testapp\1.0` || !s.Cached(e.home) {
		t.Fatalf("state after first click: %+v", s)
	}
	log, _ := os.ReadFile(e.logFile)
	if !strings.Contains(string(log), " 1.0 ") { // the app's working folder is its own
		t.Fatalf("wrong working directory: %s", log)
	}
	if entries, _ := os.ReadDir(filepath.Join(e.home, "tmp", "testapp")); len(entries) != 0 {
		t.Fatalf("tmp left behind: %v", entries)
	}
	out, err = e.ephget("list")
	if err != nil || !strings.Contains(out, "cached") {
		t.Fatalf("list: %v\n%s", err, out)
	}

	// Second click with the server gone: no download, and the app runs again.
	e.server.Close()
	time.Sleep(1100 * time.Millisecond) // timestamps must be distinguishable
	second := time.Now()
	e.click(stub)
	deadline := time.Now().Add(30 * time.Second)
	for e.appStarts() < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if e.appStarts() != 2 {
		t.Fatalf("offline click: %d starts", e.appStarts())
	}
	e.waitClosed(second)
	if e.requests.Load() != 1 {
		t.Fatalf("offline click made requests: %d", e.requests.Load())
	}
}

func TestRebuildsLostStateFromStubArguments(t *testing.T) {
	e := setup(t)
	if out, err := e.ephget("add", "testapp", "--ttl", "3d", "--no-keep-prefs", "--dir", e.stubs, "--no-start-menu"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	stub := filepath.Join(e.stubs, "Test App.lnk")
	e.click(stub)
	e.waitClosed(time.Now().Add(-time.Minute))

	if err := os.Remove(filepath.Join(e.home, "state.json")); err != nil {
		t.Fatal(err)
	}
	before := e.requests.Load()
	e.click(stub)
	s := e.waitClosed(time.Now().Add(-time.Minute))
	if s.TTL != "3d" || s.KeepPrefs {
		t.Fatalf("rebuilt entry lost the stub's settings: %+v", s)
	}
	if e.requests.Load() != before {
		t.Fatal("rebuilding state downloaded the app again")
	}
}

func TestFetchFailureExitsNonZeroAndLeavesStub(t *testing.T) {
	e := setup(t)
	e.writeIndex(strings.Repeat("0", 64))
	cmd := exec.Command(filepath.Join(e.bin, "ephget.exe"), "fetch", "testapp")
	cmd.Stdin = strings.NewReader("n\n")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("fetch succeeded:\n%s", out)
	}
	if !strings.Contains(string(out), "failed verification") || !strings.Contains(string(out), "Retry?") {
		t.Fatalf("output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(e.home, "apps", "testapp")); err == nil {
		t.Fatal("failed fetch left an app folder")
	}
	if e.appStarts() != 0 {
		t.Fatal("app ran")
	}
}

// The download console is the only thing the user sees during a first launch, so
// the fetch process must get the console's real handles, not NUL.
func TestFetchRunsAttachedToARealConsole(t *testing.T) {
	e := setup(t)
	bin := t.TempDir()
	build(t, filepath.Join(bin, "ephgetw.exe"), "-s -w -H=windowsgui", "github.com/Ephget/ephget/cmd/ephgetw")
	build(t, filepath.Join(bin, "ephget.exe"), "-s -w", "github.com/Ephget/ephget/internal/testapp")
	t.Setenv("TESTAPP_MODE", "console")
	exec.Command(filepath.Join(bin, "ephgetw.exe"), "run", "testapp").Run()
	data, err := os.ReadFile(e.logFile)
	if err != nil {
		t.Fatal(err)
	}
	want := `console stdin=true stdout=true stderr=true args=["fetch" "testapp"]`
	if !strings.Contains(string(data), want) {
		t.Fatalf("want %s in:\n%s", want, data)
	}
}

func TestAddRefusesUnknownAppsAndForeignShortcuts(t *testing.T) {
	e := setup(t)
	if out, err := e.ephget("add", "nope", "--dir", e.stubs, "--no-start-menu"); err == nil {
		t.Fatalf("added an unknown app:\n%s", out)
	}
	if out, err := e.ephget("add", "testapp", "--ttl", "5m", "--dir", e.stubs, "--no-start-menu"); err == nil {
		t.Fatalf("accepted a 5m TTL:\n%s", out)
	}
	// Someone else's shortcut with the same name stays untouched.
	foreign := filepath.Join(e.stubs, "Test App.lnk")
	if err := shortcut.Create(foreign, shortcut.Link{Target: filepath.Join(e.bin, "ephget.exe"), Description: "theirs"}); err != nil {
		t.Fatal(err)
	}
	if out, err := e.ephget("add", "testapp", "--dir", e.stubs, "--no-start-menu"); err == nil || !strings.Contains(out, "already exists") {
		t.Fatalf("%v\n%s", err, out)
	}
	if link, _ := shortcut.Read(foreign); link.Description != "theirs" {
		t.Fatalf("foreign shortcut overwritten: %+v", link)
	}
}

// Moving a stub and clicking it replaces the recorded path, so remove finds it.
func TestClickTracksAMovedStubAndRemoveDeletesIt(t *testing.T) {
	e := setup(t)
	if out, err := e.ephget("add", "testapp", "--dir", e.stubs, "--no-start-menu"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	stub := filepath.Join(e.stubs, "Test App.lnk")
	movedDir := t.TempDir()
	moved := filepath.Join(movedDir, "Renamed.lnk")
	if err := os.Rename(stub, moved); err != nil {
		t.Fatal(err)
	}
	e.click(moved)
	s := e.waitClosed(time.Now().Add(-time.Minute))
	if len(s.Shortcuts) != 1 || !strings.EqualFold(s.Shortcuts[0], moved) {
		t.Fatalf("recorded %q, want only %q", s.Shortcuts, moved)
	}

	out, err := e.ephget("remove", "testapp")
	if err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	if _, err := os.Stat(moved); err == nil {
		t.Error("stub still exists")
	}
	if _, err := os.Stat(filepath.Join(e.home, "apps", "testapp")); err == nil {
		t.Error("app still exists")
	}
	if s := e.state(); s != nil {
		t.Errorf("state entry survived: %+v", s)
	}
	if out, err := e.ephget("remove", "testapp"); err == nil || !strings.Contains(out, "isn't added") {
		t.Errorf("second remove: %v\n%s", err, out)
	}
}

func TestRemoveRefusesARunningApp(t *testing.T) {
	e := setup(t)
	t.Setenv("TESTAPP_MILLIS", "12000") // longer than the rename's retries
	if out, err := e.ephget("add", "testapp", "--dir", e.stubs, "--no-start-menu"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	stub := filepath.Join(e.stubs, "Test App.lnk")
	e.click(stub)
	deadline := time.Now().Add(30 * time.Second)
	for e.appStarts() < 1 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if e.appStarts() != 1 {
		t.Fatal("app never started")
	}

	out, err := e.ephget("remove", "testapp")
	if err == nil || !strings.Contains(out, "is open") {
		t.Fatalf("remove of a running app: %v\n%s", err, out)
	}
	if _, err := os.Stat(stub); err != nil {
		t.Errorf("stub deleted: %v", err)
	}
	if s := e.state(); s == nil || !s.Cached(e.home) {
		t.Errorf("app damaged: %+v", s)
	}

	e.waitClosed(time.Now().Add(-time.Minute))
	if out, err := e.ephget("remove", "testapp"); err != nil {
		t.Fatalf("remove after closing: %v\n%s", err, out)
	}
	if _, err := os.Stat(stub); err == nil {
		t.Error("stub still exists after closing")
	}
}

func (e *env) reap() {
	e.t.Helper()
	if err := exec.Command(filepath.Join(e.bin, "ephgetw.exe"), "reap").Run(); err != nil {
		e.t.Fatalf("reap: %v", err)
	}
}

// age moves the app's last session into the past.
func (e *env) age(by time.Duration) {
	e.t.Helper()
	store := state.Store{Path: filepath.Join(e.home, "state.json")}
	err := store.Update(func(st state.State) error {
		when := time.Now().Add(-by)
		st["testapp"].LastLaunched = &when
		if st["testapp"].LastClosed != nil {
			st["testapp"].LastClosed = &when
		}
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

// The M3 "done when": expiry returns the app to a stub, and the next click brings
// it back, with its settings.
func TestExpiryKeepsTheStubAndSettings(t *testing.T) {
	e := setup(t)
	if out, err := e.ephget("add", "testapp", "--ttl", "1h", "--dir", e.stubs, "--no-start-menu"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	stub := filepath.Join(e.stubs, "Test App.lnk")
	e.click(stub)
	s := e.waitClosed(time.Now().Add(-time.Minute))

	settings := filepath.Join(e.home, s.Path, "settings.ini")
	store := state.Store{Path: filepath.Join(e.home, "state.json")}
	store.Update(func(st state.State) error {
		st["testapp"].Persist = []string{"settings.ini"}
		return nil
	})
	if err := os.WriteFile(settings, []byte("mine"), 0o666); err != nil {
		t.Fatal(err)
	}

	e.reap()
	if !e.state().Cached(e.home) {
		t.Fatal("removed inside its TTL")
	}
	e.age(2 * time.Hour)
	e.reap()
	if _, err := os.Stat(filepath.Join(e.home, "apps", "testapp")); err == nil {
		t.Fatal("not removed after its TTL")
	}
	if _, err := os.Stat(stub); err != nil {
		t.Fatalf("stub removed: %v", err)
	}
	if s := e.state(); s == nil || s.Cached(e.home) {
		t.Fatalf("state after expiry: %+v", s)
	}

	before := e.requests.Load()
	time.Sleep(1100 * time.Millisecond)
	again := time.Now()
	e.click(stub)
	s = e.waitClosed(again)
	if e.requests.Load() != before+1 {
		t.Fatalf("re-download: %d requests, was %d", e.requests.Load(), before)
	}
	if got, err := os.ReadFile(filepath.Join(e.home, s.Path, "settings.ini")); err != nil || string(got) != "mine" {
		t.Fatalf("settings: %q, %v", got, err)
	}
}

func TestDeletingTheStubRemovesTheApp(t *testing.T) {
	e := setup(t)
	if out, err := e.ephget("add", "testapp", "--ttl", "30d", "--dir", e.stubs, "--no-start-menu"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	stub := filepath.Join(e.stubs, "Test App.lnk")
	e.click(stub)
	e.waitClosed(time.Now().Add(-time.Minute))
	if err := os.Remove(stub); err != nil {
		t.Fatal(err)
	}
	e.reap()
	if _, err := os.Stat(filepath.Join(e.home, "apps", "testapp")); err == nil {
		t.Error("app still exists")
	}
	if s := e.state(); s != nil {
		t.Errorf("state entry survived: %+v", s)
	}
}

func TestReapNeverRemovesARunningApp(t *testing.T) {
	e := setup(t)
	t.Setenv("TESTAPP_MILLIS", "8000")
	if out, err := e.ephget("add", "testapp", "--ttl", "1h", "--dir", e.stubs, "--no-start-menu"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	e.click(filepath.Join(e.stubs, "Test App.lnk"))
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if s := e.state(); e.appStarts() == 1 && s != nil && s.LastLaunched != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.age(2 * time.Hour)
	e.reap()
	s := e.state()
	if s == nil || !s.Cached(e.home) {
		t.Fatalf("a running app was removed: %+v", s)
	}
	if s.LastClosed == nil || time.Since(*s.LastClosed) > time.Minute {
		t.Fatalf("clock not restarted: %+v", s.LastClosed)
	}
	// The launcher's own pass after closing must leave it too: the close is recent.
	// The reap above wrote last_closed, so only a later one is the real close.
	final := e.waitClosed(time.Now().Add(2 * time.Second))
	if !final.Cached(e.home) {
		t.Fatalf("removed on close: %+v", final)
	}
}

func TestSetRewritesStubsAndConfigChangesDefaults(t *testing.T) {
	e := setup(t)
	if out, err := e.ephget("add", "testapp", "--dir", e.stubs, "--no-start-menu"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	stub := filepath.Join(e.stubs, "Test App.lnk")
	if out, err := e.ephget("set", "testapp", "--ttl", "2h", "--no-keep-prefs"); err != nil {
		t.Fatalf("set: %v\n%s", err, out)
	}
	link, err := shortcut.Read(stub)
	if err != nil {
		t.Fatal(err)
	}
	if link.Arguments != "run testapp --ttl 2h --no-keep-prefs" || link.Description != "ephget:testapp" ||
		!strings.EqualFold(link.Target, filepath.Join(e.bin, "ephgetw.exe")) {
		t.Fatalf("stub: %+v", link)
	}
	if s := e.state(); s.TTL != "2h" || s.KeepPrefs {
		t.Fatalf("state: %+v", s)
	}
	for _, args := range [][]string{{"set", "testapp"}, {"set", "testapp", "--ttl", "5m"}, {"set", "nope", "--ttl", "1h"}} {
		if out, err := e.ephget(args...); err == nil {
			t.Errorf("%v succeeded:\n%s", args, out)
		}
	}

	folder := t.TempDir()
	out, err := e.ephget("config", "--ttl", "3d", "--no-keep-prefs", "--shortcut-dir", folder, "--no-start-menu")
	if err != nil || !strings.Contains(out, "3d") || !strings.Contains(out, folder) {
		t.Fatalf("config: %v\n%s", err, out)
	}
	if out, err := e.ephget("remove", "testapp"); err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	if out, err := e.ephget("add", "testapp"); err != nil {
		t.Fatalf("add with defaults: %v\n%s", err, out)
	}
	link, err = shortcut.Read(filepath.Join(folder, "Test App.lnk"))
	if err != nil {
		t.Fatal(err)
	}
	if link.Arguments != "run testapp --ttl 3d --no-keep-prefs" {
		t.Fatalf("stub: %+v", link)
	}
	if out, err := e.ephget("config", "--ttl", "5m"); err == nil {
		t.Fatalf("accepted a 5m TTL:\n%s", out)
	}
	out, err = e.ephget("config", "--shortcut-dir", "")
	if err != nil || !strings.Contains(out, "Desktop") {
		t.Fatalf("config reset: %v\n%s", err, out)
	}
}

func TestFirstRunRecordsAndExpiryDeletesTheFootprint(t *testing.T) {
	e := setup(t)
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)
	settings := filepath.Join(appData, "testapp")
	other := filepath.Join(appData, "testapp-other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TESTAPP_SETTINGS", settings)
	if out, err := e.ephget("add", "testapp", "--ttl", "1h", "--no-keep-prefs", "--dir", e.stubs, "--no-start-menu"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	e.click(filepath.Join(e.stubs, "Test App.lnk"))
	s := e.waitClosed(time.Now().Add(-time.Minute))
	// The launcher writes these with the close time, so they're already there.
	if len(s.Footprint) != 1 || s.Footprint[0] != `%APPDATA%\testapp` || !s.FootprintChecked {
		t.Fatalf("footprint %q, checked %t", s.Footprint, s.FootprintChecked)
	}
	out, err := e.ephget("list")
	if err != nil || !strings.Contains(out, `%APPDATA%\testapp`) {
		t.Fatalf("list: %v\n%s", err, out)
	}
	e.age(2 * time.Hour)
	e.reap()
	if _, err := os.Stat(settings); err == nil {
		t.Error("settings survived with keep preferences off")
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another app's folder was deleted: %v", err)
	}
}

func TestListShowsAnOpenApp(t *testing.T) {
	e := setup(t)
	t.Setenv("TESTAPP_MILLIS", "6000")
	if out, err := e.ephget("add", "testapp", "--ttl", "1h", "--dir", e.stubs, "--no-start-menu"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	e.click(filepath.Join(e.stubs, "Test App.lnk"))
	deadline := time.Now().Add(30 * time.Second)
	for e.appStarts() < 1 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	out, err := e.ephget("list")
	if err != nil || !strings.Contains(out, "open") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	e.waitClosed(time.Now().Add(-time.Minute))
}

func TestPinAddsToTheStubAndUnpinRemovesIt(t *testing.T) {
	e := setup(t)
	if out, err := e.ephget("add", "testapp", "--pin", "--dir", e.stubs, "--no-start-menu"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	stub := filepath.Join(e.stubs, "Test App.lnk")
	link, err := shortcut.Read(stub)
	if err != nil || !strings.HasSuffix(link.Arguments, "--pin 1.0") {
		t.Fatalf("stub: %+v, %v", link, err)
	}
	if s := e.state(); s.Pin == nil || s.Pin.Version != "1.0" {
		t.Fatalf("state: %+v", s)
	}
	e.click(stub)
	s := e.waitClosed(time.Now().Add(-time.Minute))
	if s.Version != "1.0" || s.Source == nil {
		t.Fatalf("state: %+v", s)
	}
	out, err := e.ephget("set", "testapp", "--no-pin")
	if err != nil {
		t.Fatalf("set: %v\n%s", err, out)
	}
	link, _ = shortcut.Read(stub)
	if strings.Contains(link.Arguments, "--pin") {
		t.Fatalf("stub: %+v", link)
	}
	if s := e.state(); s.Pin != nil {
		t.Fatalf("still pinned: %+v", s.Pin)
	}
	// Pinning a cached app freezes the copy it has, with no catalog needed.
	out, err = e.ephget("set", "testapp", "--pin")
	if err != nil || !strings.Contains(out, "pinned to 1.0") {
		t.Fatalf("set: %v\n%s", err, out)
	}
}

func TestUninstallRemovesEverythingOnlyAfterConfirming(t *testing.T) {
	e := setup(t)
	if out, err := e.ephget("add", "testapp", "--dir", e.stubs, "--no-start-menu"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	stub := filepath.Join(e.stubs, "Test App.lnk")
	e.click(stub)
	e.waitClosed(time.Now().Add(-time.Minute))

	// The installed layout: the exes in the data folder, next to a file of the user's.
	for _, name := range []string{"ephget.exe", "ephgetw.exe"} {
		data, err := os.ReadFile(filepath.Join(e.bin, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(e.home, name), data, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	uninstall := func(answer string) string {
		t.Helper()
		cmd := exec.Command(filepath.Join(e.home, "ephget.exe"), "uninstall")
		cmd.Stdin = strings.NewReader(answer)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("uninstall: %v\n%s", err, out)
		}
		return string(out)
	}

	if out := uninstall("n\n"); !strings.Contains(out, "Nothing was changed") || !strings.Contains(out, stub) {
		t.Fatalf("declined:\n%s", out)
	}
	if _, err := os.Stat(stub); err != nil {
		t.Fatalf("declining deleted the stub: %v", err)
	}
	uninstall("y\n")
	if _, err := os.Stat(stub); err == nil {
		t.Error("stub survived")
	}
	// The running exe can't delete itself, so a hidden cmd finishes after it exits.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(e.home); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	entries, _ := os.ReadDir(e.home)
	t.Fatalf("data folder survived: %v", entries)
}

func TestUninstallKeepsFilesItDoesNotOwn(t *testing.T) {
	e := setup(t)
	mine := filepath.Join(e.home, "my notes.txt")
	if err := os.WriteFile(mine, []byte("mine"), 0o666); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(e.bin, "ephget.exe"), "uninstall")
	cmd.Stdin = strings.NewReader("y\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("deleted a file it doesn't own: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.bin, "ephget.exe")); err != nil {
		t.Errorf("deleted a copy outside the data folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.home, "index.json.gz")); err == nil {
		t.Error("index survived")
	}
}

// slowServer sends part of the file and waits, as a real download does, until release
// is closed. The test must close it before finishing.
func (e *env) slowServer(release chan struct{}) string {
	e.t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.requests.Add(1)
		w.Header().Set("Content-Length", strconv.Itoa(len(e.zipData)))
		half := len(e.zipData) / 2
		w.Write(e.zipData[:half])
		w.(http.Flusher).Flush()
		select {
		case <-release:
			w.Write(e.zipData[half:])
		case <-r.Context().Done():
		}
	}))
	e.t.Cleanup(server.Close)
	return server.URL
}

func (e *env) useServer(url string) {
	e.t.Helper()
	hash := sha256.Sum256(e.zipData)
	saved := e.server.URL
	e.server.URL = url
	e.writeIndex(hex.EncodeToString(hash[:]))
	e.server.URL = saved
}

// Section 4: a second click during a download waits, and both then launch.
func TestSecondClickDuringADownloadWaits(t *testing.T) {
	e := setup(t)
	release := make(chan struct{})
	e.useServer(e.slowServer(release))
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	if out, err := e.ephget("add", "testapp", "--dir", e.stubs, "--no-start-menu"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	stub := filepath.Join(e.stubs, "Test App.lnk")
	e.click(stub)
	work := filepath.Join(e.home, "tmp", "testapp", "download")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(work); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.click(stub)
	time.Sleep(1500 * time.Millisecond)
	if e.appStarts() != 0 {
		t.Fatal("an app started before its download finished")
	}
	close(release)
	released = true
	for e.appStarts() < 2 && time.Now().Before(deadline.Add(30*time.Second)) {
		time.Sleep(50 * time.Millisecond)
	}
	if e.appStarts() != 2 || e.requests.Load() != 1 {
		t.Fatalf("%d starts from %d downloads", e.appStarts(), e.requests.Load())
	}
	e.waitClosed(time.Now().Add(-time.Minute))
}

// Section 4: an app that leaves the catalog keeps working until it expires, and list
// says so.
func TestAppRemovedFromTheCatalog(t *testing.T) {
	e := setup(t)
	if out, err := e.ephget("add", "testapp", "--ttl", "1h", "--dir", e.stubs, "--no-start-menu"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	stub := filepath.Join(e.stubs, "Test App.lnk")
	e.click(stub)
	e.waitClosed(time.Now().Add(-time.Minute))

	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	json.NewEncoder(w).Encode(map[string]any{"schema": 1, "generated": "now", "packages": []index.Package{}})
	w.Close()
	if err := os.WriteFile(filepath.Join(e.home, "index.json.gz"), buf.Bytes(), 0o666); err != nil {
		t.Fatal(err)
	}
	out, err := e.ephget("list")
	if err != nil || !strings.Contains(out, "not in catalog") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	starts := e.appStarts()
	time.Sleep(1100 * time.Millisecond)
	again := time.Now()
	e.click(stub)
	e.waitClosed(again)
	if e.appStarts() != starts+1 {
		t.Fatal("the cached copy didn't run")
	}

	e.age(2 * time.Hour)
	e.reap()
	cmd := exec.Command(filepath.Join(e.bin, "ephget.exe"), "fetch", "testapp")
	cmd.Stdin = strings.NewReader("n\n")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "no longer in the catalog") {
		t.Fatalf("fetch after expiry: %v\n%s", err, output)
	}
}

// Scripts and logs read piped output, so it must stay free of colour codes and marks.
func TestPipedOutputIsPlain(t *testing.T) {
	e := setup(t)
	commands := [][]string{{"help"}, {"search", "stand-in"}, {"add", "testapp", "--dir", e.stubs, "--no-start-menu"},
		{"list"}, {"config"}, {"set", "testapp", "--ttl", "2h"}, {"nonsense"}}
	for _, args := range commands {
		out, _ := e.ephget(args...)
		if out == "" {
			t.Errorf("%v printed nothing", args)
		}
		if strings.ContainsAny(out, "\x1b\u2713\u221a\u2588") {
			t.Errorf("%v isn't plain: %q", args, out)
		}
	}
	if out, err := e.ephget("add", "--help"); err != nil || !strings.Contains(out, "Commands") {
		t.Errorf("add --help: %v\n%s", err, out)
	}
}

func TestEphgetwHasNoNetworkCode(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/Ephget/ephget/cmd/ephgetw").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep == "net" || dep == "net/http" || strings.HasSuffix(dep, "/internal/fetch") {
			t.Errorf("ephgetw depends on %s", dep)
		}
	}
}

func TestExecutablesWithinSizeLimit(t *testing.T) {
	e := setup(t)
	const limit = 10 << 20
	for _, name := range []string{"ephget.exe", "ephgetw.exe"} {
		info, err := os.Stat(filepath.Join(e.bin, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %.1f MB", name, float64(info.Size())/(1<<20))
		if info.Size() > limit {
			t.Errorf("%s is %d bytes", name, info.Size())
		}
	}
}
