#!/usr/bin/env python3
"""Reproduce the one Android-only Refract About Version consumer (#9694).

An explicit `generate` rebuilds the pinned public CLI and updates only its reviewed Kotlin. The default `check`
never changes the checkout: it generates both backends in a temporary directory, checks the Kotlin byte-for-byte,
and discards the schema-validation-only ArkTS artifact. No generated file is ever edited by this script.

Usage:
    python3 scripts/app/refract_about_version.py check --cli <path-to-pinned-refract>
    python3 scripts/app/refract_about_version.py generate --cli <path-to-pinned-refract>
    python3 scripts/app/refract_about_version.py check --download
    python3 scripts/app/refract_about_version.py generate --download

`--download` explicitly opts into fetching the exact public Git commit from toolchain.json into an isolated
source/toolchain directory. Its full commit, complete Git tree, deterministic source-archive checksum and archived
SwiftSyntax lock are verified before `swift build`; no moving branch, dirty checkout or unverified archive is accepted.
An ordinary Android build never invokes this script, downloads a generator or selects the experimental component.
With `--download`, the CLI's build parallelism is two jobs by default (`PERSONA_REFRACT_BUILD_JOBS` can lower it).
Android CI explicitly opts into the same pinned public source build with --download; no other Gradle or CI path
downloads a generator implicitly. Without --download, no network request is made: use --cli, PERSONA_REFRACT_CLI,
or a local `.toolchain/refract` built from that exact source (this untracked path is optional and never committed).
"""
from __future__ import annotations

import argparse
import gzip
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
sys.dont_write_bytecode = True
import urllib.error

ROOT = Path(__file__).resolve().parents[2]
PILOT = ROOT / "android/refract/about"
SOURCE = PILOT / "AboutVersionRow.swift"
MAPPING = PILOT / "refract.json"
GENERATED = ROOT / "android/feature/settings/src/test/kotlin/persona/feature/settings/generated/AboutVersionRow.kt"
SCHEMA_ONLY_HARMONY = PILOT / "harmony-resource-validation-only"
EXPECTED = {
    "source": "https://github.com/nwverse/refract",
    "commit": "e32413fba1f13ff0652dedea5f96fc49ec4a8c96",
    "archiveSha256": "522ec930a5b5de652acc090f1734e7d3440b18ad404a437927f713b2c3ad23fa",
    "sourceTreeSha256": "bb1e9d8fbe772541bd619a825cdb673c10d09542395aace51b1fbcfe280b5c30",
    "archiveTreeSha256": "cfaeafa9cee642c7f8e8832440977ec099851f834ffa31e87de281d5334a43be",
    "mappingSchemaVersion": 1,
    "swiftSyntaxVersion": "600.0.1",
    "swiftSyntaxRevision": "0687f71944021d616d34d922343dcef086855920",
}


def fail(message: str) -> None:
    raise ValueError(message)


def validate_lock() -> dict:
    lock = json.loads((PILOT / "toolchain.json").read_text(encoding="utf-8"))
    for key, expected in EXPECTED.items():
        if lock.get(key) != expected:
            fail(f"toolchain.json: {key} does not match the reviewed source lock")
    if lock.get("archiveUrl") != "deterministic git source archive built by scripts/package_cli.py at the pinned commit":
        fail("toolchain.json: archive source is not the reviewed deterministic packaging command")
    if json.loads(MAPPING.read_text(encoding="utf-8")).get("schemaVersion") != EXPECTED["mappingSchemaVersion"]:
        fail("refract.json: unsupported mapping schema")
    return lock


