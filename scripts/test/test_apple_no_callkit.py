"""No Apple source or build setting brings in CallKit (#9290; docs/operations/release.md "No CallKit").

App Review rejects a build offered in China when any binary in it links CallKit.framework, even weakly and even when
the only use is a CXCallObserver. Calls use LiveCommunicationKit, which links CallKit itself; that is the system's
framework, not the app's, and is fine. LiveCommunicationKit's errors keep CallKit's domain strings
(`com.apple.CallKit.error.requesttransaction`): compare them as strings, never import CallKit for a constant.

A source scan, so it runs before any build (scripts/ci/guards.sh, scripts/ci/apple.sh, check-fast.sh):
- every Swift and Objective-C file under apple/ (all targets and their tests, Shared, the local packages) for a CallKit
  import or a CallKit type (CX…), with comments and string literals blanked;
- the XcodeGen specs, Package.swift manifests, module maps and xcconfig files under apple/, and the xcconfig
  scripts/persona_config.py writes, for CallKit anywhere outside a comment: an SDK dependency, `-framework CallKit`,
  `-weak_framework CallKit`, `-lswiftCallKit`, `.linkedFramework("CallKit")`, a Call Directory extension.
What a remote package or a prebuilt framework links only shows in the binaries: scripts/release/check_no_callkit.py
checks those after the CI builds and in every release check.
"""

from functools import cache
import importlib.util
import os
from pathlib import Path
import re
import sys
import tempfile
import unittest
from unittest.mock import patch

IMPLEMENTATION_ROOT = Path(__file__).resolve().parents[2]


def _source_root():
    configured = os.environ.get("PERSONA_CI_SOURCE_ROOT")
    if configured:
        if not Path(configured).is_absolute():
            raise ValueError("PERSONA_CI_SOURCE_ROOT must be absolute")
        return Path(configured)
    return IMPLEMENTATION_ROOT


ROOT = _source_root()
sys.path.insert(0, str(IMPLEMENTATION_ROOT / "scripts" / "i18n"))
import apple_comments  # noqa: E402

_spec = importlib.util.spec_from_file_location("persona_config_for_callkit", IMPLEMENTATION_ROOT / "scripts" / "persona_config.py")
persona_config = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(persona_config)

APPLE = ROOT / "apple"
# Build output, and third-party package checkouts (what those link shows in the built binaries).
SKIP_DIRS = apple_comments.SKIP_DIRS | {"SourcePackages"}
SWIFT = (".swift",)
OBJC = (".m", ".mm", ".h", ".c")
# Not the generated .xcodeproj: it lists every source file by name, and its settings come from the specs.
CONFIG_SUFFIXES = (".yml", ".yaml", ".xcconfig", ".modulemap")

# The `import` keyword and CallKit, the name escaped or not (`import `CallKit``), with any whitespace between them,
# newlines and blanked comments included, and an import kind (`import class CallKit.CXProvider`). Not anchored to a
# line start: attributes (`@preconcurrency`, an `@_spi(X)` split over lines), an access level or a `;` before it
# don't matter. Outside comments and strings `import` is a keyword, so this finds only import declarations; the
# keyword as an argument label or member name (`func f(import CallKit: Int)`, `s.import`, `` `import` ``) is skipped.
SWIFT_IMPORT = re.compile(
    r"(?<![\w$`.@#])import\s+(?:(?:typealias|struct|class|enum|protocol|let|var|func)\s+)?"
    r"(?P<module>`?CallKit`?)(?![\w$])(?!\s*:)")
# `@import CallKit;` is code (read with strings blanked; any whitespace before the name); `#import <CallKit/…>` and
# `#include "CallKit/…"` are preprocessor lines (strings kept), whose blanks may hold a `\`-newline continuation.
OBJC_MODULE = re.compile(r"(?<![\w@])@import\s+(?P<module>CallKit)\b")
_PP_BLANK = r"(?:[ \t]|\\\r?\n)*"
OBJC_DIRECTIVE = re.compile(
    rf"^[ \t]*#{_PP_BLANK}(?:import|include(?:_next)?){_PP_BLANK}[<\"]CallKit/", re.M)
