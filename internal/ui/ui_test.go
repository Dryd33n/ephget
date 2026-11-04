package ui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func sample(s Styler, width int) Table {
	return Table{
		Headers: []string{"ID", "STATUS", "PIN", "NOTES"},
		Rows: [][]Cell{
			{{"vlc", s.Bold}, {"cached", s.Cyan}, Plain("-"), Plain("A long description of what this app does for you")},
			{{"sumatrapdf", s.Bold}, {"stub", s.Dim}, Plain("-"), Plain("Short")},
		},
		Width: width, Styler: s, HideEmpty: []string{"PIN"},
	}
}

var ansi = strings.NewReplacer("\x1b[0m", "", "\x1b[1m", "", "\x1b[2m", "", "\x1b[31m", "", "\x1b[32m", "", "\x1b[33m", "", "\x1b[36m", "")

func TestPlainTableAligns(t *testing.T) {
	got := sample(Styler{}, 0).String()
	want := "" +
		"ID          STATUS  NOTES\n" +
		"vlc         cached  A long description of what this app does for you\n" +
		"sumatrapdf  stub    Short\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Colour codes must not change the layout, which is why this isn't text/tabwriter.
func TestColourDoesNotAffectAlignment(t *testing.T) {
	plain := sample(Styler{}, 0).String()
	coloured := sample(Styler{on: true}, 0).String()
	if !strings.Contains(coloured, "\x1b[") {
		t.Fatal("no colour")
	}
	if ansi.Replace(coloured) != plain {
		t.Fatalf("layout changed:\n%q\n%q", ansi.Replace(coloured), plain)
	}
}

func TestLastColumnShrinksToFit(t *testing.T) {
	got := sample(Styler{}, 50).String()
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if n := utf8.RuneCountInString(line); n >= 50 {
			t.Errorf("%d columns: %q", n, line)
		}
	}
	if !strings.Contains(got, "...") {
		t.Fatalf("not clipped:\n%s", got)
	}
}

func TestVeryNarrowConsoleStillShowsSomething(t *testing.T) {
	if got := sample(Styler{}, 10).String(); !strings.Contains(got, "A long de...") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestPinColumnAppearsWhenUsed(t *testing.T) {
	table := sample(Styler{}, 0)
	table.Rows[1][2] = Plain("1.0")
	if got := table.String(); !strings.Contains(got, "PIN") || !strings.Contains(got, "1.0") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestLinesHaveNoTrailingSpaces(t *testing.T) {
	for _, line := range strings.Split(sample(Styler{}, 0).String(), "\n") {
		if strings.TrimRight(line, " ") != line {
			t.Errorf("%q", line)
		}
	}
}

func TestEmptyTable(t *testing.T) {
	got := Table{Headers: []string{"A", "B"}}.String()
	if got != "A  B\n" {
		t.Fatalf("%q", got)
	}
}

func TestBar(t *testing.T) {
	off := Styler{}
	for fraction, want := range map[float64]string{
		0:   "░░░░░░░░░░",
		0.5: "█████░░░░░",
		1:   "██████████",
		1.5: "██████████", // a server that sent more than it promised
		-1:  "░░░░░░░░░░",
	} {
		if got := off.Bar(fraction, 10); got != want {
			t.Errorf("%v: %q", fraction, got)
		}
	}
}

func TestOffStylerAddsNothing(t *testing.T) {
	off := Styler{}
	if off.Red("x")+off.Bold("y")+off.Dim("z") != "xyz" {
		t.Fatal("styled while off")
	}
	if (Styler{on: true}).Green("") != "" {
		t.Fatal("styled empty text")
	}
}
