#!/usr/bin/env python3
"""Build ephget's index from Scoop bucket manifests.

Writes index.json.gz and index.json.gz.sha256, site-index.json for the website, and
report.json (not published), listing why each excluded app was left out. Manifests are only ever parsed as
JSON: nothing from a bucket is executed. Uses the standard library alone.

Usage:
    python -I build_index.py --update --buckets .cache/scoop --state .cache/state.json --out out/index
"""
from __future__ import annotations

import argparse
import collections
import concurrent.futures
import datetime
import fnmatch
import gzip
import hashlib
import json
import re
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path, PurePosixPath
from urllib.parse import unquote, urlsplit

# Order matters: when two buckets share an app ID, the first one wins.
# Nirsoft is left out on purpose: its suites and password tools trip antivirus.
BUCKETS = ["Main", "Extras", "Sysinternals", "Games"]
REMOTE = "https://github.com/ScoopInstaller/{}"

SCHEMA = 1
MAX_GZ_BYTES = 300_000
# A floor well under the real count, so a broken clone can't publish an index
# that empties every client's catalog.
MIN_PACKAGES = 1000
MAX_SITE_FIELD = 300

ARCH_NAMES = {"64bit": "x64", "arm64": "arm64"}
SHA256_RE = re.compile(r"^(?:sha256:)?([0-9a-fA-F]{64})$")
# IDs and versions become folder names on the user's PC (apps\<id>\<version>).
SAFE_NAME_RE = re.compile(r"^[A-Za-z0-9](?:[A-Za-z0-9._+-]*[A-Za-z0-9_+-])?$")
RESERVED_NAMES = {"con", "prn", "aux", "nul", *(f"{d}{i}" for d in ("com", "lpt") for i in range(1, 10))}
SHORTCUT_BAD_CHARS = re.compile(r'[:*?"<>|\x00-\x1f]')
# Without this one, Sysinternals tools just show their EULA once. Other arguments
# can be essential (`-iwad doom2.wad`), and the index has nowhere to put them.
DROPPABLE_ARGS = {"/accepteula", "-accepteula"}

# --- Script classification (spec section 6). Keyword-based, so it can misjudge;
# the client checks for the exe after extraction.

