// Command ephget is the terminal client, and the only build that uses the network.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/Dryd33n/ephget/internal/config"
	"github.com/Dryd33n/ephget/internal/fetch"
	"github.com/Dryd33n/ephget/internal/index"
	"github.com/Dryd33n/ephget/internal/launch"
	"github.com/Dryd33n/ephget/internal/paths"
	"github.com/Dryd33n/ephget/internal/reap"
	"github.com/Dryd33n/ephget/internal/schedule"
	"github.com/Dryd33n/ephget/internal/shortcut"
	"github.com/Dryd33n/ephget/internal/state"
	"github.com/Dryd33n/ephget/internal/ttl"
)

const usage = `Usage:
  ephget search <query>
  ephget add <id> [--ttl 7d] [--keep-prefs | --no-keep-prefs] [--pin] [--dir <folder>] [--no-start-menu]
  ephget set <id> [--ttl 1h] [--keep-prefs | --no-keep-prefs] [--pin | --no-pin]
  ephget remove <id>
  ephget list
  ephget config [--ttl 7d] [--keep-prefs | --no-keep-prefs] [--shortcut-dir <folder>] [--start-menu | --no-start-menu]
  ephget uninstall
`

const maxSearchResults = 25

func main() {
	// Closing the console arrives as SIGTERM. Cancelling lets a download clean up
	// tmp\ in the few seconds Windows allows.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		if !errors.Is(err, errReported) {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		stop()
		os.Exit(1)
	}
}

var errReported = errors.New("already reported")

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return errReported
	}
	layout, err := paths.Default()
	if err != nil {
		return err
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(usage)
		return nil
	}
	// Registering the task only to delete it again would be silly.
	if args[0] != "uninstall" {
		ensureTask()
	}
	switch args[0] {
	case "search":
		return cmdSearch(ctx, layout, args[1:])
	case "add":
		return cmdAdd(ctx, layout, args[1:])
	case "remove":
		return cmdRemove(layout, args[1:])
	case "list":
		return cmdList(layout, args[1:])
	case "fetch":
		return cmdFetch(ctx, layout, args[1:])
	case "set":
		return cmdSet(ctx, layout, args[1:])
	case "uninstall":
		return cmdUninstall(layout, args[1:])
	case "config":
		return cmdConfig(layout, args[1:])
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n%s", args[0], usage)
	return errReported
}

// ensureTask heals a deleted or out-of-date scheduled task. Apps still work without
// it, so a failure is only a warning. EPHGET_HOME marks a test or sample run, which
// must not touch the real task.
func ensureTask() {
	if os.Getenv("EPHGET_HOME") != "" {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	target := filepath.Join(filepath.Dir(self), "ephgetw.exe")
	if _, err := os.Stat(target); err != nil {
		return
	}
	if err := schedule.Ensure(schedule.TaskName, target); err != nil {
		fmt.Fprintln(os.Stderr, "warning: apps won't expire on schedule:", err)
	}
}

// parse lets flags appear before or after positional arguments, which the flag
// package alone doesn't allow.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func cmdSearch(ctx context.Context, layout paths.Layout, args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) == 0 {
		return errors.New("usage: ephget search <query>")
	}
	client, err := fetch.New(layout, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	idx, err := client.LoadIndex(ctx)
	if err != nil {
		return err
	}
	hits := idx.Search(strings.Join(positional, " "))
	if len(hits) == 0 {
		fmt.Println("No matching apps.")
		return nil
	}
	shown := hits[:min(len(hits), maxSearchResults)]
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tVERSION\tDESCRIPTION")
	for _, p := range shown {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.ID, p.Name, p.Version, clip(p.Description, 60))
	}
	w.Flush()
	if len(hits) > len(shown) {
		fmt.Printf("...and %d more. Try a longer search.\n", len(hits)-len(shown))
	}
	return nil
}

func clip(s string, limit int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= limit {
		return string(r)
	}
	return string(r[:limit-3]) + "..."
}

