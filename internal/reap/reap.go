// Package reap deletes apps. It has no network code, so ephgetw can use it too.
package reap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Ephget/ephget/internal/config"
	"github.com/Ephget/ephget/internal/footprint"
	"github.com/Ephget/ephget/internal/fsutil"
	"github.com/Ephget/ephget/internal/lockfile"
	"github.com/Ephget/ephget/internal/paths"
	"github.com/Ephget/ephget/internal/shortcut"
	"github.com/Ephget/ephget/internal/state"
	"github.com/Ephget/ephget/internal/ttl"
)

var (
	// ErrNotAdded means nothing of this app exists: no state, stub or files.
	ErrNotAdded = errors.New("app is not added")
	// ErrRunning means the app's folder is in use, so nothing was changed.
	ErrRunning = errors.New("app is running")
	// ErrDownloading means a fetch holds the app's lock, so nothing was changed.
	ErrDownloading = errors.New("app is being downloaded")
)

// ScanFolders lists where to look for stubs that aren't at their recorded paths.
// EPHGET_HOME marks a test or sample run, which must leave the real folders alone.
func ScanFolders(layout paths.Layout) []string {
	var folders []string
	if cfg, err := config.Load(layout.Config()); err == nil && cfg.ShortcutDir != "" {
		folders = append(folders, cfg.ShortcutDir)
	}
	if os.Getenv("EPHGET_HOME") == "" {
		folders = append(folders, shortcut.SearchFolders()...)
	}
	return folders
}

// Removal says what Remove did.
type Removal struct {
	ID string
	// Deleted lists the stubs that are gone. Undeleted lists those that couldn't
	// be deleted, such as one on the public Desktop, which needs admin rights.
	Deleted, Undeleted []string
	HadFiles           bool
}

// Remove deletes the app's stubs, cached files, saved settings, icon and state entry,
// and its detected settings if keep preferences is off. This is the only way to lose
// settings kept with keep preferences on, because it is the only clear request. Besides the paths in state, it looks for stubs under scan, in case one was moved
// and not clicked.
//
// Moving the files aside comes first: it fails if the app is running, and at that
// point nothing has been touched.
func Remove(layout paths.Layout, id string, scan []string) (*Removal, error) {
	if !paths.SafeName(id) {
		return nil, fmt.Errorf("%q is not a valid app ID", id)
	}
	store := state.Store{Path: layout.State()}
	st, err := store.Read()
	if err != nil {
		return nil, err
	}
	for known := range st {
		if strings.EqualFold(known, id) {
			id = known
			break
		}
	}

	if err := os.MkdirAll(layout.Tmp(), 0o755); err != nil {
		return nil, err
	}
	lock, err := lockfile.TryAcquire(layout.FetchLock(id))
	if errors.Is(err, lockfile.ErrHeld) {
		return nil, ErrDownloading
	}
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	var recorded []string
	if e := st[id]; e != nil {
		recorded = e.Shortcuts
	}
	stubs := FindStubs(id, recorded, scan)
	dir, trash := layout.AppRoot(id), layout.Removing(id)
	hadFiles := exists(dir)
	if st[id] == nil && len(stubs) == 0 && !hadFiles && !exists(trash) && !exists(layout.Prefs(id)) && !exists(layout.Icon(id)) {
		return nil, ErrNotAdded
	}

	// An earlier removal that couldn't finish deleting left this behind.
	if err := fsutil.RemoveAll(trash); err != nil {
		return nil, err
	}
	if hadFiles {
		if err := fsutil.Rename(dir, trash); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrRunning, err)
		}
	}

	r := &Removal{ID: id, HadFiles: hadFiles}
	for _, stub := range stubs {
		if err := os.Remove(stub); err != nil {
			r.Undeleted = append(r.Undeleted, stub)
		} else {
			r.Deleted = append(r.Deleted, stub)
		}
	}
	var settings error
	if e := st[id]; e != nil && !e.KeepPrefs {
		settings = footprint.Current().DeleteAll(e.Footprint)
	}
	err = store.Update(func(st state.State) error {
		delete(st, id)
		return nil
	})
	if err != nil {
		return r, err
	}
	return r, errors.Join(settings, fsutil.RemoveAll(layout.Prefs(id)), fsutil.RemoveAll(layout.Icon(id)), fsutil.RemoveAll(trash))
}

// RemovalTime returns when the app becomes due, if it is cached and its TTL is
// valid. An app downloaded but never opened counts from when it was downloaded,
// so a failed launch can't leave it on disk forever.
func RemovalTime(layout paths.Layout, e *state.Entry) (time.Time, bool) {
	if e == nil || e.Path == "" {
		return time.Time{}, false
	}
	lifetime, err := ttl.Parse(e.TTL)
	if err != nil {
		return time.Time{}, false
	}
	switch {
	case e.LastClosed != nil:
		return e.LastClosed.Add(lifetime), true
	case e.LastLaunched != nil:
		return e.LastLaunched.Add(lifetime), true
	}
	info, err := os.Stat(filepath.Join(layout.Home, e.Path))
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime().Add(lifetime), true
}

