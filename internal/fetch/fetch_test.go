package fetch

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dryd33n/ephget/internal/index"
	"github.com/Dryd33n/ephget/internal/paths"
	"github.com/Dryd33n/ephget/internal/shortcut"
	"github.com/Dryd33n/ephget/internal/state"
)

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(content))
	}
	w.Close()
	return buf.Bytes()
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func gzipIndex(t *testing.T, schema int, pkgs ...index.Package) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	json.NewEncoder(w).Encode(map[string]any{"schema": schema, "generated": "now", "packages": pkgs})
	w.Close()
	return buf.Bytes()
}

type fixture struct {
	c        *Client
	srv      *httptest.Server
	files    map[string][]byte
	requests atomic.Int32
}

// newFixture serves files, and starts with a fresh local index holding pkgs.
func newFixture(t *testing.T, pkgs ...index.Package) *fixture {
	t.Helper()
	f := &fixture{files: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		data, ok := f.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	t.Cleanup(f.srv.Close)
	layout := paths.Layout{Home: t.TempDir()}
	f.c = &Client{Layout: layout, HTTP: f.srv.Client(), IndexURL: f.srv.URL + "/index", Machine: "x64", Out: io.Discard, Warn: io.Discard}
	for i := range pkgs {
		for j := range pkgs[i].Installers {
			pkgs[i].Installers[j].URL = f.srv.URL + pkgs[i].Installers[j].URL
		}
	}
	if err := os.WriteFile(layout.Index(), gzipIndex(t, 1, pkgs...), 0o666); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) leftovers(t *testing.T, id string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.c.Layout.Tmp(), id)); err == nil {
		t.Errorf("tmp\\%s left behind", id)
	}
}

func pkg(id string, in index.Installer) index.Package {
	in.Arch = "x64"
	return index.Package{ID: id, Name: id + " app", Bucket: "Extras", Version: "1.2", Persist: []string{"data"}, Installers: []index.Installer{in}}
}