func cmdAdd(ctx context.Context, layout paths.Layout, args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	ttlFlag := fs.String("ttl", "", "")
	keep := fs.Bool("keep-prefs", false, "")
	noKeep := fs.Bool("no-keep-prefs", false, "")
	pinFlag := fs.Bool("pin", false, "")
	dir := fs.String("dir", "", "")
	noStart := fs.Bool("no-start-menu", false, "")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("usage: ephget add <id> [--ttl 7d] [--keep-prefs | --no-keep-prefs] [--pin] [--dir <folder>] [--no-start-menu]")
	}
	if *keep && *noKeep {
		return errors.New("use only one of --keep-prefs and --no-keep-prefs")
	}
	if *ttlFlag != "" {
		if _, err := ttl.Parse(*ttlFlag); err != nil {
			return err
		}
	}

	cfg, err := config.Load(layout.Config())
	if err != nil {
		return err
	}
	if _, err := os.Stat(layout.Config()); errors.Is(err, os.ErrNotExist) {
		if err := config.Save(layout.Config(), cfg); err != nil {
			return err
		}
	}

	client, err := fetch.New(layout, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	idx, err := client.LoadIndex(ctx)
	if err != nil {
		return err
	}
	pkg := idx.Find(positional[0])
	if pkg == nil {
		return fmt.Errorf("no app with the ID %q. Try: ephget search %s", positional[0], positional[0])
	}
	if !paths.SafeName(pkg.ID) {
		return fmt.Errorf("the index has an unsafe app ID %q", pkg.ID)
	}
	if _, err := pkg.SelectInstaller(client.Machine); err != nil {
		return err
	}

	store := state.Store{Path: layout.State()}
	st, err := store.Read()
	if err != nil {
		return err
	}
	// An app that's being re-added keeps its settings unless they are given again.
	appTTL, keepPrefs := cfg.TTL, cfg.KeepPrefs
	var pin *state.Pin
	if e := st[pkg.ID]; e != nil {
		appTTL, keepPrefs, pin = e.TTL, e.KeepPrefs, e.Pin
	}
	if *pinFlag {
		if pin, err = fetch.ResolvePin(layout.Home, st[pkg.ID], pkg, client.Machine); err != nil {
			return fmt.Errorf("%s: %w", pkg.ID, err)
		}
	}
	if *ttlFlag != "" {
		appTTL = *ttlFlag
	}
	if *keep || *noKeep {
		keepPrefs = *keep
	}

	folders, err := shortcutFolders(cfg, *dir, *noStart)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	target := filepath.Join(filepath.Dir(self), "ephgetw.exe")
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("ephgetw.exe must be next to ephget.exe: %w", err)
	}
	link := shortcut.Link{
		Target:      target,
		Arguments:   shortcut.Arguments(pkg.ID, appTTL, keepPrefs, pinVersion(pin)),
		Description: shortcut.Marker(pkg.ID),
		WorkingDir:  filepath.Dir(target),
	}
	if _, err := os.Stat(layout.Icon(pkg.ID)); err == nil { // kept from an earlier download
		link.Icon = layout.Icon(pkg.ID)
	}

	var created []string
	for _, folder := range folders {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			return err
		}
		path := filepath.Join(folder, shortcut.FileName(pkg.Name, pkg.ID))
		if err := checkNotForeign(path, pkg.ID); err != nil {
			return err
		}
		if err := shortcut.Create(path, link); err != nil {
			return err
		}
		created = append(created, path)
	}
	err = store.Update(func(st state.State) error {
		e := st[pkg.ID]
		if e == nil {
			e = &state.Entry{}
			st[pkg.ID] = e
		}
		e.TTL, e.KeepPrefs, e.Pin, e.Name = appTTL, keepPrefs, pin, pkg.Name
		for _, path := range created {
			e.AddShortcut(path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("Added %s. It downloads the first time you open it.\n", pkg.Name)
	for _, path := range created {
		fmt.Println(" ", path)
	}
	return nil
}

func pinVersion(pin *state.Pin) string {
	if pin == nil {
		return ""
	}
	return pin.Version
}

func shortcutFolders(cfg config.Config, dir string, noStartMenu bool) ([]string, error) {
	var main string
	var err error
	switch {
	case dir != "":
		main, err = filepath.Abs(dir)
	case cfg.ShortcutDir != "":
		main = cfg.ShortcutDir
	default:
		main, err = shortcut.Desktop()
	}
	if err != nil {
		return nil, err
	}
	folders := []string{main}
	if cfg.StartMenu && !noStartMenu {
		programs, err := shortcut.Programs()
		if err != nil {
			return nil, err
		}
		folders = append(folders, filepath.Join(programs, "ephget"))
	}
	return folders, nil
}

// checkNotForeign refuses to replace a shortcut that isn't this app's stub.
func checkNotForeign(path, id string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	existing, err := shortcut.Read(path)
	if err != nil || existing.Description != shortcut.Marker(id) {
		return fmt.Errorf("%s already exists and isn't this app's shortcut. Move it, or use --dir", path)
	}
	return nil
}

func cmdRemove(layout paths.Layout, args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: ephget remove <id>")
	}
	id := args[0]
	r, err := reap.Remove(layout, id, reap.ScanFolders(layout))
	switch {
	case errors.Is(err, reap.ErrNotAdded):
		return fmt.Errorf("%s isn't added. See: ephget list", id)
	case errors.Is(err, reap.ErrRunning):
		return fmt.Errorf("%s is open, so nothing was removed. Close it and try again", id)
	case errors.Is(err, reap.ErrDownloading):
		return fmt.Errorf("%s is downloading, so nothing was removed. Close its window or wait, then try again", id)
	}
	if r != nil {
		fmt.Printf("Removed %s.\n", r.ID)
		for _, path := range r.Deleted {
			fmt.Println("  deleted", path)
		}
	}
	if err != nil {
		return err
	}
	if len(r.Undeleted) > 0 {
		for _, path := range r.Undeleted {
			fmt.Fprintln(os.Stderr, "couldn't delete", path)
		}
		fmt.Fprintln(os.Stderr, "Delete these yourself. If one is opened, it downloads the app again.")
		return errReported
	}
	return nil
}

func cmdSet(ctx context.Context, layout paths.Layout, args []string) error {
	fs := flag.NewFlagSet("set", flag.ContinueOnError)
	ttlFlag := fs.String("ttl", "", "")
	keep := fs.Bool("keep-prefs", false, "")
	noKeep := fs.Bool("no-keep-prefs", false, "")
	pinFlag := fs.Bool("pin", false, "")
	noPin := fs.Bool("no-pin", false, "")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 || (*ttlFlag == "" && !*keep && !*noKeep && !*pinFlag && !*noPin) {
		return errors.New("usage: ephget set <id> [--ttl 1h] [--keep-prefs | --no-keep-prefs] [--pin | --no-pin]")
	}
	if *keep && *noKeep {
		return errors.New("use only one of --keep-prefs and --no-keep-prefs")
	}
	if *pinFlag && *noPin {
		return errors.New("use only one of --pin and --no-pin")
	}
	if *ttlFlag != "" {
		if _, err := ttl.Parse(*ttlFlag); err != nil {
			return err
		}
	}
	store := state.Store{Path: layout.State()}
	st, err := store.Read()
	if err != nil {
		return err
	}
	id := positional[0]
	for known := range st {
		if strings.EqualFold(known, id) {
			id = known
		}
	}
	e := st[id]
	if e == nil {
		return fmt.Errorf("%s isn't added. See: ephget list", id)
	}
	appTTL, keepPrefs := e.TTL, e.KeepPrefs
	if *ttlFlag != "" {
		appTTL = *ttlFlag
	}
	if *keep || *noKeep {
		keepPrefs = *keep
	}
	pin := e.Pin
	if *noPin {
		pin = nil
	}
	if *pinFlag {
		var pkg *index.Package
		machine := ""
		if !e.Cached(layout.Home) || e.Source == nil { // otherwise no catalog is needed
			client, err := fetch.New(layout, os.Stdout, os.Stderr)
			if err != nil {
				return err
			}
			idx, err := client.LoadIndex(ctx)
			if err != nil {
				return err
			}
			pkg, machine = idx.Find(id), client.Machine
		}
		if pin, err = fetch.ResolvePin(layout.Home, e, pkg, machine); err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
	}

	stubs := reap.FindStubs(id, e.Shortcuts, reap.ScanFolders(layout))
	var failed []string
	for _, stub := range stubs {
		if err := shortcut.SetArguments(stub, shortcut.Arguments(id, appTTL, keepPrefs, pinVersion(pin))); err != nil {
			failed = append(failed, stub)
		}
	}
	err = store.Update(func(st state.State) error {
		if e := st[id]; e != nil {
			e.TTL, e.KeepPrefs, e.Pin = appTTL, keepPrefs, pin
			for _, stub := range stubs {
				e.AddShortcut(stub)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("%s: TTL %s, keep preferences %t", id, appTTL, keepPrefs)
	if pin != nil {
		fmt.Printf(", pinned to %s", pin.Version)
	}
	fmt.Println(". This applies from the next cleanup.")
	if len(failed) > 0 {
		// State holds the new settings and is what's used; a stub only matters if
		// state is lost, when it would bring back the old ones.
		fmt.Fprintln(os.Stderr, "warning: couldn't update:", strings.Join(failed, ", "))
	}
	return nil
}

func cmdConfig(layout paths.Layout, args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	ttlFlag := fs.String("ttl", "", "")
	keep := fs.Bool("keep-prefs", false, "")
	noKeep := fs.Bool("no-keep-prefs", false, "")
	dir := fs.String("shortcut-dir", "", "")
	startMenu := fs.Bool("start-menu", false, "")
	noStartMenu := fs.Bool("no-start-menu", false, "")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("usage: ephget config [--ttl 7d] [--keep-prefs | --no-keep-prefs] [--shortcut-dir <folder>] [--start-menu | --no-start-menu]")
	}
	if *keep && *noKeep {
		return errors.New("use only one of --keep-prefs and --no-keep-prefs")
	}
	if *startMenu && *noStartMenu {
		return errors.New("use only one of --start-menu and --no-start-menu")
	}
	cfg, err := config.Load(layout.Config())
	if err != nil {
		return err
	}
	if *ttlFlag != "" {
		if _, err := ttl.Parse(*ttlFlag); err != nil {
			return err
		}
		cfg.TTL = *ttlFlag
	}
	if *keep || *noKeep {
		cfg.KeepPrefs = *keep
	}
	if *startMenu || *noStartMenu {
		cfg.StartMenu = *startMenu
	}
	dirGiven := false
	fs.Visit(func(f *flag.Flag) { dirGiven = dirGiven || f.Name == "shortcut-dir" })
	if dirGiven {
		cfg.ShortcutDir = ""
		if *dir != "" { // an empty value goes back to the Desktop
			if cfg.ShortcutDir, err = filepath.Abs(*dir); err != nil {
				return err
			}
		}
	}
	if len(args) > 0 {
		if err := config.Save(layout.Config(), cfg); err != nil {
			return err
		}
	}
	shortcutDir := cfg.ShortcutDir
	if shortcutDir == "" {
		shortcutDir = "Desktop"
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "TTL\t%s\n", cfg.TTL)
	fmt.Fprintf(w, "Keep preferences\t%t\n", cfg.KeepPrefs)
	fmt.Fprintf(w, "Shortcut folder\t%s\n", shortcutDir)
	fmt.Fprintf(w, "Start Menu\t%t\n", cfg.StartMenu)
	return w.Flush()
}

func cmdFetch(ctx context.Context, layout paths.Layout, args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	pin := fs.String("pin", "", "")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("usage: ephget fetch <id> [--pin <version>]")
	}
	client, err := fetch.New(layout, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	input := bufio.NewReader(os.Stdin)
	for {
		err := client.FetchPinned(ctx, positional[0], *pin)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "\nerror:", err)
		var unavailable *fetch.PinUnavailableError
		if errors.As(err, &unavailable) {
			fmt.Print("Unpin it and get the latest version? [y/N] ")
			answer, _ := input.ReadString('\n')
			if !strings.EqualFold(strings.TrimSpace(answer), "y") {
				return errReported
			}
			if err := unpin(layout, unavailable.ID); err != nil {
				return err
			}
			*pin = ""
			continue
		}
		fmt.Print("Retry? [y/N] ")
		answer, _ := input.ReadString('\n')
		if !strings.EqualFold(strings.TrimSpace(answer), "y") {
			return errReported
		}
	}
}

// unpin clears the pin in state and in the app's stubs, which still name the version.
func unpin(layout paths.Layout, id string) error {
	store := state.Store{Path: layout.State()}
	var recorded []string
	appTTL, keepPrefs := "", false
	err := store.Update(func(st state.State) error {
		if e := st[id]; e != nil {
			e.Pin = nil
			recorded, appTTL, keepPrefs = e.Shortcuts, e.TTL, e.KeepPrefs
		}
		return nil
	})
	if err != nil || appTTL == "" {
		return err
	}
	for _, stub := range reap.FindStubs(id, recorded, reap.ScanFolders(layout)) {
		if err := shortcut.SetArguments(stub, shortcut.Arguments(id, appTTL, keepPrefs, "")); err != nil {
			fmt.Fprintln(os.Stderr, "warning: couldn't update:", stub)
		}
	}
	return nil
}

func cmdList(layout paths.Layout, args []string) error {
	if len(args) != 0 {
		return errors.New("usage: ephget list")
	}
	st, err := state.Store{Path: layout.State()}.Read()
	if err != nil {
		return err
	}
	if len(st) == 0 {
		fmt.Println("No apps added yet. Find one with: ephget search <query>")
		return nil
	}
	// list makes no network requests; a missing or outdated catalog just means
	// removed apps can't be flagged.
	idx, _ := index.Load(layout.Index())

	ids := make([]string, 0, len(st))
	for id := range st {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	now := time.Now()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tVERSION\tPIN\tLAST CLOSED\tREMOVED IN\tKEEP PREFS\tSHORTCUTS\tSETTINGS FOUND")
	for _, id := range ids {
		e := st[id]
		status := "stub"
		cached := e.Cached(layout.Home)
		open := cached && launch.Running(filepath.Join(layout.Home, e.Path))
		switch {
		case open:
			status = "open"
		case cached:
			status = "cached"
		}
		if idx != nil && idx.Find(id) == nil {
			status += " (not in catalog)"
		}
		version, removal := "-", "-"
		if cached {
			version = e.Version
		}
		if cached && !open { // an open app can't be removed, whatever the clock says
			removal = timeUntilRemoval(layout, e, now)
		}
		pin := "-"
		if e.Pin != nil {
			pin = e.Pin.Version
		}
		closed := "-"
		if open {
			closed = "open"
		} else if e.LastClosed != nil {
			closed = e.LastClosed.Local().Format("2006-01-02 15:04")
		} else if e.LastLaunched != nil {
			closed = "open or unknown"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%t\t%d\t%s\n", id, status, version, pin, closed, removal, e.KeepPrefs, len(e.Shortcuts), footprintText(e))
	}
	return w.Flush()
}

func footprintText(e *state.Entry) string {
	if len(e.Footprint) == 0 {
		return "-"
	}
	return strings.Join(e.Footprint, ", ")
}

// timeUntilRemoval follows the reaper's rule.
func timeUntilRemoval(layout paths.Layout, e *state.Entry, now time.Time) string {
	due, ok := reap.RemovalTime(layout, e)
	if !ok {
		return "-"
	}
	left := due.Sub(now)
	if left <= 0 {
		return "due"
	}
	if left >= 48*time.Hour {
		return fmt.Sprintf("%dd", int(left.Hours()/24))
	}
	if left >= time.Hour {
		return fmt.Sprintf("%dh", int(left.Hours()))
	}
	return fmt.Sprintf("%dm", int(left.Minutes())+1)
}
