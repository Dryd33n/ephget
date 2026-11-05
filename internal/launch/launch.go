// Package launch is the stub path behind `ephgetw run`. It must stay free of
// network code: an app that isn't cached is handed to `ephget fetch`.
package launch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Dryd33n/ephget/internal/config"
	"github.com/Dryd33n/ephget/internal/footprint"
	"github.com/Dryd33n/ephget/internal/lockfile"
	"github.com/Dryd33n/ephget/internal/paths"
	"github.com/Dryd33n/ephget/internal/reap"
	"github.com/Dryd33n/ephget/internal/state"
	"github.com/Dryd33n/ephget/internal/ttl"
)

const createNewConsole = 0x00000010

// ErrFetchFailed means the download console already showed the user why, or the
// user closed it. There is nothing further to report.
var ErrFetchFailed = errors.New("the app was not downloaded")

// Options are the settings a stub carries in its arguments.
type Options struct {
	TTL       string
	KeepPrefs *bool
	Pin       string
}

// Run is `ephgetw run <id>`: it downloads the app if needed, starts it, and
// records when it opened and closed.
func Run(layout paths.Layout, id string, opts Options) error {
	if !paths.SafeName(id) {
		return fmt.Errorf("%q is not a valid app ID", id)
	}
	if opts.TTL != "" {
		if _, err := ttl.Parse(opts.TTL); err != nil {
			return err
		}
	}
	store := state.Store{Path: layout.State()}
	entry, err := ensureEntry(layout, store, id, opts)
	if err != nil {
		return err
	}
	link := launchedFrom()
	track := func(e *state.Entry) {
		if link != "" {
			e.TrackShortcut(link)
		}
	}
	if !entry.Cached(layout.Home) {
		// Recorded now, because the download may fail or be cancelled.
		if err := setTimes(store, id, track); err != nil {
			return err
		}
		if err := download(layout, id, opts.Pin); err != nil {
			return err
		}
		st, err := store.Read()
		if err != nil {
			return err
		}
		entry = st[id]
		if !entry.Cached(layout.Home) {
			return ErrFetchFailed
		}
	}

	// Only a first run pays for this, and a cached launch never does.
	scope := footprint.Current()
	firstRun := !entry.FootprintChecked
	var before footprint.Snapshot
	if firstRun {
		before = scope.Take()
	}

	// The app starts before the state write, which is the slow part of a launch.
	appDir := filepath.Join(layout.Home, entry.Path)
	process, err := startApp(filepath.Join(appDir, entry.Exe))
	if err != nil {
		return fmt.Errorf("couldn't start %s: %w", id, err)
	}
	// Clearing last_closed makes a crash fall back to last_launched, as the reaper
	// expects, instead of leaving the previous session's close time in place.
	now := time.Now().UTC()
	err = setTimes(store, id, func(e *state.Entry) {
		e.LastLaunched, e.LastClosed = &now, nil
		track(e)
	})
	if err != nil {
		return err
	}
	if process != 0 {
		syscall.WaitForSingleObject(process, syscall.INFINITE)
		syscall.CloseHandle(process)
	}
	waitErr := WaitForFolder(appDir)
	closed := time.Now().UTC()
	var found []string
	if firstRun {
		found = scope.Find(before, []string{id, entry.Name})
	}
	err = setTimes(store, id, func(e *state.Entry) {
		e.LastClosed = &closed
		if firstRun {
			e.Footprint, e.FootprintChecked = found, true
		}
	})
	if err != nil {
		return err
	}
	// The user just closed an app and has nothing to act on if this fails, and the
	// hourly task will run another pass.
	reap.Pass(layout, closed)
	return waitErr
}

// ensureEntry returns the app's state, rebuilding a lost entry from the stub's
// arguments. Anything the stub leaves out comes from the configured defaults.
func ensureEntry(layout paths.Layout, store state.Store, id string, opts Options) (*state.Entry, error) {
	st, err := store.Read()
	if err != nil {
		return nil, err
	}
	if e := st[id]; e != nil {
		return e, nil
	}
	cfg, err := config.Load(layout.Config())
	if err != nil {
		return nil, err
	}
	// The app may have run before this entry was lost, and its settings would then
	// look old rather than new. A comparison would find nothing and claim it had
	// looked, so this entry simply has no footprint: it leaves settings behind.
	rebuilt := &state.Entry{TTL: cfg.TTL, KeepPrefs: cfg.KeepPrefs, FootprintChecked: true}
	if opts.TTL != "" {
		rebuilt.TTL = opts.TTL
	}
	if opts.KeepPrefs != nil {
		rebuilt.KeepPrefs = *opts.KeepPrefs
	}
	var result *state.Entry
	err = store.Update(func(st state.State) error {
		if st[id] == nil { // another launch may have got here first
			st[id] = rebuilt
		}
		result = st[id]
		return nil
	})
	return result, err
}

// download waits for a fetch already running for this app, or starts one in a new
// console and waits for that.
func download(layout paths.Layout, id, pin string) error {
	if err := os.MkdirAll(layout.Tmp(), 0o755); err != nil {
		return err
	}
	lock, err := lockfile.TryAcquire(layout.FetchLock(id))
	if errors.Is(err, lockfile.ErrHeld) {
		lock, err = lockfile.Acquire(layout.FetchLock(id))
		if err != nil {
			return err
		}
		lock.Release()
		return nil // the caller checks whether that fetch succeeded
	}
	if err != nil {
		return err
	}
	lock.Release()

	self, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"fetch", id}
	if pin != "" {
		args = append(args, "--pin", pin)
	}
	code, err := runInNewConsole(filepath.Join(filepath.Dir(self), "ephget.exe"), args...)
	if err != nil {
		return fmt.Errorf("couldn't start the download: %w", err)
	}
	if code != 0 {
		return ErrFetchFailed
	}
	return nil
}

func setTimes(store state.Store, id string, change func(*state.Entry)) error {
	return store.Update(func(st state.State) error {
		if e := st[id]; e != nil {
			change(e)
		}
		return nil
	})
}