func TestFetchZipWithExtractDirAndRename(t *testing.T) {
	data := zipOf(t, map[string]string{"app-1.2/tool-1.2.exe": "EXE", "app-1.2/lib/x.dll": "DLL", "stray.txt": "no"})
	f := newFixture(t, pkg("tool", index.Installer{
		Type: "zip", URL: "/tool.zip#/ignored.zip", SHA256: sum(data), Exe: "tool.exe",
		ExtractDir: `app-1.2`, Rename: []string{"TOOL-*.exe", "tool.exe"},
	}))
	f.files["/tool.zip"] = data
	if err := f.c.Fetch(context.Background(), "tool"); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(f.c.Layout.Home, "apps", "tool", "1.2")
	for path, want := range map[string]string{"tool.exe": "EXE", `lib\x.dll`: "DLL"} {
		if got, err := os.ReadFile(filepath.Join(root, path)); err != nil || string(got) != want {
			t.Errorf("%s: %q, %v", path, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "stray.txt")); err == nil {
		t.Error("file outside extract_dir was installed")
	}
	st, err := state.Store{Path: f.c.Layout.State()}.Read()
	if err != nil {
		t.Fatal(err)
	}
	e := st["tool"]
	if e == nil || e.Version != "1.2" || e.Path != `apps\tool\1.2` || e.Exe != "tool.exe" || e.Bucket != "Extras" || e.TTL != "7d" || !e.KeepPrefs || e.Persist[0] != "data" {
		t.Fatalf("%+v", e)
	}
	f.leftovers(t, "tool")

	// Now cached: no more requests.
	before := f.requests.Load()
	if err := f.c.Fetch(context.Background(), "tool"); err != nil || f.requests.Load() != before {
		t.Fatalf("second fetch: %v, %d requests", err, f.requests.Load()-before)
	}
}

func TestRefererFor(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.nirsoft.net/toolsdownload/mspass.zip":        "https://www.nirsoft.net/toolsdownload/",
		"https://host.example/a%20b/tool.zip?x=1#/dl.zip":         "https://host.example/a%20b/",
		"http://user:secret@host.example:8080/tool.zip":           "http://host.example:8080/",
		"https://downloads.sourceforge.net/project/tool/tool.zip": "",
		"https://sourceforge.net/projects/tool/tool.zip":          "",
		"https://portableapps.com/downloads/tool.zip":             "",
		"https://notsourceforge.net/tool.zip":                     "https://notsourceforge.net/",
		"https://host.example":                                    "",
	} {
		if got := refererFor(in); got != want {
			t.Errorf("refererFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFetchSendsReferer(t *testing.T) {
	data := []byte("EXE")
	var referer atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		referer.Store(r.Referer())
		w.Write(data)
	}))
	defer srv.Close()
	f := newFixture(t, pkg("tool", index.Installer{Type: "exe", URL: "/x", SHA256: sum(data), Exe: "tool.exe"}))
	p := pkg("tool", index.Installer{Type: "exe", URL: srv.URL + "/files/tool.exe", SHA256: sum(data), Exe: "tool.exe"})
	if err := os.WriteFile(f.c.Layout.Index(), gzipIndex(t, 1, p), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Fetch(context.Background(), "tool"); err != nil {
		t.Fatal(err)
	}
	if got := referer.Load(); got != srv.URL+"/files/" {
		t.Fatalf("Referer %v", got)
	}
}

func TestFetchRestoresSavedSettings(t *testing.T) {
	data := zipOf(t, map[string]string{"tool.exe": "EXE", "data/default.txt": "shipped", "settings.ini": "shipped"})
	in := index.Installer{Type: "zip", URL: "/tool.zip", SHA256: sum(data), Exe: "tool.exe"}
	f := newFixture(t, pkg("tool", in))
	f.files["/tool.zip"] = data
	saved := f.c.Layout.Prefs("tool")
	if err := os.MkdirAll(filepath.Join(saved, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(saved, "data", "mine.txt"), []byte("mine"), 0o666)
	os.WriteFile(filepath.Join(saved, "settings.ini"), []byte("mine"), 0o666)
	store := state.Store{Path: f.c.Layout.State()}
	store.Update(func(st state.State) error {
		// settings.ini was on the old version's list; pkg only lists "data".
		st["tool"] = &state.Entry{TTL: "7d", Persist: []string{"settings.ini"}}
		return nil
	})
	if err := f.c.Fetch(context.Background(), "tool"); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(f.c.Layout.Home, paths.AppRel("tool", "1.2"))
	if got, _ := os.ReadFile(filepath.Join(app, "data", "mine.txt")); string(got) != "mine" {
		t.Errorf("saved folder not restored: %q", got)
	}
	if _, err := os.Stat(filepath.Join(app, "data", "default.txt")); err == nil {
		t.Error("the saved folder should replace the shipped one")
	}
	if got, _ := os.ReadFile(filepath.Join(app, "settings.ini")); string(got) != "mine" {
		t.Errorf("settings.ini not restored: %q", got)
	}
	if _, err := os.Stat(saved); err == nil {
		t.Error("prefs folder left behind")
	}
}

// A failed download must not lose settings saved from the last copy.
func TestFailedFetchKeepsSavedSettings(t *testing.T) {
	data := zipOf(t, map[string]string{"tool.exe": "EXE"})
	f := newFixture(t, pkg("tool", index.Installer{Type: "zip", URL: "/tool.zip", SHA256: strings.Repeat("0", 64), Exe: "tool.exe"}))
	f.files["/tool.zip"] = data
	saved := filepath.Join(f.c.Layout.Prefs("tool"), "data")
	if err := os.MkdirAll(saved, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Fetch(context.Background(), "tool"); err == nil {
		t.Fatal("fetch succeeded")
	}
	if _, err := os.Stat(saved); err != nil {
		t.Errorf("settings lost: %v", err)
	}
}

// A reaped app's old times must not make the new copy look overdue.
func TestFetchRestartsTheAppsClock(t *testing.T) {
	data := []byte("EXE")
	f := newFixture(t, pkg("tool", index.Installer{Type: "exe", URL: "/tool.exe", SHA256: sum(data), Exe: "tool.exe"}))
	f.files["/tool.exe"] = data
	store := state.Store{Path: f.c.Layout.State()}
	old := time.Now().Add(-1000 * time.Hour)
	store.Update(func(st state.State) error {
		st["tool"] = &state.Entry{TTL: "1h", LastClosed: &old, LastLaunched: &old}
		return nil
	})
	if err := f.c.Fetch(context.Background(), "tool"); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Read()
	if st["tool"].LastClosed != nil || st["tool"].LastLaunched != nil {
		t.Fatalf("%+v", st["tool"])
	}
}

func TestFetchGivesStubsTheAppsIcon(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(os.Getenv("WINDIR"), "explorer.exe")) // only read, as bytes
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, pkg("tool", index.Installer{Type: "exe", URL: "/tool.exe", SHA256: sum(data), Exe: "tool.exe"}))
	f.files["/tool.exe"] = data
	stub := filepath.Join(t.TempDir(), "Tool.lnk")
	if err := shortcut.Create(stub, shortcut.Link{Target: os.Args[0], Description: shortcut.Marker("tool")}); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(t.TempDir(), "Theirs.lnk")
	if err := shortcut.Create(foreign, shortcut.Link{Target: os.Args[0], Description: "theirs"}); err != nil {
		t.Fatal(err)
	}
	store := state.Store{Path: f.c.Layout.State()}
	store.Update(func(st state.State) error {
		st["tool"] = &state.Entry{TTL: "7d", Shortcuts: []string{stub, foreign}}
		return nil
	})
	if err := f.c.Fetch(context.Background(), "tool"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.c.Layout.Icon("tool")); err != nil {
		t.Fatal(err)
	}
	link, err := shortcut.Read(stub)
	if err != nil || !strings.EqualFold(link.Icon, f.c.Layout.Icon("tool")) {
		t.Fatalf("stub %+v, %v", link, err)
	}
	if link, _ := shortcut.Read(foreign); link.Icon != "" {
		t.Fatalf("someone else's shortcut changed: %+v", link)
	}
}

func TestPinnedAppInstallsItsStoredVersion(t *testing.T) {
	old := zipOf(t, map[string]string{"tool.exe": "OLD"})
	latest := zipOf(t, map[string]string{"tool.exe": "NEW"})
	f := newFixture(t, pkg("tool", index.Installer{Type: "zip", URL: "/new.zip", SHA256: sum(latest), Exe: "tool.exe"}))
	f.files["/old.zip"], f.files["/new.zip"] = old, latest
	store := state.Store{Path: f.c.Layout.State()}
	store.Update(func(st state.State) error {
		st["tool"] = &state.Entry{TTL: "7d", Pin: &state.Pin{Version: "1.0", URL: f.srv.URL + "/old.zip", SHA256: sum(old), Type: "zip", Exe: "tool.exe"}}
		return nil
	})
	if err := f.c.Fetch(context.Background(), "tool"); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Read()
	if e := st["tool"]; e.Version != "1.0" || e.Pin == nil || e.Source == nil || e.Source.Version != "1.0" {
		t.Fatalf("%+v", e)
	}
	got, _ := os.ReadFile(filepath.Join(f.c.Layout.Home, paths.AppRel("tool", "1.0"), "tool.exe"))
	if string(got) != "OLD" {
		t.Fatalf("installed %q", got)
	}
}

func TestPinnedDownloadThatIsGoneIsReportedAsSuch(t *testing.T) {
	f := newFixture(t)
	store := state.Store{Path: f.c.Layout.State()}
	store.Update(func(st state.State) error {
		st["tool"] = &state.Entry{TTL: "7d", Pin: &state.Pin{Version: "1.0", URL: f.srv.URL + "/gone.zip", SHA256: strings.Repeat("a", 64), Type: "zip", Exe: "tool.exe"}}
		return nil
	})
	var unavailable *PinUnavailableError
	if err := f.c.Fetch(context.Background(), "tool"); !errors.As(err, &unavailable) {
		t.Fatalf("got %v", err)
	}
}

// With state lost, a stub's pin can only be met if the index still has that version.
func TestStubPinWithoutState(t *testing.T) {
	data := zipOf(t, map[string]string{"tool.exe": "EXE"})
	f := newFixture(t, pkg("tool", index.Installer{Type: "zip", URL: "/tool.zip", SHA256: sum(data), Exe: "tool.exe"}))
	f.files["/tool.zip"] = data
	var unavailable *PinUnavailableError
	if err := f.c.FetchPinned(context.Background(), "tool", "0.9"); !errors.As(err, &unavailable) {
		t.Fatalf("old version: got %v", err)
	}
	if err := f.c.FetchPinned(context.Background(), "tool", "1.2"); err != nil {
		t.Fatal(err)
	}
	st, _ := state.Store{Path: f.c.Layout.State()}.Read()
	if p := st["tool"].Pin; p == nil || p.Version != "1.2" {
		t.Fatalf("pin %+v", p)
	}
}

func TestFetchLoneExeSavedUnderExeName(t *testing.T) {
	data := []byte("MZ exe")
	f := newFixture(t, pkg("bw", index.Installer{
		Type: "exe", URL: "/Bitwarden-Portable-9.exe", SHA256: sum(data), Exe: "Bitwarden.exe",
		Rename: []string{"Bitwarden-Portable-*.exe", "Bitwarden.exe"},
	}))
	f.files["/Bitwarden-Portable-9.exe"] = data
	if err := f.c.Fetch(context.Background(), "bw"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(f.c.Layout.Home, "apps", "bw", "1.2", "Bitwarden.exe")); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("%q, %v", got, err)
	}
}

