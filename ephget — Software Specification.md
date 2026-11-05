# ephget — Software Specification

Nov 4, 2026 · @Dryden · Draft v0.1

## 1. Overview

ephget is a lightweight Windows tool that puts desktop shortcuts for portable apps on the desktop, downloads the app only when a shortcut is first clicked, and deletes it after a period of disuse. It uses the manifests in Scoop's community buckets as its catalog but never installs or runs Scoop, and never runs a manifest's scripts.

### Problem

Many apps are needed once or rarely: a disk utility, a file converter, a one-off viewer. Installing them leaves clutter, and existing package managers (WinGet, Scoop, Chocolatey) have no concept of expiry or on-demand stub shortcuts.

### Goals

- Desktop shortcuts that exist before the app does, and fetch it on first click.
- Automatic removal of apps not used within a configurable time-to-live (TTL), counted from when the app was last closed.
- A per-app "keep preferences for next time" toggle that either keeps or removes the app's settings when it expires.
- Search across all eligible GUI apps in the catalog.
- Minimal footprint: nothing running unless an ephget app is open, no admin rights, two small executables.
- Every download verified against the catalog's SHA256 hash.

### Non-goals

- Installing apps that use real installers (exe, msi, msix setup programs), including setup programs Scoop unpacks.
- Running Scoop or any PowerShell from a manifest. Apps whose scripts can't be skipped or replaced by a simple rename are left out.
- Command-line tools; a desktop shortcut doesn't suit them.
- Guaranteed removal of every setting an app writes to `%APPDATA%` or the registry; settings cleanup is best-effort (section 7).
- Version pinning or reproducible installs.
- Running on platforms other than Windows 10/11.
- Replacing WinGet or Scoop as a general package manager.

## 2. Glossary

| Term | Meaning |
| --- | --- |
| Stub shortcut | A `.lnk` that runs `ephgetw.exe run <id>` with the app's settings; exists whether or not the app is downloaded. |
| App ID | The Scoop manifest name, e.g. `vlc`. |
| Bucket | A Git repository of Scoop app manifests, e.g. `ScoopInstaller/Extras`. |
| Index | The prebuilt, compressed list of eligible apps published by the index pipeline. |
| Eligible app | A GUI app in a surveyed bucket that meets the rules in section 6. |
| Persist list | The files and folders a Scoop manifest says hold the app's settings inside its own folder. |
| TTL | Time-to-live: how long after the app was last closed it is kept before removal. Default 7 days; minimum 1 hour. |
| Reap | The cleanup pass that deletes apps past their TTL. |
| Keep preferences | Per-app toggle, default on. On: the app's settings survive reaping. Off: settings ephget detected are deleted with the app. |
| Settings footprint | The `%APPDATA%`, `%LOCALAPPDATA%` and `HKCU\Software` entries an app created on its first run, as detected by ephget. |
| Cached | The app's files are present locally and can launch without network access. |

## 3. User-facing behaviour

Users interact with ephget through a small CLI and through the desktop shortcuts it creates. A search GUI is out of scope for v1.

### Installation

