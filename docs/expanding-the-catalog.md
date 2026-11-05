# Expanding the catalog

Ways to get more apps into ephget's index, ranked by what each is worth against what it costs.

All counts come from one build on 2026-11-04, from the local clones of Scoop's `Main`, `Extras`, `Sysinternals` and `Games` buckets, before any app was dropped for a dead link. To refresh them, build the index and read `report.json`, which says why each app was left out.

## Where things stand

| | Apps |
| --- | --- |
| Manifests seen | 4,651 |
| Command-line only, out of scope for v1 | 2,194 |
| GUI apps left out | 1,198 |
| **In the catalog** | **1,259** |

The most useful number for each rule below is how many apps it alone keeps out, because removing the rule brings those apps in straight away. These are minimums: 142 more apps are blocked by a script and a setup program together, for example, so removing both rules frees more than the sum.

| Rule | Apps blocked by this alone | Apps it blocks in total | Examples blocked by this alone |
| --- | --- | --- | --- |
| Script can't be skipped | 128 | 696 | GIMP, Audacity, Everything, Ditto, FreeCAD, Flameshot, FastCopy |
| Hash isn't SHA256 | 111 | 428 | CrystalDiskInfo, CrystalDiskMark, PuTTY, DeaDBeeF, Double Commander, Eclipse |
| Setup exe opened as 7z | 107 | 448 | Krita, Chrome, KiCad, Kdenlive, digiKam, Geany, f.lux |
| MSI | 91 | 169 | Cryptomator, Cyberduck, GlazeWM, gedit |
| Inno Setup | 91 | 157 | Greenshot, darktable, FontForge, Cursor, Listary |
| Portable mode needs a script | 42 | 57 | VLC, OBS Studio, Windows Terminal, HandBrake, PCSX2, Ryujinx |
| Shortcut passes arguments | 32 | 58 | Brave, Alacritty, Cmder, ungoogled-chromium, Thorium |
| File type not supported | 25 | 405 | MSYS2, MegaMek, Kavita, Catalyst |
| Target isn't an exe | 18 | 54 | Ghidra, scrcpy, ZAP, yEd |
| Has dependencies | 15 | 135 | RPCS3, SLADE, SuperPutty |
| Several downloads | 11 | 108 | PicoTorrent, tsMuxer, Picocrypt NG |
| Not a real 7z | 10 | 34 | Wesnoth, ES-DE, WinMerge 2011 |
| No hash | 6 | 12 | Minecraft, RetroArch Nightly |

## Quick wins

Each of these is a small, contained change that doesn't weaken any safety rule.

### 1. Accept stronger and weaker hashes safely (up to 111 apps)

The hashes in question are SHA-512 (144 apps in total), SHA-1 (144) and MD5 (23).

- **SHA-512:** teach the client to verify it. It's as strong as SHA256, so nothing is lost.
- **SHA-1 and MD5:** these are too weak to rely on as they stand, because collisions can be manufactured. The indexer could instead download the file once when the manifest changes, check it against the manifest's hash, and publish its own SHA256. The client then verifies a strong hash as usual.
- **Cost:** the indexer downloads each such file once per version. That's far more traffic than the header-only link check, but only for manifests that changed.
- **Catch:** this trusts the file as it was on the day of the build, which is roughly what the manifest's author did too.

### 2. Bring portable apps back with a `create` field (42 apps, and the biggest names)

The scripts that switch on portable mode mostly do one simple thing: create an empty file or folder such as `portable.txt`. The indexer can recognise exactly that pattern and publish it as data, and the client creates the item after unpacking. No script runs.

```json
"create": [{"path": "portable_mode.txt"}, {"path": "data", "folder": true}]
```

- **Reach:** this should bring back most of VLC, OBS Studio, Windows Terminal, PCSX2, Dolphin, Ryujinx and Xenia. Scripts that write settings content need a `content` field too, or stay out.
- **Risk:** low, since paths go through the same checks as `persist` and the client only ever creates things inside the app's own folder.
- **Also fixes:** see the next item.

### 3. Create missing `persist` folders, as Scoop does

This wouldn't add apps, but it's the same problem and should ship alongside item 2. Scoop creates any `persist` item that doesn't exist as an empty folder, and some apps use that folder to switch on portable mode. ephget doesn't.

**18 apps still in the catalog have a `persist` item named after portable mode** and may be keeping settings in `%APPDATA%` today: Blender, mpv, mpv.net, mpv-git, GoldenDict, Keypirinha, mGBA, mGBA-dev, Ferrite, FluxDown, Glary Utilities, GridPlayer, Hiddify, Markdown Monster, ShadowExplorer, Smartty, Snowshot and STranslate. Worth checking during the sample test. If some truly lose portable mode, this fix keeps them in the catalog instead of dropping them.

### 4. Shortcut arguments (32 apps)

Add an `args` field to the schema and pass it through when starting the app.

- **Catch:** many of these arguments name a folder, such as `--user-data-dir=$dir\User Data`, so the client needs to substitute `$dir` and `$persist_dir`. It should support only those two.
- **Risk:** low, as long as anything else containing `$` keeps the app out.

