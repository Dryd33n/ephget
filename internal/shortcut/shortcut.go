// Package shortcut creates and reads .lnk files through the Shell's IShellLink
// COM interface, and finds the Desktop and Start Menu folders.
package shortcut

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"github.com/Dryd33n/ephget/internal/paths"
)

var (
	ole32   = syscall.NewLazyDLL("ole32.dll")
	shell32 = syscall.NewLazyDLL("shell32.dll")

	procCoInitializeEx       = ole32.NewProc("CoInitializeEx")
	procCoUninitialize       = ole32.NewProc("CoUninitialize")
	procCoCreateInstance     = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree        = ole32.NewProc("CoTaskMemFree")
	procSHGetKnownFolderPath = shell32.NewProc("SHGetKnownFolderPath")
	procSHChangeNotify       = shell32.NewProc("SHChangeNotify")

	clsidShellLink  = syscall.GUID{Data1: 0x00021401, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIShellLinkW  = syscall.GUID{Data1: 0x000214F9, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIPersistFile = syscall.GUID{Data1: 0x0000010B, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}

	folderDesktop  = syscall.GUID{Data1: 0xB4BFCC3A, Data2: 0xDB2C, Data3: 0x424C, Data4: [8]byte{0xB0, 0x29, 0x7F, 0xE9, 0x9A, 0x87, 0xC6, 0x41}}
	folderPrograms = syscall.GUID{Data1: 0xA77F5D77, Data2: 0x2E2B, Data3: 0x44C3, Data4: [8]byte{0xA6, 0xA2, 0xAB, 0xA6, 0x01, 0x05, 0x4A, 0x51}}

	folderPublicDesktop = syscall.GUID{Data1: 0xC4AA340D, Data2: 0xF20F, Data3: 0x4863, Data4: [8]byte{0xAF, 0xEF, 0xF8, 0x7E, 0xF2, 0xE6, 0xBA, 0x25}}
	folderDocuments     = syscall.GUID{Data1: 0xFDD39AD0, Data2: 0x238F, Data3: 0x46AF, Data4: [8]byte{0xAD, 0xB4, 0x6C, 0x85, 0x48, 0x03, 0x69, 0xC7}}
	folderDownloads     = syscall.GUID{Data1: 0x374DE290, Data2: 0x123F, Data3: 0x4565, Data4: [8]byte{0x91, 0x64, 0x39, 0xC4, 0x92, 0x5E, 0x46, 0x7B}}
)

// Vtable slots, in the order the interfaces declare their methods.
const (
	slotQueryInterface = 0
	slotRelease        = 2

	slotGetPath             = 3
	slotGetDescription      = 6
	slotSetDescription      = 7
	slotSetWorkingDirectory = 9
	slotGetArguments        = 10
	slotSetArguments        = 11
	slotGetIconLocation     = 16
	slotSetIconLocation     = 17
	slotSetPath             = 20

	slotPersistLoad = 5
	slotPersistSave = 6
)

const (
	clsctxInprocServer = 1
	coinitApartment    = 2
	rpcEChangedMode    = 0x80010106
	stgmRead           = 0
	stgmReadWrite      = 2
	shcneUpdateItem    = 0x2000
	shcnfPath          = 0x0005
	maxText            = 4096
)

// Link is a shortcut's contents.
type Link struct {
	Target      string
	Arguments   string
	Description string
	WorkingDir  string
	Icon        string // empty for the target's own icon
}

// comObject calls methods on a COM interface pointer through its vtable.
type comObject struct{ ptr unsafe.Pointer }

// COM writes through these addresses, so an argument that points at a variable must be
// converted to uintptr in the call to this function itself. The directive below then
// keeps that variable in place, as it does for syscall's Proc.Call. Without it, a
// variable on the goroutine's stack can move first, and COM writes to freed memory.
//
//go:uintptrescapes
func (o comObject) call(slot int, args ...uintptr) error {
	vtable := *(*unsafe.Pointer)(o.ptr)
	method := *(*uintptr)(unsafe.Add(vtable, uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	hr, _, _ := syscall.SyscallN(method, append([]uintptr{uintptr(o.ptr)}, args...)...)
	if int32(hr) < 0 {
		return fmt.Errorf("COM error %#x", uint32(hr))
	}
	return nil
}

func (o comObject) release() { o.call(slotRelease) }

// withLink runs fn with a new shell link object and its IPersistFile interface.
func withLink(fn func(link, file comObject) error) error {
	// COM's apartment belongs to the thread, so stay on this one.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hr, _, _ := procCoInitializeEx.Call(0, coinitApartment)
	if int32(hr) < 0 && uint32(hr) != rpcEChangedMode {
		return fmt.Errorf("start COM: error %#x", uint32(hr))
	}
	if int32(hr) >= 0 {
		defer procCoUninitialize.Call()
	}

	var link comObject
	hr, _, _ = procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIShellLinkW)), uintptr(unsafe.Pointer(&link.ptr)))
	if int32(hr) < 0 {
		return fmt.Errorf("create shell link: error %#x", uint32(hr))
	}
	defer link.release()

	var file comObject
	if err := link.call(slotQueryInterface, uintptr(unsafe.Pointer(&iidIPersistFile)), uintptr(unsafe.Pointer(&file.ptr))); err != nil {
		return err
	}
	defer file.release()
	return fn(link, file)
}

func setText(o comObject, slot int, text string) error {
	p, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return err
	}
	return o.call(slot, uintptr(unsafe.Pointer(p)))
}

