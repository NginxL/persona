#!/usr/bin/env python3
"""Verify Windows CLI ZIPs and gzip executables natively before release upload.

Receipts are outputs of the trusted release process, not remote attestations. The
uploader checks identity and hashes so old results cannot verify different bytes.
"""
import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import stat
import subprocess
import sys
import tempfile
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "test"))
from server_test_env import clean_environment

ARCHIVE = re.compile(r"Persona-([0-9A-Za-z][0-9A-Za-z._-]*)-windows-(amd64|arm64)\.zip\Z")
ARCHIVE_FAMILY = re.compile(r"Persona-.*-windows-.*\.zip\Z", re.IGNORECASE)
PROGRAMS = ("persona-link.exe", "personad.exe")
# The current CLI release keeps each Go executable in its own .exe.gz. The historical two-program ZIP remains
# supported so an old receipt cannot bypass its original checks.
GZIP = re.compile(r"(personad|persona-node|persona-link)-(windows)-(amd64|arm64)\.exe\.gz\Z")
MAX_BINARY_SIZE = 512 * 1024 * 1024
COMMAND_TIMEOUT = 20


class WindowsReleaseError(ValueError):
    """A release-check failure whose message contains no machine paths or command output."""


def check_pe_header(header, target):
    if len(header) < 64 or header[:2] != b"MZ":
        raise WindowsReleaseError("Windows executable has no PE header")
    offset = int.from_bytes(header[60:64], "little")
    machine = {"windows-amd64": 0x8664, "windows-arm64": 0xAA64}[target]
    if (offset < 64 or offset + 6 > len(header) or header[offset:offset + 4] != b"PE\0\0"
            or int.from_bytes(header[offset + 4:offset + 6], "little") != machine):
        raise WindowsReleaseError("Windows executable PE architecture differs from the archive")


def report_name(name):
    for suffix in (".zip", ".exe.gz"):
        if name.endswith(suffix):
            return name[:-len(suffix)] + ".verification.json"
    raise WindowsReleaseError("Windows artifact must be a ZIP or .exe.gz")


def fingerprint(path):
    digest = hashlib.sha256()
    size = 0
    with Path(path).open("rb") as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b""):
            size += len(chunk)
            digest.update(chunk)
    return {"size": size, "sha256": digest.hexdigest()}


def archive_identity(name, version):
    match = ARCHIVE.fullmatch(name)
    if match is None or match[1] != version:
        raise WindowsReleaseError("Windows archive name does not match the requested version")
    return "windows-" + match[2]


def inspect_archive(path, version, destination=None):
    """Hash the exact members; extract only the two allowlisted files, without extractall."""
    path = Path(path)
    target = archive_identity(path.name, version)
    before = fingerprint(path)
    members = {}
    with zipfile.ZipFile(path) as archive:
        entries = {}
        for entry in archive.infolist():
            # ZipInfo normalizes backslashes on Windows and truncates NULs; validate the original spelling.
            name = entry.orig_filename
            parts = name.rstrip("/").split("/")
            if (not name or "\0" in name or "\\" in name or PurePosixPath(name).is_absolute()
                    or any(part in ("", ".", "..") or ":" in part for part in parts)
                    or stat.S_ISLNK(entry.external_attr >> 16)):
                raise WindowsReleaseError("Unsafe path in Windows archive")
            if name.casefold() in entries:
                raise WindowsReleaseError("Duplicate path in Windows archive")
            entries[name.casefold()] = entry
        for program in PROGRAMS:
            member = path.stem + "/" + program
            entry = entries.get(member.casefold())
            if entry is None or entry.filename != member or entry.is_dir():
                raise WindowsReleaseError("Windows archive is missing " + program)
            if not 0 < entry.file_size <= MAX_BINARY_SIZE:
                raise WindowsReleaseError("Invalid Windows executable size")
            digest, size, header = hashlib.sha256(), 0, b""
            output = None
            try:
                if destination is not None:
                    output = (Path(destination) / program).open("wb")
                with archive.open(entry) as stream:
                    for chunk in iter(lambda: stream.read(1 << 20), b""):
                        if not header:
                            header = chunk
                        size += len(chunk)
                        if size > MAX_BINARY_SIZE:
                            raise WindowsReleaseError("Windows executable exceeds the size limit")
                        digest.update(chunk)
                        if output is not None:
                            output.write(chunk)
            finally:
                if output is not None:
                    output.close()
            if size != entry.file_size:
                raise WindowsReleaseError("Windows executable size differs from ZIP entry")
            check_pe_header(header, target)
            members[program] = {"size": size, "sha256": digest.hexdigest()}
    if fingerprint(path) != before:
        raise WindowsReleaseError("Windows archive changed during verification")
    return {"name": path.name, **before}, members