# Every CallKit type and constant is CX + a capital: CXProvider, CXCallObserver, CXCallUpdate, CXAnswerCallAction,
# CXHandle, CXTransaction, CXErrorDomain… CXX (C++ interop spellings) is not one of them.
CX_TYPE = re.compile(r"\bCX(?!X)[A-Z]\w*")
MENTION = re.compile(r"callkit", re.I)


def blank(src: str, spans) -> str:
    """src with the spans turned into spaces, newlines kept (so line numbers stay)."""
    out = list(src)
    for start, end in spans:
        for i in range(start, end):
            if out[i] != "\n":
                out[i] = " "
    return "".join(out)


def without_comments(src: str) -> str:
    return blank(src, apple_comments.c_comments(src))


def code_only(src: str) -> str:
    """The source with its comments and the text of its string literals blanked; an interpolation `\\( … )` inside a
    string is code and stays. Swift's literals (multi-line, raw `#"…"#`) and C's ("…", '"')."""
    spans, n = [], len(src)

    def string(i):
        j = i
        while j < n and src[j] == "#":
            j += 1
        hashes = j - i
        multi = src.startswith('"""', j)
        close = ('"""' if multi else '"') + "#" * hashes
        esc = "\\" + "#" * hashes
        k = start = j + (3 if multi else 1)
        while k < n:
            if src.startswith(esc, k):
                m = k + len(esc)
                if m < n and src[m] == "(":
                    spans.append((start, k))
                    k = start = code(m + 1, True)
                    continue
                k = m + 1
                continue
            if src.startswith(close, k):
                spans.append((start, k))
                return k + len(close)
            if not multi and src[k] == "\n":
                break
            k += 1
        spans.append((start, k))
        return k

    def code(i, paren=False):
        depth = 0
        while i < n:
            c = src[i]
            if src.startswith("//", i):
                j = src.find("\n", i)
                j = n if j < 0 else j
                spans.append((i, j))
                i = j
                continue
            if src.startswith("/*", i):
                start, d, i = i, 1, i + 2
                while i < n and d:
                    if src.startswith("/*", i):
                        d, i = d + 1, i + 2
                    elif src.startswith("*/", i):
                        d, i = d - 1, i + 2
                    else:
                        i += 1
                spans.append((start, i))
                continue
            if c == '"' or (c == "#" and re.match(r'#+"', src[i:i + 8])):
                i = string(i)
                continue
            if c == "'" and i + 2 < n and src[i + 2] == "'":
                i += 3  # a C character literal like '"'
                continue
            if paren:
                if c == "(":
                    depth += 1
                elif c == ")":
                    if depth == 0:
                        return i + 1
                    depth -= 1
            i += 1
        return i

    code(0)
    return blank(src, spans)


def lines_of(text: str, matches, group=0) -> list[int]:
    return sorted({text.count("\n", 0, m.start(group)) + 1 for m in matches})


def callkit_uses(src: str, objc: bool = False) -> list[int]:
    """The lines that import CallKit or name a CallKit type, outside comments and string literals. An import split
    over lines is reported at the line of the module name."""
    code = code_only(src)
    found = lines_of(code, CX_TYPE.finditer(code))
    if objc:
        found += lines_of(code, OBJC_MODULE.finditer(code), "module")
        # #import "CallKit/CallKit.h" is a string to the lexer: read directives with the strings kept.
        text = without_comments(src)
        found += lines_of(text, OBJC_DIRECTIVE.finditer(text))
    else:
        found += lines_of(code, SWIFT_IMPORT.finditer(code), "module")
    return sorted(set(found))


def xcconfig_comments(src: str):
    return [m.span() for m in re.finditer(r"//[^\n]*", src)]


