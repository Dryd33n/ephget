// Package footprint finds the settings an app creates outside its folder, by
// comparing before and after its first run, and deletes them on request.
//
// Deleting is the risk here, since a wrong match destroys someone else's data. So a
// candidate must be new, and its name must equal the app's ID or display name
// exactly. That misses settings under other names (Etcher's "balena-etcher"), which
// only leaves harmless files behind. Stored entries are checked again before any
// delete, because state.json can be edited.
package footprint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unsafe"

	"github.com/Ephget/ephget/internal/paths"
)

const (
	appDataVar      = "%APPDATA%"
	localAppDataVar = "%LOCALAPPDATA%"
	registryHive    = `HKCU`

	errorFileNotFound = syscall.Errno(2)
)

var (
	advapi32          = syscall.NewLazyDLL("advapi32.dll")
	procRegDeleteTree = advapi32.NewProc("RegDeleteTreeW")

	// Names no single app owns, even if one is somehow new on this machine.
	reserved = []string{"ephget", "microsoft", "windows", "temp", "programs", "packages",
		"classes", "policies", "wow6432node", "clients", "appdatalow"}
)

// Scope says where to look. Tests point it at temporary places.
type Scope struct {
	AppData, LocalAppData string
	// Registry is a key under HKCU, or empty to leave the registry alone.
	Registry string
}

// Current is the real locations. EPHGET_HOME marks a test or sample run, which must
// leave the real registry alone.
func Current() Scope {
	scope := Scope{AppData: os.Getenv("APPDATA"), LocalAppData: os.Getenv("LOCALAPPDATA"), Registry: "Software"}
	if os.Getenv("EPHGET_HOME") != "" {
		scope.Registry = ""
	}
	return scope
}

// Snapshot is the set of entries that existed at one moment.
type Snapshot map[string]bool

// Take records the top-level entries in each folder and the keys under the registry
// root, plus one level below each.
func (s Scope) Take() Snapshot {
	snap := Snapshot{}
	for variable, dir := range map[string]string{appDataVar: s.AppData, localAppDataVar: s.LocalAppData} {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			snap[strings.ToLower(variable+`\`+e.Name())] = true
		}
	}
	if s.Registry != "" {
		for _, vendor := range subkeys(s.Registry) {
			path := s.Registry + `\` + vendor
			snap[strings.ToLower(registryHive+`\`+path)] = true
			for _, key := range subkeys(path) {
				snap[strings.ToLower(registryHive+`\`+path+`\`+key)] = true
			}
		}
	}
	return snap
}

// Find returns what is new since before and named exactly after the app.
func (s Scope) Find(before Snapshot, names []string) []string {
	matches := func(leaf string) bool {
		if slices.Contains(reserved, strings.ToLower(leaf)) {
			return false
		}
		return slices.ContainsFunc(names, func(name string) bool {
			return name != "" && strings.EqualFold(name, leaf)
		})
	}
	var found []string
	add := func(entry, leaf string) {
		if !before[strings.ToLower(entry)] && matches(leaf) {
			found = append(found, entry)
		}
	}
	for variable, dir := range map[string]string{appDataVar: s.AppData, localAppDataVar: s.LocalAppData} {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			add(variable+`\`+e.Name(), e.Name())
		}
	}
	if s.Registry != "" {
		for _, vendor := range subkeys(s.Registry) {
			path := s.Registry + `\` + vendor
			add(registryHive+`\`+path, vendor)
			for _, key := range subkeys(path) {
				add(registryHive+`\`+path+`\`+key, key)
			}
		}
	}
	slices.Sort(found)
	return found
}

// Delete removes one stored entry, and refuses anything that couldn't have come from
// Find. An entry that is already gone is not an error.
func (s Scope) Delete(entry string) error {
	refuse := fmt.Errorf("refusing to delete %q: not a settings location", entry)
	if key, ok := strings.CutPrefix(entry, registryHive+`\`); ok {
		if s.Registry == "" {
			return nil
		}
		rest, ok := strings.CutPrefix(key, s.Registry+`\`)
		if !ok {
			return refuse
		}
		parts := strings.Split(rest, `\`)
		if len(parts) > 2 || slices.Contains(parts, "") || slices.Contains(reserved, strings.ToLower(parts[len(parts)-1])) {
			return refuse
		}
		return deleteKey(key)
	}
	variable, leaf, ok := strings.Cut(entry, `\`)
	if !ok || strings.ContainsAny(leaf, `\/`) || !paths.SafeRelative(leaf) || slices.Contains(reserved, strings.ToLower(leaf)) {
		return refuse
	}
	var dir string
	switch variable {
	case appDataVar:
		dir = s.AppData
	case localAppDataVar:
		dir = s.LocalAppData
	default:
		return refuse
	}
	if dir == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(dir, leaf))
}

// DeleteAll tries every entry, so one failure doesn't leave the rest behind.
func (s Scope) DeleteAll(entries []string) error {
	var errs []error
	for _, entry := range entries {
		errs = append(errs, s.Delete(entry))
	}
	return errors.Join(errs...)
}

func subkeys(path string) []string {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil
	}
	var key syscall.Handle
	if syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, name, 0, syscall.KEY_READ, &key) != nil {
		return nil
	}
	defer syscall.RegCloseKey(key)
	var out []string
	buf := make([]uint16, 256) // the longest key name the registry allows, plus one
	for i := uint32(0); ; i++ {
		size := uint32(len(buf))
		if syscall.RegEnumKeyEx(key, i, &buf[0], &size, nil, nil, nil, nil) != nil {
			return out
		}
		out = append(out, syscall.UTF16ToString(buf[:size]))
	}
}

func deleteKey(path string) error {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	code, _, _ := procRegDeleteTree.Call(uintptr(syscall.HKEY_CURRENT_USER), uintptr(unsafe.Pointer(name)))
	if code != 0 && syscall.Errno(code) != errorFileNotFound {
		return fmt.Errorf("delete HKCU\\%s: %w", path, syscall.Errno(code))
	}
	return nil
}
