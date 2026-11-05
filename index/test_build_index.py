"""Run with: python -m unittest"""
import contextlib
import gzip
import hashlib
import http.server
import io
import json
import shutil
import socket
import subprocess
import tempfile
import threading
import unittest
from pathlib import Path

import build_index as bi

H = "a" * 64
GUI = {"version": "1.0", "description": "An  app.", "url": "https://x/a.zip", "hash": H, "shortcuts": [["App.exe", "App"]]}


def blockers(**overrides):
    return bi.assess("app", "Extras", {**GUI, **overrides})["blockers"]


def tier(*script, field="pre_install"):
    return bi.classify_script({field: list(script)}, "App.exe")


class Eligibility(unittest.TestCase):
    def test_simple_app_produces_schema_entry(self):
        result = bi.assess("app", "Extras", {**GUI, "persist": ["data", ["a.ini", "b.ini"]], "extract_dir": "app-1.0"})
        self.assertEqual(result["package"], {
            "id": "app", "name": "App", "bucket": "Extras", "version": "1.0",
            "description": "An app.", "persist": ["data", "a.ini"],
            "installers": [{
                "arch": "any", "type": "zip", "url": "https://x/a.zip", "sha256": H,
                "exe": "App.exe", "extract_dir": "app-1.0", "rename": None,
            }],
        })

    def test_each_build_is_checked_separately(self):
        manifest = {
            "version": "1.0", "shortcuts": [["App.exe", "App"]],
            "architecture": {
                "64bit": {"url": "https://x/a.zip", "hash": "sha256:" + H.upper()},
                "arm64": {"url": "https://x/a-arm.msi", "hash": H},
                "32bit": {"url": "https://x/a32.zip", "hash": H},
            },
        }
        installers = bi.assess("app", "Main", manifest)["package"]["installers"]
        self.assertEqual([(i["arch"], i["sha256"]) for i in installers], [("x64", H)])

    def test_arch_fields_override_top_level(self):
        manifest = {
            "version": "1.0", "url": "https://x/a.zip", "hash": H,
            "architecture": {
                "64bit": {"shortcuts": [["a64.exe", "A"]]},
                "arm64": {"shortcuts": [["sub\\aarm.exe", "A"]], "extract_dir": "arm"},
            },
        }
        installers = bi.assess("app", "Main", manifest)["package"]["installers"]
        self.assertEqual([(i["arch"], i["exe"], i["extract_dir"]) for i in installers],
                         [("x64", "a64.exe", None), ("arm64", "sub\\aarm.exe", "arm")])

    def test_script_is_classified_per_build(self):
        manifest = {
            "version": "1.0", "url": "https://x/a.zip", "hash": H, "shortcuts": [["App.exe", "App"]],
            "architecture": {
                "64bit": {"pre_install": 'Rename-Item "$dir\\App64.exe" \'App.exe\''},
                "arm64": {"pre_install": 'Expand-7zipArchive "$dir\\a.7z"'},
            },
        }
        installers = bi.assess("app", "Main", manifest)["package"]["installers"]
        self.assertEqual([(i["arch"], i["rename"]) for i in installers], [("x64", ["App64.exe", "App.exe"])])

    def test_only_32bit_is_excluded(self):
        manifest = {"version": "1", "shortcuts": [["a.exe", "A"]],
                    "architecture": {"32bit": {"url": "https://x/a.zip", "hash": H}}}
        self.assertEqual(bi.assess("app", "Main", manifest)["blockers"], ["no-x64-build"])

    def test_gui_rule(self):
        self.assertEqual(blockers(shortcuts=None, bin="a.exe"), ["cli-only"])
        self.assertEqual(blockers(shortcuts=[["run.bat", "A"]]), ["target-not-exe"])
        self.assertEqual(blockers(shortcuts=[["..\\..\\shims\\a.exe", "A"]]), ["unsafe-path"])
        self.assertEqual(blockers(shortcuts=[["a.exe", "A", "-iwad doom2.wad"]]), ["shortcut-args"])
        self.assertEqual(blockers(shortcuts=[["a.exe", "A", ""]]), [])
        self.assertEqual(blockers(shortcuts=[["a.exe", "A", "/accepteula"]]), [])

    def test_shortcut_name_is_one_valid_file_name(self):
        package = bi.assess("app", "Main", {**GUI, "shortcuts": [["a.exe", 'Folder\\My: "App".']]})["package"]
        self.assertEqual(package["name"], "My App")
        package = bi.assess("app", "Main", {**GUI, "shortcuts": [["a.exe"]]})["package"]
        self.assertEqual(package["name"], "app")

    def test_download_rule(self):
        self.assertEqual(blockers(url=None), ["no-url"])
        self.assertEqual(blockers(url=["https://x/a.zip", "https://x/b.zip"], hash=[H, H]), ["multiple-downloads"])
        self.assertEqual(blockers(url=["https://x/a.zip"], hash=[H]), [])
        self.assertEqual(blockers(hash=None), ["no-hash"])
        self.assertEqual(blockers(hash="md5:abc"), ["hash-not-sha256"])
        self.assertEqual(blockers(url="ftp://x/a.zip"), ["unsupported-url-scheme"])

    def test_file_type_rule(self):
        self.assertEqual(blockers(url="https://x/a.7z"), [])
        self.assertEqual(blockers(url="https://x/setup.exe#/dl.7z"), ["setup-exe-as-7z"])
        self.assertEqual(blockers(url="https://x/get#/dl.7z"), ["not-a-real-7z"])
        self.assertEqual(blockers(url="https://x/a.msi"), ["msi"])
        self.assertEqual(blockers(url="https://x/a.tar.gz"), ["unsupported-file-type"])
        self.assertEqual(blockers(url="https://x/download"), ["unsupported-file-type"])
        self.assertEqual(blockers(url="https://x/get?id=1#/dl.zip"), [])
        self.assertEqual(blockers(innosetup=True), ["inno-setup"])

    def test_lone_exe_must_end_up_as_the_launch_exe(self):
        self.assertEqual(blockers(url="https://x/App.exe"), [])
        self.assertEqual(blockers(url="https://x/App-1.0.exe#/App.exe"), [])
        self.assertEqual(blockers(url="https://x/App-1.0.exe"), ["exe-name-mismatch"])
        renamed = {"url": "https://x/App-1.0.exe", "pre_install": 'Rename-Item "$dir\\App-$version.exe" \'App.exe\''}
        self.assertEqual(blockers(**renamed), [])
        installer = bi.assess("app", "Main", {**GUI, "url": "https://x/App.exe", "extract_dir": "x"})["package"]["installers"][0]
        self.assertEqual((installer["type"], installer["extract_dir"]), ("exe", None))

    def test_layout_fields(self):
        self.assertEqual(blockers(extract_to="IDE"), ["extract-to"])
        self.assertEqual(blockers(extract_dir="$PLUGINSDIR"), ["unsafe-path"])
        self.assertEqual(blockers(extract_dir="../up"), ["unsafe-path"])
        self.assertEqual(blockers(extract_dir=["a", "b"]), ["multiple-extract-dirs"])
        self.assertEqual(blockers(extract_dir=["a"]), [])
        self.assertEqual(blockers(depends="java"), ["depends"])

    def test_names_that_become_folders(self):
        self.assertEqual(blockers(version="1/../../x"), ["unsafe-version"])
        self.assertEqual(blockers(version=""), ["unsafe-version"])
        self.assertEqual(blockers(version="1.0-beta+2"), [])
        self.assertEqual(bi.assess("con", "Main", GUI)["blockers"], ["unsafe-id"])
        self.assertEqual(bi.assess("notepad++", "Main", GUI)["blockers"], [])

    def test_unsafe_persist_items_are_dropped(self):
        package = bi.assess("app", "Main", {**GUI, "persist": ["ok", "..\\up", "C:\\x", "sub\\dir\\"]})["package"]
        self.assertEqual(package["persist"], ["ok", "sub\\dir"])

    def test_malformed_manifests_do_not_stop_the_build(self):
        with contextlib.redirect_stderr(io.StringIO()):
            for blob in (b"{nope", b"[1]"):
                self.assertEqual(bi.assess_blob("a", "Main", blob)["blockers"], ["malformed-manifest"])
        self.assertEqual(bi.assess_blob("a", "Main", b'\xef\xbb\xbf{"version":"1"}')["blockers"], ["cli-only", "no-hash", "no-url"])
        result = bi.assess_blob("a", "Main", json.dumps({**GUI, "shortcuts": "x", "url": 5, "architecture": 3}).encode())
        self.assertIsNone(result["package"])


