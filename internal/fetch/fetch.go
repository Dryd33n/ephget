// Package fetch is ephget's only network code: it refreshes the index and
// downloads, verifies and installs apps. ephgetw must never import it.
package fetch

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/Dryd33n/ephget/internal/config"
	"github.com/Dryd33n/ephget/internal/extract"
	"github.com/Dryd33n/ephget/internal/fsutil"
	"github.com/Dryd33n/ephget/internal/icon"
	"github.com/Dryd33n/ephget/internal/index"
	"github.com/Dryd33n/ephget/internal/lockfile"
	"github.com/Dryd33n/ephget/internal/paths"
	"github.com/Dryd33n/ephget/internal/shortcut"
	"github.com/Dryd33n/ephget/internal/state"
	"github.com/Dryd33n/ephget/internal/ui"
)

// IndexURL is where the catalog is published.
//
// It names its release by tag, not releases/latest, which will be ephget's own.
const IndexURL = "https://github.com/Dryd33n/ephget/releases/download/index/index.json.gz"

const userAgent = "ephget/0.1"

// A download with no data for this long has stalled.
const stallTimeout = 60 * time.Second

var sha256Hex = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// Client does the network work for one command.
type Client struct {
	Layout   paths.Layout
	HTTP     *http.Client
	IndexURL string
	Machine  string
	Out      io.Writer // progress
	Warn     io.Writer // warnings that don't stop the command
}

// New returns a Client for this machine.
func New(layout paths.Layout, out, warn io.Writer) (*Client, error) {
	machine, err := nativeMachine()
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The index is itself a .gz file and must be saved as sent.
	transport.DisableCompression = true
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &Client{
		Layout:   layout,
		HTTP:     &http.Client{Transport: transport},
		IndexURL: IndexURL,
		Machine:  machine,
		Out:      out,
		Warn:     warn,
	}, nil
}

func nativeMachine() (string, error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	process, _ := syscall.GetCurrentProcess()
	var processMachine, native uint16
	ok, _, err := kernel32.NewProc("IsWow64Process2").Call(uintptr(process),
		uintptr(unsafe.Pointer(&processMachine)), uintptr(unsafe.Pointer(&native)))
	if ok == 0 {
		return "", fmt.Errorf("detect machine type: %w", err)
	}
	switch native {
	case 0x8664:
		return "x64", nil
	case 0xAA64:
		return "arm64", nil
	}
	return "", fmt.Errorf("unsupported machine type %#x", native)
}

// Fetch makes sure the app is cached. Another fetch of the same app finishes
// first, and then this one has nothing left to do.
func (c *Client) Fetch(ctx context.Context, id string) error {
	return c.FetchPinned(ctx, id, "")
}

// PinUnavailableError means a pinned version can no longer be downloaded.
type PinUnavailableError struct{ ID, Version string }

func (e *PinUnavailableError) Error() string {
	return fmt.Sprintf("%s %s, the version it is pinned to, can no longer be downloaded", e.ID, e.Version)
}

type statusError struct {
	code   int
	status string
}

func (e *statusError) Error() string {
	return "download failed: the server answered " + e.status
}

