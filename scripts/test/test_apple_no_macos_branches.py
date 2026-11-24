"""No macOS branch in the Apple app targets, Shared/ or their tests (#13399).

The native Mac app is retired (#6770, docs/desktop/desktop.md): apple/project.yml has iOS, visionOS and watchOS targets
only, so an `#if os(macOS)` or `canImport(AppKit)` branch there is never compiled (UIKit, never AppKit, on all three),
and new code copying one only adds lines nobody builds or tests. A source scan, so it runs before any build: every
`#if` / `#elseif` line of the Swift files under apple/ that names os(macOS) or canImport(AppKit), and every
`import AppKit`, outside multi-line string literals (a test's fixture source may hold one).

The Swift packages whose Package.swift declares a macOS platform are left out: they are built for macOS (the Persona Link
helper in apple/PersonaLink builds PersonaScreenCore, CI's package lane tests them with `swift test` on a Mac), so a
branch there is live code.
"""

import os
from pathlib import Path
import re
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
APPLE = ROOT / "apple"
# Build output and package checkouts.
SKIP_DIRS = {".build", "DerivedData", "build", "node_modules", ".swiftpm", "SourcePackages"}
DIRECTIVE = re.compile(r"^\s*(?:#(?:if|elseif)\b.*\b(?:os\(\s*macOS\s*\)|canImport\(\s*AppKit\s*\))|import\s+AppKit\b)")
MACOS_PLATFORM = re.compile(r"\.macOS\s*\(")


def files(root, match):
    """The files under `root` whose name `match` accepts, build output left out without walking into it."""
    for top, dirs, names in os.walk(root):
        dirs[:] = sorted(d for d in dirs if d not in SKIP_DIRS)
        for name in sorted(names):
            if match(name):
                yield Path(top) / name


def macos_packages(root):
    """The directories of the Package.swift manifests under `root` that declare a macOS platform."""
    return [p.parent for p in files(root, lambda n: n == "Package.swift")
            if MACOS_PLATFORM.search(p.read_text(encoding="utf-8"))]


def branches(text):
    """The 1-based line numbers of the `#if` / `#elseif` lines naming os(macOS) or canImport(AppKit), and of the
    `import AppKit` lines, skipping multi-line string literals."""
    found, in_string = [], False
    for number, line in enumerate(text.split("\n"), 1):
        if not in_string and DIRECTIVE.match(line.split("//", 1)[0]):
            found.append(number)
        if line.count('"""') % 2 == 1:
            in_string = not in_string
    return found


def scan(root):
    """`path:line` for every os(macOS) branch under `root` outside the packages built for macOS."""
    live = macos_packages(root)
    hits = []
    for path in files(root, lambda n: n.endswith(".swift")):
        if any(path.is_relative_to(p) for p in live):
            continue
        for number in branches(path.read_text(encoding="utf-8")):
            hits.append(f"{path.relative_to(root.parent)}:{number}")
    return sorted(hits)


class NoMacOSBranchesTests(unittest.TestCase):
    def test_apple_targets_have_no_macos_branches(self):
        hits = scan(APPLE)
        self.assertEqual(hits, [], "a macOS branch in an Apple app target is never compiled (no macOS target, #13399): "
                         "drop it and keep what iOS, visionOS and watchOS build:\n" + "\n".join(hits))

    def test_the_link_helpers_package_is_left_out(self):
        live = {p.relative_to(APPLE).as_posix() for p in macos_packages(APPLE)}
        self.assertIn("Packages/PersonaScreenCore", live)

    def test_branches(self):
        source = "\n".join([
            "#if os(macOS)",                       # 1
            "#endif",
            "  #if DEBUG && !os( macOS ) // x",     # 3
            "#endif",
            "#if os(iOS)",
            "#elseif os(macOS)",                   # 6
            "#endif",
            "// #if os(macOS)",
            'let s = "#if os(macOS)"',
            'let t = """',
            "#if os(macOS)",
            '"""',
            "#if os(watchOS) // not os(macOS)",
            "#endif",
            "#if canImport(UIKit)",
            "#elseif canImport(AppKit)",           # 16
            "import AppKit",                       # 17
            "#endif",
            "import UIKit",
        ])
        self.assertEqual(branches(source), [1, 3, 6, 16, 17])

    def test_scan_skips_packages_built_for_macos(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "apple"
            (root / "Persona").mkdir(parents=True)
            (root / "Persona" / "A.swift").write_text("#if os(macOS)\n#endif\n")
            mac = root / "Packages" / "MacKit"
            (mac / "Sources").mkdir(parents=True)
            (mac / "Package.swift").write_text("platforms: [.iOS(.v18), .macOS(.v15)]\n")
            (mac / "Sources" / "B.swift").write_text("#if os(macOS)\n#endif\n")
            ios = root / "Packages" / "PhoneKit"
            (ios / "Sources").mkdir(parents=True)
            (ios / "Package.swift").write_text("platforms: [.iOS(.v18)]\n")
            (ios / "Sources" / "C.swift").write_text("\n#if os(macOS)\n#endif\n")
            (root / "build" / "x").mkdir(parents=True)
            (root / "build" / "x" / "D.swift").write_text("#if os(macOS)\n#endif\n")
            self.assertEqual(scan(root), ["apple/Packages/PhoneKit/Sources/C.swift:2", "apple/Persona/A.swift:1"])


if __name__ == "__main__":
    unittest.main()
