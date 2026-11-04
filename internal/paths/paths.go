// Package paths defines the data folder layout and the checks that keep
// untrusted names inside it.
package paths

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Layout locates everything in ephget's data folder.
type Layout struct{ Home string }

// Default uses %LOCALAPPDATA%\ephget. EPHGET_HOME overrides it, so tests and
// sample runs never touch a real installation.
func Default() (Layout, error) {
	if home := os.Getenv("EPHGET_HOME"); home != "" {
		return Layout{Home: home}, nil
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return Layout{}, errors.New("LOCALAPPDATA is not set")
	}
	return Layout{Home: filepath.Join(base, "ephget")}, nil
}

func (l Layout) Config() string    { return filepath.Join(l.Home, "config.json") }
func (l Layout) Index() string     { return filepath.Join(l.Home, "index.json.gz") }
func (l Layout) IndexETag() string { return filepath.Join(l.Home, "index.json.gz.etag") }
func (l Layout) State() string     { return filepath.Join(l.Home, "state.json") }
func (l Layout) Tmp() string       { return filepath.Join(l.Home, "tmp") }

// FetchLock is held for as long as an app is being downloaded.
func (l Layout) FetchLock(id string) string { return filepath.Join(l.Tmp(), id+".lock") }

// AppRoot holds every version of an app.
func (l Layout) AppRoot(id string) string { return filepath.Join(l.Home, "apps", id) }

// Removing is where an app's folder goes while it is deleted. It is on the same
// volume as AppRoot, so moving the folder there is a rename, which fails if the
// app is running.
func (l Layout) Removing(id string) string { return filepath.Join(l.Tmp(), id+".removing") }

// AppRel is an app's folder relative to Home, as stored in state.json.
func AppRel(id, version string) string { return filepath.Join("apps", id, version) }

// Same rules as the indexer's is_safe_name.
var safeName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._+-]*[A-Za-z0-9_+-])?$`)

// SafeName reports whether an app ID or version can be used as a folder name.
func SafeName(name string) bool {
	return safeName.MatchString(name) && !reservedDevice(name)
}

// reservedDevice catches "NUL.txt" as well as "NUL": Windows 10 opens the device
// for both, and filepath.IsLocal only rejects the second.
func reservedDevice(name string) bool {
	stem, _, _ := strings.Cut(name, ".")
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$",
		"COM0", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT0", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	}
	return false
}

// SafeRelative reports whether p, which may use either slash, stays inside the
// folder it is joined to. Besides "..", it rejects what Windows would quietly
// rewrite or treat specially: "name:stream", device names, and components with
// trailing dots or spaces (".. " opens the parent folder).
func SafeRelative(p string) bool {
	p = strings.ReplaceAll(p, "/", `\`)
	if p == "" || !filepath.IsLocal(p) {
		return false
	}
	for _, part := range strings.Split(p, `\`) {
		if part == "" || part == "." {
			continue
		}
		if part == ".." || strings.TrimRight(part, " .") != part || reservedDevice(part) {
			return false
		}
		if strings.ContainsFunc(part, func(r rune) bool {
			return r < 0x20 || strings.ContainsRune(`<>:"|?*`, r)
		}) {
			return false
		}
	}
	return true
}
