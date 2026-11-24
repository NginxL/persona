#!/usr/bin/env python3
"""Check or regenerate the independent test-only About Build consumer (#13001).

The reviewed Kotlin is produced only by the exact pinned public CLI. The command never patches generated output,
never changes an ordinary app source set and discards the schema-validation-only second backend. `check --download`
builds the exact commit's canonical source archive in a disposable directory; a direct Git build has the unexpanded
`$Format:%H$` identity and is intentionally not accepted as a source-identified release CLI.
"""
from __future__ import annotations

import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import urllib.error

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("persona_refract_pinned_source", ROOT / "scripts/app/refract_about_version.py")
if spec is None or spec.loader is None:
    raise RuntimeError("Cannot load the reviewed pinned-source toolchain helpers")
source_lock = importlib.util.module_from_spec(spec)
spec.loader.exec_module(source_lock)

PILOT = ROOT / "android/refract/about-build"
SOURCE = PILOT / "AboutBuildRow.swift"
MAPPING = PILOT / "refract.json"
GENERATED = ROOT / "android/feature/settings/src/test/kotlin/persona/feature/settings/generated/AboutBuildRow.kt"
HOST = ROOT / "android/feature/settings/src/main/kotlin/persona/feature/settings/DeviceSettingsPages.kt"
EXPECTED = {
    "source": "https://github.com/nwverse/refract",
    "commit": "811db385e4a38a4d5df720c795197368efe3602c",
    "archiveSha256": "d8920f5d8be338020922ef24039180906113892c1be7d3989224ae8507b7ddba",
    "sourceTreeSha256": "ec5d90f26482772970bd408679a069d70709c702cc203712b7d1453bbf5c29ea",
    "archiveTreeSha256": "c1e1fbe476d3b059619b894c1a942ab97500478b14365bcdb4451c4bd978b0ae",
    "mappingSchemaVersion": 1,
    "swiftSyntaxVersion": "600.0.1",
    "swiftSyntaxRevision": "0687f71944021d616d34d922343dcef086855920",
}
REQUIRED = (
    "buildNumber: String?",
    "if (buildNumber != null)",
    "title = stringResource(R.string.settings_about_build),",
    "trailing = {",
    "text = value",
    "text = stringResource(R.string.settings_not_provided)",
    "style = PersonaTheme.type.micro",
    "color = PersonaTheme.palette.ink3",
)
source_lock.PILOT = PILOT
source_lock.EXPECTED = EXPECTED


def validate_host(source: str) -> None:
    signature = re.search(
        r"internal fun AboutSettingsPage\((?P<parameters>.*?)\n\) \{", source, flags=re.DOTALL)
    if signature is None or not re.search(
            r"buildRow:\s*\(@Composable \(\) -> Unit\)\? = null", signature.group("parameters")):
        source_lock.fail("About Build host must keep an optional no-argument slot with a handwritten default")
    body = source[signature.end():]
    if not re.search(
            r"JGroupItem\(GroupPosition\.of\(1, count\)\)\s*\{\s*"
            r"if \(buildRow == null\) \{\s*"
            r"JRow\(stringResource\(R\.string\.settings_about_build\), trailing = \{ "
            r'Text\("\$\{info\.longVersionCode\}"', body):
        source_lock.fail("About Build host must preserve its handwritten PackageInfo default")
    if not re.search(r"\}\s*else\s*\{\s*buildRow\(\)\s*\}\s*\}", body):
        source_lock.fail("About Build host must select only the supplied Build component")


def validate_production_exclusion(android: Path) -> None:
    source_lock.validate_production_exclusion(
        android, ("AboutBuildSnapshot", "AboutBuildRow"),
        "Ordinary production source must exclude this test-only consumer")


def validate_consumer_lock() -> dict:
    lock = source_lock.validate_lock()
    validate_host(HOST.read_text(encoding="utf-8"))
    text = GENERATED.read_text(encoding="utf-8")
    if any(fragment not in text for fragment in REQUIRED):
        source_lock.fail("Generated Build row no longer has the reviewed optional/native/token/localization boundary")
    if re.search(r"(?i)(?:^|\s)(?:import|include|require)\s*[^\n]*refract", text):
        source_lock.fail("Generated Kotlin must not import a Refract runtime")
    forbidden = ("android.content", "PackageManager", "SettingsBuildValue(text: \"settings.not.provided\"")
    if any(fragment in text for fragment in forbidden):
        source_lock.fail("Generated Build row must not own framework metadata or an unlocalized fallback")
    if not (PILOT / "harmony-resource-validation-only/string.json").is_file():
        source_lock.fail("Missing second-backend schema-validation resource fixture")
    validate_production_exclusion(ROOT / "android")
    return lock


