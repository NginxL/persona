#!/usr/bin/env python3
"""Names compiled into the iPhone app must not equal a DEBUG launch switch in scripts/release/debug_switches.txt.

release.sh refuses a Release binary whose `strings` has a line equal to one of the DEBUG-only launch switches listed
there (scripts/release/debug_switches.py): such a line means DEBUG code got into the build. A stored property's or an
enum case's name is a string in the binary too (Swift reflection metadata, SWIFT_REFLECTION_METADATA_LEVEL = all), and
so is an `@objc` member's name or a string literal: a property called `openAgent` stops the release exactly as the
switch `-openAgent` would, and only once someone archives (#2179, #8306; docs/lessons/ios.md). This reads the app's
sources outside `#if DEBUG` for those names, so the collision fails here, before merge.

Rename the property or case (`on…` / `select…` for closures), or give a new switch a name nothing in the app uses.
Property wrappers (`@State var showInfo`) are stored as `_showInfo` and never collide.
"""
import re
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts" / "release"))
import debug_switches
APPLE = ROOT / "apple"
# What goes into the iPhone app's own binary (apple/project.yml, target Persona): its sources and the local packages it
# links. The watch app and the extensions are other binaries, which release.sh does not check.
SCAN = ["Persona", "Shared", "LiveActivity"]
PACKAGES = APPLE / "Packages"

WRAPPERS = {"State", "Binding", "Environment", "EnvironmentObject", "AppStorage", "SceneStorage", "FocusState",
            "Bindable", "Published", "StateObject", "ObservedObject", "StoredValue", "GestureState", "Namespace",
            "ScaledMetric", "FetchRequest", "Query", "UIApplicationDelegateAdaptor", "AccessibilityFocusState"}
DECL = re.compile(r"\b(?:var|let)\s+([A-Za-z_]\w*)\s*[:=]")
OBJC_FUNC = re.compile(r"\bfunc\s+([A-Za-z_]\w*)\s*\(\s*\)")
CASE = re.compile(r"\bcase\s+")
LITERAL = re.compile(r'"([A-Za-z_]\w*)"')


def switches() -> set:
    return set(debug_switches.names())


# What a condition is in the iPhone app's Release archive: True, False, or None (could be either). The app target sets
# no compilation conditions of its own (apple/project.yml), so DEBUG and every flag (PERF_SIGNPOST, WIDGET_EXTENSION…)
# are false; the archive is for iOS devices.
TOKEN = re.compile(r"\s*(\(|\)|!|&&|\|\||[A-Za-z_]\w*\s*\([^()]*\)|[A-Za-z_]\w*)")


def check(call: str):
    name, arg = re.match(r"(\w+)\s*\((.*)\)", call).groups()
    arg = arg.strip()
    if name == "os":
        return arg == "iOS"
    if name == "canImport":
        return {"UIKit": True, "AppKit": False, "WatchKit": False}.get(arg)
    if name == "targetEnvironment":
        return False  # simulator, macCatalyst: not a device archive
    return None  # compiler(…), swift(…), arch(…), hasFeature(…)


def evaluate(cond: str):
    tokens = TOKEN.findall(cond.split("//")[0])
    pos = 0

    def peek():
        return tokens[pos] if pos < len(tokens) else None

    def take():
        nonlocal pos
        pos += 1
        return tokens[pos - 1]

    def unary():
        t = take()
        if t == "!":
            v = unary()
            return None if v is None else not v
        if t == "(":
            v = disjunction()
            take()
            return v
        if "(" in t:
            return check(t)
        return {"true": True, "false": False}.get(t, False)  # a flag

    def conjunction():
        values = [unary()]
        while peek() == "&&":
            take()
            values.append(unary())
        return False if False in values else (True if all(values) else None)

    def disjunction():
        values = [conjunction()]
        while peek() == "||":
            take()
            values.append(conjunction())
        return True if True in values else (False if all(v is False for v in values) else None)

    return disjunction()


def negate(v):
    return None if v is None else not v


