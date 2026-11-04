package main

import "testing"

func TestWithoutPathEntry(t *testing.T) {
	dir := `C:\Users\a\AppData\Local\ephget`
	for _, c := range []struct {
		in, want string
		changed  bool
	}{
		{`C:\one;C:\Users\a\AppData\Local\ephget;C:\two`, `C:\one;C:\two`, true},
		{`c:\users\A\appdata\local\EPHGET\;C:\two`, `C:\two`, true},
		{`C:\Users\a\AppData\Local\ephget`, ``, true},
		{`C:\one;C:\Users\a\AppData\Local\ephget2;C:\two;`, `C:\one;C:\Users\a\AppData\Local\ephget2;C:\two;`, false},
		{`%USERPROFILE%\bin`, `%USERPROFILE%\bin`, false},
		{``, ``, false},
	} {
		got, changed := withoutPathEntry(c.in, dir)
		if got != c.want || changed != c.changed {
			t.Errorf("%q: got %q, %t", c.in, got, changed)
		}
	}
}