def run(cli: Path, mode: str, android_out: Path, harmony_out: Path) -> None:
    version = subprocess.run([str(cli), "--version"], cwd=ROOT, capture_output=True, text=True, timeout=30)
    expected = f"refract 0.1.0 ({EXPECTED['commit']})"
    if version.returncode != 0 or version.stdout.strip() != expected:
        source_lock.fail(f"CLI identity mismatch: expected `{expected}`, got `{version.stdout.strip()}`")
    subprocess.run([str(cli), mode, "--input", str(SOURCE), "--mapping", str(MAPPING),
                    "--android-out", str(android_out), "--harmony-out", str(harmony_out)],
                   cwd=ROOT, capture_output=True, text=True, timeout=90, check=True, errors="replace")


def remove_new_output(generated: Path) -> None:
    """Remove a failed run's new output, or preserve an explicit cleanup failure."""
    generated.unlink(missing_ok=True)
    if generated.exists():
        source_lock.fail("New unreviewed Kotlin still exists after removal")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("check", "generate"), nargs="?", default="check")
    parser.add_argument("--cli", type=Path, help="an already-built source-identified CLI from the exact pinned archive")
    parser.add_argument("--download", action="store_true", help="explicitly fetch/build the pinned public source")
    args = parser.parse_args()
    before = GENERATED.read_bytes() if GENERATED.exists() else None
    try:
        lock = validate_consumer_lock()
        if args.download and args.cli is not None:
            parser.error("--download and --cli are mutually exclusive")
        if args.download and source_lock.shutil.which("swift") is None:
            source_lock.fail("--download requires Swift on PATH; use --cli for an offline check")
        if args.cli is None and not args.download:
            args.cli = Path(os.environ["PERSONA_REFRACT_BUILD_CLI"]) if os.environ.get("PERSONA_REFRACT_BUILD_CLI") else None
            if args.cli is None:
                source_lock.fail("Supply --cli or explicitly opt into a source build with --download; nothing was downloaded")
        if args.cli is not None and not args.cli.is_file():
            source_lock.fail("--cli must name an existing generator executable")
        with tempfile.TemporaryDirectory(prefix="persona-refract-build-row-") as temporary:
            work = Path(temporary)
            cli = args.cli.resolve() if args.cli is not None else source_lock.build_cli(work, lock)
            output = GENERATED.parent if args.mode == "generate" else work / "android"
            run(cli, "generate", output, work / "schema-validation-only")
            actual = output / "AboutBuildRow.kt"
            if not actual.is_file() or actual.read_bytes() != GENERATED.read_bytes():
                source_lock.fail("Generated Build Kotlin is stale or missing; run generate and review the diff")
            first = actual.read_bytes()
            run(cli, "generate", output, work / "schema-validation-only")
            if actual.read_bytes() != first:
                source_lock.fail("Pinned Build generation is not deterministic")
            validate_consumer_lock()
        print(f"Refract About Build {args.mode}: PASSED ({lock['commit']}); test-only, no runtime")
    except (ValueError, OSError, subprocess.SubprocessError, json.JSONDecodeError,
            source_lock.tarfile.TarError, urllib.error.URLError) as error:
        if isinstance(error, subprocess.CalledProcessError):
            source_lock.report_process_failure(error)
        print(f"Refract About Build: FAILED: {error}", file=sys.stderr)
        if args.mode == "generate":
            try:
                if before is not None:
                    source_lock.restore_reviewed_output(before, GENERATED)
                else:
                    remove_new_output(GENERATED)
            except (ValueError, OSError) as restore_error:
                print(f"Refract About Build: secondary restoration failure: {restore_error}", file=sys.stderr)
                return 1
            if before is not None:
                print("Refract About Build: original reviewed Kotlin restored after generation failure", file=sys.stderr)
            else:
                print("Refract About Build: new unreviewed Kotlin removed after generation failure", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