def gzip_identity(name):
    match = GZIP.fullmatch(name)
    if match is None:
        raise WindowsReleaseError("Windows executable name does not match a supported release program")
    return match[1], match[2] + "-" + match[3]


def expected_channel(version):
    # A beta manifest has its own feed. Keep the binary's existing release-channel stamp: beta is a distribution
    # prerelease, not a new Go runtime channel. The receipt records the selected feed independently.
    return "release"


def check_gzip_manifest(path, program, target, version, build, manifest_path):
    if program == "persona-link":
        return
    if manifest_path is None or not manifest_path.is_file():
        raise WindowsReleaseError("Windows node executable requires latest.json in the artifact receipt")
    metadata = json.loads(manifest_path.read_text(encoding="utf-8"))
    if metadata.get("version") != version or metadata.get("build") != build:
        raise WindowsReleaseError("latest.json does not identify the Windows node release")
    entry = metadata.get("files", {}).get(target) if program == "personad" else metadata.get("nodes", {}).get(program, {}).get(target)
    if not isinstance(entry, dict) or entry.get("name") != path.name or entry.get("sha256") != fingerprint(path)["sha256"]:
        raise WindowsReleaseError("latest.json does not list this exact Windows node executable")
    with tempfile.TemporaryFile() as unpacked:
        with gzip.open(path, "rb") as source:
            while chunk := source.read(1 << 20):
                unpacked.write(chunk)
        unpacked.seek(0)
        inner = hashlib.file_digest(unpacked, "sha256").hexdigest()
    if entry.get("binary_sha256") != inner:
        raise WindowsReleaseError("latest.json's Windows executable digest differs from its bytes")


def inspect_gzip(path, program, target, destination=None):
    before = fingerprint(path)
    if not 0 < before["size"] <= MAX_BINARY_SIZE:
        raise WindowsReleaseError("Invalid Windows gzip executable size")
    digest, size, header = hashlib.sha256(), 0, b""
    output = (Path(destination) / (program + ".exe")).open("wb") if destination is not None else None
    try:
        with gzip.open(path, "rb") as stream:
            for chunk in iter(lambda: stream.read(1 << 20), b""):
                if not header:
                    header = chunk
                size += len(chunk)
                if size > MAX_BINARY_SIZE:
                    raise WindowsReleaseError("Windows executable exceeds the size limit")
                digest.update(chunk)
                if output is not None:
                    output.write(chunk)
    finally:
        if output is not None:
            output.close()
    if not 0 < size <= MAX_BINARY_SIZE:
        raise WindowsReleaseError("Invalid Windows executable size")
    check_pe_header(header, target)
    if fingerprint(path) != before:
        raise WindowsReleaseError("Windows executable archive changed during verification")
    return {"name": path.name, **before}, {program + ".exe": {"size": size, "sha256": digest.hexdigest()}}


def expected_build(assets, version):
    """Either release index may provide the build; all supplied indexes must agree."""
    builds = []
    for name in ("latest.json", "BUILD.json"):
        if name not in assets:
            continue
        metadata = json.loads(assets[name].read_text(encoding="utf-8"))
        build = metadata.get("build") if isinstance(metadata, dict) else None
        if (not isinstance(metadata, dict) or metadata.get("version") != version
                or type(build) is not int or not re.fullmatch(r"[0-9]{12}", str(build))):
            raise WindowsReleaseError(name + " does not identify the requested Windows release")
        builds.append(build)
    if not builds:
        raise WindowsReleaseError("Windows archives require latest.json or BUILD.json in the receipt")
    if len(set(builds)) != 1:
        raise WindowsReleaseError("Release indexes disagree on the Windows build")
    return builds[0]


def check_outputs(outputs, version, build, platform_name):
    """Match actual command results, never a version-shaped string elsewhere in a binary."""
    if not isinstance(outputs, dict):
        raise WindowsReleaseError("Windows verification has no command outputs")
    expected = {
        "link_version": version,
        "personad_version": version,
        "personad_verbose": "%s\nchannel %s, build %d" % (version, expected_channel(version), build),
        "node_check": {"version": version, "os": "windows",
                       "arch": platform_name.removeprefix("windows-"), "program": "personad"},
    }
    if outputs != expected:
        raise WindowsReleaseError("Windows executable command outputs differ from the requested release")