There is no separate setup step. Installing ephget (a one-line PowerShell installer, or later a WinGet package) copies `ephget.exe` and `ephgetw.exe` to `%LOCALAPPDATA%\ephget\`, adds that folder to the user `PATH`, and registers the `ephget-reap` scheduled task. Every CLI command also checks that the task exists and runs this copy of `ephgetw.exe`, and re-registers it if not, so a deleted or out-of-date task heals itself.

### Commands

| Command | Behaviour |
| --- | --- |
| `ephget search <query>` | Fuzzy-matches ID, name and description in the index; prints ID, name, version and description. |
| `ephget add <id> [--ttl 7d] [--keep-prefs \| --no-keep-prefs] [--pin] [--dir <folder>] [--no-start-menu]` | Creates stub shortcuts. Does not download the app. |
| `ephget set <id> [--ttl 1h] [--keep-prefs \| --no-keep-prefs] [--pin \| --no-pin]` | Changes an app's settings and rewrites its known stubs to match. |
| `ephget remove <id>` | Deletes the app's stubs, cached files and state entry, wherever the stubs are. Refuses while the app is open or downloading. |
| `ephget list` | Shows shortcuts, cached apps, version and pin, last close and time until removal. |
| `ephget config [--ttl] [--keep-prefs \| --no-keep-prefs] [--shortcut-dir <folder>] [--start-menu \| --no-start-menu]` | Shows or sets the defaults used by `add`. |
| `ephget uninstall` | Removes all cached apps, stubs, the scheduled task, the PATH entry and the data folder. |
| `ephget fetch <id> [--pin <version>]` | Started by `ephgetw run` when the app isn't cached, not typed by users. Refreshes the index if stale, then downloads, verifies and extracts the app in a console window. |
| `ephgetw run <id> [--ttl] [--keep-prefs \| --no-keep-prefs] [--pin <version>]` | Stub target, not typed by users. Starts `ephget fetch` if not cached, launches the app, records when it closes. |
| `ephgetw reap` | Deletes cached apps past their TTL. Run by the scheduled task. |

Defaults: TTL 7 days, keep preferences on, shortcuts on the Desktop and in the Start Menu.

An app normally has two stubs, so removing it is a command rather than a job of finding both (F8).

### Expiry and preferences

- **TTL:** an app is removed once `now > last_closed + ttl`. Counting from close rather than launch means an app left open for days is never treated as idle. Default TTL is 7 days; accepted units are hours and days, minimum 1 hour.
- **Keep preferences for next time:** a per-app toggle. When on, the app's settings survive reaping, so a re-download starts where it left off: settings outside the app folder stay where they are, and those on its persist list are saved first and restored on the next download. When off, ephget deletes them along with the settings footprint it detected (section 7). Default: on.

There is no delete-on-exit mode. An accidental close would lose the app; a 1-hour TTL covers the same need safely.

Removing a cached app never removes its shortcut. The next click downloads the latest version again.

### First-run experience

Decided: option A. When a shortcut is clicked and the app is not cached, `ephgetw.exe` starts `ephget.exe` in a new console window, which shows its own progress. The options considered:

| Option | Pros | Cons |
| --- | --- | --- |
| A. Console window showing ephget's own output (download %, verify, extract), closing on success | Transparent; no UI code, since `ephget.exe` is already a console program; closing the window cancels | Plainer look; brief console flash on first run |
| B. Terminal running `winget install` | Shows WinGet's familiar output | Brings WinGet back as a runtime dependency; slower (source update); installs into WinGet's folders, which ephget would then have to track |
| C. Native TaskDialog with a progress bar | Most polished | More Win32 code; hides what is actually happening |

A gives the terminal-style transparency of B without making WinGet a runtime dependency. The window closes on success; on failure it stays open with the error and a retry prompt. Closing it cancels the download.

## 4. User flows

Every app moves through four states, and reaping returns it to a stub rather than removing it. An app ends when the user runs `ephget remove` or deletes all of its stubs.

```mermaid
stateDiagram-v2
    [*] --> Stub: ephget add
    Stub --> Downloading: shortcut clicked
    Downloading --> Stub: failed or cancelled
    Downloading --> Running: verified and extracted
    Running --> Cached: app closed
    Cached --> Running: shortcut clicked
    Cached --> Stub: TTL passed (reap)
    Stub --> [*]: ephget remove, or all stubs deleted (reap)
    Cached --> [*]: ephget remove, or all stubs deleted (reap)