# Where a statement can start: the call operator, a bare exe or Scoop itself only
# count there, so that words in messages and comments don't.
_STATEMENT = r"(?:^|[;{(|]\s*)"
HARD_SCRIPT = re.compile(
    r"Expand-|innounp|lessmsi|msiexec|Get-HelperPath"
    r"|Invoke-(?:WebRequest|RestMethod|Download|CachedDownload|ExternalCommand|Expression)"
    r"|DownloadFile|\b(?:iwr|irm|iex|curl|wget)\b|Start-Process|\bsaps\b"
    r"|\bregsvr32\b|\bsc(?:\.exe)?\s+(?:create|delete)\b|\bschtasks\b|New-Service"
    r"|\breg(?:\.exe)?\s+(?:import|add|delete|load)\b|\bHK(?:CU|LM|CR|U):"
    r"|\bRegistry::|New-ItemProperty|Set-ItemProperty"
    rf"""|{_STATEMENT}(?:&\s*["'$(\w]|scoop\s|[\w.-]+\.exe\s)""",
    re.I | re.M,
)
# \b matters: without it, case-insensitive "Move-Item" also matches "Remove-Item".
LAYOUT_SCRIPT = re.compile(r"\bRename-Item|\bMove-Item|\bmovedir|\bmv\s|\bren\s|\bRemove-Item.*\.exe", re.I)
# Copying program files or a whole folder into the app: `Copy-Item "$sub\*" $dir`.
PROGRAM_MOVE = re.compile(r"\b(?:Copy|Move)-Item\b.*(?:\*|\.(?:exe|dll)\b).*\$dir", re.I)
# Scoop's persist step moves folders aside as `name.original`; old settings live
# in the persist folder or a user folder. None of that changes the app's layout.
SETTINGS_ELSEWHERE = re.compile(r"\$persist_dir|\$env:|\.original\b", re.I)
SETTINGS_FILE = re.compile(r"\.(ini|cfg|conf|xml|json|txt)\b", re.I)
# Scripts that switch an app into portable mode. ephget never runs them, so the app
# would quietly keep its settings outside its folder, which is the opposite of what
# ephget promises. Migrating old settings doesn't count, so log messages don't either.
PORTABLE_WORD = re.compile(r"portable|StoreToEXEPath", re.I)
PORTABLE_WRITE = re.compile(r"New-Item|Set-Content|Add-Content|Out-File|\bensure\b|-replace|\.Replace\(", re.I)
# A copy from inside the app, such as a template. Copying old settings into a folder
# named "portable" is migration, and the folder comes from somewhere else.
PORTABLE_COPY = re.compile(r"""Copy-Item\s+(?:-Path\s+)?["']\$dir\\[^"']*portable""", re.I)
WRITES_TO_APP = re.compile(r"(?:New-Item|Set-Content|Add-Content|Out-File)\b.*\$dir", re.I)
PORTABLE_MESSAGE = re.compile(r"^(?:info|Write-Host)\b.*portable", re.I)
RENAME_PIPE = re.compile(
    r"""^Get-(?:ChildItem|Item)\s+(?:-Path\s+)?["']\$dir\\([^"'\\]+\.exe)["']\s*\|\s*Rename-Item\s+-NewName\s+["']([^"'\\]+\.exe)["']\s*$""",
    re.I,
)
RENAME_DIRECT = re.compile(
    r"""^Rename-Item\s+(?:-Path\s+)?["']\$dir\\([^"'\\]+\.exe)["']\s+(?:-NewName\s+)?["'](?:\$dir\\)?([^"'\\]+\.exe)["']\s*$""",
    re.I,
)


def one_or_many(value) -> list:
    if value is None:
        return []
    return value if isinstance(value, list) else [value]


def script_lines(build: dict) -> list[str]:
    """Scripts that run at install, without blanks or comments."""
    lines = []
    for field in ("installer", "pre_install", "post_install"):
        value = build.get(field)
        if isinstance(value, dict):
            value = value.get("script")
        for item in one_or_many(value):
            lines += [line.strip() for line in str(item).splitlines()]
    return [line for line in lines if line and not line.startswith("#")]


def changes_layout(line: str) -> bool:
    if SETTINGS_ELSEWHERE.search(line):
        return False
    if PROGRAM_MOVE.search(line):
        return True
    return not SETTINGS_FILE.search(line) and bool(LAYOUT_SCRIPT.search(line))


def sets_portable_mode(lines: list[str]) -> bool:
    for i, line in enumerate(lines):
        if PORTABLE_MESSAGE.search(line):
            # `info '[Portable Mode] Configuring...'` announces the line that does it.
            if i + 1 < len(lines) and WRITES_TO_APP.search(lines[i + 1]):
                return True
        elif PORTABLE_COPY.search(line) or (PORTABLE_WORD.search(line) and PORTABLE_WRITE.search(line)):
            return True
    return False


def classify_script(build: dict, exe: str) -> tuple[str, list[str] | None]:
    """Return (tier, rename), where tier is none, skip, rename, portable or unfixable."""
    # An `installer` without a `script` runs the download or a file inside it.
    installer = build.get("installer")
    if installer and not (isinstance(installer, dict) and set(installer) == {"script"}):
        return "unfixable", None
    lines = script_lines(build)
    if not lines:
        return "none", None
    if HARD_SCRIPT.search("\n".join(lines)):
        return "unfixable", None
    if sets_portable_mode(lines):
        return "portable", None
    layout_lines = [line for line in lines if changes_layout(line)]
    if not layout_lines:
        return "skip", None

    # The only fixable layout change: one rename, of a file in the app's root,
    # to the launch exe.
    if len(layout_lines) == 1:
        match = RENAME_PIPE.match(layout_lines[0]) or RENAME_DIRECT.match(layout_lines[0])
        if match and match.group(2).lower() == exe.lower():
            glob = match.group(1).replace("$version", "*")
            if not re.search(r"[$\[\]?]", glob):
                return "rename", [glob, match.group(2)]
    return "unfixable", None