func TestFetchExeInSubfolder(t *testing.T) {
	data := zipOf(t, map[string]string{"bin/a.exe": "EXE"})
	f := newFixture(t, pkg("sub", index.Installer{Type: "zip", URL: "/a.zip", SHA256: strings.ToUpper(sum(data)), Exe: `bin\a.exe`}))
	f.files["/a.zip"] = data
	if err := f.c.Fetch(context.Background(), "sub"); err != nil {
		t.Fatal(err)
	}
}

func TestFetchFailuresLeaveAStub(t *testing.T) {
	good := zipOf(t, map[string]string{"a.exe": "EXE"})
	cases := []struct {
		name, wantErr string
		in            index.Installer
	}{
		{"hash mismatch", "failed verification", index.Installer{Type: "zip", URL: "/a.zip", SHA256: strings.Repeat("0", 64), Exe: "a.exe"}},
		{"exe missing", "missing after unpacking", index.Installer{Type: "zip", URL: "/a.zip", SHA256: sum(good), Exe: "b.exe"}},
		{"extract_dir missing", "no folder", index.Installer{Type: "zip", URL: "/a.zip", SHA256: sum(good), Exe: "a.exe", ExtractDir: "x"}},
		{"404", "404", index.Installer{Type: "zip", URL: "/gone.zip", SHA256: sum(good), Exe: "a.exe"}},
		{"wrong type", "open 7z", index.Installer{Type: "7z", URL: "/a.zip", SHA256: sum(good), Exe: "a.exe"}},
		{"unsafe exe", "unsafe", index.Installer{Type: "zip", URL: "/a.zip", SHA256: sum(good), Exe: `..\a.exe`}},
		{"unsafe extract_dir", "unsafe", index.Installer{Type: "zip", URL: "/a.zip", SHA256: sum(good), Exe: "a.exe", ExtractDir: `..\..`}},
		{"unsafe rename", "unsafe", index.Installer{Type: "zip", URL: "/a.zip", SHA256: sum(good), Exe: "a.exe", Rename: []string{"a*", `..\a.exe`}}},
		{"bad hash", "unsafe", index.Installer{Type: "zip", URL: "/a.zip", SHA256: "abc", Exe: "a.exe"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, pkg("app", c.in))
			f.files["/a.zip"] = good
			err := f.c.Fetch(context.Background(), "app")
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want %q", err, c.wantErr)
			}
			if _, err := os.Stat(filepath.Join(f.c.Layout.Home, "apps", "app")); err == nil {
				t.Error("failed install left an app folder")
			}
			st, _ := state.Store{Path: f.c.Layout.State()}.Read()
			if st["app"].Cached(f.c.Layout.Home) {
				t.Error("failed install recorded as cached")
			}
			f.leftovers(t, "app")
		})
	}
}

