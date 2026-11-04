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
	"github.com/Dryd33n/ephget/internal/paths"
	"github.com/Dryd33n/ephget/internal/reap"
	"github.com/Dryd33n/ephget/internal/shortcut"
	"github.com/Dryd33n/ephget/internal/state"
	"github.com/Dryd33n/ephget/internal/ttl"
)

const usage = `Usage:
  ephget search <query>
  ephget add <id> [--ttl 7d] [--keep-prefs | --no-keep-prefs] [--dir <folder>] [--no-start-menu]
  ephget remove <id>
  ephget list
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
	case "help", "--help", "-h":
		fmt.Print(usage)
		return nil
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n%s", args[0], usage)
	return errReported
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
	dir := fs.String("dir", "", "")
	noStart := fs.Bool("no-start-menu", false, "")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("usage: ephget add <id> [--ttl 7d] [--keep-prefs | --no-keep-prefs] [--dir <folder>] [--no-start-menu]")
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
	if e := st[pkg.ID]; e != nil {
		appTTL, keepPrefs = e.TTL, e.KeepPrefs
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
	prefsArg := "--no-keep-prefs"
	if keepPrefs {
		prefsArg = "--keep-prefs"
	}
	link := shortcut.Link{
		Target:      target,
		Arguments:   fmt.Sprintf("run %s --ttl %s %s", pkg.ID, appTTL, prefsArg),
		Description: shortcut.Marker(pkg.ID),
		WorkingDir:  filepath.Dir(target),
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
		e.TTL, e.KeepPrefs = appTTL, keepPrefs
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
	cfg, err := config.Load(layout.Config())
	if err != nil {
		return err
	}
	var scan []string
	if cfg.ShortcutDir != "" {
		scan = append(scan, cfg.ShortcutDir)
	}
	// EPHGET_HOME marks a test or sample run, which must leave the real folders alone.
	if os.Getenv("EPHGET_HOME") == "" {
		scan = append(scan, shortcut.SearchFolders()...)
	}
	r, err := reap.Remove(layout, id, scan)
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

func cmdFetch(ctx context.Context, layout paths.Layout, args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("usage: ephget fetch <id>")
	}
	client, err := fetch.New(layout, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	input := bufio.NewReader(os.Stdin)
	for {
		err := client.Fetch(ctx, positional[0])
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "\nerror:", err)
		fmt.Print("Retry? [y/N] ")
		answer, _ := input.ReadString('\n')
		if !strings.EqualFold(strings.TrimSpace(answer), "y") {
			return errReported
		}
	}
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
		if e.Cached(layout.Home) {
			status = "cached"
		}
		if idx != nil && idx.Find(id) == nil {
			status += " (not in catalog)"
		}
		version, removal := "-", "-"
		if e.Cached(layout.Home) {
			version = e.Version
			removal = timeUntilRemoval(e, now)
		}
		pin := "-"
		if e.Pin != nil {
			pin = e.Pin.Version
		}
		closed := "-"
		if e.LastClosed != nil {
			closed = e.LastClosed.Local().Format("2006-01-02 15:04")
		} else if e.LastLaunched != nil {
			closed = "open or unknown"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%t\t%d\t%s\n", id, status, version, pin, closed, removal, e.KeepPrefs, len(e.Shortcuts), footprint(e))
	}
	return w.Flush()
}

func footprint(e *state.Entry) string {
	if len(e.Footprint) == 0 {
		return "-"
	}
	return strings.Join(e.Footprint, ", ")
}

// timeUntilRemoval follows the reaper's rule: last_closed + ttl, falling back to
// last_launched. An app downloaded but never opened has no clock running yet.
func timeUntilRemoval(e *state.Entry, now time.Time) string {
	base := e.LastClosed
	if base == nil {
		base = e.LastLaunched
	}
	lifetime, err := ttl.Parse(e.TTL)
	if base == nil || err != nil {
		return "-"
	}
	left := base.Add(lifetime).Sub(now)
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
