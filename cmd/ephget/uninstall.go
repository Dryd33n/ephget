package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unsafe"

	"github.com/Ephget/ephget/internal/footprint"
	"github.com/Ephget/ephget/internal/launch"
	"github.com/Ephget/ephget/internal/paths"
	"github.com/Ephget/ephget/internal/reap"
	"github.com/Ephget/ephget/internal/schedule"
	"github.com/Ephget/ephget/internal/shortcut"
	"github.com/Ephget/ephget/internal/state"
	"github.com/Ephget/ephget/internal/ui"
)

const createNoWindow = 0x08000000

// dataItems is everything ephget keeps in its folder. Only these are deleted, so a
// mistaken EPHGET_HOME can't cost anyone a folder full of their own files.
var dataItems = []string{"config.json", "index.json.gz", "index.json.gz.etag", "state.json", "state.json.lock",
	"state.json.corrupt", "apps", "prefs", "icons", "tmp"}

func cmdUninstall(layout paths.Layout, args []string) error {
	if len(args) != 0 {
		return errors.New("usage: ephget uninstall")
	}
	realInstall := os.Getenv("EPHGET_HOME") == ""
	st, err := state.Store{Path: layout.State()}.Read()
	if err != nil {
		return err
	}
	for id, e := range st {
		if e.Path != "" && launch.Running(filepath.Join(layout.Home, e.Path)) {
			return fmt.Errorf("%s is open. Close it, then run uninstall again", id)
		}
	}

	stubs := shortcut.FindAny(reap.ScanFolders(layout))
	var notFound []string
	for _, e := range st {
		for _, path := range e.Shortcuts {
			link, err := shortcut.Read(path)
			switch {
			case err == nil && strings.HasPrefix(link.Description, shortcut.MarkerPrefix):
				if !slices.ContainsFunc(stubs, func(s string) bool { return strings.EqualFold(s, path) }) {
					stubs = append(stubs, path)
				}
			case err != nil && !slices.ContainsFunc(stubs, func(s string) bool { return strings.EqualFold(s, path) }):
				notFound = append(notFound, path)
			}
		}
	}

	fmt.Println(ui.Out.Bold("This removes:"))
	fmt.Printf("  %d added apps and their downloads, in %s\n", len(st), layout.Home)
	fmt.Println("  settings ephget saved for apps that aren't downloaded (prefs folder)")
	for _, stub := range stubs {
		ui.Detail("shortcut " + stub)
	}
	var settings []string
	for _, e := range st {
		if !e.KeepPrefs {
			settings = append(settings, e.Footprint...)
		}
	}
	for _, entry := range settings {
		ui.Detail("settings " + entry)
	}
	if realInstall {
		fmt.Println("  the", schedule.TaskName, "scheduled task and the PATH entry")
	}
	fmt.Println("Settings of apps with keep preferences on stay where they are, outside this folder.")
	fmt.Print(ui.Out.Bold("Uninstall ephget?") + " [y/N] ")
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if !strings.EqualFold(strings.TrimSpace(answer), "y") {
		fmt.Println("Nothing was changed.")
		return nil
	}

	var problems []string
	note := func(err error) {
		if err != nil {
			problems = append(problems, err.Error())
		}
	}
	note(footprint.Current().DeleteAll(settings))
	for _, stub := range stubs {
		note(os.Remove(stub))
	}
	if realInstall {
		if programs, err := shortcut.Programs(); err == nil {
			os.Remove(filepath.Join(programs, "ephget")) // only succeeds when empty
		}
		if _, err := schedule.Target(schedule.TaskName); err == nil {
			note(schedule.Delete(schedule.TaskName))
		}
		note(removeFromUserPath(layout.Home))
	}
	for _, item := range dataItems {
		note(os.RemoveAll(filepath.Join(layout.Home, item)))
	}
	note(removeProgram(layout.Home))

	for _, problem := range problems {
		ui.Warnf("couldn't finish: %s", problem)
	}
	if len(notFound) > 0 {
		fmt.Println("These shortcuts weren't found. If they still exist, delete them yourself:")
		for _, path := range notFound {
			fmt.Println(" ", path)
		}
	}
	if len(problems) > 0 {
		return errReported
	}
	ui.Success("ephget is uninstalled.")
	return nil
}

