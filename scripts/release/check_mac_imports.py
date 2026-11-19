#!/usr/bin/env python3
"""Fail when an iPhone executable strongly imports a symbol its Mac libraries do not export.

    scripts/release/check_mac_imports.py path/to/Persona [--mac-sdk /path/to/MacOSX.sdk]

An iPhone app on an Apple silicon Mac loads the Mac's frameworks. A strong undefined symbol present in the iPhone SDK
but absent there stops dyld before app code runs. Weak imports are allowed. `xcrun dyld_info -imports` supplies the
imports and `otool -L` their install names; MacOSX.sdk TBD text supplies the exports, preferring System/iOSSupport.
System libraries with no Mac or iOS-support stub cannot be compared and are skipped. A clean check prints nothing.
"""
import argparse
import os
from pathlib import Path
import re
import subprocess
import sys

from check_no_callkit import CheckError, dependencies

TIMEOUT = 120
HEX = re.compile(r"0x[0-9A-Fa-f]+$")
MAC_TARGET = "arm64e-macos"
IOS_SUPPORT_TARGET = "arm64e-maccatalyst"
TARGET_BLOCK = re.compile(r"^[ \t]*-[ \t]+(targets|archs):[ \t]*\[(.*?)\]", re.MULTILINE | re.DOTALL)


def parse_imports(text):
    """Return (source, symbol, weak) in dyld_info order; accept its address and weak-import annotations."""
    found = []
    for line in text.splitlines():
        if "(from " not in line:
            continue
        match = re.match(r"^\s*(.*?)\s+\(from\s+([^)]+)\)\s*$", line)
        if not match:
            raise CheckError("unrecognized dyld import line %r" % line[:200])
        before, source_attributes = match.groups()
        source_parts = source_attributes.split(",")
        source = source_parts[0].strip()
        weak = "weak-import" in before or any("weak" in part for part in source_parts[1:])
        before = before.replace("[weak-import]", " ")
        parts = before.split()
        if parts and HEX.fullmatch(parts[0]):
            parts.pop(0)
        # Some dyld_info versions print a separate strong/weak flags field (0x00 / 0x01).
        if len(parts) > 1 and parts[0] in ("0x00", "0x01"):
            weak = weak or parts.pop(0) == "0x01"
        if len(parts) != 1 or not source:
            raise CheckError("unrecognized dyld import line %r" % line[:200])
        found.append((source, parts[0], weak))
    return found


def library_aliases(install_name):
    """Names dyld_info can put in `(from ...)` for one install name."""
    if ".framework/" in install_name:
        return {install_name.split(".framework/", 1)[0].rsplit("/", 1)[-1]}
    base = os.path.basename(install_name)
    names = {base}
    if base.endswith(".dylib"):
        stem = base[:-len(".dylib")]
        names.update((stem, stem.split(".", 1)[0]))
    return names


def system_library(install_name):
    return install_name.startswith((
        "/System/Library/",
        "/usr/lib/",
        "/System/iOSSupport/System/Library/",
        "/System/iOSSupport/usr/lib/",
    ))


def tbd_relative(install_name, ios_support=False):
    already_support = install_name.startswith("/System/iOSSupport/")
    relative = install_name.lstrip("/")
    if relative.endswith(".dylib"):
        relative = relative[:-len(".dylib")] + ".tbd"
    else:
        relative += ".tbd"
    if ios_support and not already_support and install_name.startswith(("/System/Library/", "/usr/lib/")):
        relative = "System/iOSSupport/" + relative
    return relative


def tbd_target(relative):
    if relative.startswith("System/iOSSupport/") or relative.startswith("System/Library/"):
        return (IOS_SUPPORT_TARGET,)
    # System dylibs also supply the compiler runtime from their macOS target document.
    return (MAC_TARGET, IOS_SUPPORT_TARGET)


def flow_values(text):
    for token in re.finditer(r"'([^']+)'|\"([^\"]+)\"|([^\s,\[\]]+)", text):
        yield next(value for value in token.groups() if value is not None)


def array_values(text, key):
    """Values of TBD flow arrays named key."""
    pattern = re.compile(r"^[ \t]*" + re.escape(key) + r":[ \t]*\[(.*?)\]", re.MULTILINE | re.DOTALL)
    for match in pattern.finditer(text):
        yield from flow_values(match.group(1))


def target_blocks(text, targets):
    """TBD export/reexport blocks for the runtime targets, never another CPU's exports."""
    if isinstance(targets, str):
        targets = (targets,)
    matches = list(TARGET_BLOCK.finditer(text))
    if not matches:
        return [text]
    architectures = {target.split("-", 1)[0] for target in targets}
    blocks = []
    for index, match in enumerate(matches):
        values = set(flow_values(match.group(2)))
        if match.group(1) == "targets":
            selected = any(target in values for target in targets)
        else:
            # TBD v3 has architecture-only blocks; the document supplies its platform.
            selected = bool(architectures & values) or ("arm64e" in architectures and "arm64" in values)
        if selected:
            end = matches[index + 1].start() if index + 1 < len(matches) else len(text)
            blocks.append(text[match.start():end])
    return blocks