```

### F1. Install ephget

1. User runs the one-line installer.
2. The installer shows the default shortcut locations, Desktop and Start Menu, and asks the user to confirm. They can paste a different folder to use instead of the Desktop, and can turn the Start Menu entries off.
3. `ephget.exe` and `ephgetw.exe` are copied to `%LOCALAPPDATA%\ephget\`, the answers are saved to `config.json`, the folder is added to the user `PATH`, and the `ephget-reap` task is registered.
4. `ephget` is available in any new terminal. Nothing else changes on the system.

### F2. Find and add an app

1. `ephget search vlc` lists matching portable apps (ID, name, version).
2. `ephget add vlc` (optionally `--ttl 1h`, `--no-keep-prefs`, `--dir <folder>`, `--no-start-menu`).
3. Shortcuts named after the app appear in the shortcut folder and, if enabled, the Start Menu, with ephget's generic icon. Nothing is downloaded.

### F3. First launch (not cached)

1. User double-clicks the shortcut.
2. `ephgetw` starts `ephget fetch` in a console window: refresh index if stale → download with progress → verify SHA256 → extract.
3. The shortcut's icon changes to the app's own icon.
4. The console closes and `ephgetw` starts the app, snapshotting settings locations first.
5. When the user closes the app, ephget records the close time and the detected settings footprint.

### F4. Cached launch

1. User double-clicks the shortcut.
2. The app starts with no visible ephget window and no network access.
3. On close, the close time is recorded.

### F5. Expiry

1. The hourly task finds an app whose last close is older than its TTL and which isn't running.
2. If keep preferences is on, the app's persist-list items are saved to `prefs\<id>\`. The app folder is then deleted. If keep preferences is off, its settings footprint is deleted too.
3. The shortcut stays, with the app's icon. The next click is F3; with keep preferences on, the app opens with its old settings.

### F6. Change an app's settings

1. `ephget set <id> --ttl 1h` or `--keep-prefs` / `--no-keep-prefs` updates that app.
2. `ephget config --ttl … --keep-prefs …` changes the defaults for future `add`s.
3. Changes apply from the next reap pass; nothing is downloaded or deleted immediately.

### F7. Check status

1. `ephget list` shows each shortcut: cached or not, version, last close, time until removal, keep-preferences setting and footprint.

### F8. Remove an app

1. User runs `ephget remove <id>`. It needs no network and works for an app that has left the catalog.
2. ephget moves `apps\<id>\` aside to `tmp\`. If that fails, the app is open: it says so and changes nothing. It also refuses while the app is downloading.
3. The app's stubs are deleted: its recorded paths, and any others the scan finds (Finding stubs). Only `.lnk` files with this app's marker are touched. A stub it can't delete is listed for the user.
4. The files and anything saved in `prefs\<id>\` are deleted, along with its settings outside the app folder if keep preferences is off, and the state entry and icon are dropped.

Deleting the shortcuts by hand also works. On the next reap pass (within the hour), ephget sees that none of the app's shortcuts exist and removes the app, whatever its TTL. Deleting only one of them leaves the app in place. Missing shortcuts may only have moved somewhere the scan can't see, or be on an unplugged drive, so this doesn't count as a request to lose settings: with keep preferences on, they stay in `prefs\<id>\`, and a shortcut that turns up gets them back on its next click. Only `ephget remove` clears them.

### F9. Uninstall ephget

1. `ephget uninstall` finds every stub (recorded paths, then a scan) and lists everything that will be removed, then asks for confirmation.
2. All cached apps, the found stubs, the scheduled task, the PATH entry and the data folder are deleted. Settings outside the data folder follow each app's keep-preferences setting; settings saved in `prefs\` go with the data folder, and the confirmation says so. It refuses while any app is open. Only ephget's own files are deleted, so anything else in the folder stays, and so does the folder. A running exe can't delete itself, so a hidden `cmd` deletes the two exes and the folder a few seconds after `ephget` exits.
3. Stubs that couldn't be found are listed by last known path so the user can delete them. Clicked after uninstall, they show Windows' standard "missing target" prompt, since ephgetw.exe is gone.

### Exception flows

| Situation | What the user sees | What ephget does |
| --- | --- | --- |
| Offline on first launch | Console shows the error and a retry prompt | Nothing changes; cached apps still work |
| Hash mismatch | Console error: download failed verification | Deletes the download; app stays a stub |
| App's exe missing after extraction (usually a manifest script that shouldn't have been skipped) | Console error naming the missing file | Deletes the extracted files; app stays a stub |
| User closes the console mid-download | Window closes | Cancels, cleans `tmp\`, app stays a stub |
| App removed from the index | Console error on first launch; `ephget list` flags it | A cached copy keeps working until it expires |
| Shortcut clicked twice during download | Second click waits on the first | Download lock; both launch once it finishes |
| Stub moved to another folder | Nothing; it keeps working | Next launch records its new path |
| Stub clicked with no state entry (moved, then its state was dropped) | Same as a first launch | Rebuilds the state entry from the stub's own arguments (ID, TTL, keep-prefs) |
| App launched outside the shortcut | Nothing | Not tracked; reaper treats it as just closed whenever it finds it running |
| PC shuts down with the app open | Nothing | No close time recorded; launch time used as fallback |
| TTL passes while the app is open | Nothing; `ephget list` shows it as open | The rename fails, so nothing is removed; the TTL counts from the real close |
| All of an app's stubs moved somewhere a scan can't see | Nothing; the stub keeps working | App removed, settings kept per the toggle; the next click downloads it and restores them |

## 5. System architecture

ephget has two parts: an index pipeline that runs in the cloud, and the client on the user's PC. The client is two builds of the same code: `ephget.exe` for the terminal, and `ephgetw.exe`, which the stubs and the scheduled task start without any console window.

```mermaid
flowchart TD
    subgraph cloud["GitHub (daily)"]
        buckets["Scoop buckets"] --> action["Index Action"]
        action --> index["index.json.gz release"]
    end

    subgraph pc["User's PC"]
        user["Terminal"] --> ephget["ephget.exe"]
        stub["Stub shortcuts"] --> ephgetw["ephgetw.exe"]
        task["ephget-reap task"] --> ephgetw
        ephgetw -- "app not cached" --> ephget
        ephget --> data["Data folder: state, apps, prefs, icons"]
        ephgetw --> data
    end

    index -- "refresh when stale" --> ephget
    hosts["Download hosts in the manifests"] -- "app download" --> ephget