def collisions(text: str, names: set) -> list:
    """(line number, name, line) for each name in names that this Swift source compiles into a Release build."""
    found = []
    stack = []  # per open #if: [this branch is compiled, an earlier branch was taken] (True / False / None)
    previous = ""
    for number, raw in enumerate(text.splitlines(), 1):
        line = raw.strip()
        if line.startswith("#if "):
            v = evaluate(line[4:])
            stack.append([v, v])
            continue
        if line.startswith("#elseif ") and stack:
            v, taken = evaluate(line[8:]), stack[-1][1]
            branch = False if taken is True or v is False else (True if taken is False and v is True else None)
            stack[-1] = [branch, True if True in (taken, v) else (False if taken is False and v is False else None)]
            continue
        if line.startswith("#else") and stack:
            stack[-1][0] = negate(stack[-1][1])
            continue
        if line.startswith("#endif"):
            if stack:
                stack.pop()
            continue
        if any(branch is False for branch, _ in stack) or line.startswith("//") or not line:
            continue
        attributes = set(re.findall(r"@(\w+)", line.split("var ")[0].split("let ")[0]))
        if re.fullmatch(r"(@\w+(\([^)]*\))?\s*)+", previous):
            attributes |= set(re.findall(r"@(\w+)", previous))
        hits = []
        if not (attributes & WRAPPERS):
            for m in DECL.finditer(line):
                # `if let x = …`, `guard var x`, `case let x`, a second binding after a comma: not declarations.
                if not re.search(r"(\b(if|guard|while|case)|[,(])\s*$", line[:m.start()]):
                    hits.append(m.group(1))
        if "objc" in attributes or "IBAction" in attributes:
            hits += OBJC_FUNC.findall(line)
        for m in CASE.finditer(line):
            # An enum's cases: `case a, b(Int), c = "x"`. A switch's `case .a:` / `case let x:` has a colon left once the
            # parentheses are gone.
            segment = re.split(r"[};]|//", line[m.end():])[0]
            depth, flat = 0, ""
            for ch in segment:
                depth += ch == "("
                if depth == 0:
                    flat += ch
                depth -= ch == ")" and depth > 0
            if ":" in flat:
                continue
            for part in flat.split(","):
                word = re.match(r"\s*([A-Za-z_]\w*)", part)
                if word and word.group(1) not in ("let", "var", "is", "_"):
                    hits.append(word.group(1))
        hits += LITERAL.findall(line.split(" //")[0])
        found += [(number, n, line) for n in dict.fromkeys(hits) if n in names]
        previous = line
    return found


def sources():
    for top in SCAN:
        yield from sorted((APPLE / top).rglob("*.swift"))
    for package in sorted(PACKAGES.iterdir()):
        if (package / "Sources").is_dir():
            yield from sorted((package / "Sources").rglob("*.swift"))


class ReleaseSwitchNamesTest(unittest.TestCase):
    def test_list_is_read(self):
        # A broken list file must not quietly empty the list and pass everything.
        self.assertGreater(len(switches()), 100, "scripts/release/debug_switches.txt lists no DEBUG switches")

    def test_no_release_name_equals_a_debug_switch(self):
        names = switches()
        bad = []
        for path in sources():
            for number, name, line in collisions(path.read_text(encoding="utf-8"), names):
                bad.append(f"{path.relative_to(ROOT)}:{number}: {name}: {line[:100]}")
        self.assertEqual(bad, [], "these names reach the Release binary and equal a DEBUG launch switch in "
                                  "scripts/release/debug_switches.txt, so release.sh refuses the build: rename them "
                                  "(closures: on… / select…), or move the code into #if DEBUG")

    def test_the_scan_itself(self):
        names = {"openAgent", "showInfo", "openSession"}
        sample = "\n".join([
            "struct A {",
            "    var openAgent: (() -> Void)? = nil",        # 2: stored property
            "    @State private var showInfo = false",      # wrapper: stored as _showInfo
            "    @Environment(\\.x)",
            "    var openSession",                          # wrapper on the line before
            "    #if DEBUG",
            "    let openAgent = 1",                        # DEBUG only
            "    #else",
            "    let showInfo = 2",                         # 9: the Release side
            "    #endif",
            "    enum E { case one, openSession(Int) }",
            "    enum F: String {",
            "        case two, openSession = \"x\"",       # 13: enum case
            "    }",
            "    func f() { switch e { case .openSession: break }; if let openAgent = x {} }",
            "    // var openAgent = 1",
            "    let k = UserDefaults.standard.bool(forKey: \"showInfo\")",  # 17: literal
            "    #if os(macOS)",
            "    #else",
            "    var openAgent = 3",                        # 20: the iOS side of a platform #if
            "    #endif",
            "    #if DEBUG && (os(iOS) || os(visionOS))",
            "    let showInfo = 4",                         # DEBUG only
            "    #endif",
            "    #if DEBUG || PERF_SIGNPOST",
            "    let showInfo = 5",                         # not in a Release archive either
            "    #elseif canImport(PersonaScreenKit)",
            "    let openSession = 6",                      # 28: may be compiled
            "    #endif",
            "    #if !DEBUG",
            "    let openSession = 7",                      # 31: Release
            "    #else",
            "    let openSession = 8",                      # DEBUG
            "    #endif",
            "}",
        ])
        self.assertEqual([(n, name) for n, name, _ in collisions(sample, names)],
                         [(2, "openAgent"), (9, "showInfo"), (11, "openSession"), (13, "openSession"), (17, "showInfo"),
                          (20, "openAgent"), (28, "openSession"), (31, "openSession")])


if __name__ == "__main__":
    unittest.main()