# --- Eligibility (spec section 6).


def saved_name(url: str) -> str:
    """The file's name once downloaded. Scoop's `#/name.ext` fragment overrides the URL's."""
    parts = urlsplit(url)
    return unquote(parts.fragment.lstrip("/") or parts.path).rsplit("/", 1)[-1]


def is_safe_relative(path: str) -> bool:
    """False for anything that could point outside the app folder, or isn't a literal path."""
    if not path or "$" in path or ":" in path or "%" in path:
        return False
    parts = PurePosixPath(path.replace("\\", "/"))
    return not parts.is_absolute() and ".." not in parts.parts


def is_safe_name(name: str) -> bool:
    return bool(SAFE_NAME_RE.match(name)) and name.split(".", 1)[0].lower() not in RESERVED_NAMES


def shortcut_name(raw: str, fallback: str) -> str:
    """Scoop allows `Folder\\Name`, but a stub is a single file: keep the last part."""
    name = re.split(r"[\\/]", raw)[-1]
    name = SHORTCUT_BAD_CHARS.sub("", name).strip().rstrip(".")
    return name or fallback


def builds_of(manifest: dict) -> list[tuple[str, dict]]:
    """(arch, build) for x64, arm64 or any. Arch fields override top-level ones."""
    blocks = manifest.get("architecture")
    blocks = blocks if isinstance(blocks, dict) else {}
    top = {k: v for k, v in manifest.items() if k != "architecture"}
    specific = [a for a in ARCH_NAMES if isinstance(blocks.get(a), dict)]
    if specific:
        return [(ARCH_NAMES[a], {**top, **blocks[a]}) for a in specific]
    return [] if blocks else [("any", top)]


def file_type_blockers(url: str, kind: str) -> list[str]:
    if kind in ("zip", "exe"):
        return []
    if kind == "7z":
        path = urlsplit(url).path.lower()
        if path.endswith(".7z"):
            return []
        return ["setup-exe-as-7z" if path.endswith(".exe") else "not-a-real-7z"]
    return ["msi" if kind == "msi" else "unsupported-file-type"]


