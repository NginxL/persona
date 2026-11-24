#!/usr/bin/env python3
"""Fail when web/src gains a disable of the feature-module boundary rules.

web/eslint.modules.config.mjs keeps Persona Core out of the feature modules (#13434, #13430 section 6;
docs/web/web.md "Modules") with ESLint's no-restricted-imports, no-restricted-syntax and its own
persona-modules/boundary rule. A line that turns one of them off
(`eslint-disable…` naming one of them, or a bare `eslint-disable…` that turns every rule off) is a hole in that
boundary. This counts them per file under web/src, tests included. The committed counts
(scripts/ci/web_module_boundary_baseline.json) may stay or go down, never up, and a file with no entry starts at zero,
so a new file cannot add one. The baseline starts empty: the boundary landed with no exception.

Only a rise fails. A file under its baseline passes with a notice: lowering the baseline is tidying anyone can do,
with --update here or scripts/ci/lower_baselines.py for every count ratchet (docs/operations/testing.md, "Count
ratchets"). --update rewrites the baseline downward and drops files that no longer have any; it refuses to raise a
count or add a file, and will not create a baseline.
"""
import argparse
import json
import os
from pathlib import Path
import re
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
import ratchet_base  # noqa: E402


def _source_root():
    configured = os.environ.get("PERSONA_CI_SOURCE_ROOT")
    if configured:
        if not Path(configured).is_absolute():
            raise ValueError("PERSONA_CI_SOURCE_ROOT must be absolute")
        return Path(configured)
    return Path(__file__).resolve().parents[2]


ROOT = _source_root()
SOURCE = "web/src"
BASELINE_REL = "scripts/ci/web_module_boundary_baseline.json"
SUFFIXES = (".ts", ".tsx", ".mts", ".js", ".jsx", ".mjs")
RULES = ("no-restricted-imports", "no-restricted-syntax", "persona-modules/boundary")
# `eslint-disable`, `eslint-disable-line`, `eslint-disable-next-line`, and what follows on the line (the rules named)
DIRECTIVE = re.compile(r"eslint-disable(?:-next-line|-line)?\b([^\n]*)")


class BoundaryError(Exception):
    """The check could not be run (broken baseline)."""


def count_disables(text):
    """How many lines turn off a boundary rule: naming one, or naming no rule at all (every rule)."""
    found = 0
    for match in DIRECTIVE.finditer(text):
        rules = match.group(1).split("--")[0].replace("*/", "").strip()
        if not rules or any(rule in rules for rule in RULES):
            found += 1
    return found


def scan(root):
    """Positive counts keyed by repo-relative path. Files with none are absent."""
    root = Path(root)
    counts = {}
    base = root / SOURCE
    if not base.is_dir():
        return counts
    for path in sorted(base.rglob("*")):
        if not path.is_file() or path.suffix not in SUFFIXES or "node_modules" in path.parts:
            continue
        found = count_disables(path.read_text(encoding="utf-8", errors="replace"))
        if found:
            counts[path.relative_to(root).as_posix()] = found
    return counts


def render(counts):
    kept = {path: count for path, count in sorted(counts.items()) if count > 0}
    return json.dumps(kept, indent=1, sort_keys=True) + "\n"


def parse_baseline(text, label):
    try:
        data = json.loads(text)
    except json.JSONDecodeError as error:
        raise BoundaryError("web module boundary ratchet: {0} is not valid JSON ({1})".format(label, error.msg))
    if not isinstance(data, dict):
        raise BoundaryError("web module boundary ratchet: {0} must be a JSON object of file to count".format(label))
    counts = {}
    for key, value in data.items():
        if not isinstance(key, str) or not key.startswith(SOURCE + "/"):
            raise BoundaryError("web module boundary ratchet: {0} has a key outside {1}: {2!r}".format(label, SOURCE, key))
        if isinstance(value, bool) or not isinstance(value, int) or value < 0:
            raise BoundaryError("web module boundary ratchet: {0} count for {1} must be a non-negative integer".format(label, key))
        if value > 0:
            counts[key] = value
    return counts


def violations(actual, baseline):
    """Files over the baseline. Missing on either side counts as zero; a file under it passes (lowerable)."""
    return [
        "{0}: {1} disable(s) of the module boundary, baseline {2}; reach a module through the module list and the "
        "module host, or name it in the importing module's `needs` (docs/web/web.md \"Modules\")".format(path, got, baseline.get(path, 0))
        for path, got in sorted(actual.items()) if got > baseline.get(path, 0)
    ]


def lowerable(actual, baseline):
    return ["{0}: {1} disable(s), baseline {2}".format(path, actual.get(path, 0), allowed)
            for path, allowed in sorted(baseline.items()) if actual.get(path, 0) < allowed]


def check(root, notes=None):
    path = Path(root) / BASELINE_REL
    if not path.is_file():
        return ["web module boundary ratchet: baseline missing ({0})".format(BASELINE_REL)]
    actual, baseline = scan(root), parse_baseline(path.read_text(encoding="utf-8"), BASELINE_REL)
    if notes is not None:
        notes.extend(lowerable(actual, baseline))
    return violations(actual, baseline)


def update(root):
    path = Path(root) / BASELINE_REL
    if not path.is_file():
        raise BoundaryError("web module boundary ratchet: no baseline at {0}; refusing to create one".format(BASELINE_REL))
    old, actual = parse_baseline(path.read_text(encoding="utf-8"), BASELINE_REL), scan(root)
    refusals = ["web module boundary ratchet: refusing to raise {0} from {1} to {2}".format(p, old.get(p, 0), n)
                for p, n in sorted(actual.items()) if n > old.get(p, 0)]
    if refusals:
        return refusals
    rendered = render(actual)
    if path.read_text(encoding="utf-8") != rendered:
        path.write_text(rendered, encoding="utf-8")
        print("web module boundary ratchet: lowered the baseline to {0} file(s)".format(len(actual)))
    else:
        print("web module boundary ratchet: baseline already matches ({0} file(s))".format(len(actual)))
    return []


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", default=str(ROOT), help="repository root (default: this checkout)")
    parser.add_argument("--update", action="store_true", help="lower the baseline; refuses to raise or to add a file")
    args = parser.parse_args(argv)
    try:
        notes = []
        problems = update(args.root) if args.update else check(args.root, notes=notes)
    except BoundaryError as error:
        print(error, file=sys.stderr)
        return 2
    if problems:
        print("\n".join(problems), file=sys.stderr)
        return 1
    if not args.update:
        ratchet_base.print_notice("web module boundary ratchet", notes, "python3 scripts/ci/web_module_boundary_ratchet.py --update")
        total = sum(parse_baseline((Path(args.root) / BASELINE_REL).read_text(encoding="utf-8"), BASELINE_REL).values())
        print("web module boundary ratchet: {0} disable(s) in the baseline, none above it".format(total))
    return 0


if __name__ == "__main__":
    sys.exit(main())
