// Package fsutil holds file operations that must cope with Windows sharing
// rules, where antivirus or a reader can briefly hold a file open.
package fsutil

import (
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	retries       = 40
	firstDelay    = 5 * time.Millisecond
	maxRetryDelay = 200 * time.Millisecond
)

// retry keeps the first waits short, since most holds last only milliseconds, and
// gives up after several seconds.
func retry(op func() error) error {
	var err error
	delay := firstDelay
	for range retries {
		if err = op(); err == nil {
			return nil
		}
		time.Sleep(delay)
		delay = min(delay*2, maxRetryDelay)
	}
	return err
}

// Rename retries, because a scanner often holds freshly written files.
func Rename(from, to string) error {
	return retry(func() error { return os.Rename(from, to) })
}

// RemoveAll retries for the same reason as Rename.
func RemoveAll(path string) error {
	return retry(func() error { return os.RemoveAll(path) })
}

// WriteAtomic replaces path through a temp file and a rename, so readers see
// the old content or the new, never part of it.
func WriteAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return Rename(tmp.Name(), path)
}

// ClearMarkOfTheWeb removes the Zone.Identifier stream from every file under root.
func ClearMarkOfTheWeb(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			// Most files have no such stream, so a failure here means nothing to clear.
			os.Remove(path + ":Zone.Identifier")
		}
		return nil
	})
}