def assess_build(arch: str, build: dict, app_id: str) -> tuple[dict | None, str, list[str]]:
    """Apply the rules to one build. Returns (installer or None, shortcut name, blockers)."""
    blockers = []

    # Rule 1: a GUI app, launched by an exe that's inside the app folder and
    # needs no arguments.
    shortcuts = one_or_many(build.get("shortcuts"))
    first = shortcuts[0] if shortcuts and isinstance(shortcuts[0], list) else []
    exe = str(first[0]) if first else ""
    name = shortcut_name(str(first[1]), app_id) if len(first) > 1 else app_id
    exe_name = exe.replace("\\", "/").rsplit("/", 1)[-1]
    if not first:
        blockers.append("cli-only")
    elif not exe.lower().endswith(".exe"):
        blockers.append("target-not-exe")
    elif not is_safe_relative(exe):
        blockers.append("unsafe-path")
    if len(first) > 2 and first[2] and str(first[2]).strip().lower() not in DROPPABLE_ARGS:
        blockers.append("shortcut-args")

    # Rule 2: one download, with a SHA256.
    urls = one_or_many(build.get("url"))
    hashes = one_or_many(build.get("hash"))
    url = str(urls[0]) if urls else ""
    sha = SHA256_RE.match(str(hashes[0])) if len(hashes) == 1 else None
    if not urls:
        blockers.append("no-url")
    elif len(urls) > 1:
        blockers.append("multiple-downloads")
    elif urlsplit(url).scheme.lower() not in ("http", "https"):
        blockers.append("unsupported-url-scheme")
    if not hashes:
        blockers.append("no-hash")
    elif not sha and len(urls) == 1:
        blockers.append("hash-not-sha256")

    # Rule 3: a file the client can unpack.
    kind = saved_name(url).rsplit(".", 1)[-1].lower() if "." in saved_name(url) else ""
    if url:
        blockers += file_type_blockers(url, kind)
    if build.get("innosetup"):
        blockers.append("inno-setup")

    # Layout fields the client has to be able to apply.
    extract_dir = one_or_many(build.get("extract_dir"))
    if len(extract_dir) > 1:
        blockers.append("multiple-extract-dirs")
    elif extract_dir and not is_safe_relative(str(extract_dir[0])):
        blockers.append("unsafe-path")
    if build.get("extract_to"):
        blockers.append("extract-to")

    # Rule 4: no dependencies.
    if build.get("depends"):
        blockers.append("depends")

    # Rule 5: no script needed, or one rename that replaces it.
    tier, rename = classify_script(build, exe_name)
    if tier == "unfixable":
        blockers.append("script-not-fixable")
    elif tier == "portable":
        blockers.append("portable-mode-needs-script")

    # A lone exe is saved under one name, so that name has to end up as the launch exe.
    if kind == "exe" and exe and not blockers:
        saved = saved_name(url)
        matches = fnmatch.fnmatch(saved.lower(), rename[0].lower()) if rename else saved.lower() == exe.lower()
        if not matches:
            blockers.append("exe-name-mismatch")

    if blockers:
        return None, name, sorted(set(blockers))
    installer = {
        "arch": arch,
        "type": kind,
        "url": url,
        "sha256": sha.group(1).lower(),
        "exe": exe,
        "extract_dir": str(extract_dir[0]) if extract_dir and kind != "exe" else None,
        "rename": rename,
    }
    return installer, name, []


def persist_items(manifest: dict) -> list[str]:
    """Paths to keep, relative to the app folder. `[source, target]` pairs keep the source."""
    items = []
    for item in one_or_many(manifest.get("persist")):
        path = str(item[0]) if isinstance(item, list) and item else item
        if isinstance(path, str) and is_safe_relative(path):
            items.append(path.rstrip("\\/"))
    return items


def site_details(manifest: dict) -> dict:
    """What only the website needs, so the client's index stays small. Manifests are
    untrusted and the page turns the homepage into a link, so it must be a web address."""
    homepage = str(manifest.get("homepage", "")).strip()
    if urlsplit(homepage).scheme.lower() not in ("http", "https") or not urlsplit(homepage).netloc:
        homepage = ""
    licence = manifest.get("license", "")
    if isinstance(licence, dict):  # {"identifier": ..., "url": ...}
        licence = licence.get("identifier", "")
    return {"homepage": homepage[:MAX_SITE_FIELD], "license": " ".join(str(licence).split())[:MAX_SITE_FIELD]}


def assess(app_id: str, bucket: str, manifest: dict) -> dict:
    """Return {"package": entry or None, "blockers": [...], "tier": ...} for a manifest."""
    version = str(manifest.get("version", ""))
    installers, names, blockers = [], [], set()
    builds = builds_of(manifest)
    if not builds:
        blockers.add("no-x64-build")
    for arch, build in builds:
        installer, name, build_blockers = assess_build(arch, build, app_id)
        blockers.update(build_blockers)
        if installer:
            installers.append(installer)
            names.append(name)
    if not is_safe_name(app_id):
        installers = []
        blockers.add("unsafe-id")
    if not is_safe_name(version):
        installers = []
        blockers.add("unsafe-version")
    if not installers:
        return {"package": None, "blockers": sorted(blockers)}
    return {
        "package": {
            "id": app_id,
            "name": names[0],
            "bucket": bucket,
            "version": version,
            "description": " ".join(str(manifest.get("description", "")).split()),
            "persist": persist_items(manifest),
            "installers": installers,
        },
        "site": site_details(manifest),
        "blockers": [],
    }