```

The client only talks to the internet to refresh the index and to download an app that isn't cached, and only `ephget.exe` does either. `ephgetw.exe` never touches the network; a cached launch touches nothing but `state.json`.

## 6. Index pipeline

A scheduled GitHub Action builds the index once a day from the code in `index/`, and publishes it as a release asset of this repository. Clients never read Scoop's buckets directly.

### Source

- Shallow clones of the `ScoopInstaller` buckets `Main`, `Extras`, `Sysinternals` and `Games`, cached between runs. If two buckets have the same app ID, the first in that order wins.
- `Versions`, `Nonportable`, `PHP`, `Java` and the nerd-fonts bucket are excluded: they hold old releases, installers, runtimes and fonts rather than current desktop apps. `Nirsoft` is excluded too: its suites and password tools trip antivirus, which reflects on ephget.
- Each run diffs against the previous commit and re-parses only changed manifests; results for unchanged manifests carry over from a state file cached with the clones. A change to the indexer's code, a missing state file or a missing old commit causes a full parse, which takes seconds.

### Eligibility rules

An app is included if its manifest has an x64, arm64 or architecture-independent build where all of these hold:

1. **GUI app:** the manifest has a `shortcuts` entry, and the first one gives the exe to launch and the shortcut's name. The target must be an `.exe` inside the app folder, not a `.bat`, `.jar` or document, and must not need arguments. `/accepteula` is the one exception: it's dropped, so a Sysinternals tool shows its EULA once.
2. **One download:** a single URL, with a SHA256 hash. Manifests with only MD5 or SHA-1 hashes, or none, are excluded.
3. **Unpackable file:** a `.zip`, a real `.7z`, or an exe that runs as it is. Setup programs are excluded, including exes Scoop opens as 7z (`#/dl.7z`), `innosetup` apps and `.msi` files. So are manifests using `extract_to`, or more than one `extract_dir`.
4. **No dependencies:** the manifest has no `depends` field.
5. **No script needed:** see below.
6. **File still exists:** see Download checks.

**Manifest scripts are never run.** The indexer classifies each manifest's `installer`, `pre_install` and `post_install` scripts:

| Script | Treatment |
| --- | --- |
| None | Included. |
| Only prepares settings: creates config files, copies old settings, removes spare files | Skipped; included. |
| One simple rename of the exe to the launch name | Replaced by the installer's `rename` field; included. |
| Switches on portable mode: creates or edits a marker such as `portable.txt` | Excluded. Without it the app would quietly keep its settings outside its folder, which is the opposite of what ephget promises. |
| Runs a setup program, unpacks files, starts programs, edits the registry or downloads anything; moves or renames other program files; an `installer` with no script | Excluded. |

Moving or copying files out of `<name>.original` folders, the persist folder or user folders counts as preparing settings, even into a folder named `portable`: that is migrating old settings, not switching the mode on. Copying a wildcard or an exe or DLL into the app folder counts as moving program files. The classification uses keyword matching, so it can misjudge a script. The client checks for the exe after extraction and fails with a clear error if it's missing. Scripts that are skipped can leave different defaults: for example, an app's update check may stay on.

### Schema

The `packages` array, one object per app:

```json
{
  "id": "vlc",
  "name": "VLC media player",
  "bucket": "Extras",
  "version": "3.0.24",
  "description": "A free and open source cross-platform multimedia player.",
  "persist": ["portable"],
  "installers": [
    {
      "arch": "x64",
      "type": "7z",
      "url": "https://...",
      "sha256": "...",
      "exe": "vlc.exe",
      "extract_dir": "vlc-3.0.24",
      "rename": null
    }
  ]
}
```

`arch` is `x64`, `arm64` or `any`. `type` is `zip`, `7z` or `exe`. `extract_dir` is the folder inside the archive to use as the app's root. `rename` is either null or a `[glob, name]` pair. The example's values are illustrative. Licences and release notes are deliberately left out to keep the index small.

Manifests are untrusted, so the indexer also excludes apps whose `id` or `version` isn't safe as a folder name and any `exe` or `extract_dir` that isn't a literal path inside the app folder, and drops such `persist` items. A `persist` item given as a `[source, target]` pair keeps only the source. `name` is the last part of Scoop's shortcut name (`7-Zip\7-Zip File Manager` becomes `7-Zip File Manager`).

`url` keeps any Scoop `#/name` fragment, which HTTP clients don't send. For `type: exe`, the client saves the download as `exe`: the indexer has checked that this is the name the file would end up with, so `rename` needs no further action there. `extract_dir` is always null for that type.

### Download checks

A manifest's URL can die long before its manifest changes, and the user would otherwise find out on their first click. So the build asks each new or changed URL, every URL under suspicion and 200 others a day whether its file still exists, reading only the response's headers. All of them get looked at over about a week.

- An installer is dropped once its URL has answered 404 or 410 on two different days. An app with no installers left is excluded as `dead-url`.
- Timeouts, TLS errors, 403s and 5xx responses don't count. They're often temporary, or a host refusing GitHub's servers.
- If over 10% of a run's checks say "gone", the run counts for nothing: that's an outage, not hundreds of dead apps.
- Requests send the same `Referer` as the client (section 7), so a host that needs one doesn't look dead. The user agent names ephget honestly.

This can't catch a working URL that serves the wrong file, which only downloading and hashing would show. Those apps fail verification on first click.

### Publishing

- Artifact: `index.json.gz` attached to a rolling `index` release of `Dryd33n/ephget`, plus `index.json.gz.sha256` and `NOTICE.md`, which holds the buckets' licence notices. It is never marked as the latest release, because `releases/latest` is for ephget itself.
- Client URL: `https://github.com/Dryd33n/ephget/releases/download/index/index.json.gz`.
- Schema version in a top-level wrapper (`{"schema": 1, "generated": "<ISO time>", "packages": [...]}`) so clients can refuse an index format they don't understand.
- Target size: under 300 KB compressed. A build with 1,259 apps came to about 179 KB. The build fails and publishes nothing if the index is over 300 KB, has fewer than 1,000 apps, or has an entry with no URL, hash or exe path.