// Create writes the shortcut at path, replacing any file already there.
func Create(path string, l Link) error {
	return withLink(func(link, file comObject) error {
		for _, field := range []struct {
			slot int
			text string
		}{
			{slotSetPath, l.Target},
			{slotSetArguments, l.Arguments},
			{slotSetDescription, l.Description},
			{slotSetWorkingDirectory, l.WorkingDir},
		} {
			if err := setText(link, field.slot, field.text); err != nil {
				return err
			}
		}
		if l.Icon != "" {
			if err := setIcon(link, l.Icon); err != nil {
				return err
			}
		}
		p, err := syscall.UTF16PtrFromString(path)
		if err != nil {
			return err
		}
		const remember = 1
		if err := file.call(slotPersistSave, uintptr(unsafe.Pointer(p)), remember); err != nil {
			return fmt.Errorf("save %s: %w", path, err)
		}
		return nil
	})
}

// Read returns the target, arguments and description of the shortcut at path.
func Read(path string) (Link, error) {
	var l Link
	err := withLink(func(link, file comObject) error {
		p, err := syscall.UTF16PtrFromString(path)
		if err != nil {
			return err
		}
		if err := file.call(slotPersistLoad, uintptr(unsafe.Pointer(p)), stgmRead); err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		// Each address is converted in the call itself (see comObject.call).
		buf := make([]uint16, maxText)
		text := func() string { return syscall.UTF16ToString(buf) }
		buf[0] = 0
		if err := link.call(slotGetPath, uintptr(unsafe.Pointer(&buf[0])), maxText, 0, 0); err != nil {
			return err
		}
		l.Target = text()
		buf[0] = 0
		if err := link.call(slotGetArguments, uintptr(unsafe.Pointer(&buf[0])), maxText); err != nil {
			return err
		}
		l.Arguments = text()
		buf[0] = 0
		if err := link.call(slotGetDescription, uintptr(unsafe.Pointer(&buf[0])), maxText); err != nil {
			return err
		}
		l.Description = text()
		buf[0] = 0
		var iconIndex int32
		if err := link.call(slotGetIconLocation, uintptr(unsafe.Pointer(&buf[0])), maxText, uintptr(unsafe.Pointer(&iconIndex))); err != nil {
			return err
		}
		l.Icon = text()
		return nil
	})
	return l, err
}

// Arguments is what a stub passes to ephgetw. It carries the app's settings, so a
// stub can rebuild a lost state entry.
func Arguments(id, ttl string, keepPrefs bool, pinVersion string) string {
	prefs := "--no-keep-prefs"
	if keepPrefs {
		prefs = "--keep-prefs"
	}
	args := fmt.Sprintf("run %s --ttl %s %s", id, ttl, prefs)
	if pinVersion != "" {
		args += " --pin " + pinVersion
	}
	return args
}

// SetArguments changes only a shortcut's arguments, leaving its icon and anything
// else as it is.
func SetArguments(path, arguments string) error {
	return modify(path, func(link comObject) error { return setText(link, slotSetArguments, arguments) })
}

