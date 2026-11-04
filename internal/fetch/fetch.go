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
	"github.com/Dryd33n/ephget/internal/index"
	"github.com/Dryd33n/ephget/internal/lockfile"
	"github.com/Dryd33n/ephget/internal/paths"
	"github.com/Dryd33n/ephget/internal/state"
)

// IndexURL is where the catalog is published.
const IndexURL = "https://github.com/Dryd33n/ephget-index/releases/latest/download/index.json.gz"

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

	idx, err := c.LoadIndex(ctx)
	if err != nil {
		return err
	}
	pkg := idx.Find(id)
	if pkg == nil {
		return fmt.Errorf("%s is no longer in the catalog, so it can't be downloaded", id)
	}
	inst, err := pkg.SelectInstaller(c.Machine)
	if err != nil {
		return err
	}
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
		return record(store, c.Layout, pkg, inst, rel)
	}

	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "Downloading %s %s\n%s\n", pkg.Name, pkg.Version, inst.URL)
	file := filepath.Join(work, "download")
	if err := c.download(ctx, inst, file); err != nil {
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
	return record(store, c.Layout, pkg, inst, rel)
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

func record(store state.Store, layout paths.Layout, pkg *index.Package, inst *index.Installer, rel string) error {
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
		e.Bucket = pkg.Bucket
		e.Version = pkg.Version
		e.Path = rel
		e.Exe = inst.Exe
		e.Persist = slices.Clone(pkg.Persist)
		return nil
	})
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
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: the server answered %s", resp.Status)
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