def check_windows_archives(assets, expected_version):
    """Reject absent, stale or mismatched native checks before any release asset upload."""
    archives = [asset for asset in assets if ARCHIVE_FAMILY.fullmatch(asset["name"])]
    if not archives:
        return
    by_name = {asset["name"]: Path(asset["path"]) for asset in assets}
    if len(by_name) != len(assets):
        raise WindowsReleaseError("Duplicate asset names in Windows release receipt")
    build = expected_build(by_name, expected_version)
    for asset in archives:
        name, path = asset["name"], Path(asset["path"])
        target = archive_identity(name, expected_version)
        if path.name != name:
            raise WindowsReleaseError("Windows archive path and asset name differ")
        report_path = by_name.get(report_name(name))
        if report_path is None:
            raise WindowsReleaseError("Windows archive requires its native verification report: " + report_name(name))
        report = json.loads(report_path.read_text(encoding="utf-8"))
        if (not isinstance(report, dict) or type(report.get("schema")) is not int or report["schema"] != 1
                or report.get("version") != expected_version or report.get("build") != build
                or report.get("platform") != target):
            raise WindowsReleaseError("Windows verification report has the wrong release identity")
        archive, members = inspect_archive(path, expected_version)
        if report.get("archive") != archive or report.get("binaries") != members:
            raise WindowsReleaseError("Windows verification report hashes do not match the archive")
        check_outputs(report.get("outputs"), expected_version, build, target)


def check_gzip_outputs(outputs, program, version, build, target):
    expected = {
        "version": version,
        "verbose": "%s\nchannel %s, build %d" % (version, expected_channel(version), build),
    }
    if program in ("personad", "persona-node"):
        expected["node_check"] = {"version": version, "os": "windows",
                                  "arch": target.removeprefix("windows-"), "program": program}
    if not isinstance(outputs, dict) or outputs != expected:
        raise WindowsReleaseError("Windows executable command outputs differ from the requested release")


def check_gzip_executables(assets, expected_version):
    """Require a native report for each current .exe.gz, bound to the exact compressed and executable bytes."""
    executables = [asset for asset in assets if GZIP.fullmatch(asset["name"])]
    if not executables:
        return
    by_name = {asset["name"]: Path(asset["path"]) for asset in assets}
    if len(by_name) != len(assets):
        raise WindowsReleaseError("Duplicate asset names in Windows release receipt")
    build = expected_build(by_name, expected_version)
    for asset in executables:
        name, path = asset["name"], Path(asset["path"])
        program, target = gzip_identity(name)
        if path.name != name:
            raise WindowsReleaseError("Windows executable path and asset name differ")
        report_path = by_name.get(report_name(name))
        if report_path is None:
            raise WindowsReleaseError("Windows executable requires its native verification report: " + report_name(name))
        report = json.loads(report_path.read_text(encoding="utf-8"))
        if (not isinstance(report, dict) or type(report.get("schema")) is not int or report["schema"] != 2
                or report.get("version") != expected_version or report.get("build") != build
                or report.get("platform") != target or report.get("program") != program
                or report.get("channel") != ("beta" if "-beta." in expected_version else "release")):
            raise WindowsReleaseError("Windows executable verification report has the wrong release identity")
        check_gzip_manifest(path, program, target, expected_version, build, by_name.get("latest.json"))
        archive, members = inspect_gzip(path, program, target)
        if report.get("archive") != archive or report.get("binaries") != members:
            raise WindowsReleaseError("Windows executable verification report hashes do not match the archive")
        check_gzip_outputs(report.get("outputs"), program, expected_version, build, target)


def check_windows_artifacts(assets, expected_version):
    check_windows_archives(assets, expected_version)
    check_gzip_executables(assets, expected_version)


def verify_gzip_native(path, program, target, version, build):
    if type(build) is not int or not re.fullmatch(r"[0-9]{12}", str(build)):
        raise WindowsReleaseError("Expected Windows build must be a twelve-digit number")
    with tempfile.TemporaryDirectory(prefix="persona-windows-release-check-") as directory:
        archive, members = inspect_gzip(path, program, target, directory)
        executable = Path(directory) / (program + ".exe")
        environment = clean_environment(os.environ)
        home = Path(directory) / "home"
        home.mkdir()
        for name in ("AppData", "LocalAppData"):
            (home / name).mkdir()
        environment.update(HOME=str(home), USERPROFILE=str(home), APPDATA=str(home / "AppData"),
                           LOCALAPPDATA=str(home / "LocalAppData"), PERSONA_HOME=str(home / ".persona"))
        outputs = {
            "version": command_output(executable, ["version"], directory, environment),
            "verbose": command_output(executable, ["version", "-v"], directory, environment),
        }
        if program in ("personad", "persona-node"):
            node_check = json.loads(command_output(executable, ["node-update-check"], directory, environment))
            if not isinstance(node_check, dict):
                raise WindowsReleaseError("Node self-check did not return an object")
            outputs["node_check"] = {key: node_check.get(key) for key in ("version", "os", "arch", "program")}
        check_gzip_outputs(outputs, program, version, build, target)
    if fingerprint(path) != {key: archive[key] for key in ("size", "sha256")}:
        raise WindowsReleaseError("Windows executable archive changed while its program was checked")
    return {"schema": 2, "version": version, "build": build, "platform": target,
            "channel": "beta" if "-beta." in version else "release", "program": program,
            "archive": archive, "binaries": members, "outputs": outputs}


