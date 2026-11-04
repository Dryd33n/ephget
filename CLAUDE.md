# ephget

Windows tool that puts stub shortcuts for portable apps on the desktop, downloads an app on first click, and deletes it after a period of disuse. The spec is the source of truth: `ephget/ephget — Software Specification.md`. Read the sections for the current milestone before starting.

## Layout

The workspace root is not a Git repo. It holds two separate repos:

- `ephget/`: the Go client (M2 onwards) and the spec. Branch `main`.
- `ephget-index/`: the Python index pipeline (M1). Pushed to `Dryd33n/ephget-index`. The M0 surveys are on local branch `m0-survey`, which stays unpushed.

`ephget/` has no GitHub remote yet, and `gh` isn't installed.

## Decisions

- **Catalog:** manifests from Scoop's `Main`, `Extras`, `Nirsoft`, `Sysinternals` and `Games` buckets. WinGet was surveyed and dropped.
- **No Scoop at runtime:** ephget never runs Scoop or a manifest's scripts. An app is included only if its script can be skipped or replaced by one simple exe rename.
- **Two executables:** `ephgetw.exe` (GUI subsystem) is the target of stubs and the hourly task, launches cached apps and reaps, and never uses the network or opens a console. `ephget.exe` handles typed commands and all network use; `ephgetw` starts `ephget fetch` in a new console when an app isn't cached.
- **v1 scope:** GUI apps only (manifest has `shortcuts`), one download, SHA256 required, `.zip`, real `.7z` or a directly runnable exe. Setup programs are out, including exes Scoop opens as 7z.

## Status

M0 survey is done. M1's indexer is live: the workflow has run by hand on GitHub, publishing 1,584 apps at about 216 KB. The user's 20-app spot-check was set aside for now.

M2's code is written and passes its tests, but M2 isn't done: the sample test and the clean-machine check need a Windows VM. The user ran one real app themselves on this machine: Etcher downloaded, opened and recorded its close, using the default Desktop and Start Menu folders. Their copy of ephget's data is in `%LOCALAPPDATA%\ephget`. The same session found `fysty`, the catalog's PuTTY, returns a 404: its manifest's URL is dead, and neither the indexer nor `add` checks for that. The only VM on this machine is a Debian one in VirtualBox, and Windows 11 Home has no Windows Sandbox. End-to-end tests run the real executables and click a real stub, with `internal/testapp` standing in for the app.

**Next: finish M2 in a Windows VM, then M3 (spec sections 7 and 10).** In the VM, copy `dist/ephget.exe` and `dist/ephgetw.exe` to one folder, then:

1. Check the "Done when" line in M2: `ephget search`, `ephget add`, click the Desktop shortcut, then click it again offline.
2. Add and open each app in the sample below, and note which fail. All 32 have already downloaded, verified and extracted without errors on this machine, with nothing run. The first 10 use `rename`, the rest had a script skipped.

```
7tsp bandizip bitwarden clash.mini featherwallet ft2-clone neoray scummvm serial-cloner sumatrapdf workshopdl jpegview-fork netch pcsx2-dev simple-assembly-explorer shutup10 accesser keyboard-chatter-blocker openfreebuds aegisub ventoy speedcrunch throne tsip antimicrox obs-studio sourcegit azahar volume2 http-downloader wizfile musicplayer2
```

Also untested: the UAC path for exes that demand admin rights (`startElevated`), and closing the console during a download. A killed run's `tmp\` is cleared by the next fetch, and that is tested.

Measured here with a GUI test app: a cached launch adds about 32 ms over starting the app directly (target 50 ms), and the waiting launcher used no CPU over 8 s at about 7 MB. Sizes are 8.3 MB and 3.0 MB (limit 10 MB).

M1 has one open box, left unticked in the spec: no scheduled run had happened yet, only manual ones. Once a run with `"event": "schedule"` succeeds, tick it. No login is needed to check:

```
curl -s "https://api.github.com/repos/Dryd33n/ephget-index/actions/workflows/build-index.yml/runs?event=schedule&per_page=1"
```

M2's code is committed as `282ab37`. `ephget remove`, stub tracking and the `Referer` are uncommitted. `CLAUDE.md` is tracked even though `.gitignore` lists it.