def source_tree_digest(source: Path) -> str:
    entries = [(path.relative_to(source).as_posix(), hashlib.sha256(path.read_bytes()).hexdigest())
               for path in source.rglob("*") if path.is_file()]
    encoded = json.dumps(sorted(entries), separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def validate_archived_lock(source: Path, lock: dict) -> None:
    actual_tree = source_tree_digest(source)
    if actual_tree != lock["archiveTreeSha256"]:
        fail(f"Source-tree SHA-256 mismatch: got {actual_tree}")
    resolved = json.loads((source / "Package.resolved").read_text(encoding="utf-8"))
    pins = [pin for pin in resolved.get("pins", []) if pin.get("identity") == "swift-syntax"]
    if len(pins) != 1:
        fail("Pinned source must contain exactly one SwiftSyntax lock")
    state = pins[0].get("state", {})
    expected = (lock["swiftSyntaxVersion"], lock["swiftSyntaxRevision"])
    if (state.get("version"), state.get("revision")) != expected:
        fail("Archived SwiftSyntax lock differs from the reviewed source lock")


def safe_extract(archive: Path, destination: Path) -> None:
    with tarfile.open(archive, "r:gz") as tar:
        members = tar.getmembers()
        roots = {member.name.split("/", 1)[0] for member in members}
        if len(roots) != 1:
            fail("Generator archive must have one source root")
        for member in members:
            path = Path(member.name)
            if path.is_absolute() or ".." in path.parts or member.issym() or member.islnk():
                fail("Generator archive has an unsafe path or link")
            if not (member.isfile() or member.isdir()):
                fail("Generator archive has an unsupported special file")
        tar.extractall(destination)
    extracted = list(destination.iterdir())
    if len(extracted) != 1 or not (extracted[0] / "Package.swift").is_file():
        fail("Generator archive does not contain a Swift package")
    shutil.move(str(extracted[0]), str(destination / "source"))


def validate_git_tree(checkout: Path, lock: dict) -> None:
    # Read the selected commit's regular blobs, never incidental files produced by a build or Python import.
    listing = subprocess.run(["git", "ls-tree", "-r", "-z", lock["commit"]], check=True, cwd=checkout,
                             capture_output=True, timeout=30).stdout
    entries = []
    for record in listing.split(b"\0"):
        if not record:
            continue
        metadata, encoded_name = record.split(b"\t", 1)
        mode, kind, _ = metadata.split()
        if kind != b"blob" or mode not in (b"100644", b"100755"):
            fail("Pinned source contains a non-regular Git entry")
        name = encoded_name.decode("utf-8")
        data = subprocess.run(["git", "show", f"{lock['commit']}:{name}"], check=True, cwd=checkout,
                              capture_output=True, timeout=30).stdout
        if name == "Sources/RefractCLI/Version.swift":
            data, count = re.subn(rb'(?m)^(\s*static let sourceRevision = )"[^"]*"$',
                                  rb'\g<1>"$Format:%H$"', data)
            if count != 1:
                fail("The pinned CLI source identity placeholder is missing")
        entries.append((name, hashlib.sha256(data).hexdigest()))
    actual_tree = hashlib.sha256(json.dumps(sorted(entries), separators=(",", ":")).encode()).hexdigest()
    if actual_tree != lock["sourceTreeSha256"]:
        fail(f"Source-tree SHA-256 mismatch: got {actual_tree}")


def canonical_source_archive(checkout: Path, lock: dict, archive: Path) -> None:
    # Use the pinned packager's canonical metadata and source selection, but read Git blobs rather than requiring
    # a Git LFS smudge filter in a clean CI checkout. Version identity is stamped by the same upstream function.
    source = checkout
    spec = importlib.util.spec_from_file_location("persona_refract_source_package", source / "scripts/package_cli.py")
    if spec is None or spec.loader is None:
        fail("Pinned source packaging module cannot be loaded")
    packaging = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(packaging)
    entries = []
    for name in packaging.source_files(lock["commit"]):
        metadata, object_name = packaging.run(["git", "ls-tree", "-z", lock["commit"], "--", name]).split("\0")[0].split("\t", 1)
        mode, kind, _ = metadata.split()
        if kind != "blob" or mode not in ("100644", "100755"):
            fail(f"Pinned source contains a non-regular entry: {object_name}")
        data = packaging.run_bytes(["git", "show", f"{lock['commit']}:{name}"])
        if name == "Sources/RefractCLI/Version.swift":
            data = packaging.version_source(data, lock["commit"])
        entries.append((name, 0o755 if mode == "100755" else 0o644, data))
    compressed = bytearray(gzip.compress(packaging.deterministic_tar("refract-source-0.1.0", entries),
                                        compresslevel=9, mtime=packaging.ARCHIVE_EPOCH))
    # Python's zlib and GzipFile paths write different OS bytes; the reviewed lock uses Unix (3).
    compressed[9] = 3
    archive.write_bytes(compressed)


def build_cli(work: Path, lock: dict) -> Path:
    checkout = work / "checkout"
    archive = work / "source.tar.gz"
    subprocess.run(["git", "clone", "--quiet", "--no-checkout", "--filter=blob:none", lock["source"], str(checkout)],
                   check=True, cwd=work, timeout=300)
    subprocess.run(["git", "checkout", "--quiet", "--detach", lock["commit"]], check=True, cwd=checkout, timeout=300)
    revision = subprocess.run(["git", "rev-parse", "HEAD"], check=True, cwd=checkout, capture_output=True, text=True,
                              timeout=30).stdout.strip()
    if revision != lock["commit"]:
        fail(f"Git source revision mismatch: {revision}")
    # Match only regular files from the selected commit, not an arbitrary checkout or untracked file.
    status = subprocess.run(["git", "status", "--porcelain", "--untracked-files=all"], check=True, cwd=checkout,
                            capture_output=True, text=True, timeout=30).stdout
    if status:
        fail("Pinned source checkout is not clean")
    validate_git_tree(checkout, lock)
    # The fixed source's own reviewed packager supplies canonical tar metadata. Do not accept a download cache's
    # changing gzip bytes as a stable archive. A second independently generated archive must be identical.
    for candidate in (archive, work / "source-second.tar.gz"):
        canonical_source_archive(checkout, lock, candidate)
        digest = hashlib.sha256(candidate.read_bytes()).hexdigest()
        if digest != lock["archiveSha256"]:
            fail(f"Deterministic source archive SHA-256 mismatch: got {digest}")
    if archive.read_bytes() != (work / "source-second.tar.gz").read_bytes():
        fail("Two pinned source archives are not byte-identical")
    source = work / "source"
    source.mkdir()
    safe_extract(archive, source)
    validate_archived_lock(source / "source", lock)
    subprocess.run(
        ["swift", "build", "--package-path", str(source / "source"), "--scratch-path", str(work / "build"),
         "--product", "refract", "--disable-sandbox", "--jobs", os.environ.get("PERSONA_REFRACT_BUILD_JOBS", "2")],
        check=True, cwd=ROOT, timeout=900,
    )
    cli = work / "build/debug/refract"
    if not cli.is_file():
        fail("The pinned generator build produced no CLI")
    # The pinned CLI's `--version` is checked before every invocation. Do not stamp or trust a source entry path.
    return cli


def run(cli: Path, mode: str, android_out: Path, harmony_out: Path, sources=(SOURCE,)) -> None:
    version = subprocess.run([str(cli), "--version"], cwd=ROOT, capture_output=True, text=True, timeout=30)
    if version.returncode != 0:
        fail("CLI does not expose the required `--version` source identity; rebuild the exact pinned public source")
    expected = f"refract 0.1.0 ({EXPECTED['commit']})"
    if version.stdout.strip() != expected:
        fail(f"CLI identity mismatch: expected `{expected}`, got `{version.stdout.strip()}`; rebuild the pinned source")
    arguments = [str(cli), mode]
    for source in sources:
        arguments += ["--input", str(source)]
    arguments += ["--mapping", str(MAPPING), "--android-out", str(android_out), "--harmony-out", str(harmony_out)]
    subprocess.run(arguments, check=True, cwd=ROOT, capture_output=True, text=True, timeout=90,
                   errors="replace")


def report_process_failure(error: subprocess.CalledProcessError) -> None:
    """Preserve a failed CLI's own diagnostic instead of printing only its process status."""
    diagnostics = error.stderr or error.stdout
    if not diagnostics:
        return
    if isinstance(diagnostics, bytes):
        # Raw source-build diagnostics must not be decoded or replaced with U+FFFD.
        sys.stderr.flush()
        sys.stderr.buffer.write(diagnostics)
        if not diagnostics.endswith(b"\n"):
            sys.stderr.buffer.write(b"\n")
        sys.stderr.buffer.flush()
        return
    sys.stderr.write(diagnostics)
    if not diagnostics.endswith("\n"):
        sys.stderr.write("\n")


def restore_reviewed_output(reviewed: bytes, generated: Path | None = None) -> None:
    destination = GENERATED if generated is None else generated
    try:
        destination.write_bytes(reviewed)
        restored = destination.read_bytes() == reviewed
    except OSError as error:
        raise ValueError(f"Could not restore the reviewed Kotlin after a generation failure: {error}") from error
    if not restored:
        fail("Could not restore the reviewed Kotlin after a generation failure: restored bytes differ")


def remove_new_output(generated: Path) -> None:
    """Remove a failed run's new output, or preserve an explicit cleanup failure."""
    generated.unlink(missing_ok=True)
    if generated.exists():
        fail("New unreviewed Kotlin still exists after removal")


def check_unsupported_input(cli: Path, work: Path) -> None:
    """A valid sibling must not be published when another input has no native-component mapping."""
    unsupported = work / "UnsupportedAboutControl.swift"
    work.mkdir()
    unsupported.write_text("""import SwiftUI

struct UnsupportedAboutControl: View {
    var body: some View {
        UnsupportedAboutControlHost(value: \"fixture\")
    }
}
""", encoding="utf-8")
    before = GENERATED.read_bytes() if GENERATED.exists() else None
    for mode in ("check", "generate"):
        android_out = work / mode / "android"
        harmony_out = work / mode / "harmony"
        try:
            run(cli, mode, android_out, harmony_out, sources=(SOURCE, unsupported))
        except subprocess.CalledProcessError as error:
            diagnostics = error.stderr or error.stdout
        else:
            fail(f"Unsupported component was accepted in {mode} mode")
        for fragment in ("UnsupportedAboutControl.swift:5:9", "[missingComponentMapping]",
                         "UnsupportedAboutControlHost", "Suggestion: Declare its typed target"):
            if fragment not in diagnostics:
                fail(f"Unsupported component lacked a source-located diagnostic in {mode} mode: {diagnostics}")
        if android_out.exists() or harmony_out.exists():
            fail(f"Unsupported component published partial output in {mode} mode")
        current = GENERATED.read_bytes() if GENERATED.exists() else None
        if current != before:
            fail(f"Rejected {mode} changed the reviewed generated Kotlin")


def kotlin_code_only(source: str) -> str:
    """Blank comments and literal text, retaining interpolation as Kotlin code.

    This is a bounded lexer for the exclusion check, not a Kotlin parser. Nested block comments and
    nested interpolation braces are tracked so a literal cannot hide a real interpolated reference.
    """
    code = []
    stack = [("code", 0)]
    index = 0
    while index < len(source):
        kind, depth = stack[-1]
        char = source[index]
        following = source[index + 1] if index + 1 < len(source) else ""
        if kind == "code":
            if source.startswith("//", index):
                code.append("  ")
                stack.append(("line", 0))
                index += 2
            elif source.startswith("/*", index):
                code.append("  ")
                stack.append(("block", 1))
                index += 2
            elif source.startswith('"""', index):
                code.append("   ")
                stack.append(("raw", 0))
                index += 3
            elif char in ('"', "'"):
                code.append(" ")
                stack.append(("string" if char == '"' else "char", 0))
                index += 1
            elif char == "}" and depth:
                code.append(char)
                stack.pop()
                index += 1
            elif char == "{" and depth:
                stack[-1] = (kind, depth + 1)
                code.append(char)
                index += 1
            else:
                code.append(char)
                index += 1
        elif kind == "line":
            code.append("\n" if char == "\n" else " ")
            index += 1
            if char == "\n":
                stack.pop()
        elif kind == "block":
            if source.startswith("/*", index):
                stack[-1] = (kind, depth + 1)
                code.append("  ")
                index += 2
            elif source.startswith("*/", index):
                code.append("  ")
                index += 2
                if depth == 1:
                    stack.pop()
                else:
                    stack[-1] = (kind, depth - 1)
            else:
                code.append("\n" if char == "\n" else " ")
                index += 1
        elif kind == "char":
            code.append("\n" if char == "\n" else " ")
            if char == "\\":
                code.append("\n" if following == "\n" else " ")
                index += 2
            else:
                index += 1
                if char == "'":
                    stack.pop()
        else:
            if char == "\\" and kind == "string":
                code.append("  ")
                index += 2
            elif (kind == "raw" and source.startswith('"""', index)) or (kind == "string" and char == '"'):
                terminator = 3 if kind == "raw" else 1
                code.append(" " * terminator)
                index += terminator
                stack.pop()
            elif char == "$" and following == "{":
                code.append("${")
                stack.append(("code", 1))
                index += 2
            elif char == "$" and (following == "_" or following.isalpha()):
                code.append(char)
                stack[-1] = (kind + "-interpolation", 0)
                index += 1
            else:
                code.append("\n" if char == "\n" else " ")
                index += 1
        if stack[-1][0].endswith("-interpolation"):
            while index < len(source) and (source[index] == "_" or source[index].isalnum()):
                code.append(source[index])
                index += 1
            stack[-1] = (stack[-1][0][:-len("-interpolation")], 0)
    return "".join(code)


def validate_production_exclusion(android: Path, identifiers: tuple[str, ...], message: str) -> None:
    for production in android.rglob("*.kt"):
        relative = production.relative_to(android)
        parts = relative.parts
        source_sets = {parts[index + 1] for index, part in enumerate(parts[:-1]) if part == "src"}
        if not source_sets or source_sets <= {"test", "androidTest"}:
            continue
        code = kotlin_code_only(production.read_text(encoding="utf-8"))
        if any(identifier in code for identifier in identifiers):
            fail(f"{message}: {relative}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("check", "generate"), nargs="?", default="check")
    parser.add_argument("--cli", type=Path, help="an already-built CLI from the exact pinned source (not downloaded)")
    parser.add_argument("--download", action="store_true",
                        help="explicitly fetch and build the pinned public CLI when --cli is not supplied")
    args = parser.parse_args()
    try:
        lock = validate_lock()
        if args.download and args.cli is not None:
            parser.error("--download and --cli are mutually exclusive")
        if args.download and shutil.which("swift") is None:
            fail("--download needs Swift on PATH; without Swift, supply the already-built pinned CLI with --cli")
        if args.cli is None and not args.download:
            cached = os.environ.get("PERSONA_REFRACT_CLI")
            if cached:
                args.cli = Path(cached)
            else:
                cached_path = PILOT / ".toolchain/refract"
                if cached_path.is_file():
                    args.cli = cached_path
                else:
                    fail("No pinned CLI was supplied. Use --cli <path-to-pinned-refract> (or PERSONA_REFRACT_CLI), "
                         "or opt into a public source download/build with --download. Nothing was downloaded; "
                         "Kotlin compilation and tests can still run without network access.")
        if args.cli is not None and not args.cli.is_file():
            fail("--cli must name an existing generator executable")
        with tempfile.TemporaryDirectory(prefix="persona-refract-about-") as temporary:
            work = Path(temporary)
            cli = args.cli.resolve() if args.cli is not None else build_cli(work, lock)
            before = GENERATED.read_bytes() if GENERATED.exists() else None
            check_unsupported_input(cli, work / "rejected")
            current = GENERATED.read_bytes() if GENERATED.exists() else None
            if current != before:
                restore_reviewed_output(before)
                fail("Rejected input changed the reviewed Kotlin; the original reviewed bytes were restored")
            if args.mode == "generate":
                # The mapping's own output.directory is not used: this command is run only from Persona's root.
                # Its second backend is schema validation only and never published as a Persona destination.
                run(cli, "generate", GENERATED.parent, work / "schema-only")
            else:
                run(cli, "generate", work / "android", work / "schema-only")
                actual = work / "android/AboutVersionRow.kt"
                if not actual.is_file() or actual.read_bytes() != GENERATED.read_bytes():
                    fail("Generated Kotlin is stale or missing; run this command with `generate` and review the diff")
            text = GENERATED.read_text(encoding="utf-8")
            required = (
                "// Generated by Refract; DO NOT EDIT.",
                "// Source: AboutVersionRow.swift",
                "import persona.core.designsystem.JRow",
                "value = model.versionName.value",
            )
            if any(fragment not in text for fragment in required):
                fail("Generated Kotlin lost the reviewed source/native-model boundary")
            if re.search(r"(?i)(?:^|\s)(?:import|include|require)\s*[^\n]*refract", text):
                fail("Generated Kotlin must not import a Refract runtime")
            forbidden = ("android.content", "android.content.res", "PackageManager", "ClipboardManager")
            if any(fragment in text for fragment in forbidden):
                fail("Generated UI must not own framework metadata or clipboard behavior")
            if not (SCHEMA_ONLY_HARMONY / "string.json").is_file():
                fail("Missing CLI schema-validation resource fixture")
            validate_production_exclusion(
                ROOT / "android", ("AboutVersionModel", "AboutVersionSnapshot", "generated.AboutVersionRow"),
                "Ordinary production source must exclude the pilot")
            if args.mode == "generate":
                # Compare two invocations, but never confuse the first unreviewed output with the entry snapshot.
                first_generation = GENERATED.read_bytes()
                run(cli, "generate", GENERATED.parent, work / "schema-only")
                if GENERATED.read_bytes() != first_generation:
                    fail("Pinned generation is not deterministic")
                print(f"Generated {GENERATED.relative_to(ROOT)} with pinned source {lock['commit']}")
            print(f"Refract About Version {args.mode}: PASSED ({lock['commit']}); Android-only, no runtime")
    except (ValueError, OSError, subprocess.SubprocessError, json.JSONDecodeError, tarfile.TarError,
            urllib.error.URLError) as error:
        if args.mode == "generate" and "before" in locals():
            if isinstance(error, subprocess.CalledProcessError):
                report_process_failure(error)
            print(f"Refract About Version: FAILED: {error}", file=sys.stderr)
            try:
                if before is not None:
                    restore_reviewed_output(before)
                else:
                    remove_new_output(GENERATED)
            except (ValueError, OSError) as restore_error:
                print(f"Refract About Version: secondary restoration failure: {restore_error}", file=sys.stderr)
                return 1
            if before is not None:
                print("Refract About Version: original reviewed Kotlin restored after generation failure", file=sys.stderr)
            else:
                print("Refract About Version: new unreviewed Kotlin removed after generation failure", file=sys.stderr)
            return 1
        if isinstance(error, subprocess.CalledProcessError):
            report_process_failure(error)
        print(f"Refract About Version: FAILED: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
