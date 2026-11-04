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

M3's code is written and passes its tests: the reaper, the scheduled task, `set`, `config`, and saving persist items. Two boxes remain: no real scheduled run has removed an app yet, and task registration has only run with Administrators filtered from the token (`runas /trustlevel:0x20000`), not as a true standard user.

**The next real `ephget` command, with no `EPHGET_HOME`, registers the real `ephget-reap` task pointing at `dist\ephgetw.exe`.** From then on, apps in `%LOCALAPPDATA%\ephget` really expire.

Measured for an idle reap with 30 apps: about 12 ms in total, CPU below the timer's 16 ms resolution, 5.4 MB. Each typed command adds a `schtasks` query of about 41 ms.

M4's code is written and passes its tests: icons, footprint, pinning, `uninstall`, and shortcuts that vanish keeping their settings. No real app has gone through any of it, and `uninstall`'s PATH and real-task steps have never run, because tests set `EPHGET_HOME`.

**Next: finish M2, M3 and M4 in a Windows VM, then M5 (spec section 10).** In the VM, copy `dist/ephget.exe` and `dist/ephgetw.exe` to one folder, then:

1. Check the "Done when" line in M2: `ephget search`, `ephget add`, click the Desktop shortcut, then click it again offline.
2. Add and open each app in the sample below, and note which fail. All 32 have already downloaded, verified and extracted without errors on this machine, with nothing run. The first 10 use `rename`, the rest had a script skipped.

```
7tsp bandizip bitwarden clash.mini featherwallet ft2-clone neoray scummvm serial-cloner sumatrapdf workshopdl jpegview-fork netch pcsx2-dev simple-assembly-explorer shutup10 accesser keyboard-chatter-blocker openfreebuds aegisub ventoy speedcrunch throne tsip antimicrox obs-studio sourcegit azahar volume2 http-downloader wizfile musicplayer2
```

3. Close the download console mid-download of a large app: `tmp\<id>\` should go and the app should stay a stub. A test couldn't reach a console window on this machine: with `conhost`, the window can't be found by title or by owner.
4. Run a real `ephget uninstall` from an installed copy, and check the task, PATH entry and folder are gone. Check Defender's reaction to its hidden `cmd`.
5. Register the task as a true standard user, and watch a real hourly run remove an app.
6. Look at a real app's shortcut icon, and at what its first run puts in `ephget list`.

Also untested: the UAC path for exes that demand admin rights (`startElevated`). A killed run's `tmp\` is cleared by the next fetch, and that is tested.

Measured here with a GUI test app: a cached launch adds about 32 ms over starting the app directly (target 50 ms), and the waiting launcher used no CPU over 8 s at about 7 MB. Sizes are 8.6 MB and 3.1 MB (limit 10 MB).

M1 has one open box, left unticked in the spec: no scheduled run had happened yet, only manual ones. Once a run with `"event": "schedule"` succeeds, tick it. No login is needed to check:

```
curl -s "https://api.github.com/repos/Dryd33n/ephget-index/actions/workflows/build-index.yml/runs?event=schedule&per_page=1"
```

`ephget remove`, stub tracking and the `Referer` are committed as `b8548a1`. M3's and M4's code is uncommitted. `CLAUDE.md` is tracked even though `.gitignore` lists it.

**Defender:** on 2026-11-04 it flagged `dist\ephget.exe` as `Trojan:Win32/Bearfoos.B!ml` (VirusTotal: 1 of 61), which looks like a false positive. Reporting it to Microsoft was recommended; whether that happened is unknown. About two minutes earlier, a test fetch of `systools` had unpacked `ProduKey.exe`, flagged as `HackTool:Win32/ProductKey`. Don't fetch Nirsoft suites or password tools on this machine, and don't change builds to get past the detection. Those 23 apps need a decision in the catalog work; code signing is the real fix.

**Priority:** get the client's main functionality working first; refining the catalog comes later. That covers the catalog problems found on 2026-11-04: about 38 of 1,583 URLs failed even with a `Referer` (26 of them 404s), and nothing checks URLs at index time. Also parked: skipped scripts and Scoop's creation of missing `persist` folders both switch some apps into portable mode, and ephget does neither, so apps like OBS and VLC may keep settings in `%APPDATA%`. Check where settings land during the sample test, not just that the app opens.

The survey stays on branch `m0-survey`, on purpose: the user doesn't want the survey code on `main`. `git -C ephget-index show m0-survey:survey/survey_scoop.py` reads it. Its 1,663 count used rougher rules and is superseded.

## Client notes

- **Network boundary:** only `internal/fetch` uses the network, and `ephgetw` must never import it. `TestEphgetwHasNoNetworkCode` enforces this.
- **Starting processes:** use `createProcess` in `internal/launch`, not `os/exec`, for the app and the fetch console. `os/exec` always redirects standard handles, which leaves a new console blank and unable to read input.
- **Test apps:** build `internal/testapp` with `-H=windowsgui` unless a test needs a console. A console build makes Windows open a window for it, which adds about 450 ms to each launch.
- **`EPHGET_HOME`:** overrides the data folder. Use it for any manual run, so nothing touches a real installation.
- **Removing apps:** `internal/reap` holds both `remove` and the reaper. Moving the app folder comes first, because that fails while the app runs and nothing has changed yet.
- **`EPHGET_HOME` side effects:** with it set, nothing scans the real Desktop, Start Menu or Documents, and no command touches the real scheduled task.
- **COM calls:** in `internal/shortcut`, an address passed to `comObject.call` must be converted to `uintptr` in that call itself, not stored in a slice or passed through a helper. Otherwise the stack can move first and COM writes to freed memory. `TestReadSurvivesStackMoves` catches this, and failed every run before the fix.
- **Terminal output:** use `internal/ui`, not `text/tabwriter`, which counts colour codes as characters. Styling stays off unless stdout or stderr is a console, so piped output stays plain; `TestPipedOutputIsPlain` enforces this. `ephgetw` must not need it: it has no console.
- **Footprint safety:** matches must equal the app's ID or name exactly, and `footprint.Delete` rechecks every entry. Don't loosen either: a miss leaves harmless files, while a wrong match deletes someone's data.
- **Settings:** only `ephget remove` may lose settings kept with keep preferences on. Missing stubs may only have moved.
- **`os/exec`:** its redirected handles also break `conhost`, so tests that need a real console must use `CreateProcess`.
- **`persist` paths:** they come from the unsigned index, so pass them through `paths.SafeItems` before joining them to a folder.
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

Not yet fixed in the spec:

None at the moment.

## Working here

- Commit under the user's Git identity, with no `Co-Authored-By` line. Commit only when asked.
- Treat cloned buckets and downloaded apps as untrusted: never execute anything from them, and run apps only in a VM.
