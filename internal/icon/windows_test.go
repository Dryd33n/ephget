package icon

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// Checks with Windows itself, since a file can have the right shape and still not
// load: it must draw the extracted icon from the same API Explorer's shortcuts use.
func TestWindowsCanLoadTheExtractedIcon(t *testing.T) {
	out := filepath.Join(t.TempDir(), "app.ico")
	if err := Extract(systemExe, out); err != nil {
		t.Fatal(err)
	}
	name, err := syscall.UTF16PtrFromString(out)
	if err != nil {
		t.Fatal(err)
	}
	const (
		imageIcon     = 1
		loadFromFile  = 0x10
		defaultSize   = 0x40
		loadedBitSize = 32
	)
	user32 := syscall.NewLazyDLL("user32.dll")
	for _, size := range []uintptr{16, loadedBitSize, 256} {
		handle, _, callErr := user32.NewProc("LoadImageW").Call(0, uintptr(unsafe.Pointer(name)), imageIcon, size, size, loadFromFile)
		if handle == 0 {
			t.Errorf("size %d: %v", size, callErr)
			continue
		}
		user32.NewProc("DestroyIcon").Call(handle)
	}
	handle, _, callErr := user32.NewProc("LoadImageW").Call(0, uintptr(unsafe.Pointer(name)), imageIcon, 0, 0, loadFromFile|defaultSize)
	if handle == 0 {
		t.Errorf("default size: %v", callErr)
	} else {
		user32.NewProc("DestroyIcon").Call(handle)
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 || strings.HasSuffix(out, ".exe") {
		t.Fatalf("%v, %v", info, err)
	}
}
