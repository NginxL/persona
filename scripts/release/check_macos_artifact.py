#!/usr/bin/env python3
"""Check a packaged Mac app and optional DMG without signing or mounting them.

Usage: python3 scripts/release/check_macos_artifact.py path/to/Persona.app [path/to/Persona.dmg]
"""

from pathlib import Path
import plistlib
import re
import subprocess
import sys

EXPECTED_MINIMUM = "15.0"
EXPECTED_ARCHITECTURES = ("x86_64", "arm64")


class CheckError(Exception):
    """An artifact could not be inspected or an inspection tool failed."""


def run_tool(command, run, timeout):
    runner = run or subprocess.run
    try:
        result = runner(command, capture_output=True, text=True, timeout=timeout, check=False)
    except (OSError, subprocess.SubprocessError) as error:
        raise CheckError(f"{command[0]} failed for {command[-1]}: {error}") from error
    if result.returncode != 0:
        detail = (result.stderr or result.stdout or "unknown error").strip()
        raise CheckError(f"{command[0]} failed for {command[-1]}: {detail}")
    return result.stdout or ""


def build_versions(path, run=None):
    """Return (architecture, minimum OS) for every Mach-O slice reported by vtool."""
    output = run_tool(["vtool", "-show-build", str(path)], run, 30)
    versions = []
    architecture = None
    platform = None
    for line in output.splitlines():
        if line and not line[0].isspace() and line.endswith(":"):
            match = re.search(r" \(architecture ([^)]+)\):$", line)
            architecture = match.group(1) if match else ""
            platform = None
            continue
        stripped = line.strip()
        if stripped.startswith("platform "):
            platform = stripped.split(None, 1)[1]
        elif stripped.startswith("minos ") and platform == "MACOS" and architecture is not None:
            versions.append((architecture, stripped.split(None, 1)[1]))
    if not versions:
        raise CheckError(f"vtool reported no macOS build versions for {path}")
    return versions


def check_bundle(bundle, run):
    name = bundle.name
    info_path = bundle / "Contents" / "Info.plist"
    try:
        with info_path.open("rb") as source:
            info = plistlib.load(source)
    except (OSError, ValueError, plistlib.InvalidFileException) as error:
        raise CheckError(f"cannot read {info_path}: {error}") from error

    failures = []
    minimum = info.get("LSMinimumSystemVersion")
    if minimum != EXPECTED_MINIMUM:
        found = minimum if minimum is not None else "missing"
        failures.append(f"{name}: LSMinimumSystemVersion is {found}; expected {EXPECTED_MINIMUM}")

    executable = info.get("CFBundleExecutable")
    if not isinstance(executable, str) or not executable:
        raise CheckError(f"{info_path}: CFBundleExecutable is missing")
    binary = bundle / "Contents" / "MacOS" / executable
    if not binary.is_file():
        raise CheckError(f"{name}: executable does not exist: {binary}")

    versions = build_versions(binary, run)
    for architecture in EXPECTED_ARCHITECTURES:
        slices = [minimum for actual, minimum in versions if actual == architecture]
        if not slices:
            failures.append(f"{name}: missing {architecture}; expected x86_64 and arm64")
        for actual_minimum in slices:
            if actual_minimum != EXPECTED_MINIMUM:
                failures.append(
                    f"{name}: {architecture} minimum is {actual_minimum}; expected {EXPECTED_MINIMUM}"
                )
    return failures


def check_app(path, run=None):
    """Return deployment-contract failures for an app and any bundled extensions."""
    app = Path(path)
    failures = check_bundle(app, run)
    plugins = app / "Contents" / "PlugIns"
    if plugins.is_dir():
        for extension in sorted(plugins.glob("*.appex")):
            failures.extend(check_bundle(extension, run))
    return failures


def check_dmg(path, run=None):
    run_tool(["hdiutil", "verify", str(path)], run, 120)


def main(argv=None):
    argv = argv or sys.argv
    if len(argv) not in (2, 3):
        print(f"usage: {argv[0]} <Persona.app> [Persona.dmg]", file=sys.stderr)
        return 2
    try:
        failures = check_app(argv[1])
        if failures:
            for failure in failures:
                print(f"FAIL {failure}", file=sys.stderr)
            return 1
        print(f"PASS {argv[1]}: macOS 15.0, universal x86_64 + arm64")
        if len(argv) == 3:
            check_dmg(argv[2])
            print(f"PASS {argv[2]}: DMG checksum is valid")
    except CheckError as error:
        print(f"FAIL {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
