// Package ui styles ephget's terminal output. It stays off until Init finds a real
// console, so piped output and tests get plain text. NO_COLOR turns colour off.
package ui

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unicode/utf8"
	"unsafe"
)

const enableVirtualTerminal = 0x0004

var (
	kernel32                   = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleMode         = kernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBuffer = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

// Styler adds colour when it's on. Out is for stdout, and Err for stderr, because
// either can be redirected without the other.
type Styler struct{ on bool }

var (
	Out, Err Styler
	// terminal means stdout is a console, whether or not it can show colour.
	terminal bool
	mark     = "√" // in every console font
)

// Init looks at the console. Only ephget.exe calls it.
func Init() {
	terminal = isConsole(os.Stdout)
	if os.Getenv("NO_COLOR") == "" {
		Out.on = enableColor(os.Stdout)
		Err.on = enableColor(os.Stderr)
	}
	if os.Getenv("WT_SESSION") != "" {
		mark = "✓" // Windows Terminal draws this, while the old console's fonts may not
	}
}

func isConsole(f *os.File) bool {
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(f.Fd()), &mode) == nil
}

func enableColor(f *os.File) bool {
	var mode uint32
	handle := syscall.Handle(f.Fd())
	if syscall.GetConsoleMode(handle, &mode) != nil {
		return false
	}
	ok, _, _ := procSetConsoleMode.Call(uintptr(handle), uintptr(mode|enableVirtualTerminal))
	return ok != 0
}

// Interactive reports whether stdout is a console a person is reading.
func Interactive() bool { return terminal }

// Width is the console's width in columns, or 0 if stdout isn't a console.
func Width() int {
	if !terminal {
		return 0
	}
	// CONSOLE_SCREEN_BUFFER_INFO: the window's left and right are the 6th and 8th.
	var info [11]int16
	ok, _, _ := procGetConsoleScreenBuffer.Call(os.Stdout.Fd(), uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return 0
	}
	return int(info[7]-info[5]) + 1
}

func (s Styler) wrap(code, text string) string {
	if !s.on || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s Styler) Bold(text string) string   { return s.wrap("1", text) }
func (s Styler) Dim(text string) string    { return s.wrap("2", text) }
func (s Styler) Red(text string) string    { return s.wrap("31", text) }
func (s Styler) Green(text string) string  { return s.wrap("32", text) }
func (s Styler) Yellow(text string) string { return s.wrap("33", text) }
func (s Styler) Cyan(text string) string   { return s.wrap("36", text) }

// Success prints a line for something that worked.
func Success(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	if terminal {
		message = Out.Green(mark) + " " + message
	}
	fmt.Println(message)
}

// Detail prints a less important line below another.
func Detail(text string) { fmt.Println("  " + Out.Dim(text)) }

// Errorf prints an error to stderr.
func Errorf(format string, args ...any) {
	fmt.Fprintln(os.Stderr, Err.Red("error:"), fmt.Sprintf(format, args...))
}

// Warnf prints a warning to stderr.
func Warnf(format string, args ...any) {
	fmt.Fprintln(os.Stderr, Err.Yellow("warning:"), fmt.Sprintf(format, args...))
}

// Cell is one table value, with the style to show it in, if any.
type Cell struct {
	Text  string
	Style func(string) string
}

// Plain is a cell with no style.
func Plain(text string) Cell { return Cell{Text: text} }

// Table aligns columns. It can't use text/tabwriter, which counts colour codes as
// characters. Width is the room available, or 0 for no limit: if the rows don't fit,
// the last column is shortened.
type Table struct {
	Headers []string
	Rows    [][]Cell
	Width   int
	Styler  Styler
	// HideEmpty leaves out columns where every value is "-" or blank.
	HideEmpty []string
}

const columnGap = 2

// minLastColumn is the narrowest a shortened last column gets, because less isn't
// worth reading.
const minLastColumn = 12

// String renders the table.
func (t Table) String() string {
	columns := t.visibleColumns()
	widths := make([]int, len(columns))
	for i, column := range columns {
		widths[i] = utf8.RuneCountInString(t.Headers[column])
		for _, row := range t.Rows {
			widths[i] = max(widths[i], utf8.RuneCountInString(row[column].Text))
		}
	}
	last := len(columns) - 1
	if t.Width > 0 && last >= 0 {
		used := 0
		for _, width := range widths[:last] {
			used += width + columnGap
		}
		// One column spare, because filling the last one makes the console wrap.
		widths[last] = min(widths[last], max(t.Width-1-used, minLastColumn))
	}

	var out strings.Builder
	line := func(cells []Cell) {
		for i, cell := range cells {
			text := clip(cell.Text, widths[i])
			styled := text
			if cell.Style != nil {
				styled = cell.Style(text)
			}
			out.WriteString(styled)
			if i < last { // no padding after the last, so lines have no trailing spaces
				out.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(text)+columnGap))
			}
		}
		out.WriteByte('\n')
	}
	header := make([]Cell, len(columns))
	for i, column := range columns {
		header[i] = Cell{t.Headers[column], t.Styler.Dim}
	}
	line(header)
	for _, row := range t.Rows {
		cells := make([]Cell, len(columns))
		for i, column := range columns {
			cells[i] = row[column]
		}
		line(cells)
	}
	return out.String()
}

func (t Table) visibleColumns() []int {
	var columns []int
next:
	for column, header := range t.Headers {
		for _, hidden := range t.HideEmpty {
			if hidden == header && t.allEmpty(column) {
				continue next
			}
		}
		columns = append(columns, column)
	}
	return columns
}

func (t Table) allEmpty(column int) bool {
	for _, row := range t.Rows {
		if text := row[column].Text; text != "-" && text != "" {
			return false
		}
	}
	return true
}

func clip(text string, width int) string {
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

// Bar draws a progress bar of the given width, for a fraction from 0 to 1.
func (s Styler) Bar(fraction float64, width int) string {
	filled := int(min(max(fraction, 0), 1) * float64(width))
	return s.Cyan(strings.Repeat("█", filled)) + s.Dim(strings.Repeat("░", width-filled))
}