// SetIcon changes only a shortcut's icon, and tells Explorer to show the new one.
func SetIcon(path, icon string) error {
	if err := modify(path, func(link comObject) error { return setIcon(link, icon) }); err != nil {
		return err
	}
	if p, err := syscall.UTF16PtrFromString(path); err == nil {
		procSHChangeNotify.Call(shcneUpdateItem, shcnfPath, uintptr(unsafe.Pointer(p)), 0)
	}
	return nil
}

func setIcon(link comObject, icon string) error {
	p, err := syscall.UTF16PtrFromString(icon)
	if err != nil {
		return err
	}
	return link.call(slotSetIconLocation, uintptr(unsafe.Pointer(p)), 0)
}

func modify(path string, change func(link comObject) error) error {
	return withLink(func(link, file comObject) error {
		p, err := syscall.UTF16PtrFromString(path)
		if err != nil {
			return err
		}
		if err := file.call(slotPersistLoad, uintptr(unsafe.Pointer(p)), stgmReadWrite); err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if err := change(link); err != nil {
			return err
		}
		const remember = 1
		if err := file.call(slotPersistSave, uintptr(unsafe.Pointer(p)), remember); err != nil {
			return fmt.Errorf("save %s: %w", path, err)
		}
		return nil
	})
}

// Marker is the description that identifies an app's stub (section 7, Finding stubs).
func Marker(id string) string { return MarkerPrefix + id }

// MarkerPrefix starts the description of every ephget stub.
const MarkerPrefix = "ephget:"

// IsStubOf reports whether a shortcut is this app's stub.
func IsStubOf(l Link, id string) bool { return strings.EqualFold(l.Description, Marker(id)) }

// Find returns every .lnk under folders that is this app's stub. Folders it can't
// read, and shortcuts it can't open, are skipped: the search is best-effort.
func Find(id string, folders []string) []string {
	return findWhere(folders, func(l Link) bool { return IsStubOf(l, id) })
}

// FindAny returns every ephget stub under folders, whatever the app.
func FindAny(folders []string) []string {
	return findWhere(folders, func(l Link) bool { return strings.HasPrefix(l.Description, MarkerPrefix) })
}

func findWhere(folders []string, match func(Link) bool) []string {
	var found []string
	seen := map[string]bool{}
	for _, folder := range folders {
		filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.Type().IsRegular() && strings.EqualFold(filepath.Ext(path), ".lnk") {
				if link, err := Read(path); err == nil && match(link) && !seen[strings.ToLower(path)] {
					seen[strings.ToLower(path)] = true
					found = append(found, path)
				}
			}
			return nil
		})
	}
	return found
}

// SearchFolders lists where a moved stub is likely to be (section 7, Finding stubs).
func SearchFolders() []string {
	var folders []string
	for _, id := range []*syscall.GUID{&folderDesktop, &folderPublicDesktop, &folderPrograms, &folderDocuments, &folderDownloads} {
		if path, err := knownFolder(id); err == nil {
			folders = append(folders, path)
		}
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		folders = append(folders, filepath.Join(appData, "Microsoft", "Internet Explorer", "Quick Launch", "User Pinned", "TaskBar"))
	}
	return folders
}

// Desktop returns the user's Desktop folder, wherever it has been redirected.
func Desktop() (string, error) { return knownFolder(&folderDesktop) }

// Programs returns the user's Start Menu Programs folder.
func Programs() (string, error) { return knownFolder(&folderPrograms) }

func knownFolder(id *syscall.GUID) (string, error) {
	var path *uint16
	hr, _, _ := procSHGetKnownFolderPath.Call(uintptr(unsafe.Pointer(id)), 0, 0, uintptr(unsafe.Pointer(&path)))
	if int32(hr) < 0 {
		return "", fmt.Errorf("find known folder: error %#x", uint32(hr))
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(path)))
	if path == nil {
		return "", errors.New("find known folder: no path returned")
	}
	n := 0
	for *(*uint16)(unsafe.Add(unsafe.Pointer(path), n*2)) != 0 {
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice(path, n)), nil
}

// FileName turns an app's display name into a shortcut file name. Names come from
// manifests, so anything Windows wouldn't allow is replaced; fallback is used if
// nothing usable remains.
func FileName(name, fallback string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	clean = strings.TrimRight(strings.TrimSpace(clean), ". ")
	if clean == "" || !paths.SafeRelative(clean) {
		clean = fallback
	}
	return clean + ".lnk"
}