def assess_blob(app_id: str, bucket: str, blob: bytes) -> dict:
    """One bad manifest must not stop the build, so malformed input becomes a blocker."""
    try:
        manifest = json.loads(blob.decode("utf-8-sig"), strict=False)
        if not isinstance(manifest, dict):
            raise ValueError("not an object")
        return assess(app_id, bucket, manifest)
    except (ValueError, TypeError, AttributeError, IndexError, KeyError) as error:
        print(f"warning: {bucket}/{app_id}: {error}", file=sys.stderr)
        return {"package": None, "blockers": ["malformed-manifest"]}


# --- Git access. Clones are shallow, have no checkout, and are only read through plumbing.


def git(repo: Path, *args: str, stdin: bytes | None = None, check: bool = True) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["git", "-C", str(repo), *args],
        input=stdin, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=check,
    )


def update_clone(repo: Path, url: str) -> None:
    """Clone the bucket if it isn't cached, otherwise move HEAD to the remote's latest commit."""
    if (repo / ".git").exists():
        git(repo, "fetch", "--quiet", "--depth", "1", "origin", "HEAD")
        git(repo, "update-ref", "HEAD", "FETCH_HEAD")
    else:
        repo.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(
            ["git", "clone", "--quiet", "--depth", "1", "--no-checkout", "--single-branch", url, str(repo)],
            check=True, stderr=subprocess.PIPE,
        )


def list_manifests(repo: Path) -> dict[str, str]:
    """Path -> blob ID for every manifest in the bucket's HEAD."""
    listing = git(repo, "ls-tree", "-r", "-z", "HEAD", "bucket").stdout
    manifests = {}
    for record in listing.split(b"\0"):
        if record:
            meta, path = record.split(b"\t", 1)
            name = path.decode("utf-8", "replace")
            if name.lower().endswith(".json"):
                manifests[name] = meta.split()[2].decode()
    return manifests


def read_blobs(repo: Path, oids: list[str]) -> list[bytes]:
    if not oids:
        return []
    out = git(repo, "cat-file", "--batch", stdin="\n".join(oids).encode() + b"\n").stdout
    blobs, pos = [], 0
    for _ in oids:
        end = out.index(b"\n", pos)
        size = int(out[pos:end].split()[2])
        pos = end + 1
        blobs.append(out[pos:pos + size])
        pos += size + 1
    return blobs


def changed_since(repo: Path, commit: str, head: str) -> set[str] | None:
    """Manifest paths changed between two commits, or None if the old one is gone."""
    if git(repo, "cat-file", "-e", f"{commit}^{{commit}}", check=False).returncode != 0:
        return None
    out = git(repo, "diff", "--name-only", "--no-renames", "-z", commit, head, "--", "bucket").stdout
    return {p.decode("utf-8", "replace") for p in out.split(b"\0") if p}


def scan_bucket(repo: Path, bucket: str, previous: dict | None) -> tuple[dict, int]:
    """Assess a bucket, re-parsing only what changed since `previous`.

    Returns ({"commit": ..., "apps": {id: assessment}}, manifests parsed).
    """
    head = git(repo, "rev-parse", "HEAD").stdout.decode().strip()
    manifests = list_manifests(repo)
    prior_apps = previous["apps"] if previous else {}
    changed = changed_since(repo, previous["commit"], head) if previous else None

    apps, to_parse = {}, []
    for path, oid in manifests.items():
        app_id = PurePosixPath(path).stem
        if changed is not None and path not in changed and app_id in prior_apps:
            apps[app_id] = prior_apps[app_id]
        else:
            to_parse.append((app_id, oid))
    blobs = read_blobs(repo, [oid for _, oid in to_parse])
    for (app_id, _), blob in zip(to_parse, blobs):
        apps[app_id] = assess_blob(app_id, bucket, blob)
    return {"commit": head, "apps": apps}, len(to_parse)