def config_mentions(src: str, name: str) -> list[int]:
    """The lines of an XcodeGen spec, manifest, module map or xcconfig file that mention CallKit outside a
    comment. Strings count: that is where `.linkedFramework("CallKit")` and `-framework CallKit` are written."""
    if name.endswith((".yml", ".yaml")):
        spans = apple_comments.hash_comments(src)
    elif name.endswith(".xcconfig"):
        spans = xcconfig_comments(src)
    else:
        spans = apple_comments.c_comments(src)
    text = blank(src, spans)
    return lines_of(text, MENTION.finditer(text))


@cache
def apple_files(root: Path = ROOT) -> tuple[Path, ...]:
    found = []
    for directory, dirs, names in os.walk(root / "apple"):
        dirs[:] = sorted(d for d in dirs if d not in SKIP_DIRS)
        found += [Path(directory, name) for name in sorted(names)]
    return tuple(found)


def sources(root: Path = ROOT) -> list[Path]:
    return [p for p in apple_files(root) if p.name.endswith(SWIFT + OBJC)]


def configs(root: Path = ROOT) -> list[Path]:
    return [p for p in apple_files(root) if p.name.endswith(CONFIG_SUFFIXES) or p.name == "Package.swift"]


def report(root: Path, path: Path, src: str, lines: list[int]) -> list[str]:
    text = src.split("\n")
    return [f"{path.relative_to(root).as_posix()}:{n}: {text[n - 1].strip()}" for n in lines]


def source_problems(root: Path = ROOT) -> list[str]:
    """`file:line: code` for each CallKit import or type in the sources under root/apple."""
    found = []
    for path in sources(root):
        src = path.read_text(errors="replace")
        if "CallKit" in src or "CX" in src:  # most files have neither: skip the lexer
            found += report(root, path, src, callkit_uses(src, objc=path.name.endswith(OBJC)))
    return found


def config_problems(root: Path = ROOT) -> list[str]:
    """`file:line: code` for each mention of CallKit outside a comment in the build files under root/apple."""
    found = []
    for path in configs(root):
        src = path.read_text(errors="replace")
        found += report(root, path, src, config_mentions(src, path.name))
    return found


HINT = ("App Review rejects any binary that links CallKit for China (docs/operations/release.md \"No CallKit\"); use "
        "LiveCommunicationKit and compare its error domains as strings:\n")


