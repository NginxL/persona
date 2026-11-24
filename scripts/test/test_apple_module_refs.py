#!/usr/bin/env python3
"""The Apple app's core may not name a feature module's types (#13432, part of #13430; docs/apple/ios.md, "Modules").

Each guarded module owns directories under apple/ (MODULES below, read from schema/module-classification.txt). The types declared at the top level of those directories are
the module's symbols. Every Swift file of the core (apple/ outside the module directories, outside the module list
apple/Persona/App/Modules.swift and outside tests) is scanned for those symbols as whole words, with comments and string
literals blanked out: a comment or a localized "Glasses Photo" is not a reference, `MetaGlasses.shared` is.

This is a ratchet: each core file has a committed count per module (apple_module_refs_baseline.json) that may stay the
same or go down, never up, and a file with no entry starts at zero. Reach the module through `Modules.registry` instead.
Like the other Apple ratchets it also fails when a count falls, so the baseline is lowered in the same change.
No comment or marker clears a reference.

  python3 scripts/test/test_apple_module_refs.py            run the check (unittest)
  python3 scripts/test/test_apple_module_refs.py --update   rewrite the baseline downward after removing references
  python3 scripts/test/test_apple_module_refs.py --list     print every counted reference, file:line
"""
from collections import Counter
import json
from pathlib import Path
import re
import sys
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts" / "i18n"))
import apple_comments  # noqa: E402
sys.path.insert(0, str(Path(__file__).resolve().parent))
import module_classification  # noqa: E402

BASELINE = Path(__file__).resolve().with_name("apple_module_refs_baseline.json")

# The modules this ratchet guards (#13430 section 2). Their directories come from schema/module-classification.txt
# (#13843), the one list of what is core and what is a module. A module joins here once its references from the core
# are worth counting; the other classified modules (about 700 references today) would only make every move churn the
# baseline.
GUARDED = ("glasses", "screen-share", "health")
_DIRS = module_classification.apple_module_dirs()
MODULES = {module: _DIRS.get(module, []) for module in GUARDED}
# The one core file allowed to name module types: the module list (the seed of PersonaHostApp).
MODULE_LIST = "apple/Persona/App/Modules.swift"
# Tests and tooling are not the core.
SKIP_PARTS = {"PersonaTests", "PersonaUITests", "PersonaWatchTests", "Tests", "verify"}

DECLARATION = re.compile(
    r"^((?:(?:@\w+(?:\([^)\n]*\))?|public|internal|fileprivate|private|final|nonisolated|indirect)\s+)*)"
    r"(?:struct|class|enum|protocol|actor|typealias)\s+([A-Z]\w*)", re.M)


def declared(code):
    """The type names a file declares at its top level for other files: a `private` or `fileprivate` one is the file's
    own (no other file can name it), so a core type of the same name is not a reference to it."""
    return [name for mods, name in DECLARATION.findall(code) if not re.search(r"\b(?:private|fileprivate)\b", mods)]


def blank(src):
    """The source with its comments and the text of its string literals blanked out, lines kept. Code inside a string's
    interpolation stays."""
    out = list(src)
    for start, end in apple_comments.c_comments(src):
        for i in range(start, end):
            if out[i] != "\n":
                out[i] = " "
    code = "".join(out)
    out = list(code)
    i, n = 0, len(code)
    while i < n:
        m = re.match(r'(#*)("""|")', code[i:i + 16]) if code[i] in '#"' else None
        if not m or (code[i] == "#" and i > 0 and (code[i - 1].isalnum() or code[i - 1] == "_")):
            i += 1
            continue
        hashes, quote = m.group(1), m.group(2)
        close, esc = quote + hashes, "\\" + hashes
        k = i + len(m.group(0))
        depth = 0
        while k < n:
            if depth == 0 and code.startswith(esc + "(", k):
                depth, k = 1, k + len(esc) + 1
                continue
            if depth:
                depth += {"(": 1, ")": -1}.get(code[k], 0)
                k += 1
                continue
            if code.startswith(esc, k):
                for j in range(k, min(k + len(esc) + 1, n)):
                    if out[j] != "\n":
                        out[j] = " "
                k += len(esc) + 1
                continue
            if code.startswith(close, k):
                k += len(close)
                break
            if quote == '"' and code[k] == "\n":
                break
            if out[k] != "\n":
                out[k] = " "
            k += 1
        i = k
    return "".join(out)


def swift_files(root=ROOT):
    for path in apple_comments.sources():
        p = Path(path)
        rel = p.relative_to(root)
        if p.suffix == ".swift" and not SKIP_PARTS.intersection(rel.parts) and not rel.parts[1].endswith("Tests"):
            yield rel


def owner(rel):
    """The module whose directory holds this file, or None for the core."""
    s = rel.as_posix()
    for module, dirs in MODULES.items():
        if any(s == d or s.startswith(d + "/") for d in dirs):
            return module
    return None


def symbols(files):
    """{module: set of type names} declared at the top level of each module's files."""
    out = {m: set() for m in MODULES}
    for rel in files:
        module = owner(rel)
        if module:
            out[module].update(declared(blank((ROOT / rel).read_text(encoding="utf-8", errors="replace"))))
    return out