// FetchPinned is Fetch for a stub that names a pinned version. If state has lost the
// pin, the version can only still come from the index, which lists latest versions.
func (c *Client) FetchPinned(ctx context.Context, id, pinVersion string) error {
	if !paths.SafeName(id) {
		return fmt.Errorf("%q is not a valid app ID", id)
	}
	if err := os.MkdirAll(c.Layout.Tmp(), 0o755); err != nil {
		return err
	}
	lock, err := lockfile.Acquire(c.Layout.FetchLock(id))
	if err != nil {
		return err
	}
	defer lock.Release()

	store := state.Store{Path: c.Layout.State()}
	st, err := store.Read()
	if err != nil {
		return err
	}
	if st[id].Cached(c.Layout.Home) {
		return nil
	}

	pkg, inst, err := c.choose(ctx, id, st[id], pinVersion)
	if err != nil {
		return err
	}
	pinned := pinVersion != "" || (st[id] != nil && st[id].Pin != nil)
	if err := validate(pkg, inst); err != nil {
		return err
	}

	// The lock is held, so anything left here is from a run that was killed.
	work := filepath.Join(c.Layout.Tmp(), pkg.ID)
	if err := fsutil.RemoveAll(work); err != nil {
		return err
	}
	defer fsutil.RemoveAll(work)

	rel := paths.AppRel(pkg.ID, pkg.Version)
	dest := filepath.Join(c.Layout.Home, rel)
	if info, err := os.Stat(filepath.Join(dest, inst.Exe)); err == nil && info.Mode().IsRegular() {
		// Its state entry was lost but the app is still here. Installs are renamed
		// into place whole, so this copy is complete.
		fmt.Fprintf(c.Out, "%s %s is already downloaded.\n", pkg.Name, pkg.Version)
		if err := restorePrefs(c.Layout, pkg, st[id], dest); err != nil {
			return err
		}
		return c.finish(store, pkg, inst, dest, rel, pinVersion != "")
	}

	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "Downloading %s %s\n%s\n", ui.Out.Bold(pkg.Name), pkg.Version, ui.Out.Dim(inst.URL))
	file := filepath.Join(work, "download")
	if err := c.download(ctx, inst, file); err != nil {
		var status *statusError
		if pinned && errors.As(err, &status) && (status.code == http.StatusNotFound || status.code == http.StatusGone) {
			return &PinUnavailableError{pkg.ID, pkg.Version}
		}
		return err
	}
	fmt.Fprintln(c.Out, "Verified SHA256. Installing...")
	root, err := unpack(ctx, inst, file, filepath.Join(work, "files"))
	if err != nil {
		return err
	}
	if err := fsutil.ClearMarkOfTheWeb(root); err != nil {
		return err
	}
	if err := fsutil.RemoveAll(dest); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := fsutil.Rename(root, dest); err != nil {
		return err
	}
	if err := restorePrefs(c.Layout, pkg, st[id], dest); err != nil {
		return err
	}
	return c.finish(store, pkg, inst, dest, rel, pinVersion != "")
}

// choose decides what to install: the pinned version in state, or else the index's.
func (c *Client) choose(ctx context.Context, id string, e *state.Entry, pinVersion string) (*index.Package, *index.Installer, error) {
	if e != nil && e.Pin != nil && (pinVersion == "" || pinVersion == e.Pin.Version) {
		p := e.Pin
		inst := index.Installer{Type: p.Type, URL: p.URL, SHA256: p.SHA256, Exe: p.Exe, ExtractDir: p.ExtractDir, Rename: p.Rename}
		name := e.Name
		if name == "" {
			name = id
		}
		pkg := &index.Package{ID: id, Name: name, Bucket: e.Bucket, Version: p.Version, Persist: e.Persist, Installers: []index.Installer{inst}}
		return pkg, &pkg.Installers[0], nil
	}
	idx, err := c.LoadIndex(ctx)
	if err != nil {
		return nil, nil, err
	}
	pkg := idx.Find(id)
	if pinVersion != "" && (pkg == nil || pkg.Version != pinVersion) {
		return nil, nil, &PinUnavailableError{id, pinVersion}
	}
	if pkg == nil {
		return nil, nil, fmt.Errorf("%s is no longer in the catalog, so it can't be downloaded", id)
	}
	inst, err := pkg.SelectInstaller(c.Machine)
	if err != nil {
		return nil, nil, err
	}
	return pkg, inst, nil
}

func pinOf(version string, inst *index.Installer) *state.Pin {
	return &state.Pin{Version: version, URL: inst.URL, SHA256: inst.SHA256, Type: inst.Type, Exe: inst.Exe,
		ExtractDir: inst.ExtractDir, Rename: inst.Rename}
}

