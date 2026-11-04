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
	"unsafe"

	"github.com/Dryd33n/ephget/internal/launch"
	"github.com/Dryd33n/ephget/internal/paths"
)

func main() {
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
	if err := fs.Parse(args[2:]); err != nil {
		return fmt.Errorf("this shortcut has an invalid argument: %w", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("this shortcut has an unexpected argument: %q", fs.Arg(0))
	}
	if *keep && *noKeep {
		return errors.New("this shortcut sets both --keep-prefs and --no-keep-prefs")
	}
	opts := launch.Options{TTL: *ttl}
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