class AppleNoCallKitTests(unittest.TestCase):
    def test_no_source_imports_callkit_or_names_a_callkit_type(self):
        found = source_problems()
        self.assertEqual(found, [], HINT + "\n".join(found))

    def test_no_build_setting_links_callkit(self):
        found = config_problems()
        self.assertEqual(found, [], HINT + "\n".join(found))

    def test_a_planted_use_is_reported_with_its_place(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, text in {
                    "apple/PersonaWatch/Call.swift": "import Foundation\nimport CallKit\n",
                    # Unused imports, nothing CX… in the file: the import alone links libswiftCallKit.
                    "apple/PersonaWidgets/Escaped.swift": "import Foundation\nimport `CallKit`\n",
                    "apple/PersonaShare/Split.swift": "import Foundation\nimport\nCallKit\n",
                    "apple/Packages/X/Sources/X/Observer.swift": "// CallKit\nlet o = CXCallObserver()\n",
                    "apple/Packages/X/Package.swift": 'linkerSettings: [.linkedFramework("CallKit")]\n',
                    "apple/Persona/Bridge.m": "#import <CallKit/CallKit.h>\n",
                    "apple/extra.yml": "dependencies:\n  - sdk: CallKit.framework\n",
                    "apple/Persona/Clean.swift": "// no CallKit here\nlet d = \"com.apple.CallKit.error\"\n",
                    "apple/build/Generated.swift": "import CallKit\n",  # build output is not scanned
            }.items():
                (root / name).parent.mkdir(parents=True, exist_ok=True)
                (root / name).write_text(text)
            self.assertEqual(source_problems(root), [
                "apple/Packages/X/Sources/X/Observer.swift:2: let o = CXCallObserver()",
                "apple/Persona/Bridge.m:1: #import <CallKit/CallKit.h>",
                "apple/PersonaShare/Split.swift:3: CallKit",
                "apple/PersonaWatch/Call.swift:2: import CallKit",
                "apple/PersonaWidgets/Escaped.swift:2: import `CallKit`",
            ])
            self.assertEqual(config_problems(root), [
                "apple/extra.yml:2: - sdk: CallKit.framework",
                'apple/Packages/X/Package.swift:1: linkerSettings: [.linkedFramework("CallKit")]',
            ])

    def test_the_generated_xcconfig_links_no_callkit(self):
        # Every switch on and the tunnel core set, so each optional line, linker flags included, is written.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            entitlements = root / "apple" / persona_config.APP_ENTITLEMENTS
            entitlements.parent.mkdir(parents=True)
            entitlements.write_bytes((APPLE / persona_config.APP_ENTITLEMENTS).read_bytes())
            core = root / "tunnel"
            core.mkdir()
            for name in ("libpersonatunnel.a", "module.modulemap"):
                (core / name).write_text("")
            app = {"bundle_id": "org.example.persona.ci", "team_id": "", "tunnel_core": str(core)}
            app.update({switch.split(".", 1)[1]: True for switch in persona_config.OPT_IN})
            with patch.object(persona_config, "ROOT", str(root)):
                persona_config.xcconfig({"app": app})
            text = (root / "apple" / "Config.xcconfig").read_text()
        self.assertIn("-lpersonatunnel", text)
        lines = text.split("\n")
        self.assertEqual([lines[n - 1] for n in config_mentions(text, "Config.xcconfig")], [], HINT)

    def test_the_scan_reaches_every_target_and_manifest(self):
        tops = {p.relative_to(APPLE).parts[0] for p in sources()}
        self.assertTrue({"Persona", "Shared", "PersonaWatch", "PersonaWidgets", "PersonaNotify", "PersonaShare",
                         "PersonaTests", "Packages"} <= tops, tops)
        files = {p.relative_to(APPLE).as_posix() for p in configs()}
        self.assertTrue({"project.yml", "meta-wearables.yml", "IslandPeer/project.yml", "PersonaLink/Package.swift",
                         "Packages/PersonaKit/Package.swift", "Packages/PersonaCore/Package.swift"} <= files, files)

    def test_swift_imports_and_types(self):
        for src in ("import CallKit", "@preconcurrency import CallKit", "@_implementationOnly import CallKit",
                    "@_weakLinked import CallKit", "public import CallKit", "import class CallKit.CXProvider",
                    "import Foundation; import CallKit", "  import CallKit // for the observer",
                    "let observer = CXCallObserver()", "final class D: NSObject, CXProviderDelegate {}",
                    "func provider(_ p: CXProvider, perform action: CXAnswerCallAction) {}",
                    "let h = CallKit.CXHandle(type: .generic, value: v)",
                    "if (error as NSError).domain == CXErrorDomainRequestTransaction {}",
                    'let s = "calls: \\(CXCallObserver().calls.count)"', "/* a */ import CallKit",
                    # The name escaped (all legal Swift, checked with Xcode 27.1's swiftc).
                    "import `CallKit`", "@preconcurrency import `CallKit`", "import class `CallKit`.CXHandle",
                    "import `CallKit`.CXCall", "@_exported import CallKit", "import\tCallKit",
                    "import /* why */ CallKit", "@_spi(Calls) import CallKit"):
            with self.subTest(src=src):
                self.assertEqual(callkit_uses(src), [1])
        # Split over lines, at the line of the module name.
        for src, lines in (("import Foundation\n// ok\nimport CallKit\n", [3]),
                           ("import\nCallKit\n", [2]),
                           ("import\n  `CallKit`\nlet a = 1\n", [2]),
                           ("import Foundation; import\n\tCallKit\n", [2]),
                           ("@preconcurrency\nimport\n/* a\n b */ CallKit\n", [4]),
                           ("import class\n`CallKit`.CXProvider\n", [2]),
                           ("@_spi(\n  Calls) import CallKit\n", [2]),
                           ("#if canImport(CallKit)\nimport CallKit\n#endif\n", [2]),
                           ("import \r\nCallKit\r\n", [2])):
            with self.subTest(src=src):
                self.assertEqual(callkit_uses(src), lines)

    def test_comments_and_strings_are_not_uses(self):
        for src in ("// a VoIP call without CallKit, still going", "/* import CallKit */", "/// CXCallObserver",
                    "/* outer /* import CallKit */ still a comment */",
                    'let domain = "com.apple.CallKit.error.requesttransaction"', 'let name = "CXCallObserver"',
                    'let doc = """\nimport CallKit\nCXProvider\n"""', 'let raw = #"import CallKit "quoted""#',
                    "import LiveCommunicationKit", "import PushKit", "#if canImport(CallKit)",
                    "let record = CXXRecord()", "let p = ACXProvider()", "let c = cx.provider",
                    "import CallKitMock", "import `CallKitMock`", "import MyCallKit", "import\nCallKitMock",
                    'let doc = """\nimport\nCallKit\n"""', "/* import\nCallKit */",
                    # `import` as an argument label or a member name, not a declaration (legal Swift).
                    "func f(import CallKit: Int) {}", "let i = S().import", "func `import`(_ x: Int) {}"):
            with self.subTest(src=src):
                self.assertEqual(callkit_uses(src), [])

    def test_objective_c(self):
        for src in ("@import CallKit;", "#import <CallKit/CallKit.h>", "#include <CallKit/CXCall.h>",
                    '#import "CallKit/CallKit.h"', "  # import <CallKit/CXProvider.h>", "CXProvider *provider;",
                    "@import Foundation; @import CallKit;", "@import CallKit.CXCall;",
                    "#include_next <CallKit/CallKit.h>", "/* a */ #import <CallKit/CallKit.h>"):
            with self.subTest(src=src):
                self.assertEqual(callkit_uses(src, objc=True), [1])
        # Legal for clang too: `@import` over lines, a directive continued with `\`.
        for src, lines in (("@import\nCallKit;\n", [2]), ("@import /* a\n */ CallKit;\n", [2]),
                           ("#import \\\n<CallKit/CallKit.h>\n", [1]), ("# \\\n import <CallKit/CallKit.h>\n", [1])):
            with self.subTest(src=src):
                self.assertEqual(callkit_uses(src, objc=True), lines)
        for src in ("// #import <CallKit/CallKit.h>", '@"CXProvider"', "#import <PushKit/PushKit.h>",
                    "/* @import CallKit; */", 'NSString *s = @"@import CallKit;";', "@import CallKitMock;",
                    "#if __has_include(<CallKit/CallKit.h>)"):
            with self.subTest(src=src):
                self.assertEqual(callkit_uses(src, objc=True), [])

    def test_build_settings(self):
        for name, src, lines in (
                ("project.yml", "dependencies:\n  - sdk: CallKit.framework\n", [2]),
                ("project.yml", 'settings:\n  OTHER_LDFLAGS: "$(inherited) -weak_framework CallKit"\n', [2]),
                ("project.yml", "settings:\n  OTHER_LDFLAGS: -lswiftCallKit\n", [2]),
                ("project.yml", "NSExtensionPointIdentifier: com.apple.callkit.call-directory\n", [1]),
                ("project.yml", "# CallKit stays out (#9290)\n  - sdk: PushKit.framework # not CallKit\n", []),
                ("Package.swift", 'linkerSettings: [.linkedFramework("CallKit")]', [1]),
                ("Package.swift", '// linkerSettings: [.linkedFramework("CallKit")]\n.linkedFramework("PushKit")', []),
                ("Config.xcconfig", "OTHER_LDFLAGS = $(inherited) -framework CallKit\n", [1]),
                ("Config.xcconfig", "// CallKit stays out\nPERSONA_PUSH_RELAY = https:/$()/relay.example\n", []),
                ("module.modulemap", 'module X {\n  link framework "CallKit"\n}\n', [2]),
                ("module.modulemap", '/* link framework "CallKit" */\nlink framework "PushKit"\n', [])):
            with self.subTest(name=name, src=src):
                self.assertEqual(config_mentions(src, name), lines)


if __name__ == "__main__":
    unittest.main()