def parse_tbd(text, target=IOS_SUPPORT_TARGET):
    """Return (exported symbols, reexported install names) for the selected runtime targets."""
    symbols = set()
    reexports = set()
    for block in target_blocks(text, target):
        for key in ("symbols", "weak-symbols", "reexports"):
            for value in array_values(block, key):
                if not value.startswith("/"):
                    symbols.add(value)
        for key, prefixes in (("objc-classes", ("_OBJC_CLASS_$_", "_OBJC_METACLASS_$_")),
                              ("objc-eh-types", ("_OBJC_EHTYPE_$_",)),
                              ("objc-ivars", ("_OBJC_IVAR_$_",))):
            for value in array_values(block, key):
                for prefix in prefixes:
                    symbols.add(prefix + value)
        reexports.update(value for value in array_values(block, "libraries") if system_library(value))
    return symbols, reexports


def preferred_tbd(install_name, tbd_texts):
    support = tbd_relative(install_name, ios_support=True)
    regular = tbd_relative(install_name)
    if support in tbd_texts:
        return support
    if regular in tbd_texts:
        return regular
    return None


def find_missing_imports(imports_text, linked_dylibs, tbd_texts):
    """Ordered (install name, symbol) strong imports absent from their selected Mac TBD exports."""
    cache = {}

    def exports(install_name):
        path = preferred_tbd(install_name, tbd_texts)
        if path is None:
            return None  # No Mac/iOS-support stub: this symbol comparison is not applicable.
        if path in cache:
            return cache[path]
        cache[path] = set()  # A reexport cycle contributes no symbols until its own exports are added.
        symbols, reexports = parse_tbd(tbd_texts[path], target=tbd_target(path))
        for reexport in sorted(reexports):
            inherited = exports(reexport)
            if inherited:
                symbols.update(inherited)
        cache[path] = symbols
        return symbols

    missing = []
    for source, symbol, weak in parse_imports(imports_text):
        if weak or (source.startswith("<") and source.endswith(">")):
            continue  # dyld pseudo-sources such as <weak-def-coalesce> name no Mac dependency.
        if source not in linked_dylibs:
            raise CheckError("no linked dylib for import source %s" % source)
        install_name = linked_dylibs[source]
        if not system_library(install_name):
            continue  # A library delivered inside the app is not supplied by the Mac.
        exported = exports(install_name)
        if exported is not None and symbol not in exported:
            missing.append((install_name, symbol))
    return missing


def run_text(command):
    try:
        result = subprocess.run(command, capture_output=True, text=True, timeout=TIMEOUT)
    except (OSError, subprocess.SubprocessError) as error:
        raise CheckError("%s: %s" % (command[0], error))
    if result.returncode != 0 or result.stderr.strip():
        raise CheckError("%s exited %d: %s" % (" ".join(command), result.returncode, result.stderr.strip()[:500]))
    return result.stdout


def linked_install_names(executable):
    linked = {}
    for _architecture, install_name, _attribute in dependencies(executable):
        # @rpath Swift runtime libraries are system libraries on an iPhone app running on a Mac. Other rpath
        # libraries belong to the app bundle and need no Mac SDK stub.
        resolved = install_name
        if install_name.startswith("@rpath/libswift"):
            resolved = "/usr/lib/swift/" + os.path.basename(install_name)
        for alias in library_aliases(install_name):
            linked.setdefault(alias, resolved)
    return linked


def load_tbd_texts(mac_sdk, install_names):
    """Read only the needed TBDs and their reexports, retaining regular and preferred iOS-support copies."""
    texts = {}
    queue = sorted({name for name in install_names if system_library(name)})
    while queue:
        install_name = queue.pop(0)
        for relative in dict.fromkeys((tbd_relative(install_name, True), tbd_relative(install_name))):
            if relative in texts:
                continue
            path = Path(mac_sdk) / relative
            try:
                text = path.read_text(encoding="utf-8")
            except FileNotFoundError:
                continue
            except OSError as error:
                raise CheckError("reading %s: %s" % (path, error))
            texts[relative] = text
            _symbols, reexports = parse_tbd(text, target=tbd_target(relative))
            queue.extend(sorted(reexports - set(install_names)))
            install_names.update(reexports)
    return texts


def check_executable(executable, mac_sdk=None):
    if mac_sdk is None:
        mac_sdk = run_text(["xcrun", "--sdk", "macosx", "--show-sdk-path"]).strip()
    if not os.path.isdir(mac_sdk):
        raise CheckError("Mac SDK is not a directory: %s" % mac_sdk)
    imports_text = run_text(["xcrun", "dyld_info", "-imports", executable])
    imports = parse_imports(imports_text)
    if not imports:
        raise CheckError("dyld_info printed no imports for %s" % executable)
    linked = linked_install_names(executable)
    install_names = {linked[source] for source, _symbol, weak in imports if not weak and source in linked}
    tbd_texts = load_tbd_texts(mac_sdk, set(install_names))
    if not tbd_texts:
        raise CheckError("no comparable Mac TBDs found under %s" % mac_sdk)
    return find_missing_imports(imports_text, linked, tbd_texts)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("executable", help="the built iPhone app's main executable")
    parser.add_argument("--mac-sdk", help="MacOSX.sdk path (default: xcrun --sdk macosx --show-sdk-path)")
    args = parser.parse_args(argv)
    try:
        missing = check_executable(args.executable, args.mac_sdk)
    except CheckError as error:
        print("Mac import check: %s" % error, file=sys.stderr)
        return 2
    for install_name, symbol in missing:
        print("%s: %s" % (install_name, symbol))
    return 1 if missing else 0


if __name__ == "__main__":
    sys.exit(main())