# --- Build.


# --- Download checks. A manifest's URL can die long before its manifest changes, and
# the user would only find out on their first click. Only a server saying the file
# is gone counts: timeouts, 403s and 5xx are often temporary, or a host refusing
# GitHub's servers, and must not cost an app its place.

USER_AGENT = "ephget-index (+https://github.com/Dryd33n/ephget)"
GONE_STATUSES = {404, 410}
# Answers on two different days, so one bad day at a host doesn't drop an app.
DEAD_AFTER_MISSES = 2
# Beyond new, changed and suspect URLs, how many of the rest to re-check per run. All
# of them get looked at over about a week, without some 1,300 requests a day.
ROTATION_PER_RUN = 200
CHECK_WORKERS = 16
CHECKS_PER_HOST = 3
CHECK_TIMEOUT = 25
CHECK_BUDGET_SECONDS = 480
# If this share of a run's checks are "gone", the problem is GitHub's network or the
# internet, not hundreds of apps. Nothing from that run counts.
OUTAGE_SHARE = 0.10
OUTAGE_MIN_CHECKS = 20
# Sending the file's folder as Referer is what the client does, and Scoop before it:
# some hosts answer 404 or a web page without one. These two do so with one.
NO_REFERER_DOMAINS = ("sourceforge.net", "portableapps.com")


def referer_for(url: str) -> str | None:
    parts = urlsplit(url)
    host = (parts.hostname or "").lower()
    if any(host == d or host.endswith("." + d) for d in NO_REFERER_DOMAINS):
        return None
    if "/" not in parts.path:
        return None
    return f"{parts.scheme}://{parts.netloc.rsplit('@', 1)[-1]}{parts.path.rsplit('/', 1)[0]}/"


def probe(url: str, timeout: float = CHECK_TIMEOUT) -> int | None:
    """The status a download URL answers with, or None if it gave no answer. Reads
    only the response's headers, so no file is downloaded."""
    try:
        request = urllib.request.Request(url.split("#", 1)[0], headers={"User-Agent": USER_AGENT})
        referer = referer_for(url)
        if referer:
            request.add_header("Referer", referer)
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return response.status
    except urllib.error.HTTPError as error:
        return error.code
    except (OSError, ValueError):  # URLError, timeouts and TLS errors are all OSErrors
        return None


def choose_urls(urls: set[str], seen: dict, today: str, rotation: int = ROTATION_PER_RUN) -> list[str]:
    """What to check today: new URLs, then suspect ones, then the longest unchecked."""
    new = sorted(u for u in urls if u not in seen)
    known = [u for u in urls if u in seen and seen[u].get("checked") != today]
    suspect = sorted(u for u in known if seen[u].get("misses"))
    rest = sorted((u for u in known if not seen[u].get("misses")), key=lambda u: (seen[u].get("checked", ""), u))
    return new + suspect + rest[:rotation]


def check_downloads(urls: set[str], seen: dict, today: str, probe=probe, budget: float = CHECK_BUDGET_SECONDS) -> dict:
    """Return the updated record of each URL: {url: {"checked": day, "misses": n}}."""
    chosen = choose_urls(urls, seen, today)
    hosts = collections.defaultdict(lambda: threading.Semaphore(CHECKS_PER_HOST))
    deadline = time.monotonic() + budget

    def check(url: str):
        if time.monotonic() > deadline:  # what's left waits for the next run
            return url, False, None
        with hosts[urlsplit(url).hostname or ""]:
            if time.monotonic() > deadline:
                return url, False, None
            return url, True, probe(url)

    with concurrent.futures.ThreadPoolExecutor(CHECK_WORKERS) as pool:
        results = [r for r in pool.map(check, chosen) if r[1]]

    records = {u: r for u, r in seen.items() if u in urls}  # forget URLs no longer used
    gone = sum(1 for _, _, status in results if status in GONE_STATUSES)
    if len(results) >= OUTAGE_MIN_CHECKS and gone > len(results) * OUTAGE_SHARE:
        print(f"warning: {gone} of {len(results)} downloads look gone, so this run's checks are ignored", file=sys.stderr)
        return records
    for url, _, status in results:
        if status is None:
            continue  # no answer says nothing about the file
        misses = records.get(url, {}).get("misses", 0) + 1 if status in GONE_STATUSES else 0
        records[url] = {"checked": today, "misses": misses}
    return records