// removeProgram deletes the folder and the exes in it. Windows won't delete a running
// exe, so if this one is in the folder, a hidden cmd finishes the job after it exits.
// A copy running from anywhere else, such as a build folder, is left alone.
func removeProgram(home string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Dir(self), filepath.Clean(home)) {
		os.Remove(home) // only succeeds when empty
		return nil
	}
	gui := filepath.Join(home, "ephgetw.exe")
	// cmd would expand % and end the string at ", so such a path can't be passed safely.
	if strings.ContainsAny(home, `%"`) {
		return fmt.Errorf("delete %s yourself once this window closes", home)
	}
	script := fmt.Sprintf(`/c ping -n 3 127.0.0.1 >nul & del /f /q "%s" "%s" & rmdir "%s"`, self, gui, home)
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"))
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       `cmd.exe ` + script,
		CreationFlags: createNoWindow,
		HideWindow:    true,
	}
	cmd.Dir = os.TempDir() // cmd's working folder can't be the one it deletes
	return cmd.Start()
}

const (
	hwndBroadcast    = 0xFFFF
	wmSettingChange  = 0x1A
	smtoAbortIfHung  = 2
	keySetValue      = 0x0002
	regExpandSZ      = 2
	settingChangeMax = 5000
)

// withoutPathEntry returns the PATH value without dir, and whether anything changed.
func withoutPathEntry(value, dir string) (string, bool) {
	dir = strings.TrimRight(dir, `\`)
	parts := strings.Split(value, ";")
	kept := slices.DeleteFunc(slices.Clone(parts), func(part string) bool {
		return part != "" && strings.EqualFold(strings.TrimRight(strings.TrimSpace(part), `\`), dir)
	})
	return strings.Join(kept, ";"), len(kept) != len(parts)
}

func removeFromUserPath(dir string) error {
	subkey, _ := syscall.UTF16PtrFromString("Environment")
	name, _ := syscall.UTF16PtrFromString("Path")
	var key syscall.Handle
	if err := syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, subkey, 0, syscall.KEY_READ|keySetValue, &key); err != nil {
		return fmt.Errorf("open the PATH setting: %w", err)
	}
	defer syscall.RegCloseKey(key)
	var kind, size uint32
	if err := syscall.RegQueryValueEx(key, name, nil, &kind, nil, &size); err != nil || size == 0 {
		return nil // no user PATH, so nothing to remove
	}
	buf := make([]uint16, size/2+1)
	if err := syscall.RegQueryValueEx(key, name, nil, &kind, (*byte)(unsafe.Pointer(&buf[0])), &size); err != nil {
		return fmt.Errorf("read the PATH setting: %w", err)
	}
	updated, changed := withoutPathEntry(syscall.UTF16ToString(buf), dir)
	if !changed {
		return nil
	}
	data, err := syscall.UTF16FromString(updated)
	if err != nil {
		return err
	}
	advapi32 := syscall.NewLazyDLL("advapi32.dll")
	code, _, _ := advapi32.NewProc("RegSetValueExW").Call(uintptr(key), uintptr(unsafe.Pointer(name)), 0, uintptr(kind),
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)*2))
	if code != 0 {
		return fmt.Errorf("update the PATH setting: %w", syscall.Errno(code))
	}
	// New terminals pick up the change once everything has been told about it.
	environment, _ := syscall.UTF16PtrFromString("Environment")
	var result uintptr
	syscall.NewLazyDLL("user32.dll").NewProc("SendMessageTimeoutW").Call(hwndBroadcast, wmSettingChange, 0,
		uintptr(unsafe.Pointer(environment)), smtoAbortIfHung, settingChangeMax, uintptr(unsafe.Pointer(&result)))
	return nil
}