// ResolvePin returns what `--pin` should freeze: the cached copy, or else the version
// that would be downloaded next, which pkg (from the index) gives.
func ResolvePin(home string, e *state.Entry, pkg *index.Package, machine string) (*state.Pin, error) {
	if e.Cached(home) && e.Source != nil {
		frozen := *e.Source
		return &frozen, nil
	}
	if pkg == nil {
		return nil, errors.New("the app is no longer in the catalog, so it can't be pinned")
	}
	if e.Cached(home) && e.Version != pkg.Version {
		return nil, fmt.Errorf("the cached version %s is no longer in the catalog, so it can't be pinned until the app is downloaded again", e.Version)
	}
	inst, err := pkg.SelectInstaller(machine)
	if err != nil {
		return nil, err
	}
	return pinOf(pkg.Version, inst), nil
}

// finish records the install and gives the app's stubs its icon. A missing or broken
// icon isn't worth failing a working app over, so the stub just keeps its old one.
func (c *Client) finish(store state.Store, pkg *index.Package, inst *index.Installer, dest, rel string, pin bool) error {
	if err := record(store, c.Layout, pkg, inst, rel, pin); err != nil {
		return err
	}
	file := c.Layout.Icon(pkg.ID)
	if icon.Extract(filepath.Join(dest, inst.Exe), file) != nil {
		return nil
	}
	if st, err := store.Read(); err == nil && st[pkg.ID] != nil {
		for _, path := range st[pkg.ID].Shortcuts {
			if link, err := shortcut.Read(path); err == nil && shortcut.IsStubOf(link, pkg.ID) {
				shortcut.SetIcon(path, file)
			}
		}
	}
	return nil
}

// validate rechecks what the indexer promises, since the index is unsigned and
// its values become file paths on this machine.
func validate(pkg *index.Package, inst *index.Installer) error {
	bad := func(what, value string) error {
		return fmt.Errorf("%s: the index has an unsafe %s %q", pkg.ID, what, value)
	}
	if !paths.SafeName(pkg.ID) {
		return bad("ID", pkg.ID)
	}
	if !paths.SafeName(pkg.Version) {
		return bad("version", pkg.Version)
	}
	if !paths.SafeRelative(inst.Exe) {
		return bad("exe path", inst.Exe)
	}
	if inst.ExtractDir != "" && !paths.SafeRelative(inst.ExtractDir) {
		return bad("extract_dir", inst.ExtractDir)
	}
	if !sha256Hex.MatchString(inst.SHA256) {
		return bad("SHA256", inst.SHA256)
	}
	if u, err := url.Parse(inst.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return bad("URL", inst.URL)
	}
	if inst.Rename != nil {
		if len(inst.Rename) != 2 {
			return bad("rename", strings.Join(inst.Rename, ", "))
		}
		// The glob only ever matches names read from the app's root. The new name
		// must be a plain file name in that same root.
		if !paths.SafeRelative(inst.Rename[1]) || strings.ContainsAny(inst.Rename[1], `\/`) {
			return bad("rename", inst.Rename[1])
		}
	}
	return nil
}

// unpack lays out the verified download in dir and returns the app's root, which
// is confirmed to contain the launch exe.
func unpack(ctx context.Context, inst *index.Installer, file, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	root := dir
	switch inst.Type {
	case "exe":
		target := filepath.Join(dir, inst.Exe)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		if err := fsutil.Rename(file, target); err != nil {
			return "", err
		}
	case "zip", "7z":
		extractor := extract.Zip
		if inst.Type == "7z" {
			extractor = extract.SevenZip
		}
		if err := extractor(file, dir); err != nil {
			return "", err
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if inst.ExtractDir != "" {
			root = filepath.Join(dir, inst.ExtractDir)
			if info, err := os.Stat(root); err != nil || !info.IsDir() {
				return "", fmt.Errorf("the archive has no folder %q", inst.ExtractDir)
			}
		}
		if inst.Rename != nil {
			if err := applyRename(root, inst.Rename[0], inst.Rename[1]); err != nil {
				return "", err
			}
		}
	default:
		return "", fmt.Errorf("unknown download type %q", inst.Type)
	}
	info, err := os.Stat(filepath.Join(root, inst.Exe))
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("the app's program file %q is missing after unpacking", inst.Exe)
	}
	return root, nil
}

