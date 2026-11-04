# ephget

Windows tool that puts stub shortcuts for portable apps on the desktop, downloads an app on first click, and deletes it after a period of disuse. The spec is the source of truth: `ephget/ephget — Software Specification.md`. Read the sections for the current milestone before starting.

## Layout

The workspace root is not a Git repo. It holds two separate repos:

- `ephget/`: the Go client (M2 onwards) and the spec. Branch `main`.
- `ephget-index/`: the Python index pipeline (M1). `main` has only `.gitignore`; the M0 surveys are on branch `m0-survey`.

Neither repo has a GitHub remote yet, and `gh` isn't installed. M1's publishing tasks need `Dryd33n/ephget-index` to exist, so raise that when you reach them.

## Decisions

- **Catalog:** manifests from Scoop's `Main`, `Extras`, `Nirsoft`, `Sysinternals` and `Games` buckets. WinGet was surveyed and dropped.
- **No Scoop at runtime:** ephget never runs Scoop or a manifest's scripts. An app is included only if its script can be skipped or replaced by one simple exe rename.
- **Two executables:** `ephgetw.exe` (GUI subsystem) is the target of stubs and the hourly task, launches cached apps and reaps, and never uses the network or opens a console. `ephget.exe` handles typed commands and all network use; `ephgetw` starts `ephget fetch` in a new console when an app isn't cached.
- **v1 scope:** GUI apps only (manifest has `shortcuts`), one download, SHA256 required, `.zip`, real `.7z` or a directly runnable exe. Setup programs are out, including exes Scoop opens as 7z.

## Status

M0 survey is done: 1,663 eligible apps of 4,941, with a test index of about 219 KB against the 300 KB limit. No app has been downloaded or launched yet. The user's 20-app spot-check and creating the GitHub repos were set aside for now.

**Next: M1, the index pipeline (spec sections 6 and 10).**

## Starting M1

Work in `ephget-index/`, on a new branch off `main`. The survey isn't on `main`, on purpose: the user doesn't want the survey code there. Use it as the starting point for the indexer without merging the branch:

```
git -C ephget-index show m0-survey:survey/survey_scoop.py
```

Known gaps between that script and the spec:

- `installers()` emits every x64 and arm64 build but only the primary build passes the eligibility rules. Check each build separately.
- Its output lacks `name` (the shortcut's name) and `type`, stores `rename` per app rather than per installer, and uses arch names `64bit`/`arm64` where the spec says `x64`/`arm64`/`any`. It also writes `"schema": 2`; the spec says 1.
- Script classification is keyword-based. `Move-Item` needs `\b` or it also matches `Remove-Item` (this caused a real bug).
- Scoop `url` fragments like `#/rufus.exe` set the saved filename, and `#/dl.7z` on an exe means a setup program.

## Commands

Python is `py` on this machine, with a venv at `ephget-index/.venv`. Go isn't installed yet (needed from M2).

```
# Bucket clones are gitignored, under .cache/scoop/<Bucket>
git clone --depth 1 --no-checkout --single-branch https://github.com/ScoopInstaller/Extras ephget-index/.cache/scoop/Extras

# On branch m0-survey, from ephget-index/. Run with -I.
.venv/Scripts/python.exe -I survey/survey_scoop.py --self-test
.venv/Scripts/python.exe -I survey/survey.py --repo .cache/winget-pkgs --out out
.venv/Scripts/python.exe -I survey/survey_scoop.py --buckets .cache/scoop --winget out/eligible.csv --out out/scoop
```

`survey_scoop.py` reads `out/eligible.csv` from the WinGet survey, only to report overlap; the indexer doesn't need it. Results in `out/` are gitignored.

## Open spec problems

Not yet fixed in the spec; deal with them in M3 and M2:

- **Reaper:** step 3 treats a failed rename as "app is running", but the rename also fails when the folder doesn't exist. A deleted shortcut for an uncached app never has its state dropped, and uncached entries get a `last_closed` write every hour.
- **Launcher wait:** "no process from the app folder remains" includes children it didn't start, so blocking on handles alone isn't enough. That affects the "no CPU" claim.
- **Rebuilt state entries:** a rebuilt entry has no footprint and may count as a first run, so the comparison finds nothing new and settings cleanup silently stops.

## Working here

- Commit under the user's Git identity, with no `Co-Authored-By` line. Commit only when asked.
- Treat cloned buckets and downloaded apps as untrusted: never execute anything from them, and run apps only in a VM.