func TestFetchRejectsBadIDAndUnknownApp(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{`..\x`, "a/b", ""} {
		if err := f.c.Fetch(context.Background(), id); err == nil || !strings.Contains(err.Error(), "not a valid") {
			t.Errorf("%q: %v", id, err)
		}
	}
	if err := f.c.Fetch(context.Background(), "gone"); err == nil || !strings.Contains(err.Error(), "no longer in the catalog") {
		t.Error(err)
	}
}

func TestFetchCancelCleansUp(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	data := []byte("x")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.Write(data)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	p := pkg("slow", index.Installer{Type: "zip", URL: srv.URL + "/a.zip", SHA256: strings.Repeat("0", 64), Exe: "a.exe"})
	os.WriteFile(f.c.Layout.Index(), gzipIndex(t, 1, p), 0o666)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- f.c.Fetch(ctx, "slow") }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel didn't stop the download")
	}
	f.leftovers(t, "slow")
}

func TestStaleTmpFromKilledRunIsCleared(t *testing.T) {
	data := zipOf(t, map[string]string{"a.exe": "EXE"})
	f := newFixture(t, pkg("app", index.Installer{Type: "zip", URL: "/a.zip", SHA256: sum(data), Exe: "a.exe"}))
	f.files["/a.zip"] = data
	stale := filepath.Join(f.c.Layout.Tmp(), "app", "files", "junk.txt")
	os.MkdirAll(filepath.Dir(stale), 0o755)
	os.WriteFile(stale, []byte("junk"), 0o666)
	if err := f.c.Fetch(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.c.Layout.Home, "apps", "app", "1.2", "junk.txt")); err == nil {
		t.Fatal("stale file was installed")
	}
	f.leftovers(t, "app")
}