## 7. Client design

The client is one Go codebase built as two executables, following the `python.exe` / `pythonw.exe` pattern:

- `ephget.exe` — console subsystem. Every command a user types, plus `fetch` for first-run downloads. Behaves like any CLI: waits, returns exit codes, supports prompts and piping. The only build that uses the network.
- `ephgetw.exe` — GUI subsystem. Target of every stub and of the scheduled task, so neither ever flashes a console window. Handles only `run` and `reap`, never opens a console and never uses the network. When an app isn't cached, it hands the download to `ephget fetch`.

The two differ only in a build flag and a small entry point that exposes their commands; all logic lives in shared internal packages.

### Data layout

```
%LOCALAPPDATA%\ephget\
  ephget.exe         CLI (console)
  ephgetw.exe        stub and task target (GUI)
  config.json        defaults: TTL, keep-prefs, shortcut folder, Start Menu on/off
  index.json.gz      cached index + ETag
  state.json         per-app state (a cache; stubs are the source of truth)
  apps\<id>\<version>\  extracted app files
  prefs\<id>\        persist-list items saved while the app isn't cached
  icons\<id>.ico     extracted icons
  tmp\               in-progress downloads
```

No registry keys are written apart from the scheduled task.

### State file

```json
{
  "vlc": {
    "bucket": "Extras",
    "version": "3.0.24",
    "path": "apps\\vlc\\3.0.24",
    "exe": "vlc.exe",
    "persist": ["portable"],
    "last_launched": "2026-11-04T01:05:00Z",
    "last_closed": "2026-11-04T02:40:00Z",
    "ttl": "7d",
    "keep_prefs": true,
    "pin": {
      "version": "3.0.24",
      "url": "https://...",
      "sha256": "..."
    },
    "shortcuts": [
      "%USERPROFILE%\\Desktop\\VLC media player.lnk",
      "%APPDATA%\\Microsoft\\Windows\\Start Menu\\Programs\\ephget\\VLC media player.lnk"
    ],
    "footprint": [
      "%APPDATA%\\vlc",
      "HKCU\\Software\\VideoLAN"
    ]
  }
}
```

Writes go to a temp file followed by an atomic rename, guarded by a lock file, so concurrent launches cannot corrupt it.

### Launch path (`ephgetw run <id>`)

1. Read `state.json`. If the app is cached and its folder exists, go to step 3.
2. Start `ephget fetch <id>` in a new console window (`CREATE_NEW_CONSOLE`), passing `--pin` if the stub has it, and wait for it to exit. If a fetch for this app is already running, wait for that one instead of starting a second. If the fetch fails or the user closes its window, exit without launching.
3. If this is the app's first run, snapshot the settings locations (see Settings footprint).
4. Start the app with `CreateProcess`, then write `last_launched = now` and clear `last_closed`, and wait. An exe that demands admin rights can't start this way, so it goes through `ShellExecuteEx`, which shows its UAC prompt. The wait ends when no process whose image path is inside the app folder is still running, which covers apps that hand off to a child process and exit.
5. Write `last_closed = now`. On first run, diff the snapshot and store the footprint.
6. Run a reap pass, then exit.

A cached launch performs no network access and reads no index. While waiting, the launcher blocks on the handles of the app folder's processes and lists them again whenever one exits, so it finds children it didn't start: a few MB of memory and no CPU. A process it can see but not wait on, such as an elevated one, is checked for every 5 seconds instead.

### Download path (`ephget fetch <id>`)

