// Package reap deletes apps. It has no network code, so ephgetw can use it too.
package reap

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Dryd33n/ephget/internal/fsutil"
	"github.com/Dryd33n/ephget/internal/lockfile"
	"github.com/Dryd33n/ephget/internal/paths"
	"github.com/Dryd33n/ephget/internal/shortcut"
	"github.com/Dryd33n/ephget/internal/state"
)

var (
	// ErrNotAdded means nothing of this app exists: no state, stub or files.
	ErrNotAdded = errors.New("app is not added")
	// ErrRunning means the app's folder is in use, so nothing was changed.
	ErrRunning = errors.New("app is running")
	// ErrDownloading means a fetch holds the app's lock, so nothing was changed.
	ErrDownloading = errors.New("app is being downloaded")
)

// Removal says what Remove did.
type Removal struct {
	ID string
	// Deleted lists the stubs that are gone. Undeleted lists those that couldn't
	// be deleted, such as one on the public Desktop, which needs admin rights.
	Deleted, Undeleted []string
	HadFiles           bool
}

// Remove deletes the app's stubs, cached files and state entry. Besides the paths
// in state, it looks for stubs under scan, in case one was moved and not clicked.
//
// Only stubs carrying this app's marker are deleted, because a recorded path may
// since have been taken by something else. Moving the files aside comes first: it
// fails if the app is running, and at that point nothing has been touched.
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
	stubs := findStubs(id, recorded, scan)
	dir, trash := layout.AppRoot(id), layout.Removing(id)
	hadFiles, hadTrash := exists(dir), exists(trash)
	if st[id] == nil && len(stubs) == 0 && !hadFiles && !hadTrash {
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
	err = store.Update(func(st state.State) error {
		delete(st, id)
		return nil
	})
	if err != nil {
		return r, err
	}
	return r, fsutil.RemoveAll(trash)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// findStubs returns the recorded paths that really are this app's stubs, then any
// others under scan.
func findStubs(id string, recorded, scan []string) []string {
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