class Scripts(unittest.TestCase):
    def test_no_script(self):
        self.assertEqual(tier(), ("none", None))
        self.assertEqual(tier("# only a comment", ""), ("none", None))

    def test_settings_preparation_is_skipped(self):
        for line in (
            'if (!(Test-Path "$persist_dir\\a.ini")) { New-Item "$dir\\a.ini" | Out-Null }',
            'Remove-Item "$dir\\docs" -Recurse -Force',  # must not be mistaken for Move-Item
            'Move-Item -Path "$env:APPDATA\\App" -Destination "$persist_dir\\data" -Force',
            'Copy-Item "$env:APPDATA\\vlc\\*" "$dir\\portable" -Recurse -Force',
            'Copy-Item "$persist_dir\\*" "$dir" -Include \'a.ini\'',
            'Copy-Item -Recurse "$dir\\plugins.original\\*" "$dir\\plugins\\" -Force',
            'Move-Item "$dir\\portable\\a.ini" "$dir\\portable\\vlc"',
            'if (Test-Path "$bucketsdir\\extras\\scripts\\app\\$_.reg") {',
            "Write-Host 'Migrating settings to the Scoop persist folder'",
        ):
            self.assertEqual(tier(line), ("skip", None), line)

    def test_one_rename_becomes_the_rename_field(self):
        cases = {
            'Get-ChildItem "$dir\\App-*.exe" | Rename-Item -NewName \'App.exe\'': ["App-*.exe", "App.exe"],
            'Get-Item "$dir\\App_*.exe" | Rename-Item -NewName "App.exe"': ["App_*.exe", "App.exe"],
            'Rename-Item "$dir\\App-Portable-$version.exe" \'App.exe\'': ["App-Portable-*.exe", "App.exe"],
            'Rename-Item -Path "$dir\\app-$version.exe" -NewName "app.exe"': ["app-*.exe", "app.exe"],
            'Rename-Item "$dir\\feather-1.exe" "$dir\\App.exe"': ["feather-1.exe", "App.exe"],
        }
        for line, rename in cases.items():
            self.assertEqual(tier(line), ("rename", rename), line)
        self.assertEqual(tier('Rename-Item "$dir\\App-$version.exe" \'App.exe\'', 'New-Item "$dir\\a.ini"')[0], "rename")

    def test_other_layout_changes_are_unfixable(self):
        for lines in (
            ['Move-Item "$dir\\x64\\*" "$dir\\"'],
            ['Rename-Item "$dir\\Other.exe" \'Other2.exe\''],  # not the launch exe
            ['Rename-Item "$dir\\a-$($version -replace "x").exe" \'App.exe\''],  # glob can't express it
            ['Rename-Item "$dir\\A.exe" \'App.exe\'', 'Move-Item "$dir\\x\\*" "$dir"'],
            ['Remove-Item "$dir\\App32.exe"'],
            ['Remove-Item @(\'x.exe\', \'x.ini\'); Move-Item "$dir\\x64\\*" $dir | Out-Null'],
            ['Copy-Item -Path "$subfolder\\*" -Destination $dir -Recurse'],
            ['Copy-Item "$dir\\loot_*\\*" "$dir" -Recurse'],
            ['Copy-Item "$dir\\bin\\mesa\\opengl32.dll" "$dir\\bin"'],
            ['pushd $dir ; mv */* . ; rm App_* ; popd'],
        ):
            self.assertEqual(tier(*lines), ("unfixable", None), lines)

    def test_scripts_that_do_real_work_are_unfixable(self):
        for line in (
            'Expand-7zipArchive "$dir\\a.7z" "$dir"',
            'Invoke-WebRequest -Uri "https://x" -OutFile a',
            'Invoke-CachedDownload $app $version "$_" "$dir\\a"',
            'Start-Process "$dir\\a.exe" -Wait',
            'reg import "$dir\\a.reg"',
            'Set-ItemProperty -Path "HKCU:\\Software\\A" -Name B -Value 1',
            '$key = "HKCU:\\Software\\A"',
            '& "$bucketsdir\\extras\\scripts\\jetbrains\\portable.ps1" $dir',
            'if ($x) { & $setup --installpath=$dir }',
            'app.exe -CreateProfile "Scoop $persist_dir"',
            'scoop which java',
            'regsvr32 /s "$dir\\a.dll"',
        ):
            self.assertEqual(tier(line), ("unfixable", None), line)

    def test_installer_without_a_script_runs_a_program(self):
        self.assertEqual(bi.classify_script({"installer": {"file": "setup.exe"}}, "a.exe")[0], "unfixable")
        self.assertEqual(bi.classify_script({"installer": {"args": ["/S"]}}, "a.exe")[0], "unfixable")
        self.assertEqual(bi.classify_script({"installer": {"script": ['New-Item "$dir\\a.ini"']}}, "a.exe")[0], "skip")

    def test_multi_line_strings_are_split(self):
        self.assertEqual(tier('New-Item "$persist_dir\\a"\nMove-Item "$dir\\x\\*" "$dir"')[0], "unfixable")


