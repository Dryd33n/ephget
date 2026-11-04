package footprint

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"unsafe"
)

var (
	procRegCreateKeyEx = advapi32.NewProc("RegCreateKeyExW")
)

func createKey(t *testing.T, path string) {
	t.Helper()
	name, _ := syscall.UTF16PtrFromString(path)
	var key syscall.Handle
	code, _, _ := procRegCreateKeyEx.Call(uintptr(syscall.HKEY_CURRENT_USER), uintptr(unsafe.Pointer(name)), 0, 0, 0,
		uintptr(syscall.KEY_ALL_ACCESS), 0, uintptr(unsafe.Pointer(&key)), 0)
	if code != 0 {
		t.Fatalf("create %s: %v", path, syscall.Errno(code))
	}
	syscall.RegCloseKey(key)
}

// scope uses temporary folders and its own key, so the real ones are never touched.
func scope(t *testing.T) Scope {
	t.Helper()
	root := fmt.Sprintf(`Software\ephget-test-%d`, os.Getpid())
	createKey(t, root)
	t.Cleanup(func() { deleteKey(root) })
	return Scope{AppData: t.TempDir(), LocalAppData: t.TempDir(), Registry: root}
}

func TestFindsOnlyNewEntriesNamedExactlyAfterTheApp(t *testing.T) {
	s := scope(t)
	createKey(t, s.Registry+`\OldVendor`)
	os.MkdirAll(filepath.Join(s.AppData, "existing"), 0o755)
	before := s.Take()

	for _, dir := range []string{"VLC", "vlc-helper", "myvlc", "existing", "Unrelated"} {
		os.MkdirAll(filepath.Join(s.AppData, dir), 0o755)
	}
	os.MkdirAll(filepath.Join(s.LocalAppData, "VideoLAN Player"), 0o755)
	createKey(t, s.Registry+`\vlc`)
	createKey(t, s.Registry+`\OldVendor\VLC`)
	createKey(t, s.Registry+`\OldVendor\Other`)
	createKey(t, s.Registry+`\NewVendor\deep\vlc`)

	got := s.Find(before, []string{"vlc", "VideoLAN Player", "existing", ""})
	want := []string{
		`%APPDATA%\VLC`,
		`%LOCALAPPDATA%\VideoLAN Player`,
		`HKCU\` + s.Registry + `\OldVendor\VLC`,
		`HKCU\` + s.Registry + `\vlc`,
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestReservedNamesNeverMatch(t *testing.T) {
	s := scope(t)
	before := s.Take()
	os.MkdirAll(filepath.Join(s.AppData, "Microsoft"), 0o755)
	if got := s.Find(before, []string{"microsoft"}); len(got) != 0 {
		t.Fatalf("%q", got)
	}
	if err := s.Delete(`%APPDATA%\Microsoft`); err == nil {
		t.Fatal("deleted a reserved name")
	}
}

func TestDeleteRemovesWhatFindReturned(t *testing.T) {
	s := scope(t)
	before := s.Take()
	os.MkdirAll(filepath.Join(s.AppData, "vlc", "sub"), 0o755)
	createKey(t, s.Registry+`\vlc\deep\er`)
	found := s.Find(before, []string{"vlc"})
	if len(found) != 2 {
		t.Fatalf("%q", found)
	}
	if err := s.DeleteAll(found); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.AppData, "vlc")); err == nil {
		t.Error("folder survived")
	}
	if slices.Contains(subkeys(s.Registry), "vlc") {
		t.Error("key survived")
	}
	if err := s.DeleteAll(found); err != nil {
		t.Errorf("deleting again: %v", err)
	}
}

// state.json can be edited, and a bad entry mustn't reach a delete.
func TestDeleteRefusesAnythingFindCouldNotReturn(t *testing.T) {
	s := scope(t)
	canary := filepath.Join(s.AppData, "canary")
	os.MkdirAll(canary, 0o755)
	createKey(t, s.Registry+`\keep\a\b`)
	for _, entry := range []string{
		`C:\Windows`, `%APPDATA%`, `%APPDATA%\`, `%APPDATA%\..`, `%APPDATA%\..\x`, `%APPDATA%\canary\sub`,
		`%USERPROFILE%\Documents`, `..\canary`, ``,
		`HKCU\`, `HKCU\Software`, `HKCU\Other\key`, `HKLM\Software\x`,
		`HKCU\` + s.Registry, `HKCU\` + s.Registry + `\`, `HKCU\` + s.Registry + `\keep\a\b`, `HKCU\` + s.Registry + `\\keep`,
	} {
		if err := s.Delete(entry); err == nil {
			t.Errorf("no refusal for %q", entry)
		}
	}
	if _, err := os.Stat(canary); err != nil {
		t.Errorf("canary gone: %v", err)
	}
	if !slices.Contains(subkeys(s.Registry), "keep") {
		t.Error("key gone")
	}
}

func TestEmptyRegistryScopeLeavesTheRegistryAlone(t *testing.T) {
	s := Scope{AppData: t.TempDir()}
	if len(s.Take()) != 0 {
		t.Fatal("found something")
	}
	if err := s.Delete(`HKCU\Software\ephget-does-not-exist`); err != nil {
		t.Fatal(err)
	}
}