### 5. More file types (some of the 25)

Of the 405 apps blocked here, the extensions in use (among GUI apps) are mainly:

| Extension | Apps | Effort |
| --- | --- | --- |
| `.nupkg` | 40 | Trivial: it's a zip |
| `.gz`, `.tgz`, `.xz` | 12 | Small: Go's standard library handles gzip and tar, and `xz` is already an indirect dependency |
| `.jar` | 14 | Needs Java, so not really a file-type question |
| `.rar` | 9 | Needs a new dependency, so probably not worth it |

### 6. Check real file contents for "not a real 7z" (10 apps)

These are real archives whose URL doesn't end in `.7z`, so the indexer can't tell them from a setup program. Checking the file's first bytes at index time would separate them, using the same download as item 1.

## Larger projects

### 7. A small set of safe script operations (128 apps, and the most valuable group)

This is the only route to GIMP, Audacity and Everything. Many of these scripts probably just move files around, such as `Move-Item "$dir\app\*" $dir`. The client could support a short list of operations (move, copy, rename, delete) expressed as data, never as PowerShell:

```json
"steps": [{"move": "app-1.0/*", "to": "."}]
```

- **First step:** survey the 696 scripts and count the patterns. Build only what covers enough apps, because the value here depends entirely on that count, and I haven't measured it.
- **Risk:** medium. Each new operation is more untrusted input for the client to interpret, and the classifier can misjudge what a script needs.
- **Testing:** apps brought back this way should go through a sample test in a VM.

### 8. Unpacking setup programs without running them (up to 289 apps by this alone, and more with item 7)

Scoop opens setup programs with 7-Zip, innounp or lessmsi, so unpacking without running them is well proven. ephget could do the same with pure-Go readers.

| Format | Alone | With a script too | Notes |
| --- | --- | --- | --- |
| Setup exe as 7z (mostly NSIS) | 107 | 142 | The current 7z library can't read these, so it needs a new reader |
| MSI | 91 | 10 | Needs a compound-file and CAB reader |
| Inno Setup | 91 | 18 | Its format changes between versions, so this is the hardest |

- **Spec change:** this reverses a stated non-goal ("Installing apps that use real installers, including setup programs Scoop unpacks"), so it's a product decision first.
- **Costs:** more code parsing untrusted files, a larger exe against the 10 MB limit (NF-3), and apps that may expect things only a real install does.
- **Order:** do MSI first if at all, since it's the most stable format.

### 9. Curated entries for the apps people expect

Some well-known apps stay out however far the rules are relaxed, or have a better download than Scoop's. A small hand-maintained file could add them directly:

- **Pros:** the highest value per app, and it needs no new client code. It fills exactly the gaps the M0 spot-check is meant to reveal.
- **Cons:** someone has to keep versions and hashes current by hand, or write a version check per app. Keep it short.

## Other sources

Each extra source brings more apps but also more trust to manage, since whoever controls a manifest controls which file gets downloaded. Duplicates would need resolving too, as Scoop's bucket order already does.

| Source | Reach | Considerations |
| --- | --- | --- |
| **WinGet** | The M0 survey found 3,682 portable packages out of 15,949, including many command-line tools | SHA256 everywhere, but no reliable "this is a GUI app" or shortcut data, which is why it was dropped. Could work as a source for specific apps. |
| **Community Scoop buckets** | Unknown | Same format, so nearly free to read. Quality and trust vary a lot, so only take named, reviewed buckets. |
| **Scoop's `Nonportable` bucket** | Unknown | Excluded as installers. Only relevant after item 8. |
| **PortableApps.com** | Large, and aimed at this use | Its packages are installers, and its terms need reading before redistributing anything. |
| **Nirsoft** | 283 apps | Dropped on purpose, because its suites and password tools trip antivirus. A short allowlist of individual utilities could return later, if each is checked against Defender. |

## Probably not

- **Command-line tools (2,194 apps):** the largest group, but a desktop shortcut doesn't suit them. It's a different product, and a stated non-goal.
- **Resolving dependencies (15 apps alone):** a lot of machinery for very few apps. Many dependencies are runtimes such as `vcredist`, or tools like `lessmsi` needed only by the install script.
- **Running manifest scripts:** this would unlock the most, but ephget's safety promise rests on never doing it.

## Suggested order

1. **Items 2 and 3, `create` and `persist` folders:** they restore the most recognisable apps and fix a known gap.
2. **Item 1's SHA-512 half, then item 4 and the easy part of item 5:** each is a small change.
3. **The M0 spot-check:** see which missing apps actually matter before spending more.
4. **Item 1's SHA-1 and MD5 half, with item 6:** both need the same index-time download.
5. **The survey for item 7:** then decide based on what it finds.
6. **Items 8 and 9 and other sources:** only if the spot-check still shows important gaps.

Steps 1 and 2 could add somewhere around 150 to 200 apps with little risk, though that's an estimate: the hash count isn't split by type among apps blocked only by their hash, and not every portable script will fit the `create` pattern.