def drop_dead(packages: list[dict], records: dict) -> tuple[list[dict], list[str]]:
    """Remove dead installers, and apps left with none. Returns (packages, dropped IDs).

    Works on copies: `packages` belongs to the state saved for the next run."""
    kept, dropped = [], []
    for package in packages:
        alive = [i for i in package["installers"] if records.get(i["url"], {}).get("misses", 0) < DEAD_AFTER_MISSES]
        if alive:
            kept.append({**package, "installers": alive} if len(alive) != len(package["installers"]) else package)
        else:
            dropped.append(package["id"])
    return kept, dropped


def code_version() -> str:
    """Rule changes must reach every app, so state from different code isn't reused."""
    return hashlib.sha256(Path(__file__).read_bytes()).hexdigest()


def read_state(path: Path | None) -> dict:
    if not path or not path.exists():
        return {}
    try:
        state = json.loads(path.read_text(encoding="utf-8"))
    except ValueError:
        return {}
    return state if isinstance(state, dict) else {}


def load_state(path: Path | None) -> dict:
    state = read_state(path)
    return state.get("buckets", {}) if state.get("code") == code_version() else {}


def load_url_records(path: Path | None) -> dict:
    """Download checks describe the URLs, not the code, so they survive a code change."""
    records = read_state(path).get("urls", {})
    return records if isinstance(records, dict) else {}


def merge_buckets(scanned: dict[str, dict], order: list[str]) -> tuple[list[dict], dict[str, list[str]]]:
    """Packages, sorted by ID, and why each other app is out. The first bucket with an ID wins."""
    packages, excluded, seen = [], {}, set()
    for bucket in order:
        for app_id, result in scanned[bucket]["apps"].items():
            key = app_id.lower()
            if key in seen:
                continue
            seen.add(key)
            if result["package"]:
                packages.append(result["package"])
            else:
                excluded[app_id] = result["blockers"]
    packages.sort(key=lambda p: p["id"].lower())
    return packages, excluded


def validate(packages: list[dict], size: int, min_packages: int = MIN_PACKAGES) -> list[str]:
    """Reasons this index must not be published."""
    problems = []
    if size > MAX_GZ_BYTES:
        problems.append(f"index is {size:,} bytes compressed; the limit is {MAX_GZ_BYTES:,}")
    if len(packages) < min_packages:
        problems.append(f"only {len(packages)} packages; expected at least {min_packages}")
    ids = collections.Counter(p["id"].lower() for p in packages)
    problems += [f"duplicate id: {i}" for i, n in ids.items() if n > 1]
    for package in packages:
        if not package["installers"]:
            problems.append(f"{package['id']}: no installers")
        for installer in package["installers"]:
            for field in ("url", "sha256", "exe"):
                if not installer.get(field):
                    problems.append(f"{package['id']} ({installer.get('arch')}): missing {field}")
    return problems


def site_index(packages: list[dict], scanned: dict[str, dict], generated: str) -> dict:
    """The website's copy of the catalog: the apps people can add, and how to learn more."""
    apps = []
    for package in packages:
        result = scanned[package["bucket"]]["apps"].get(package["id"], {})
        apps.append({
            "id": package["id"],
            "name": package["name"],
            "version": package["version"],
            "description": package["description"],
            "bucket": package["bucket"],
            **result.get("site", {"homepage": "", "license": ""}),
        })
    return {"generated": generated, "apps": apps}


