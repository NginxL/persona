"""Windows release receipts bind native version checks to the archive being uploaded."""
import copy
import gzip
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import warnings
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parent))
import windows_release as release

VERSION = "0.5.3"
BUILD = 202611140822


def pe_fixture(body=b"fixture", machine=0x8664):
    header = bytearray(128)
    header[:2] = b"MZ"
    header[60:64] = (64).to_bytes(4, "little")
    header[64:68] = b"PE\0\0"
    header[68:70] = machine.to_bytes(2, "little")
    return bytes(header) + body


class WindowsReleaseTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="windows-release-tests-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.archive = self.root / "Persona-0.5.3-windows-amd64.zip"
        self.make_zip()
        self.metadata = self.root / "BUILD.json"
        self.metadata.write_text(json.dumps({"version": VERSION, "build": BUILD}))
        self.report_path = self.root / release.report_name(self.archive.name)
        self.outputs = {
            "link_version": VERSION, "personad_version": VERSION,
            "personad_verbose": "%s\nchannel release, build %d" % (VERSION, BUILD),
            "node_check": {"version": VERSION, "os": "windows", "arch": "amd64", "program": "personad"},
        }

    def make_zip(self, entries=None):
        if entries is None:
            entries = [(self.archive.stem + "/" + name, b"synthetic executable " + name.encode())
                       for name in release.PROGRAMS]
        with warnings.catch_warnings():
            warnings.simplefilter("ignore", UserWarning)
            with zipfile.ZipFile(self.archive, "w", zipfile.ZIP_DEFLATED) as archive:
                for name, body in entries:
                    # ZipInfo's constructor normalizes backslashes on Windows; set the raw name afterward.
                    entry = zipfile.ZipInfo("placeholder")
                    entry.filename = name
                    archive.writestr(entry, body if body.startswith(b"MZ") else pe_fixture(body))

    def report(self):
        archive, members = release.inspect_archive(self.archive, VERSION)
        return {"schema": 1, "version": VERSION, "build": BUILD, "platform": "windows-amd64",
                "archive": archive, "binaries": members, "outputs": copy.deepcopy(self.outputs)}

    def assets(self, report=None):
        self.report_path.write_text(json.dumps(report or self.report()), encoding="utf-8")
        return [{"name": path.name, "path": str(path)} for path in
                (self.archive, self.metadata, self.report_path)]

    def test_correct_report_accepts_exact_archive(self):
        release.check_windows_archives(self.assets(), VERSION)

    def test_unrelated_receipt_without_windows_zip_needs_no_report(self):
        release.check_windows_archives([{"name": "Persona-0.5.3-web.tar.gz", "path": "unused"}], VERSION)
        release.check_windows_archives([], VERSION)

    def test_missing_report_and_duplicate_asset_names_are_rejected(self):
        assets = self.assets()
        with self.assertRaisesRegex(ValueError, "native verification report"):
            release.check_windows_archives(assets[:-1], VERSION)
        with self.assertRaisesRegex(ValueError, "Duplicate asset"):
            release.check_windows_archives(assets + assets[:1], VERSION)

    def test_report_must_be_the_requested_version_and_build(self):
        for field, value in (("version", "0.5.0"), ("build", BUILD - 1),
                             ("platform", "windows-arm64"), ("schema", True)):
            report = self.report()
            report[field] = value
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, "identity"):
                release.check_windows_archives(self.assets(report), VERSION)

    def test_same_build_old_program_version_is_rejected(self):
        for field in ("link_version", "personad_version"):
            report = self.report()
            report["outputs"][field] = "0.5.0"
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, "command outputs"):
                release.check_windows_archives(self.assets(report), VERSION)

    def test_actual_verbose_and_node_identity_outputs_are_required(self):
        mutations = [
            lambda out: out.update(personad_verbose="0.5.0\nchannel release, build %d" % BUILD),
            lambda out: out["node_check"].update(arch="arm64"),
            lambda out: out["node_check"].update(program="persona-node"),
            lambda out: out["node_check"].update(version="0.5.0"),
            lambda out: out.pop("personad_verbose"),
        ]
        for change in mutations:
            report = self.report()
            change(report["outputs"])
            with self.subTest(change=change), self.assertRaisesRegex(ValueError, "command outputs"):
                release.check_windows_archives(self.assets(report), VERSION)

    def test_stale_zip_or_member_hash_or_size_is_rejected(self):
        mutations = [
            lambda report: report["archive"].update(sha256="0" * 64),
            lambda report: report["archive"].update(size=1),
            lambda report: report["binaries"]["personad.exe"].update(sha256="0" * 64),
            lambda report: report["binaries"]["persona-link.exe"].update(size=1),
        ]
        for change in mutations:
            report = self.report()
            change(report)
            with self.subTest(change=change), self.assertRaisesRegex(ValueError, "hashes"):
                release.check_windows_archives(self.assets(report), VERSION)
        assets = self.assets()
        self.make_zip([(self.archive.stem + "/" + name, b"replacement") for name in release.PROGRAMS])
        with self.assertRaisesRegex(ValueError, "hashes"):
            release.check_windows_archives(assets, VERSION)

    def test_metadata_is_required_and_all_available_indexes_must_agree(self):
        assets = self.assets()
        with self.assertRaisesRegex(ValueError, "require latest.json or BUILD.json"):
            release.check_windows_archives([a for a in assets if a["name"] != "BUILD.json"], VERSION)
        latest = self.root / "latest.json"
        latest.write_text(json.dumps({"version": VERSION, "build": BUILD}))
        assets.append({"name": latest.name, "path": str(latest)})
        release.check_windows_archives(assets, VERSION)
        release.check_windows_archives([a for a in assets if a["name"] != "BUILD.json"], VERSION)
        for index in ({"version": VERSION, "build": BUILD - 1},
                      {"version": "0.5.0", "build": BUILD},
                      {"version": VERSION, "build": str(BUILD)}):
            latest.write_text(json.dumps(index))
            with self.subTest(index=index), self.assertRaises(ValueError):
                release.check_windows_archives(assets, VERSION)

    def test_archive_name_cannot_relabel_another_version(self):
        other = self.archive.with_name("Persona-0.5.0-windows-amd64.zip")
        self.archive.rename(other)
        with self.assertRaisesRegex(ValueError, "requested version"):
            release.check_windows_archives([
                {"name": other.name, "path": str(other)}, {"name": "BUILD.json", "path": str(self.metadata)}
            ], VERSION)

    def test_misspelled_windows_bundle_name_cannot_bypass_checks(self):
        for name in ("Persona-0.5.3-windows-x64.zip", "persona-0.5.3-Windows-amd64.ZIP"):
            path = self.root / name
            path.write_bytes(self.archive.read_bytes())
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, "requested version"):
                release.check_windows_archives([
                    {"name": name, "path": str(path)}, {"name": "BUILD.json", "path": str(self.metadata)}
                ], VERSION)

    def test_member_pe_architecture_must_match_archive_even_with_a_report(self):
        entries = [(self.archive.stem + "/" + name, pe_fixture(machine=0xAA64)) for name in release.PROGRAMS]
        self.make_zip(entries)
        with self.assertRaisesRegex(ValueError, "PE architecture"):
            release.inspect_archive(self.archive, VERSION)

    def test_unsafe_missing_duplicate_members_are_rejected_before_commands(self):
        normal = [(self.archive.stem + "/" + name, b"fixture") for name in release.PROGRAMS]
        bad_entries = [
            normal[:1], normal + normal[:1],
            normal + [(normal[0][0].upper(), b"duplicate on Windows")],
            normal + [("../escaped.exe", b"bad")],
            normal + [("/absolute.exe", b"bad")],
            normal + [("folder\\bad.exe", b"bad")],
            normal + [("folder/./bad.exe", b"bad")],
            normal + [("folder/bad.exe:stream", b"bad")],
        ]
        for entries in bad_entries:
            self.make_zip(entries)
            with self.subTest(entries=entries), patch.object(release, "command_output") as command:
                with self.assertRaises(ValueError):
                    release.inspect_archive(self.archive, VERSION)
                command.assert_not_called()
        self.assertFalse((self.root / "escaped.exe").exists())

    def run_command(self, command, **kwargs):
        self.assertEqual(kwargs["timeout"], release.COMMAND_TIMEOUT)
        self.assertTrue(kwargs["check"])
        self.assertEqual(Path(kwargs["env"]["USERPROFILE"]).parent, Path(kwargs["cwd"]))
        self.assertEqual(kwargs["env"]["HOME"], kwargs["env"]["USERPROFILE"])
        self.assertEqual(Path(kwargs["env"]["PERSONA_HOME"]).parent, Path(kwargs["env"]["HOME"]))
        self.assertNotIn("PERSONA_CONFIG", kwargs["env"])
        for name in ("PERSONA_TOKEN", "XDG_CONFIG_HOME", "HTTP_PROXY", "PGHOST"):
            self.assertNotIn(name, kwargs["env"])
        program = Path(command[0]).name
        self.assertTrue(Path(command[0]).is_file())
        if command[1:] == ["node-update-check"]:
            output = json.dumps({**self.outputs["node_check"], "future_capability": "not recorded"})
        elif command[1:] == ["version", "-v"]:
            output = self.outputs["personad_verbose"]
        else:
            self.assertEqual(command[1:], ["version"])
            output = self.outputs["link_version" if program == "persona-link.exe" else "personad_version"]
        return subprocess.CompletedProcess(command, 0, output + "\r\n", "")

    def native_context(self):
        return patch.multiple(release.platform, system=lambda: "Windows", machine=lambda: "AMD64")

    def test_native_check_runs_four_bounded_commands_and_cleans_temporary_files(self):
        paths = []

        def command(argv, **kwargs):
            paths.append(Path(argv[0]))
            return self.run_command(argv, **kwargs)

        with self.native_context(), patch.dict(release.os.environ, {
                "PERSONA_CONFIG": "fixture-profile-config", "PERSONA_TOKEN": "fixture-token",
                "XDG_CONFIG_HOME": "fixture-config", "HTTP_PROXY": "https://proxy.example.com",
                "PGHOST": "db.example.com"}), \
                patch.object(release, "clean_environment", wraps=release.clean_environment) as cleaned, \
                patch.object(release.subprocess, "run", side_effect=command) as run:
            report = release.verify_native(self.archive, VERSION, BUILD)
        cleaned.assert_called_once()
        self.assertEqual(run.call_count, 4)
        self.assertEqual(report["outputs"], self.outputs)
        self.assertTrue(all(not path.exists() for path in paths))
        release.check_windows_archives(self.assets(report), VERSION)

    def test_wrong_native_host_is_not_executed(self):
        for system, machine in (("Linux", "x86_64"), ("Windows", "ARM64"), ("Windows", "unknown")):
            with self.subTest(system=system, machine=machine), \
                    patch.object(release.platform, "system", return_value=system), \
                    patch.object(release.platform, "machine", return_value=machine), \
                    patch.object(release.subprocess, "run") as run:
                with self.assertRaisesRegex(ValueError, "matching native"):
                    release.verify_native(self.archive, VERSION, BUILD)
                run.assert_not_called()

    def test_timeout_or_failed_command_leaves_no_success_report(self):
        for failure in (subprocess.TimeoutExpired("version", release.COMMAND_TIMEOUT),
                        subprocess.CalledProcessError(1, "version")):
            self.report_path.write_text(json.dumps(self.report()))
            with self.subTest(failure=type(failure).__name__), self.native_context(), \
                    patch.object(release.subprocess, "run", side_effect=failure), \
                    patch.object(sys, "stderr"):
                with self.assertRaises(SystemExit) as exit_code:
                    release.main(["--archive", str(self.archive), "--version", VERSION, "--build", str(BUILD)])
                self.assertEqual(exit_code.exception.code, 1)
            self.assertFalse(self.report_path.exists())

    def test_cli_writes_only_a_successful_report(self):
        with self.native_context(), patch.object(release.subprocess, "run", side_effect=self.run_command):
            self.assertEqual(release.main([
                "--archive", str(self.archive), "--version", VERSION, "--build", str(BUILD)]), 0)
        report = json.loads(self.report_path.read_text())
        self.assertEqual(report["outputs"], self.outputs)
        self.assertNotIn(str(self.root), self.report_path.read_text())
        self.assertEqual(list(self.root.glob(".windows-verification-*")), [])
        self.outputs["link_version"] = "0.5.0"
        with self.native_context(), patch.object(release.subprocess, "run", side_effect=self.run_command), \
                patch.object(sys, "stderr"), self.assertRaises(SystemExit):
            release.main(["--archive", str(self.archive), "--version", VERSION, "--build", str(BUILD)])
        self.assertFalse(self.report_path.exists())

    @unittest.skipUnless(os.name == "nt" and release.platform.machine().lower() in ("amd64", "x86_64")
                         and shutil.which("go"), "requires native Windows amd64 and Go")
    def test_real_windows_executables_reject_same_build_with_old_version(self):
        source = self.root / "fixture.go"
        source.write_text("""package main
import ("fmt"; "os"; "runtime")
var version = "dev"
func main() {
 if len(os.Args) > 1 && os.Args[1] == "node-update-check" {
  fmt.Printf(`{"version":%q,"os":%q,"arch":%q,"program":"personad"}\n`, version, runtime.GOOS, runtime.GOARCH)
  return
 }
 fmt.Println(version)
 if len(os.Args) > 2 && os.Args[2] == "-v" { fmt.Println("channel release, build 202611140822") }
}
""")
        executable = self.root / "fixture.exe"
        environment = {**os.environ, "GO111MODULE": "off", "GOWORK": "off", "GOTOOLCHAIN": "local"}
        for version in (VERSION, "0.5.0"):
            subprocess.run(["go", "build", "-trimpath", "-ldflags", "-X main.version=" + version,
                            "-o", str(executable), str(source)], env=environment, check=True,
                           capture_output=True, timeout=120)
            self.make_zip([(self.archive.stem + "/" + name, executable.read_bytes()) for name in release.PROGRAMS])
            if version == VERSION:
                report = release.verify_native(self.archive, VERSION, BUILD)
                release.check_windows_archives(self.assets(report), VERSION)
            else:
                with self.assertRaisesRegex(ValueError, "command outputs"):
                    release.verify_native(self.archive, VERSION, BUILD)


class WindowsGzipReleaseTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="windows-gzip-release-tests-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.program = "persona-link"
        self.path = self.root / "persona-link-windows-amd64.exe.gz"
        self.binary = pe_fixture(b"the exact native executable")
        self.path.write_bytes(gzip.compress(self.binary, mtime=0))
        self.metadata = self.root / "BUILD.json"
        self.metadata.write_text(json.dumps({"version": VERSION, "build": BUILD}))
        self.outputs = {"version": VERSION, "verbose": "%s\nchannel release, build %d" % (VERSION, BUILD)}

    def report(self, program=None):
        program = program or self.program
        platform_name = self.path.name.removesuffix(".exe.gz").rsplit("-", 2)[-2] + "-" + self.path.name.removesuffix(".exe.gz").rsplit("-", 1)[-1]
        archive, members = release.inspect_gzip(self.path, program, platform_name)
        return {"schema": 2, "version": VERSION, "build": BUILD, "platform": platform_name,
                "channel": "release", "program": program, "archive": archive,
                "binaries": members, "outputs": copy.deepcopy(self.outputs)}

    def assets(self, report=None, with_report=True):
        paths = [self.path, self.metadata]
        latest = self.root / "latest.json"
        if latest.is_file():
            paths.append(latest)
        report_path = self.root / release.report_name(self.path.name)
        if with_report:
            report_path.write_text(json.dumps(report or self.report()))
            paths.append(report_path)
        return [{"name": path.name, "path": str(path)} for path in paths]

    def test_current_link_archive_requires_and_accepts_its_exact_native_report(self):
        assets = self.assets()
        release.check_windows_artifacts(assets, VERSION)
        with self.assertRaisesRegex(ValueError, "native verification report"):
            release.check_windows_artifacts(assets[:-1], VERSION)
        for change in ("version", "build", "platform", "program", "channel", "archive", "binaries", "outputs"):
            report = self.report()
            if change == "archive":
                report[change]["sha256"] = "0" * 64
            elif change == "binaries":
                report[change]["persona-link.exe"]["sha256"] = "0" * 64
            elif change == "outputs":
                report[change]["version"] = "0.5.0"
            elif change == "build":
                report[change] = BUILD - 1
            elif change == "version":
                report[change] = "0.5.0"
            else:
                report[change] = "different"
            with self.subTest(change=change), self.assertRaises(ValueError):
                release.check_windows_artifacts(self.assets(report), VERSION)

    def test_the_report_binds_the_decompressed_program_not_only_a_filename(self):
        stale = self.report()
        self.path.write_bytes(gzip.compress(pe_fixture(b"same-named replacement"), mtime=0))
        with self.assertRaisesRegex(ValueError, "hashes"):
            release.check_windows_artifacts(self.assets(stale), VERSION)
        self.path.write_bytes(b"not a gzip archive")
        with self.assertRaises((OSError, EOFError, gzip.BadGzipFile)):
            release.check_windows_artifacts(self.assets(), VERSION)

    def test_node_program_must_be_listed_in_the_exact_manifest_and_self_check(self):
        self.program = "persona-node"
        self.path = self.root / "persona-node-windows-amd64.exe.gz"
        self.path.write_bytes(gzip.compress(self.binary, mtime=0))
        self.outputs["node_check"] = {"version": VERSION, "os": "windows", "arch": "amd64", "program": "persona-node"}
        latest = self.root / "latest.json"
        file_entry = {"name": self.path.name, "sha256": hashlib.sha256(self.path.read_bytes()).hexdigest(),
                      "binary_sha256": hashlib.sha256(self.binary).hexdigest()}
        latest.write_text(json.dumps({"version": VERSION, "build": BUILD,
                                      "nodes": {"persona-node": {"windows-amd64": file_entry}}}))
        release.check_windows_artifacts(self.assets(), VERSION)
        for mutation in (lambda d: d.update(build=BUILD - 1),
                         lambda d: d["nodes"]["persona-node"]["windows-amd64"].update(name="other.exe.gz"),
                         lambda d: d["nodes"]["persona-node"]["windows-amd64"].update(binary_sha256="0" * 64)):
            manifest = json.loads(latest.read_text())
            mutation(manifest)
            latest.write_text(json.dumps(manifest))
            with self.subTest(manifest=manifest), self.assertRaises(ValueError):
                release.check_windows_artifacts(self.assets(), VERSION)
        latest.write_text(json.dumps({"version": VERSION, "build": BUILD,
                                      "nodes": {"persona-node": {"windows-amd64": file_entry}}}))
        for program in ("personad", "another"):
            outputs = self.report()
            outputs["outputs"]["node_check"]["program"] = program
            with self.subTest(program=program), self.assertRaisesRegex(ValueError, "command outputs"):
                release.check_windows_artifacts(self.assets(outputs), VERSION)

    def test_beta_verbose_output_is_not_misreported_as_stable(self):
        self.path = self.root / "persona-link-windows-arm64.exe.gz"
        self.path.write_bytes(gzip.compress(pe_fixture(b"the exact native executable", machine=0xAA64), mtime=0))
        self.metadata.write_text(json.dumps({"version": "0.5.3-beta.1", "build": BUILD}))
        report = self.report()
        report.update(version="0.5.3-beta.1", platform="windows-arm64", channel="beta")
        report["outputs"]["verbose"] = "0.5.3-beta.1\nchannel release, build %d" % BUILD
        report["outputs"]["version"] = "0.5.3-beta.1"
        report["archive"], report["binaries"] = release.inspect_gzip(self.path, "persona-link", "windows-arm64")
        release.check_windows_artifacts(self.assets(report), "0.5.3-beta.1")
        report["channel"] = "release"
        with self.assertRaisesRegex(ValueError, "identity"):
            release.check_windows_artifacts(self.assets(report), "0.5.3-beta.1")

    def test_native_commands_are_bounded_and_isolated(self):
        with patch.object(release.platform, "system", return_value="Windows"), \
                patch.object(release.platform, "machine", return_value="AMD64"), \
                patch.object(release.subprocess, "run") as run:
            run.side_effect = lambda command, **kwargs: subprocess.CompletedProcess(
                command, 0, self.outputs["verbose"] if command[1:] == ["version", "-v"] else VERSION + "\r\n", "")
            result = release.verify_native(self.path, VERSION, BUILD)
        self.assertEqual(result["outputs"], self.outputs)
        self.assertEqual(run.call_count, 2)
        for call in run.call_args_list:
            self.assertEqual(call.kwargs["timeout"], release.COMMAND_TIMEOUT)
            self.assertNotIn("PERSONA_TOKEN", call.kwargs["env"])
        with patch.object(release.platform, "system", return_value="Darwin"), \
                patch.object(release.subprocess, "run") as run:
            with self.assertRaisesRegex(ValueError, "native Windows"):
                release.verify_native(self.path, VERSION, BUILD)
            run.assert_not_called()

    def test_main_writes_a_gzip_report_only_after_a_valid_native_run(self):
        with patch.object(release.platform, "system", return_value="Windows"), \
                patch.object(release.platform, "machine", return_value="AMD64"), \
                patch.object(release.subprocess, "run") as run:
            run.side_effect = lambda command, **kwargs: subprocess.CompletedProcess(
                command, 0, self.outputs["verbose"] if command[1:] == ["version", "-v"] else VERSION + "\r\n", "")
            self.assertEqual(release.main(["--archive", str(self.path), "--version", VERSION, "--build", str(BUILD)]), 0)
        report = json.loads((self.root / release.report_name(self.path.name)).read_text())
        self.assertEqual(report["program"], "persona-link")
        self.assertEqual(report["binaries"]["persona-link.exe"]["sha256"], hashlib.sha256(self.binary).hexdigest())
        release.check_windows_artifacts(self.assets(), VERSION)


if __name__ == "__main__":
    unittest.main()

if __name__ == "__main__":
    unittest.main()