def command_output(path, arguments, directory, environment):
    result = subprocess.run([str(path), *arguments], capture_output=True, text=True,
                            encoding="utf-8", timeout=COMMAND_TIMEOUT, check=True,
                            cwd=directory, env=environment)
    if len(result.stdout) > 16384:
        raise WindowsReleaseError("Windows version command returned too much output")
    return result.stdout.replace("\r\n", "\n").strip()


def verify_native(path, version, build):
    """Run only version/self-check commands on the archive's matching native Windows host."""
    path = Path(path)
    machine = platform.machine().lower()
    architecture = {"amd64": "amd64", "x86_64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(machine)
    if platform.system() != "Windows":
        raise WindowsReleaseError("Windows artifacts must be verified on their matching native Windows architecture")
    gzip_match = GZIP.fullmatch(path.name)
    if gzip_match:
        program, target = gzip_identity(path.name)
        if target != "windows-" + str(architecture):
            raise WindowsReleaseError("Windows executable must be verified on its matching native Windows architecture")
        return verify_gzip_native(path, program, target, version, build)
    target = archive_identity(path.name, version)
    if platform.system() != "Windows" or target != "windows-" + str(architecture):
        raise WindowsReleaseError("Windows archive must be verified on its matching native Windows architecture")
    if type(build) is not int or not re.fullmatch(r"[0-9]{12}", str(build)):
        raise WindowsReleaseError("Expected Windows build must be a twelve-digit number")
    with tempfile.TemporaryDirectory(prefix="persona-windows-release-check-") as directory:
        archive, members = inspect_archive(path, version, directory)
        link, node = (Path(directory) / name for name in PROGRAMS)
        environment = clean_environment(os.environ)
        home = Path(directory) / "home"
        home.mkdir()
        for name in ("AppData", "LocalAppData"):
            (home / name).mkdir()
        environment.update(HOME=str(home), USERPROFILE=str(home), APPDATA=str(home / "AppData"),
                           LOCALAPPDATA=str(home / "LocalAppData"), PERSONA_HOME=str(home / ".persona"))

        def output(program, arguments):
            return command_output(program, arguments, directory, environment)

        outputs = {"link_version": output(link, ["version"]),
                   "personad_version": output(node, ["version"]),
                   "personad_verbose": output(node, ["version", "-v"])}
        node_check = json.loads(output(node, ["node-update-check"]))
        if not isinstance(node_check, dict):
            raise WindowsReleaseError("Node self-check did not return an object")
        # Future node capabilities are not release identity and must not copy private data into the report.
        outputs["node_check"] = {key: node_check.get(key) for key in ("version", "os", "arch", "program")}
        check_outputs(outputs, version, build, target)
    if fingerprint(path) != {key: archive[key] for key in ("size", "sha256")}:
        raise WindowsReleaseError("Windows archive changed while its programs were checked")
    return {"schema": 1, "version": version, "build": build, "platform": target,
            "archive": archive, "binaries": members, "outputs": outputs}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--build", type=int, required=True)
    args = parser.parse_args(argv)
    output = args.archive.with_name(report_name(args.archive.name))
    temporary = None
    try:
        # A failed recheck must not leave an earlier successful report beside this artifact.
        if output.exists() or output.is_symlink():
            output.unlink()
        report = verify_native(args.archive, args.version, args.build)
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=output.parent,
                                         prefix=".windows-verification-", delete=False) as stream:
            temporary = Path(stream.name)
            json.dump(report, stream, indent=2)
            stream.write("\n")
        temporary.replace(output)
    except (OSError, ValueError, zipfile.BadZipFile, gzip.BadGzipFile, EOFError, subprocess.SubprocessError) as error:
        # Child stdout/stderr and operating-system errors may contain machine paths.
        reason = "invalid release data"
        if isinstance(error, WindowsReleaseError):
            reason = str(error)
        elif isinstance(error, subprocess.TimeoutExpired):
            reason = "version command timed out"
        elif isinstance(error, subprocess.CalledProcessError):
            reason = "version command failed"
        elif isinstance(error, zipfile.BadZipFile):
            reason = "invalid ZIP archive"
        elif isinstance(error, OSError):
            reason = "could not read archive or write report"
        parser.exit(1, "Windows release verification failed: %s; no report was written.\n" % reason)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)
    print("Verified " + args.archive.name)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
