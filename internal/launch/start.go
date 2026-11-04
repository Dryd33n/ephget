package launch

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	errorElevationRequired = syscall.Errno(740)
	seeMaskNoCloseProcess  = 0x40
	swShowNormal           = 1
)

var (
	shell32             = syscall.NewLazyDLL("shell32.dll")
	procShellExecuteExW = shell32.NewProc("ShellExecuteExW")
)

// shellExecuteInfo mirrors SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	size       uint32
	mask       uint32
	hwnd       uintptr
	verb       *uint16
	file       *uint16
	parameters *uint16
	directory  *uint16
	show       int32
	instApp    uintptr
	idList     uintptr
	class      *uint16
	keyClass   uintptr
	hotKey     uint32
	icon       uintptr
	process    syscall.Handle
}

// createProcess starts exe and returns its process handle. It calls CreateProcess
// directly because os/exec always redirects standard handles, to NUL if nothing
// else is given. That would leave a new console blank and unable to read input.
func createProcess(exe string, args []string, dir string, flags uint32) (syscall.Handle, error) {
	parts := []string{syscall.EscapeArg(exe)}
	for _, arg := range args {
		parts = append(parts, syscall.EscapeArg(arg))
	}
	cmdline, err := syscall.UTF16PtrFromString(strings.Join(parts, " "))
	if err != nil {
		return 0, err
	}
	app, err := syscall.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	var workDir *uint16
	if dir != "" {
		if workDir, err = syscall.UTF16PtrFromString(dir); err != nil {
			return 0, err
		}
	}
	startup := syscall.StartupInfo{}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	var info syscall.ProcessInformation
	err = syscall.CreateProcess(app, cmdline, nil, nil, false, flags, nil, workDir, &startup, &info)
	if err != nil {
		return 0, err
	}
	syscall.CloseHandle(info.Thread)
	return info.Process, nil
}

// startApp starts exe from its own folder and returns its process handle, which
// can be 0 if the shell doesn't report one. Exes that demand admin rights can't
// start through CreateProcess, so those go through ShellExecuteEx, which shows the
// UAC prompt the app is asking for.
func startApp(exe string) (syscall.Handle, error) {
	h, err := createProcess(exe, nil, filepath.Dir(exe), 0)
	if errors.Is(err, errorElevationRequired) {
		return startElevated(exe)
	}
	return h, err
}

func startElevated(exe string) (syscall.Handle, error) {
	file, err := syscall.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	dir, err := syscall.UTF16PtrFromString(filepath.Dir(exe))
	if err != nil {
		return 0, err
	}
	info := shellExecuteInfo{mask: seeMaskNoCloseProcess, file: file, directory: dir, show: swShowNormal}
	info.size = uint32(unsafe.Sizeof(info))
	ok, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return 0, fmt.Errorf("start %s: %w", exe, callErr)
	}
	return info.process, nil
}

// runInNewConsole starts exe with its own console, waits, and returns its exit code.
func runInNewConsole(exe string, args ...string) (uint32, error) {
	h, err := createProcess(exe, args, "", createNewConsole)
	if err != nil {
		return 0, err
	}
	defer syscall.CloseHandle(h)
	if _, err := syscall.WaitForSingleObject(h, syscall.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	err = syscall.GetExitCodeProcess(h, &code)
	return code, err
}