// applyRename renames the first file in root matching glob, ignoring case.
func applyRename(root, glob, name string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	glob = strings.ToLower(glob)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ok, err := filepath.Match(glob, strings.ToLower(e.Name()))
		if err != nil {
			return fmt.Errorf("bad rename pattern %q: %w", glob, err)
		}
		if ok {
			return fsutil.Rename(filepath.Join(root, e.Name()), filepath.Join(root, name))
		}
	}
	return nil // the exe check that follows reports anything that matters
}

// restorePrefs moves saved settings into the installed app. It runs after the app is
// in place, so a failed download can't lose them. A failure leaves them saved, and
// the next attempt finds the app already downloaded and tries again. A new version
// may have dropped an item from its list, so the old list counts too.
func restorePrefs(layout paths.Layout, pkg *index.Package, old *state.Entry, dest string) error {
	saved := layout.Prefs(pkg.ID)
	if _, err := os.Stat(saved); err != nil {
		return nil
	}
	items := slices.Clone(pkg.Persist)
	if old != nil {
		items = append(items, old.Persist...)
	}
	if err := fsutil.MoveItems(saved, dest, paths.SafeItems(items)); err != nil {
		return fmt.Errorf("restore saved settings: %w", err)
	}
	return fsutil.RemoveAll(saved)
}

func record(store state.Store, layout paths.Layout, pkg *index.Package, inst *index.Installer, rel string, pin bool) error {
	cfg, err := config.Load(layout.Config())
	if err != nil {
		return err
	}
	return store.Update(func(st state.State) error {
		e := st[pkg.ID]
		if e == nil {
			e = &state.Entry{TTL: cfg.TTL, KeepPrefs: cfg.KeepPrefs}
			st[pkg.ID] = e
		}
		e.Name = pkg.Name
		e.Bucket = pkg.Bucket
		e.Source = pinOf(pkg.Version, inst)
		if pin && e.Pin == nil {
			e.Pin = pinOf(pkg.Version, inst)
		}
		e.Version = pkg.Version
		e.Path = rel
		e.Exe = inst.Exe
		e.Persist = slices.Clone(pkg.Persist)
		// This copy's clock starts now, not from the session before it was reaped.
		e.LastLaunched, e.LastClosed = nil, nil
		return nil
	})
}

// refererFor returns the file's own folder, which is what Scoop sends. Some hosts
// answer with a web page or a 404 instead of the file if there is no Referer.
// SourceForge and PortableApps are the opposite, as they are in Scoop: a Referer
// gets the web page.
func refererFor(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	for _, domain := range []string{"sourceforge.net", "portableapps.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return ""
		}
	}
	path := u.EscapedPath()
	slash := strings.LastIndex(path, "/")
	if slash < 0 {
		return ""
	}
	return u.Scheme + "://" + u.Host + path[:slash+1]
}

// download saves inst.URL to file and checks its hash before returning.
func (c *Client) download(ctx context.Context, inst *index.Installer, file string) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, inst.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	if referer := refererFor(inst.URL); referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &statusError{resp.StatusCode, resp.Status}
	}
	out, err := os.Create(file)
	if err != nil {
		return err
	}
	defer out.Close()

	errStalled := errors.New("no data received for 60 seconds")
	stall := time.AfterFunc(stallTimeout, func() { cancel(errStalled) })
	defer stall.Stop()

	hash := newHash()
	progress := newProgress(c.Out, resp.ContentLength)
	body := io.TeeReader(resp.Body, hash)
	buf := make([]byte, 256*1024)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			stall.Reset(stallTimeout)
			if _, err := out.Write(buf[:n]); err != nil {
				return err
			}
			progress.add(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			progress.finish()
			if cause := context.Cause(ctx); cause != nil {
				return fmt.Errorf("download failed: %w", cause)
			}
			return fmt.Errorf("download failed: %w", readErr)
		}
	}
	progress.finish()
	if err := out.Close(); err != nil {
		return err
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(got, inst.SHA256) {
		return fmt.Errorf("download failed verification: expected SHA256 %s, got %s", inst.SHA256, got)
	}
	return nil
}
