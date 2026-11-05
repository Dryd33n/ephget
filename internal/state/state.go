// Package state reads and writes state.json under a lock file. Windows won't
// replace a file that a reader has open, so reads take the lock too.
package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Ephget/ephget/internal/fsutil"
	"github.com/Ephget/ephget/internal/lockfile"
)

// Pin is a frozen version (section 7, Versions and updates).
//
// The index only lists latest versions, so this holds all it takes to install this
// one without it.
type Pin struct {
	Version    string   `json:"version"`
	URL        string   `json:"url"`
	SHA256     string   `json:"sha256"`
	Type       string   `json:"type"`
	Exe        string   `json:"exe"`
	ExtractDir string   `json:"extract_dir,omitempty"`
	Rename     []string `json:"rename,omitempty"`
}

// Entry is one app's state. Path, and the fields describing the cached copy,
// are empty until the app has been downloaded.
type Entry struct {
	Name         string     `json:"name,omitempty"`
	Bucket       string     `json:"bucket,omitempty"`
	Version      string     `json:"version,omitempty"`
	Path         string     `json:"path,omitempty"`
	Exe          string     `json:"exe,omitempty"`
	Persist      []string   `json:"persist,omitempty"`
	LastLaunched *time.Time `json:"last_launched,omitempty"`
	LastClosed   *time.Time `json:"last_closed,omitempty"`
	TTL          string     `json:"ttl"`
	KeepPrefs    bool       `json:"keep_prefs"`
	Pin          *Pin       `json:"pin,omitempty"`
	// Source is what the cached copy was installed from, which `--pin` freezes.
	Source    *Pin     `json:"source,omitempty"`
	Shortcuts []string `json:"shortcuts,omitempty"`
	Footprint []string `json:"footprint,omitempty"`
	// FootprintChecked means the first-run comparison has already happened, or can't.
	FootprintChecked bool `json:"footprint_checked,omitempty"`
}

// AddShortcut records path unless it is already known.
func (e *Entry) AddShortcut(path string) {
	if !slices.ContainsFunc(e.Shortcuts, func(s string) bool { return strings.EqualFold(s, path) }) {
		e.Shortcuts = append(e.Shortcuts, path)
	}
}

// TrackShortcut records the stub the app was just opened from, and forgets recorded
// stubs that no longer exist, so a moved stub replaces its old path.
func (e *Entry) TrackShortcut(path string) {
	e.Shortcuts = slices.DeleteFunc(e.Shortcuts, func(s string) bool {
		_, err := os.Lstat(s)
		return errors.Is(err, fs.ErrNotExist)
	})
	e.AddShortcut(path)
}

// Cached reports whether the app can launch from under home right now.
func (e *Entry) Cached(home string) bool {
	if e == nil || e.Path == "" || e.Exe == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(home, e.Path, e.Exe))
	return err == nil && info.Mode().IsRegular()
}

// State maps app IDs to their entries.
type State map[string]*Entry

// Store is a state file on disk.
type Store struct{ Path string }

func (s Store) lock() (*lockfile.Lock, error) {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return nil, err
	}
	return lockfile.Acquire(s.Path + ".lock")
}

// load expects the lock to be held. state.json is only a cache, because stubs can
// rebuild it, so a damaged file is set aside rather than blocking every launch.
func (s Store) load() (State, error) {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return nil, err
	}
	st := State{}
	if err := json.Unmarshal(data, &st); err != nil {
		if err := fsutil.Rename(s.Path, s.Path+".corrupt"); err != nil {
			return nil, err
		}
		return State{}, nil
	}
	// A hand-edited "id": null would otherwise crash every reader.
	for id, e := range st {
		if e == nil {
			delete(st, id)
		}
	}
	return st, nil
}

// Read returns the current state.
func (s Store) Read() (State, error) {
	l, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer l.Release()
	return s.load()
}

// Update runs change under the lock and saves the result. If change returns an
// error, nothing is saved.
func (s Store) Update(change func(State) error) error {
	l, err := s.lock()
	if err != nil {
		return err
	}
	defer l.Release()
	st, err := s.load()
	if err != nil {
		return err
	}
	if err := change(st); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteAtomic(s.Path, append(data, '\n'))
}