def encode_index(packages: list[dict], generated: str) -> bytes:
    payload = json.dumps(
        {"schema": SCHEMA, "generated": generated, "packages": packages},
        separators=(",", ":"), ensure_ascii=False,
    ).encode("utf-8")
    return gzip.compress(payload, 9, mtime=0)


def build(buckets_dir: Path, state_path: Path | None, out_dir: Path, update: bool,
          order: list[str] = BUCKETS, min_packages: int = MIN_PACKAGES,
          check_urls: bool = False, probe=probe) -> int:
    previous = load_state(state_path)
    url_records = load_url_records(state_path)
    scanned, parsed = {}, {}
    for bucket in order:
        repo = buckets_dir / bucket
        if update:
            update_clone(repo, REMOTE.format(bucket))
        scanned[bucket], parsed[bucket] = scan_bucket(repo, bucket, previous.get(bucket))

    packages, excluded = merge_buckets(scanned, order)
    now = datetime.datetime.now(datetime.timezone.utc)
    if check_urls:
        urls = {i["url"] for p in packages for i in p["installers"]}
        url_records = check_downloads(urls, url_records, now.strftime("%Y-%m-%d"), probe=probe)
        packages, dead = drop_dead(packages, url_records)
        excluded.update({app_id: ["dead-url"] for app_id in dead})
    if state_path:
        state_path.parent.mkdir(parents=True, exist_ok=True)
        state_path.write_text(
            json.dumps({"code": code_version(), "buckets": scanned, "urls": url_records}, separators=(",", ":")),
            encoding="utf-8",
        )
    generated = now.strftime("%Y-%m-%dT%H:%M:%SZ")
    data = encode_index(packages, generated)

    installers = [i for p in packages for i in p["installers"]]
    report = {
        "generated": generated,
        "commits": {b: scanned[b]["commit"] for b in order},
        "manifests_parsed": parsed,
        "apps_seen": len(packages) + len(excluded),
        "packages": len(packages),
        "index_gz_bytes": len(data),
        "by_bucket": dict(collections.Counter(p["bucket"] for p in packages)),
        "installers_by_arch": dict(collections.Counter(i["arch"] for i in installers)),
        "installers_by_type": dict(collections.Counter(i["type"] for i in installers)),
        "with_rename": sum(bool(i["rename"]) for i in installers),
        "with_persist": sum(bool(p["persist"]) for p in packages),
        "blockers": dict(collections.Counter(b for bs in excluded.values() for b in bs).most_common()),
        "excluded": dict(sorted(excluded.items(), key=lambda item: item[0].lower())),
    }
    print(json.dumps({k: v for k, v in report.items() if k != "excluded"}, indent=2))

    problems = validate(packages, len(data), min_packages)
    if problems:
        for problem in problems:
            print(f"error: {problem}", file=sys.stderr)
        return 1

    out_dir.mkdir(parents=True, exist_ok=True)
    (out_dir / "index.json.gz").write_bytes(data)
    (out_dir / "index.json.gz.sha256").write_bytes(
        f"{hashlib.sha256(data).hexdigest()}  index.json.gz\n".encode()
    )
    (out_dir / "report.json").write_text(json.dumps(report, indent=2), encoding="utf-8")
    (out_dir / "site-index.json").write_text(
        json.dumps(site_index(packages, scanned, generated), separators=(",", ":"), ensure_ascii=False),
        encoding="utf-8",
    )
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--buckets", type=Path, required=True, help="folder holding a clone of each bucket")
    parser.add_argument("--state", type=Path, help="file that keeps results between runs; omit for a full parse")
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--update", action="store_true", help="clone or fetch the buckets first")
    parser.add_argument("--check-urls", action="store_true", help="check downloads, and drop apps whose files are gone; needs --state to remember them")
    args = parser.parse_args()
    return build(args.buckets, args.state, args.out, args.update, check_urls=args.check_urls)


if __name__ == "__main__":
    sys.exit(main())