1. Load the index (refresh first if older than 24 h) and pick the installer for the machine's architecture, preferring native over x64 emulation on arm64.
2. Download to `tmp\` with progress shown (section 3); verify SHA256. On mismatch, delete and fail. Like Scoop, requests send the file's folder as the `Referer`, except to SourceForge and PortableApps: some hosts answer with a web page if it's missing, and those two do if it's present.
3. Extract (zip, 7z) or move (exe) into `apps\<id>\<version>\`, using `extract_dir` as the root if set and applying `rename` if set. If the launch exe isn't there, delete the files and fail. Clear the Mark of the Web; extract the exe's icon to `icons\<id>.ico` and update the shortcut's icon.
4. Move any saved settings from `prefs\<id>\` back into the app folder. Record the version and path in `state.json` and exit 0. On failure, show the error and a retry prompt; if the user declines, exit non-zero.

### Reaper (`ephgetw reap`)

For each entry in `state.json`:

1. **Check for stubs.** If none of the recorded `shortcuts` exist, scan for the app's `ephget:<id>` marker (Finding stubs) in case the stub was moved but not yet clicked. If a stub is found, update its path; if none is, the user deleted the app: treat it as expired now and go to step 3.
2. Skip it if it isn't cached, or if `now <= last_closed + ttl`. If `last_closed` is missing (the launcher was killed or the PC shut down with the app open), use `last_launched`. If both are missing (downloaded, but it never started), use the app folder's modified time.
3. If `apps\<id>\` exists, try to rename it to `tmp\<id>.removing`. If the rename fails, the app is running: set `last_closed = now` and skip it. It retries for about a second, so a scanner briefly holding a file doesn't count as the app running, and an open app can't hold up the pass for long.
4. If `keep_prefs` is on, move each `persist` item that exists out of the renamed folder into `prefs\<id>\`, even if the stubs are missing. If that fails, put the folder back and try again next pass. Delete the renamed folder. If `keep_prefs` is off, also delete each footprint entry that still exists and any `prefs\<id>\`.
5. If the app was deleted by the user, drop the state entry and its icon. Otherwise keep the entry, minus the cache path, so the stub stays ready to relaunch.

An app whose download is in progress is skipped. Each pass also finishes deleting any `tmp\*.removing` folder that a crash left behind. A reap never shows a dialog; a failure appears as the scheduled task's last result.

The marker scan only runs when every recorded path is missing, which is rare, so the hourly pass normally stays a plain file read.

**Triggers:** the `ephget-reap` per-user scheduled task runs at logon and hourly, with "run as soon as possible after a missed start", so a 1-hour TTL is honoured within about an hour. Each run reads one JSON file and exits. A reap pass also runs after every app closes.

**Load:** each hourly run starts ephgetw.exe, reads a state file of a few KB, compares timestamps and exits. Measured with 30 cached apps and a 12.6 KB state file: about 12 ms in total, CPU below the timer's 16 ms resolution, 5.4 MB of memory, no network, and no disk writes unless something expired. That is well under a second of CPU per day. The task is set not to wake the PC and to run only while the user is logged on.

### Settings footprint

Many portable apps keep settings next to their exe, and reaping deletes that folder. The manifest's `persist` list names those items where it has one, so ephget saves them to `prefs\<id>\` and restores them on the next download. Settings in the app folder that aren't on the list are lost when the app is reaped.

For settings outside the app folder, ephget detects where an app keeps them by comparing before and after its first run.

1. Before the first launch, record the names of the top-level entries in `%APPDATA%`, `%LOCALAPPDATA%` and `HKCU\Software`, plus one level under each `HKCU\Software\<vendor>` key.
2. After the app closes, list them again. New entries are candidates.
3. Keep only candidates whose name equals the app's ID or name exactly, ignoring case. Partial matches don't count, and neither do names no single app owns, such as `Microsoft` or `Temp`.
4. Store the result in the state entry's `footprint`; `ephget list` shows it.

The footprint is always recorded, so the keep-preferences toggle can be changed later. Deleting is the risk here, since a wrong match destroys someone else's data, so detection deliberately misses things: settings written inside an existing shared folder, under another name (Etcher's `balena-etcher`), or only on a later run. Those stay behind, which is harmless. Stored entries are checked again before any delete, because `state.json` can be edited.

A state entry rebuilt from a stub has no footprint and never looks for one: the app may have run before the entry was lost, so its settings wouldn't look new. Such an app leaves its settings behind.

### Shortcuts and icons

- `ephget add` creates a `.lnk` in the shortcut folder (the Desktop unless the user chose another) and, unless disabled, in `Start Menu\Programs\ephget\`, via the Shell `IShellLink` COM interface. Target: `ephgetw.exe run <id> --ttl <ttl> --keep-prefs|--no-keep-prefs`. Its paths are stored in the state entry's `shortcuts`.
- The stub carries all of the app's settings in its arguments, so `state.json` is only a cache: a stub can always rebuild its entry.
- Until first run the shortcut uses ephget's own generic icon. After first extraction it points to `icons\<id>.ico`, which survives reaping.

### Finding stubs

Users can move or copy stubs anywhere, so ephget never relies only on the paths it created.

1. **Track on launch.** When Windows starts a program from a shortcut, it passes the `.lnk` path in the startup info (`STARTF_TITLEISLINKNAME`). Every `run` records that path in `shortcuts` and forgets recorded paths that no longer exist, so a moved stub replaces its old path the first time it is clicked.
2. **Identify by content.** Every stub's arguments start with `run <id>` and its description field carries an `ephget:<id>` marker, so any `.lnk` can be recognised without a lookup table.
3. **Scan when needed** (`remove`, `uninstall`, `set`, and the reaper's deleted-shortcut check). Check the recorded paths first; then scan the configured shortcut folder, the user and public Desktop, the Start Menu, the taskbar pin folder and the user's Documents and Downloads for `.lnk` files with the marker.
4. **Report what's left.** Stubs not found are listed by last known path for manual deletion.

### Index refresh

- Refreshed only by `ephget.exe`, during `search`, `add` and `fetch`, when the local copy is older than 24 h.
- Uses `If-None-Match` with the stored ETag; a 304 response costs no download.
- If refresh fails, the stale index is used and a warning is printed.

### Versions and updates

A cached app is never updated in place. Whenever an app has to be downloaded (first launch, or after it expired), ephget fetches the latest version in the index by default.

- `--pin` on `add` or `set` freezes the app at the version that is cached, or at the catalog's current version if none is. The stub's arguments gain `--pin <version>`, and the state entry's `pin` field stores that version's number, URL, SHA256, file type, exe, `extract_dir` and `rename`. Unpinned apps have no `pin` field.
- The index only lists latest versions, so a pinned re-download uses what's stored. If the state entry is gone, the version can only come from the index; if the index has moved on, it counts as unavailable. Looking it up in a bucket's Git history would take a lot of network and parsing code for a rare case, so v1 doesn't.
- If the pinned version can no longer be downloaded (publishers sometimes remove old files), the console says so and offers to unpin and get the latest.
- `--no-pin` returns the app to latest-on-download.

## 8. Non-functional requirements

Lightweight is the primary design constraint: when no ephget app is open, ephget uses no CPU, no memory and no network.

| ID | Requirement | Target |
| --- | --- | --- |
| NF-1 | No background process when no app is open | Only a waiting launcher per open app (a few MB, no CPU), plus the brief hourly reap |
| NF-2 | Cached launch overhead | < 50 ms from click to `CreateProcess` |
| NF-3 | Binary size | < 10 MB per exe, stripped (`-ldflags "-s -w"`) |
| NF-4 | Dependencies | Go standard library + direct Win32/COM calls + one pure-Go 7z reader; no GUI framework, no Scoop, no PowerShell at runtime, no external 7z/lessmsi |
| NF-5 | Index download | < 300 KB compressed, at most once a day. The index is republished daily, so a refresh is normally a full download; a 304 only happens if nothing has been published since the last check |
| NF-6 | Privileges | Never requires admin; per-user task and paths only |
| NF-7 | Supported OS | Windows 10 (1809+) and 11, x64 and arm64 |
| NF-8 | Footprint after uninstall | Data folder, PATH entry and scheduled task removed; nothing else left by ephget |

### Security

- Every download is verified against the SHA256 from the Scoop manifest before extraction; a mismatch aborts and deletes the file.
- Scripts in manifests are never run, and never included in the index.
- Manifests choose their download hosts, and some use third-party mirrors. The hash proves the file matches the manifest, not that it came from the publisher.
- Downloads go only to URLs present in the index; the index is fetched over HTTPS from this project's releases.
- Mark of the Web is cleared only after hash verification succeeds.
- Zip extraction rejects entries with absolute paths or `..` components (zip-slip).
- The index itself is unsigned in v1. Hash checks protect against a tampered download or mirror, not against a compromised index, which controls both the URL and the hash. Signing the index is the fix if this matters later.

## 9. Known limitations and open questions

### Limitations

- **Settings detection is best-effort:** with keep preferences off, settings the footprint missed (shared folders, unrelated names, later runs) stay behind.
- **Launches that bypass the shortcut** aren't tracked, so `last_closed` isn't updated. The reaper still refreshes it whenever it finds the app running, so the worst case is an early re-download.
- **Catalog coverage** leaves out apps that need a setup program, an unfixable script or a non-SHA256 hash, including GIMP, 7-Zip, KeePass, Krita, Audacity, CrystalDiskInfo, Firefox and Everything. Apps that need a script to switch on portable mode are out too, including VLC, OBS Studio, Windows Terminal and HandBrake. `docs/expanding-the-catalog.md` covers ways to bring more back. Apps whose shortcut passes arguments are also out, including Brave and other Chromium builds that set a profile folder this way. Adding an `args` field to the schema could bring some back.
- **Skipped scripts** can change an app's defaults, and the script classification is keyword-based, so some included apps may fail or behave differently.
- **Reliance on Scoop's buckets:** ephget doesn't use Scoop itself, but its catalog depends on the buckets continuing to be updated. If they stop, the index goes stale and old download URLs start failing.
- **arm64:** only 205 of the 1,259 eligible apps have an eligible arm64 build. Others that have no architecture-independent build run under x64 emulation.
- **SmartScreen** may still warn on unsigned executables even after Mark of the Web is cleared, depending on system policy.

### Open questions

- [x] How many eligible apps does the catalog contain? 1,259 GUI apps in Scoop's buckets, after dropping `Nirsoft` and apps that need a script for portable mode (M1 gave 1,584; the M0 survey's rougher rules gave 1,663).
- [x] Is it fine to redistribute manifest data from each bucket? Yes. `Main` and `Extras` use the Unlicense, `Games` uses CC0 and needs nothing further, and `Sysinternals` uses MIT, whose notice ships in `NOTICE.md`.
- [ ] How often does skipping a script actually break an app? Answered by the sample test in M2.

## 10. Milestones

Each milestone ends with a usable result; no dates are set yet.

### M0 — Catalog survey

Answers the one go/no-go question: are there enough useful portable apps in the catalog?

- [ ] Create the `Dryd33n/ephget` repo, which holds both the client and the index pipeline.
- [x] Survey WinGet: 3,682 of 15,949 packages are portable, but utilities like CPU-Z, HWiNFO and WizTree are installer-only there, so it was dropped as the catalog.
- [x] Survey Scoop's buckets and apply the eligibility rules (section 6): 1,663 of 4,941 apps.
- [x] Output the eligible count, breakdowns, and a CSV of eligible apps.
- [ ] Check the list for 20 apps you'd actually use.

**Done when:** you have the count and the spot-check, and can call go or no-go.

### M1 — Index pipeline

- [x] Turn the survey script into the indexer: emit schema-1 `index.json.gz` and its `.sha256`.
- [x] Add per-architecture entries, `extract_dir`, `rename`, `persist` and the shortcut name to the index.
- [x] Store each bucket's last processed commit; re-parse only manifests changed since (`git diff --name-only`).
- [ ] GitHub Action on a daily schedule plus manual trigger, caching the clones between runs.
- [x] Upload to the rolling `index` release.
- [x] Check download URLs at index time, and drop apps whose files are gone.
- [x] Fail the build if the index exceeds 300 KB or any entry lacks a URL, hash or exe path.
- [x] Read the `Games` bucket licence; drop the bucket if it doesn't allow this use. It's CC0, so the bucket stays.

**Done when:** the client URL serves a fresh index every day and a repeat request with the ETag returns 304.

### M2 — Core client

- [x] Go module with shared internal packages and two entry points: `ephget.exe` (console) and `ephgetw.exe` (GUI subsystem).
- [x] `config.json` load and save with defaults (TTL 7 days, keep preferences on, Desktop + Start Menu).
- [x] Index fetch with ETag, gzip decode, fuzzy search; `search` command.
- [x] `add`: create stubs via `IShellLink` targeting `ephgetw.exe`, with full arguments and the `ephget:<id>` marker, in the shortcut folder and the Start Menu if enabled.
- [x] `fetch`: pick the installer for the machine's architecture, show progress in the console, download, verify SHA256, extract zip or 7z with zip-slip protection, apply `extract_dir` and `rename`, check the exe exists, clear Mark of the Web.
- [ ] Download and launch a sample of 20–30 eligible apps with skipped scripts, in a VM; fix the classifier for anything that breaks. The download half is done: 32 of 32 downloaded, verified and extracted with the launch exe present. Launching needs a Windows VM.
- [x] `run`: if not cached, start `ephget fetch` in a new console and wait for it, exiting if it fails. Launch with `CreateProcess`, wait until no process from the app folder remains, record launch and close times.
- [x] `state.json` with a lock file and atomic writes; rebuild entries from stub arguments when missing.
- [x] `list` command.

**Done when:** on a clean machine, search → add → click downloads and runs the app, and a second click starts it instantly while offline.

### M3 — Expiry

- [x] TTL parsing (hours and days, minimum 1 hour).
- [x] `ephget remove`: delete an app's stubs, files and state; refuse while it's open or downloading. Footprint and icon removal come with those features in M4.
- [x] Stub tracking via `STARTF_TITLEISLINKNAME`; marker scan.
- [x] Reaper as specified in section 7, including the deleted-shortcut check. Footprint deletion comes with M4.
- [x] Register the per-user `ephget-reap` task (logon + hourly, no wake); re-register from any CLI command if missing.
- [x] Reap pass after each app closes.
- [x] `set` and `config` commands. `--pin` comes with M4.
- [x] Benchmark the hourly run's CPU and memory.
- [x] Save persist-list items on reap and restore them on the next download, so expiry can't lose settings.
- [ ] Watch a real scheduled run remove an app, and register the task as a true standard user.

**Done when:** an app with a 1-hour TTL is removed within about 2 hours of closing; deleting its shortcut removes it within an hour; a running app is never removed.

### M4 — Preferences and polish

- [x] Settings footprint: snapshot before first launch, diff after close, exact name matching.
- [x] Keep-preferences toggle: delete the footprint on reap when off. Saved items are already handled (M3).
- [x] Icon extraction and shortcut icon update.
- [x] Version pinning (`--pin`, stored install details, no Git history fallback).
- [x] `uninstall`.
- [ ] Every exception flow in section 4. All have code. Closing the console mid-download still needs trying by hand: the test couldn't reach a console window on this machine.

**Done when:** every flow in section 4 behaves as written.

### M5 — Release

- [ ] `install.ps1`: show the default shortcut locations for confirmation, accept a pasted folder instead of the Desktop, allow turning the Start Menu off; copy both exes, write `config.json`, add to PATH, register the task.
- [ ] Release builds of `ephget.exe` and `ephgetw.exe` for x64 and arm64 with checksums.
- [ ] Code signing: evaluate free options for open-source projects before release.
- [ ] README with install line, commands and the lifecycle diagram.
- [ ] Optional: a WinGet manifest for ephget itself.

**Done when:** the one-line install works on clean Windows 10 and Windows 11 VMs.