**Priority:** get the client's main functionality working first; refining the catalog comes later. That covers the catalog problems found on 2026-11-04: about 38 of 1,583 URLs failed even with a `Referer` (26 of them 404s), and nothing checks URLs at index time. Also parked: skipped scripts and Scoop's creation of missing `persist` folders both switch some apps into portable mode, and ephget does neither, so apps like OBS and VLC may keep settings in `%APPDATA%`. Check where settings land during the sample test, not just that the app opens.

The survey stays on branch `m0-survey`, on purpose: the user doesn't want the survey code on `main`. `git -C ephget-index show m0-survey:survey/survey_scoop.py` reads it. Its 1,663 count used rougher rules and is superseded.

## Client notes

- **Network boundary:** only `internal/fetch` uses the network, and `ephgetw` must never import it. `TestEphgetwHasNoNetworkCode` enforces this.
- **Starting processes:** use `createProcess` in `internal/launch`, not `os/exec`, for the app and the fetch console. `os/exec` always redirects standard handles, which leaves a new console blank and unable to read input.
- **Test apps:** build `internal/testapp` with `-H=windowsgui` unless a test needs a console. A console build makes Windows open a window for it, which adds about 450 ms to each launch.
- **`EPHGET_HOME`:** overrides the data folder. Use it for any manual run, so nothing touches a real installation.
- **Removing apps:** `internal/reap` has no network code, so the M3 reaper can share it with `remove`. Moving the app folder comes first, because that fails while the app runs and nothing has changed yet.
- **Real folders:** with `EPHGET_HOME` set, `remove` doesn't scan the real Desktop, Start Menu or Documents.
- **Hash file:** the client doesn't read `index.json.gz.sha256`. The release's files are replaced one at a time, so the pair can briefly disagree during a publish, and gzip's own checksum and a JSON parse already catch damage.
- **Sample test:** if an app breaks because of a skipped script, fix the classifier in `ephget-index/build_index.py` and add a test. Any change to that file causes a full parse on the next run.
- **Publishing a fix:** merge to `ephget-index` `main` and start the workflow from the Actions tab. Take a fresh screenshot before each click in the browser, because the page navigates after a run starts.

## Commands

Python is `py` on this machine, with a venv at `ephget-index/.venv`. Go 1.27.3 is at `%LOCALAPPDATA%\Programs\go1.27.3\go\bin`, and isn't on `PATH`.

```
# From ephget/
./build.ps1            # builds dist\ephget.exe and dist\ephgetw.exe
go test ./...          # the e2e tests briefly open a console window

# Bucket clones are gitignored, under .cache/scoop/<Bucket>
git clone --depth 1 --no-checkout --single-branch https://github.com/ScoopInstaller/Extras ephget-index/.cache/scoop/Extras

# From ephget-index/. Run the build with -I; it reads the untrusted clones.
.venv/Scripts/python.exe -m unittest
.venv/Scripts/python.exe -I build_index.py --update --buckets .cache/scoop --state .cache/state.json --out out/index
```

`out/index/report.json` says why each excluded app was left out.

## Open spec problems

Not yet fixed in the spec; deal with them in M3 and M4:

- **Reaper:** step 3 treats a failed rename as "app is running", but the rename also fails when the folder doesn't exist. A deleted shortcut for an uncached app never has its state dropped, and uncached entries get a `last_closed` write every hour. `reap.Remove` already checks that the folder exists first. The spec also doesn't say what happens to an entry with neither timestamp, or who cleans up a `.reaping` folder left by a crash.
- **`ephgetw` errors:** it shows a message box on any error, which the hourly reap must not do.
- **Tests:** anything using `EPHGET_HOME` must not register the real scheduled task.
- **`uninstall`:** it has to delete its own running exe, and the spec has no plan for that.
- **`persist`:** the client's `validate` doesn't check these paths yet, and must before M4 uses them.
- **Rebuilt state entries:** a rebuilt entry has no footprint and may count as a first run, so the comparison finds nothing new and settings cleanup silently stops.

## Working here

- Commit under the user's Git identity, with no `Co-Authored-By` line. Commit only when asked.
- Treat cloned buckets and downloaded apps as untrusted: never execute anything from them, and run apps only in a VM.