class Validation(unittest.TestCase):
    PACKAGE = {"id": "a", "installers": [{"arch": "x64", "url": "u", "sha256": H, "exe": "a.exe"}]}

    def test_good_index_passes(self):
        self.assertEqual(bi.validate([self.PACKAGE], 1000, min_packages=1), [])

    def test_failures(self):
        self.assertIn("300,000", bi.validate([self.PACKAGE], 300_001, min_packages=1)[0])
        self.assertIn("only 1 packages", bi.validate([self.PACKAGE], 1)[0])
        self.assertIn("duplicate", bi.validate([self.PACKAGE, {**self.PACKAGE, "id": "A"}], 1, min_packages=1)[0])
        self.assertIn("no installers", bi.validate([{"id": "a", "installers": []}], 1, min_packages=1)[0])
        for field in ("url", "sha256", "exe"):
            package = {"id": "a", "installers": [{**self.PACKAGE["installers"][0], field: ""}]}
            self.assertIn(f"missing {field}", bi.validate([package], 1, min_packages=1)[0])


def run_git(repo, *args):
    subprocess.run(
        ["git", "-C", str(repo), "-c", "user.name=t", "-c", "user.email=t@example.invalid",
         "-c", "commit.gpgsign=false", *args],
        check=True, capture_output=True,
    )