// Pass is one run of the reaper (section 7). A pass with nothing to do reads
// state.json and writes nothing. Errors on one app don't stop the others.
func Pass(layout paths.Layout, now time.Time) error {
	store := state.Store{Path: layout.State()}
	st, err := store.Read()
	if err != nil {
		return err
	}
	errs := []error{sweepLeftovers(layout)}
	scan := sync.OnceValue(func() []string { return ScanFolders(layout) })
	ids := make([]string, 0, len(st))
	for id := range st {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		// Entries can be hand-edited, and an ID becomes a folder name below.
		if !paths.SafeName(id) {
			continue
		}
		if !stubExists(st[id]) || isDue(layout, st[id], now) {
			errs = append(errs, reapApp(layout, store, id, now, scan))
		}
	}
	return errors.Join(errs...)
}

func isDue(layout paths.Layout, e *state.Entry, now time.Time) bool {
	due, ok := RemovalTime(layout, e)
	return ok && now.After(due)
}

// reapApp holds the app's fetch lock, so it can't delete a download in progress.
// A held lock means the app is busy, and the next pass will look again.
func reapApp(layout paths.Layout, store state.Store, id string, now time.Time, scan func() []string) error {
	if err := os.MkdirAll(layout.Tmp(), 0o755); err != nil {
		return err
	}
	lock, err := lockfile.TryAcquire(layout.FetchLock(id))
	if errors.Is(err, lockfile.ErrHeld) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Release()

	// That wait may have let a launch or fetch finish, so decide from fresh state.
	st, err := store.Read()
	if err != nil {
		return err
	}
	e := st[id]
	if e == nil {
		return nil
	}
	deleted := false
	if !stubExists(e) {
		found := shortcut.Find(id, scan())
		if len(found) == 0 {
			deleted = true
		} else if err := updateEntry(store, id, func(e *state.Entry) { e.Shortcuts = found }); err != nil {
			return err
		}
	}
	if !deleted && !isDue(layout, e, now) {
		return nil
	}

	dir, trash := layout.AppRoot(id), layout.Removing(id)
	hadFiles := exists(dir)
	if hadFiles {
		if err := fsutil.RemoveAll(trash); err != nil {
			return err
		}
		// One attempt: a false "running" only waits for the next pass, while the
		// retries would hold up every pass for as long as the app stays open.
		if err := os.Rename(dir, trash); err != nil {
			return updateEntry(store, id, func(e *state.Entry) { e.LastClosed = &now })
		}
	}
	// Missing stubs get the app removed, but they may only have moved somewhere a scan
	// can't see, or be on an unplugged drive. So settings follow the toggle either way,
	// and a stub that turns up finds them on its next click.
	if e.KeepPrefs && hadFiles {
		items := paths.SafeItems(e.Persist)
		versionRoot := filepath.Join(layout.Home, e.Path)
		if rel, err := filepath.Rel(dir, versionRoot); err == nil && e.Path != "" {
			if err := fsutil.MoveItems(filepath.Join(trash, rel), layout.Prefs(id), items); err != nil {
				// Settings could be lost, so put the app back and try again next pass.
				return errors.Join(err, fsutil.Rename(trash, dir))
			}
		}
	}
	if !e.KeepPrefs {
		err := errors.Join(fsutil.RemoveAll(layout.Prefs(id)), footprint.Current().DeleteAll(e.Footprint))
		if err != nil {
			return err
		}
	}
	if deleted {
		if err := fsutil.RemoveAll(layout.Icon(id)); err != nil {
			return err
		}
	}
	err = store.Update(func(st state.State) error {
		if deleted {
			delete(st, id)
		} else if current := st[id]; current != nil {
			current.Path = ""
		}
		return nil
	})
	if err != nil {
		return err
	}
	return fsutil.RemoveAll(trash)
}

func updateEntry(store state.Store, id string, change func(*state.Entry)) error {
	return store.Update(func(st state.State) error {
		if e := st[id]; e != nil {
			change(e)
		}
		return nil
	})
}

// stubExists treats only "not found" as missing, so a folder that can't be read
// doesn't get an app deleted.
func stubExists(e *state.Entry) bool {
	return slices.ContainsFunc(e.Shortcuts, func(path string) bool {
		_, err := os.Lstat(path)
		return !errors.Is(err, os.ErrNotExist)
	})
}

// sweepLeftovers finishes deletions that a crash or a full disk interrupted.
func sweepLeftovers(layout paths.Layout) error {
	matches, err := filepath.Glob(filepath.Join(layout.Tmp(), "*.removing"))
	if err != nil || len(matches) == 0 {
		return err
	}
	var errs []error
	for _, path := range matches {
		id := strings.TrimSuffix(filepath.Base(path), ".removing")
		lock, err := lockfile.TryAcquire(layout.FetchLock(id))
		if err != nil {
			continue // in use by a removal right now
		}
		errs = append(errs, fsutil.RemoveAll(path))
		lock.Release()
	}
	return errors.Join(errs...)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// FindStubs returns the recorded paths that really are this app's stubs, then any
// others under scan. A recorded path may since have been taken by something else,
// so only stubs carrying this app's marker count.
func FindStubs(id string, recorded, scan []string) []string {
	var stubs []string
	seen := map[string]bool{}
	add := func(path string) {
		if key := strings.ToLower(path); !seen[key] {
			seen[key] = true
			stubs = append(stubs, path)
		}
	}
	for _, path := range recorded {
		if link, err := shortcut.Read(path); err == nil && shortcut.IsStubOf(link, id) {
			add(path)
		}
	}
	for _, path := range shortcut.Find(id, scan) {
		add(path)
	}
	return stubs
}