func TestLostStateAdoptsExistingCopy(t *testing.T) {
	f := newFixture(t, pkg("app", index.Installer{Type: "zip", URL: "/a.zip", SHA256: strings.Repeat("0", 64), Exe: "a.exe"}))
	exe := filepath.Join(f.c.Layout.Home, "apps", "app", "1.2", "a.exe")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, []byte("EXE"), 0o666)
	if err := f.c.Fetch(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	st, _ := state.Store{Path: f.c.Layout.State()}.Read()
	if !st["app"].Cached(f.c.Layout.Home) || f.requests.Load() != 0 {
		t.Fatalf("%+v, %d requests", st["app"], f.requests.Load())
	}
}

// etagServer follows the real URL's shape: two redirects, then an ETag host.
func etagServer(t *testing.T, body []byte, etag string, sawHeader *atomic.Int32, downloads *atomic.Int32) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/index", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/hop", http.StatusFound) })
	mux.HandleFunc("/hop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/final", http.StatusFound) })
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		if inm := r.Header.Get("If-None-Match"); inm != "" {
			sawHeader.Add(1)
			if inm == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		downloads.Add(1)
		w.Header().Set("ETag", etag)
		w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestRefreshETagAcrossRedirects(t *testing.T) {
	var saw, downloads atomic.Int32
	body := gzipIndex(t, 1, index.Package{ID: "one"})
	srv := etagServer(t, body, `"v1"`, &saw, &downloads)
	layout := paths.Layout{Home: t.TempDir()}
	c := &Client{Layout: layout, HTTP: srv.Client(), IndexURL: srv.URL + "/index", Machine: "x64", Out: io.Discard, Warn: io.Discard}

	idx, err := c.LoadIndex(context.Background())
	if err != nil || idx.Find("one") == nil || downloads.Load() != 1 {
		t.Fatalf("first load: %v, %v, %d", idx, err, downloads.Load())
	}
	// Fresh: no request at all.
	if _, err := c.LoadIndex(context.Background()); err != nil || downloads.Load() != 1 || saw.Load() != 0 {
		t.Fatalf("fresh load: %v, %d, %d", err, downloads.Load(), saw.Load())
	}
	// Stale: a 304, no download, and the copy counts as fresh again.
	old := time.Now().Add(-25 * time.Hour)
	os.Chtimes(layout.Index(), old, old)
	if _, err := c.LoadIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if saw.Load() != 1 || downloads.Load() != 1 {
		t.Fatalf("stale load: saw %d, downloads %d", saw.Load(), downloads.Load())
	}
	if info, _ := os.Stat(layout.Index()); time.Since(info.ModTime()) > time.Minute {
		t.Fatal("304 didn't refresh the copy's age")
	}
}

func TestRefreshFailureUsesStaleCopyAndWarns(t *testing.T) {
	var warn bytes.Buffer
	layout := paths.Layout{Home: t.TempDir()}
	os.WriteFile(layout.Index(), gzipIndex(t, 1, index.Package{ID: "old"}), 0o666)
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(layout.Index(), old, old)
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c := &Client{Layout: layout, HTTP: srv.Client(), IndexURL: srv.URL, Machine: "x64", Out: io.Discard, Warn: &warn}
	idx, err := c.LoadIndex(context.Background())
	if err != nil || idx.Find("old") == nil || !strings.Contains(warn.String(), "old copy") {
		t.Fatalf("%v, %v, %q", idx, err, warn.String())
	}
}

func TestRefreshRefusesNewSchemaAndKeepsCopy(t *testing.T) {
	layout := paths.Layout{Home: t.TempDir()}
	os.WriteFile(layout.Index(), gzipIndex(t, 1, index.Package{ID: "old"}), 0o666)
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(layout.Index(), old, old)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(gzipIndex(t, 2)) }))
	defer srv.Close()
	var warn bytes.Buffer
	c := &Client{Layout: layout, HTTP: srv.Client(), IndexURL: srv.URL, Machine: "x64", Out: io.Discard, Warn: &warn}
	idx, err := c.LoadIndex(context.Background())
	if err != nil || idx.Find("old") == nil || !strings.Contains(warn.String(), "format 2") {
		t.Fatalf("%v, %v, %q", idx, err, warn.String())
	}
}

func TestNoIndexAndNoNetworkFails(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c := &Client{Layout: paths.Layout{Home: t.TempDir()}, HTTP: srv.Client(), IndexURL: srv.URL, Machine: "x64", Out: io.Discard, Warn: io.Discard}
	if _, err := c.LoadIndex(context.Background()); err == nil {
		t.Fatal("no error")
	}
}