class Pipeline(unittest.TestCase):
    """Real Git repos, because the incremental logic depends on Git's behaviour."""

    def setUp(self):
        self.root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.root, ignore_errors=True)
        self.buckets = self.root / "buckets"
        self.state = self.root / "state.json"
        self.out = self.root / "out"

    def write(self, bucket, app, manifest, message="change"):
        repo = self.buckets / bucket
        if not repo.exists():
            (repo / "bucket").mkdir(parents=True)
            run_git(repo, "init", "--quiet")
        path = repo / "bucket" / f"{app}.json"
        if manifest is None:
            path.unlink()
        else:
            path.write_text(json.dumps(manifest), encoding="utf-8")
        run_git(repo, "add", "-A")
        run_git(repo, "commit", "--quiet", "-m", message)

    def run_build(self, order, min_packages=1):
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            return bi.build(self.buckets, self.state, self.out, False, list(order), min_packages)

    def build(self, order=None):
        order = order or [b for b in ("Main", "Extras") if (self.buckets / b).exists()]
        self.assertEqual(self.run_build(order), 0)
        return json.loads(gzip.decompress((self.out / "index.json.gz").read_bytes()))

    def report(self):
        return json.loads((self.out / "report.json").read_text(encoding="utf-8"))

    def test_outputs(self):
        self.write("Main", "a", GUI)
        self.write("Extras", "b", GUI)
        index = self.build()
        self.assertEqual(index["schema"], 1)
        self.assertRegex(index["generated"], r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$")
        self.assertEqual([p["id"] for p in index["packages"]], ["a", "b"])
        data = (self.out / "index.json.gz").read_bytes()
        self.assertEqual((self.out / "index.json.gz.sha256").read_text(),
                         f"{hashlib.sha256(data).hexdigest()}  index.json.gz\n")

    def test_failed_validation_writes_nothing(self):
        self.write("Main", "a", GUI)
        self.assertEqual(self.run_build(["Main"], min_packages=2), 1)
        self.assertFalse((self.out / "index.json.gz").exists())

    def test_only_changed_manifests_are_parsed(self):
        self.write("Main", "a", GUI)
        self.write("Main", "b", GUI)
        self.write("Main", "cli", {"version": "1"})
        self.write("Extras", "c", GUI)
        self.build()
        self.assertEqual(self.report()["manifests_parsed"], {"Main": 3, "Extras": 1})

        self.build()
        self.assertEqual(self.report()["manifests_parsed"], {"Main": 0, "Extras": 0})

        self.write("Main", "a", {**GUI, "version": "2.0"})
        index = self.build()
        self.assertEqual(self.report()["manifests_parsed"], {"Main": 1, "Extras": 0})
        versions = {p["id"]: p["version"] for p in index["packages"]}
        self.assertEqual(versions, {"a": "2.0", "b": "1.0", "c": "1.0"})
        self.assertEqual(self.report()["excluded"], {"cli": ["cli-only", "no-hash", "no-url"]})

    def test_deleted_app_leaves_the_index(self):
        self.write("Main", "a", GUI)
        self.write("Main", "b", GUI)
        self.build()
        self.write("Main", "b", None)
        index = self.build()
        self.assertEqual([p["id"] for p in index["packages"]], ["a"])
        self.assertEqual(self.report()["manifests_parsed"]["Main"], 0)

    def test_app_that_stops_being_eligible_leaves_the_index(self):
        self.write("Main", "a", GUI)
        self.write("Main", "b", GUI)
        self.build()
        self.write("Main", "b", {**GUI, "depends": "java"})
        index = self.build()
        self.assertEqual([p["id"] for p in index["packages"]], ["a"])
        self.assertEqual(self.report()["excluded"], {"b": ["depends"]})

    def test_first_bucket_wins_even_when_its_copy_is_ineligible(self):
        self.write("Main", "App", {**GUI, "description": "main"})
        self.write("Main", "cli", {"version": "1"})
        self.write("Extras", "app", {**GUI, "description": "extras"})
        self.write("Extras", "cli", GUI)
        index = self.build()
        self.assertEqual([(p["id"], p["bucket"]) for p in index["packages"]], [("App", "Main")])

    def test_later_bucket_takes_over_when_the_first_drops_an_app(self):
        self.write("Main", "app", GUI)
        self.write("Extras", "app", GUI)
        self.assertEqual(self.build()["packages"][0]["bucket"], "Main")
        self.write("Main", "app", None)
        index = self.build()
        self.assertEqual(index["packages"][0]["bucket"], "Extras")
        self.assertEqual(self.report()["manifests_parsed"], {"Main": 0, "Extras": 0})

    def test_full_parse_when_the_previous_commit_is_gone(self):
        self.write("Main", "a", GUI)
        self.build()
        state = json.loads(self.state.read_text(encoding="utf-8"))
        state["buckets"]["Main"]["commit"] = "0" * 40
        self.state.write_text(json.dumps(state), encoding="utf-8")
        self.build(["Main"])
        self.assertEqual(self.report()["manifests_parsed"], {"Main": 1})

    def test_full_parse_when_state_came_from_other_code_or_is_corrupt(self):
        self.write("Main", "a", GUI)
        self.build(["Main"])
        state = json.loads(self.state.read_text(encoding="utf-8"))
        state["code"] = "older"
        self.state.write_text(json.dumps(state), encoding="utf-8")
        self.build()
        self.assertEqual(self.report()["manifests_parsed"], {"Main": 1})
        self.state.write_text("{not json", encoding="utf-8")
        self.build(["Main"])
        self.assertEqual(self.report()["manifests_parsed"], {"Main": 1})

    def test_shallow_clone_updates_and_diffs(self):
        self.write("Main", "a", GUI)
        self.write("Main", "b", GUI)
        upstream = (self.buckets / "Main").as_uri()
        clones = self.root / "clones"
        repo = clones / "Main"

        bi.update_clone(repo, upstream)
        self.assertEqual(subprocess.run(["git", "-C", str(repo), "rev-list", "--count", "HEAD"],
                                        capture_output=True, text=True).stdout.strip(), "1")
        first, parsed = bi.scan_bucket(repo, "Main", None)
        self.assertEqual(parsed, 2)

        self.write("Main", "a", {**GUI, "version": "2.0"})
        bi.update_clone(repo, upstream)
        second, parsed = bi.scan_bucket(repo, "Main", first)
        self.assertEqual(parsed, 1)
        self.assertEqual(second["apps"]["a"]["package"]["version"], "2.0")
        self.assertEqual(second["apps"]["b"], first["apps"]["b"])



class PortableMode(unittest.TestCase):
    def test_scripts_that_switch_it_on_are_excluded(self):
        for script in (
            ['New-Item "$dir\\portable_mode.txt" -ItemType File -Force | Out-Null'],
            ['Set-Content -Value $null -Path "$dir\\portable.txt"'],
            ['if (!(Test-Path "$persist_dir\\.portable")) { Add-Content "$dir\\.portable" "" }'],
            ['ensure "$dir\\portable\\vlc" | Out-Null'],
            ["Set-Content \"$dir\\$CFG\" @('[config]', 'portable_mode = true') -Encoding Ascii"],
            ['Copy-Item "$dir\\portable.ini.template" "$dir\\portable.ini" -Force'],
            ["$c = (Get-Content \"$dir\\a.ini\").Replace('StoreToEXEPath=false', 'StoreToEXEPath=true')"],
            ["info '[Portable Mode] Configuring profiles...'", "'{}' | Set-Content -Path \"$dir\\profiles.config\""],
        ):
            with self.subTest(script=script):
                self.assertEqual(tier(*script), ("portable", None))

    def test_it_outranks_a_rename(self):
        script = ['Get-Item "$dir\\app-*.exe" | Rename-Item -NewName "App.exe"', 'New-Item "$dir\\.portable" | Out-Null']
        self.assertEqual(tier(*script)[0], "portable")

    def test_migrating_old_settings_is_not_portable_mode(self):
        for script in (
            ["info '[Portable Mode] Copying user data...'", 'Move-Item "$env:APPDATA\\X\\*" "$persist_dir\\data"'],
            ['Write-Host "Move config from non-portable version..."', 'Copy-Item -Path $env:AppData\\X\\* -Destination "$persist_dir\\data"'],
            ["info '[Portable Mode]: Copying settings...'", 'Copy-Item "$env:AppData\\X\\s.xaml" "$dir"'],
            ['Rename-Item "$dir\\App-Portable-1.exe" "App.exe"'],
        ):
            with self.subTest(script=script):
                self.assertNotEqual(tier(*script)[0], "portable")

    def test_blocker_is_reported(self):
        self.assertEqual(blockers(pre_install=['New-Item "$dir\\portable.txt" | Out-Null']), ["portable-mode-needs-script"])


class Referer(unittest.TestCase):
    def test_matches_the_client(self):
        for url, want in {
            "https://host.example/a%20b/tool.zip?x=1#/dl.zip": "https://host.example/a%20b/",
            "http://user:secret@host.example:8080/tool.zip": "http://host.example:8080/",
            "https://downloads.sourceforge.net/project/tool/tool.zip": None,
            "https://portableapps.com/downloads/tool.zip": None,
            "https://notsourceforge.net/tool.zip": "https://notsourceforge.net/",
            "https://host.example": None,
        }.items():
            with self.subTest(url=url):
                self.assertEqual(bi.referer_for(url), want)


class _Handler(http.server.BaseHTTPRequestHandler):
    seen = []

    def do_GET(self):
        type(self).seen.append((self.path, self.headers.get("Referer"), self.headers.get("User-Agent")))
        self.send_response({"/ok.zip": 200, "/gone.zip": 404, "/removed.zip": 410, "/busy.zip": 503, "/no.zip": 403}.get(self.path, 200))
        self.send_header("Content-Length", "0")
        self.end_headers()

    def log_message(self, *args):
        pass


class Probe(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
        threading.Thread(target=cls.server.serve_forever, daemon=True).start()
        cls.base = f"http://127.0.0.1:{cls.server.server_port}"

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()

    def test_reports_each_status(self):
        for path, want in {"/ok.zip": 200, "/gone.zip": 404, "/removed.zip": 410, "/busy.zip": 503, "/no.zip": 403}.items():
            self.assertEqual(bi.probe(self.base + path), want)

    def test_no_answer_is_none(self):
        with socket.socket() as s:  # a port nothing listens on
            s.bind(("127.0.0.1", 0))
            port = s.getsockname()[1]
        self.assertIsNone(bi.probe(f"http://127.0.0.1:{port}/x.zip", timeout=2))
        self.assertIsNone(bi.probe("not a url"))

    def test_sends_what_the_client_sends_without_the_fragment(self):
        _Handler.seen.clear()
        bi.probe(self.base + "/ok.zip#/dl.zip")
        path, referer, agent = _Handler.seen[-1]
        self.assertEqual(path, "/ok.zip")
        self.assertEqual(referer, self.base + "/")
        self.assertIn("ephget-index", agent)


def fake_probe(statuses):
    return lambda url: statuses[url]


class DownloadChecks(unittest.TestCase):
    def run_check(self, statuses, seen=None, today="2026-11-05"):
        return bi.check_downloads(set(statuses), seen or {}, today, probe=fake_probe(statuses))

    def test_gone_needs_two_different_days(self):
        statuses = {"u": 404}
        day1 = self.run_check(statuses)
        self.assertEqual(day1["u"]["misses"], 1)
        self.assertEqual(self.run_check(statuses, day1)["u"]["misses"], 1, "a second run the same day counted")
        day2 = self.run_check(statuses, day1, "2026-11-06")
        self.assertEqual(day2["u"]["misses"], 2)

    def test_recovery_clears_misses(self):
        day1 = self.run_check({"u": 404})
        self.assertEqual(self.run_check({"u": 200}, day1, "2026-11-06")["u"]["misses"], 0)

    def test_temporary_failures_do_not_count(self):
        for status in (403, 500, 503, 522, 200):
            self.assertEqual(self.run_check({"u": status})["u"]["misses"], 0, status)

    def test_no_answer_leaves_the_record_alone(self):
        seen = {"u": {"checked": "2026-11-01", "misses": 1}}
        self.assertEqual(self.run_check({"u": None}, seen), seen)

    def test_a_mass_failure_is_ignored(self):
        statuses = {f"u{i}": 404 for i in range(30)}
        self.assertEqual(self.run_check(statuses), {})

    def test_a_few_failures_among_many_count(self):
        statuses = {f"u{i}": 200 for i in range(30)} | {"gone": 404}
        self.assertEqual(self.run_check(statuses)["gone"]["misses"], 1)

    def test_forgets_urls_no_longer_used(self):
        seen = {"old": {"checked": "2026-11-01", "misses": 0}}
        self.assertNotIn("old", self.run_check({"u": 200}, seen))

    def test_budget_leaves_the_rest_for_later(self):
        records = bi.check_downloads({"a", "b"}, {}, "2026-11-05", probe=lambda url: 200, budget=-1)
        self.assertEqual(records, {})

    def test_order_new_then_suspect_then_oldest(self):
        seen = {
            "fresh": {"checked": "2026-11-04", "misses": 0},
            "old": {"checked": "2026-10-01", "misses": 0},
            "suspect": {"checked": "2026-11-04", "misses": 1},
            "today": {"checked": "2026-11-05", "misses": 1},
        }
        chosen = bi.choose_urls({"new", *seen}, seen, "2026-11-05", rotation=1)
        self.assertEqual(chosen, ["new", "suspect", "old"])

    def test_drop_dead(self):
        records = {"dead": {"misses": 2}, "suspect": {"misses": 1}, "ok": {"misses": 0}}
        inst = lambda url: {"url": url, "arch": "x64"}
        packages = [
            {"id": "gone", "installers": [inst("dead")]},
            {"id": "partial", "installers": [inst("dead"), inst("ok")]},
            {"id": "suspect", "installers": [inst("suspect")]},
            {"id": "unknown", "installers": [inst("never-checked")]},
        ]
        kept, dropped = bi.drop_dead(packages, records)
        self.assertEqual(dropped, ["gone"])
        self.assertEqual([p["id"] for p in kept], ["partial", "suspect", "unknown"])
        self.assertEqual(kept[0]["installers"], [inst("ok")])
        self.assertEqual(len(packages[1]["installers"]), 2, "changed the state saved for next time")


class BucketChoice(unittest.TestCase):
    def test_nirsoft_is_out(self):
        self.assertNotIn("Nirsoft", bi.BUCKETS)

if __name__ == "__main__":
    unittest.main()
