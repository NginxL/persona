"""Read schema/module-classification.txt: which unit of the code is Persona Core and which belongs to a feature module
(#13843; docs/architecture/core-and-modules.md "Classification"). The file's header describes the format.

Used by test_module_classification.py (every unit classified, no stale line) and by the Apple module ratchet
(test_apple_module_refs.py), which takes a module's Apple directories from here.
"""
from dataclasses import dataclass
from importlib.util import module_from_spec, spec_from_file_location
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[2]
FILE = ROOT / "schema" / "module-classification.txt"
KINDS = ("go", "apple", "gradle", "web")
CORE = "core"


@dataclass(frozen=True)
class Rule:
    kind: str
    pattern: str
    target: str  # "core", a module id, or "*": the name the wildcard matched
    line: int

    @property
    def recursive(self):
        return self.pattern.endswith("/...")

    @property
    def exact(self):
        return not self.recursive and "*" not in self.pattern

    def regex(self):
        sep = ":" if self.kind == "gradle" else "/"
        body = self.pattern[:-4] if self.recursive else self.pattern
        parts = [f"([^{sep}]+)" if seg == "*" else re.escape(seg) for seg in body.split(sep)]
        return re.compile(re.escape(sep).join(parts) + (r"(?:/.*)?" if self.recursive else "") + r"\Z")

    def specificity(self):
        """Exact lines beat patterns; among patterns the longer literal part wins."""
        return (self.exact, len(self.pattern.replace("*", "").replace("...", "")))

    def match(self, unit):
        """The class this rule gives unit, or None when it does not match."""
        m = self.regex().match(unit)
        if not m:
            return None
        return m.group(1) if self.target == "*" else self.target


class FormatError(ValueError):
    pass


def parse(text):
    """(module ids, rules); FormatError on a malformed line."""
    modules, rules, seen = set(), [], {}
    for n, raw in enumerate(text.splitlines(), 1):
        line = raw.split("#", 1)[0].strip()
        if not line:
            continue
        words = line.split()
        if words[0] == "module":
            if len(words) != 2 or not re.fullmatch(r"[a-z][a-z0-9-]*", words[1]):
                raise FormatError(f"line {n}: expected `module <id>`: {raw!r}")
            modules.add(words[1])
            continue
        if len(words) != 3 or words[0] not in KINDS:
            raise FormatError(f"line {n}: expected `<{'|'.join(KINDS)}> <unit> <core|module id>`: {raw!r}")
        rule = Rule(words[0], words[1], words[2], n)
        if (rule.kind, rule.pattern) in seen:
            raise FormatError(f"line {n}: {rule.kind} {rule.pattern} is already classified on line {seen[rule.kind, rule.pattern]}")
        seen[rule.kind, rule.pattern] = n
        if rule.target == "*" and rule.pattern.count("*") != 1:
            raise FormatError(f"line {n}: a `*` class needs exactly one `*` in the unit: {raw!r}")
        if rule.target != "*" and "*" in rule.pattern:
            raise FormatError(f"line {n}: a wildcard unit takes its module from the wildcard; write `*` as the class: {raw!r}")
        if rule.recursive and rule.target == CORE:
            raise FormatError(f"line {n}: core is never inherited; list each core unit on its own line: {raw!r}")
        if rule.recursive and rule.kind == "gradle":
            raise FormatError(f"line {n}: Gradle projects take no `/...`: {raw!r}")
        rules.append(rule)
    for rule in rules:
        if rule.target not in (CORE, "*") and rule.target not in modules:
            raise FormatError(f"line {rule.line}: {rule.target!r} is neither core nor a declared `module` id")
    return modules, rules


def tracked_files(root=ROOT):
    out = subprocess.run(["git", "-C", str(root), "ls-files", "-z", "--", "server", "sdk/go", "apple", "web/src"],
                         check=True, capture_output=True).stdout.decode()
    return [p for p in out.split("\0") if p]


def gradle_projects(settings_text):
    """Use the same literal include inventory as the Android CI shards."""
    spec = spec_from_file_location("android_shards", ROOT / "scripts/ci/android-shards.py")
    shards = module_from_spec(spec)
    spec.loader.exec_module(shards)
    return shards.modules(settings_text)


def inventory(files, settings_text):
    """kind -> the units to classify; and the set of every tracked directory (a deeper line may name one)."""
    dirs = set()
    for f in files:
        parts = f.split("/")[:-1]
        for k in range(1, len(parts) + 1):
            dirs.add("/".join(parts[:k]))
    go = {f.rsplit("/", 1)[0] for f in files
          if f.endswith(".go") and (f.startswith("server/") or f.startswith("sdk/go/")) and "testdata" not in f.split("/")}
    apple = {d for d in dirs if re.fullmatch(r"apple/[^/]+", d) and d not in ("apple/Persona", "apple/Packages")}
    apple |= {d for d in dirs if re.fullmatch(r"apple/(Persona|Packages)/[^/]+", d)}
    web = {d for d in dirs if re.fullmatch(r"web/src/[^/]+(/[^/]+)?", d)}
    return {"go": go, "apple": apple, "gradle": set(gradle_projects(settings_text)), "web": web}, dirs


def load(root=ROOT):
    """(module ids, rules, units by kind, tracked directories) of the repository at root."""
    modules, rules = parse((root / "schema" / "module-classification.txt").read_text(encoding="utf-8"))
    units, dirs = inventory(tracked_files(root), (root / "android" / "settings.gradle.kts").read_text(encoding="utf-8"))
    return modules, rules, units, dirs


def classify(unit, kind, rules):
    """(class, rule) of the most specific rule matching unit, or (None, None)."""
    best = None
    for rule in rules:
        if rule.kind != kind:
            continue
        target = rule.match(unit)
        if target is not None and (best is None or rule.specificity() > best[1].specificity()):
            best = (target, rule)
    return best or (None, None)


def problems(modules, rules, units, dirs):
    """Every failure, as readable lines: unclassified units, units a module root gives an undeclared id, stale lines."""
    out, used = [], set()
    for kind in KINDS:
        for unit in sorted(units[kind]):
            target, rule = classify(unit, kind, rules)
            if target is None:
                out.append(f"unclassified {kind} unit {unit}: add `{kind} {unit} <core|module id>` to "
                           "schema/module-classification.txt")
                continue
            used.add(rule)
            if target != CORE and target not in modules:
                out.append(f"{kind} {unit}: {target!r} (line {rule.line}) is not a declared `module` id")
    for rule in rules:
        if rule in used:
            continue
        if rule.exact and rule.kind in ("apple", "web") and rule.pattern in dirs:
            continue  # a deeper directory's future home, still there
        out.append(f"stale line {rule.line}: `{rule.kind} {rule.pattern} {rule.target}` matches nothing; delete it or "
                   "point it at the unit's new place")
    return out


def apple_module_dirs(root=ROOT):
    """Module id -> the Apple directories the file gives it (sorted)."""
    modules, rules = parse((root / "schema" / "module-classification.txt").read_text(encoding="utf-8"))
    out = {}
    for rule in rules:
        if rule.kind == "apple" and rule.target not in (CORE, "*") and "*" not in rule.pattern:
            out.setdefault(rule.target, []).append(rule.pattern[:-4] if rule.recursive else rule.pattern)
    return {k: sorted(v) for k, v in sorted(out.items())}
