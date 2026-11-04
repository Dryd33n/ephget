package paths

import "testing"

func TestSafeRelative(t *testing.T) {
	good := []string{"a.exe", `bin\a.exe`, "bin/a.exe", "./a.exe", "dir/", "a..b/c.d"}
	bad := []string{"", "..", `..\a`, "a/../b", `C:\a`, `\a`, "/a", `\\srv\a`, "a:b", "a.exe:evil",
		"CON", `x\NUL.txt`, ".. ", `.. \a`, "a. ", "a.", "a*", "a\x01"}
	for _, p := range good {
		if !SafeRelative(p) {
			t.Errorf("SafeRelative(%q) = false", p)
		}
	}
	for _, p := range bad {
		if SafeRelative(p) {
			t.Errorf("SafeRelative(%q) = true", p)
		}
	}
}

func TestSafeName(t *testing.T) {
	for _, n := range []string{"vlc", "7zip", "a.b-c_d+e", "3.0.24"} {
		if !SafeName(n) {
			t.Errorf("SafeName(%q) = false", n)
		}
	}
	for _, n := range []string{"", ".", "..", ".a", "a.", "a/b", `a\b`, "a b", "CON", "nul.txt"} {
		if SafeName(n) {
			t.Errorf("SafeName(%q) = true", n)
		}
	}
}
