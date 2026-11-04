// Command ephgetw is the target of every stub. It is built for the GUI subsystem,
// so it never opens a console, and it never uses the network.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
	"unsafe"

	"github.com/Dryd33n/ephget/internal/launch"
	"github.com/Dryd33n/ephget/internal/paths"
	"github.com/Dryd33n/ephget/internal/reap"
)

func main() {
	// The scheduled task has no one watching, so a reap never shows a dialog. Its
	// exit code appears as the task's last result.
	if len(os.Args) == 2 && os.Args[1] == "reap" {
		layout, err := paths.Default()
		if err == nil {
			err = reap.Pass(layout, time.Now())
		}
		if err != nil {
			os.Exit(1)
		}
		return
	}
	err := run(os.Args[1:])
	if err == nil || errors.Is(err, launch.ErrFetchFailed) {
		if err != nil {
			os.Exit(1)
		}
		return
	}
	showError(err.Error())
	os.Exit(1)
}

func run(args []string) error {
	if len(args) < 2 || args[0] != "run" {
		return errors.New("ephgetw is started by ephget's shortcuts. Use ephget.exe in a terminal instead.")
	}
	id := args[1]
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	ttl := fs.String("ttl", "", "")
	keep := fs.Bool("keep-prefs", false, "")
	noKeep := fs.Bool("no-keep-prefs", false, "")
	pin := fs.String("pin", "", "")
	if err := fs.Parse(args[2:]); err != nil {
		return fmt.Errorf("this shortcut has an invalid argument: %w", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("this shortcut has an unexpected argument: %q", fs.Arg(0))
	}
	if *keep && *noKeep {
		return errors.New("this shortcut sets both --keep-prefs and --no-keep-prefs")
	}
	opts := launch.Options{TTL: *ttl, Pin: *pin}
	if *keep || *noKeep {
		opts.KeepPrefs = keep
	}
	layout, err := paths.Default()
	if err != nil {
		return err
	}
	return launch.Run(layout, id, opts)
}

func showError(message string) {
	const mbIconError = 0x10
	text, err := syscall.UTF16PtrFromString(message)
	if err != nil {
		return
	}
	title, _ := syscall.UTF16PtrFromString("ephget")
	syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(0,
		uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), mbIconError)
}