def references(src, syms):
    """(module, line, symbol) for each reference to a module's type in one Swift file."""
    code = blank(src)
    found = []
    for module, names in syms.items():
        if not names:
            continue
        pattern = re.compile(r"(?<![\w.])(?:" + "|".join(sorted(map(re.escape, names))) + r")\b")
        for m in pattern.finditer(code):
            found.append((module, code.count("\n", 0, m.start()) + 1, m.group(0)))
    return sorted(found, key=lambda x: (x[1], x[0]))


def scan():
    """{core file: {module: count}} and the references behind it."""
    files = list(swift_files())
    syms = symbols(files)
    counts, sites = {}, []
    for rel in files:
        if owner(rel) or rel.as_posix() == MODULE_LIST:
            continue
        found = references((ROOT / rel).read_text(encoding="utf-8", errors="replace"), syms)
        if found:
            counts[rel.as_posix()] = dict(sorted(Counter(m for m, _, _ in found).items()))
            sites += [f"{rel}:{line} {module} {sym}" for module, line, sym in found]
    return dict(sorted(counts.items())), sites


def load_baseline():
    return json.loads(BASELINE.read_text(encoding="utf-8"))


def compare(counts, baseline):
    """(grown, shrunk): lines describing files over their baseline, and under it."""
    grown, shrunk = [], []
    for f in sorted(set(counts) | set(baseline)):
        now, was = counts.get(f, {}), baseline.get(f, {})
        for module in sorted(set(now) | set(was)):
            n, w = now.get(module, 0), was.get(module, 0)
            if n > w:
                grown.append(f"{f}: {module} {w} -> {n}")
            elif n < w:
                shrunk.append(f"{f}: {module} {w} -> {n}")
    return grown, shrunk


def update():
    counts, _ = scan()
    grown, _ = compare(counts, load_baseline())
    if grown:
        print("refusing to raise the baseline (reach the module through Modules.registry):")
        print("\n".join(grown))
        return 1
    BASELINE.write_text(json.dumps(counts, indent=1, sort_keys=True) + "\n", encoding="utf-8")
    print(f"apple module references: {sum(sum(r.values()) for r in counts.values())} in {len(counts)} core files")
    return 0


class AppleModuleRefsTests(unittest.TestCase):
    def test_core_names_no_more_module_types(self):
        counts, _ = scan()
        grown, shrunk = compare(counts, load_baseline())
        self.assertEqual(grown, [], "a core file names a module's type (docs/apple/ios.md, \"Modules\"): reach it through "
                         "Modules.registry, or add the slot it needs to PersonaModuleAPI; "
                         "python3 scripts/test/test_apple_module_refs.py --list shows each reference")
        self.assertEqual(shrunk, [], "fewer module references than the baseline: run "
                         "python3 scripts/test/test_apple_module_refs.py --update and commit the baseline")

    def test_every_module_directory_exists_and_declares_types(self):
        syms = symbols(list(swift_files()))
        for module, dirs in MODULES.items():
            self.assertTrue(dirs, f"{module} has no Apple directory in schema/module-classification.txt")
            for d in dirs:
                self.assertTrue((ROOT / d).is_dir(), d)
            self.assertTrue(syms[module], module)
        self.assertTrue((ROOT / MODULE_LIST).is_file(), MODULE_LIST)

    def test_what_counts(self):
        syms = {"glasses": {"MetaGlasses", "GlassesCaptureSheet"}}
        self.assertEqual(references("MetaGlasses.shared.configure()", syms), [("glasses", 1, "MetaGlasses")])
        self.assertEqual(references("x\n.sheet { GlassesCaptureSheet { add($0) } }", syms), [("glasses", 2, "GlassesCaptureSheet")])
        self.assertEqual(references("extension MetaGlasses {}", syms), [("glasses", 1, "MetaGlasses")])
        self.assertEqual(references("// MetaGlasses.shared\n/* GlassesCaptureSheet */", syms), [])
        self.assertEqual(references('let s = "MetaGlasses"\nlet t = String(localized: "Glasses Photo")', syms), [])
        self.assertEqual(references('let s = #"MetaGlasses"#\nlet m = """\nMetaGlasses\n"""', syms), [])
        self.assertEqual(references('let s = "\\(MetaGlasses.isBuiltIn)"', syms), [("glasses", 1, "MetaGlasses")])
        self.assertEqual(references("let x = MyMetaGlasses(); let y = MetaGlassesKit; let z = a.MetaGlasses", syms), [])
        self.assertEqual(references("var showGlasses = false; Theme.Icon.glasses", syms), [])

    def test_declarations(self):
        src = ("nonisolated struct GlassesError: Error {}\n@Observable\n@MainActor\nfinal class MetaGlasses {\n"
               "    enum Inner {}\n}\nprivate struct Helper {}\n// struct Commented {}\n")
        self.assertEqual(declared(blank(src)), ["GlassesError", "MetaGlasses"])  # a private type is the file's own

    def test_compare(self):
        self.assertEqual(compare({"a": {"x": 2}}, {"a": {"x": 1}}), (["a: x 1 -> 2"], []))
        self.assertEqual(compare({"b": {"x": 1}}, {}), (["b: x 0 -> 1"], []))
        self.assertEqual(compare({}, {"a": {"x": 1}}), ([], ["a: x 1 -> 0"]))


if __name__ == "__main__":
    if sys.argv[1:] == ["--update"]:
        sys.exit(update())
    if sys.argv[1:] == ["--list"]:
        print("\n".join(scan()[1]))
        sys.exit(0)
    unittest.main()
