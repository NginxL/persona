#!/usr/bin/env python3
"""Fail when a web source file grows its hard-coded pixel font sizes.

Counts, per file under web/src (tests excluded), the arbitrary Tailwind sizes (`text-[13px]`,
`text-[length:13px]`), inline `fontSize: 13` / `fontSize: "13px"` and CSS `font-size: 13px`.
The committed counts (scripts/ci/web_font_baseline.json) may stay or go down, never up, and a
file with no entry starts at zero, so a new file cannot add one. Sizes come from the named
roles in docs/design/task-cards.md section 4.1 (`text-xs`, `text-sm`, the `--chat-*` variables).

Only a rise fails. Where a base is given (--base, else CI_BASE_SHA; scripts/ci/ratchet_base.py), a
file the change touches is also held to its count at the merge base, so a change cannot add sizes
back into room a lower count left under the baseline; an entry the merge base's baseline does not
have yet (a moved file --update recorded) is held to the baseline. With neither, as on main, only
the baseline holds.
A file under its baseline passes with a notice: lowering the baseline is tidying anyone can do, with
--update here or scripts/ci/lower_baselines.py for every count ratchet (docs/operations/testing.md,
"Count ratchets"). --update rewrites the baseline downward and drops files that no longer have any.
It refuses to raise a count, lets a file with no entry take only the sizes that left other files in
the same change (a split or a move), and will not create a baseline (that would record what is
already in the tree).
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
BASELINE_REL = "scripts/ci/web_font_baseline.json"
SUFFIXES = (".ts", ".tsx", ".css")
PATTERNS = (
    re.compile(r"text-\[(?:length:)?\d+(?:\.\d+)?px\]"),
    re.compile(r"\bfontSize\s*:\s*(?:\d+(?:\.\d+)?|[\"']\d+(?:\.\d+)?px[\"'])"),
    re.compile(r"(?<![\w-])font-size\s*:\s*\d+(?:\.\d+)?px"),
)


class FontError(Exception):
    """The check could not be run (broken baseline)."""


def is_test(name):
    return ".test." in name or ".spec." in name or name.endswith(".d.ts")


def count_sizes(text):
    """How many hard-coded pixel font sizes text has."""
    return sum(len(pattern.findall(text)) for pattern in PATTERNS)


def scan(root):
    """Positive counts keyed by repo-relative path. Files with none are absent."""
    root = Path(root)
    counts = {}
    base = root / SOURCE
    if not base.is_dir():
        return counts
    for path in sorted(base.rglob("*")):
        if not path.is_file() or path.suffix not in SUFFIXES or is_test(path.name):
            continue
        if "node_modules" in path.parts:
            continue
        found = count_sizes(path.read_text(encoding="utf-8", errors="replace"))
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
        raise FontError("web font ratchet: {0} is not valid JSON ({1})".format(label, error.msg))
    if not isinstance(data, dict):
        raise FontError("web font ratchet: {0} must be a JSON object of file to count".format(label))
    counts = {}
    for key, value in data.items():
        if not isinstance(key, str) or not key.startswith(SOURCE + "/"):
            raise FontError("web font ratchet: {0} has a key outside {1}: {2!r}".format(label, SOURCE, key))
        if isinstance(value, bool) or not isinstance(value, int) or value < 0:
            raise FontError("web font ratchet: {0} count for {1} must be a non-negative integer".format(label, key))
        if value > 0:
            counts[key] = value
    return counts


def violations(actual, baseline):
    """Files over the baseline. Missing on either side counts as zero; a file under it passes (lowerable)."""
    found = []
    for path in sorted(actual):
        got = actual[path]
        allowed = baseline.get(path, 0)
        if got > allowed:
            found.append(
                "{0}: {1} hard-coded px font size(s), baseline {2}; use a named size "
                "(text-xs, text-sm, a --chat-* variable; docs/design/task-cards.md 4.1). "
                "If the sizes moved here from another file, run --update: a new file may take what other files lost".format(path, got, allowed))
    return found


def lowerable(actual, baseline):
    """Notice lines for files under the baseline, which lowering it would tidy."""
    return ["{0}: {1} hard-coded px font size(s), baseline {2}".format(path, actual.get(path, 0), allowed)
            for path, allowed in sorted(baseline.items()) if actual.get(path, 0) < allowed]


def covers(path):
    """Whether scan counts the file: a source under web/src, tests and node_modules excluded."""
    parts = path.split("/")
    return (path.startswith(SOURCE + "/") and os.path.splitext(path)[1] in SUFFIXES and not is_test(parts[-1])
            and "node_modules" not in parts)


def base_violations(actual, baseline, merge_base):
    """Files the change took above their merge-base count while the baseline still has room (ratchet_base.held)."""
    if merge_base is None:
        return []
    paths = sorted(path for path in merge_base.changed()
                   if covers(path) and 0 < actual.get(path, 0) <= baseline.get(path, 0))
    if not paths:
        return []  # nothing the change touches has room left to rise into
    text = merge_base.texts([BASELINE_REL]).get(BASELINE_REL)
    if text is None:
        return []  # the base predates the baseline
    entries = parse_baseline(text, merge_base.label + ":" + BASELINE_REL)
    texts = merge_base.texts(paths)
    found = []
    for path in paths:
        was = count_sizes(texts[path]) if path in texts else 0
        if actual[path] > ratchet_base.held(path, baseline[path], entries, was):
            found.append(
                "{0}: {1} hard-coded px font size(s), {2} at the merge base {3} (baseline {4}); a count may not rise, "
                "even where the baseline still has room; use a named size (text-xs, text-sm, a --chat-* variable; "
                "docs/design/task-cards.md 4.1)".format(path, actual[path], was, merge_base.label, baseline[path]))
    return found


def plan_update(old, actual):
    """The lowered baseline, or (None, refusals) when a count would rise or a file would take more than moved to it.

    A file with no entry may be given sizes that left other files in the same change (a component split out of, or
    moved from, a file): what a file lost, by a lowered count or by being gone, is a pool the new files draw on, so the
    total still never goes up."""
    pool = sum(max(0, allowed - actual.get(path, 0)) for path, allowed in old.items())
    refusals = []
    for path in sorted(actual):
        got = actual[path]
        allowed = old.get(path, 0)
        if got <= allowed:
            continue
        if allowed:
            refusals.append("web font ratchet: refusing to raise {0} from {1} to {2}".format(path, allowed, got))
        elif got <= pool:
            pool -= got
        else:
            refusals.append(
                "web font ratchet: refusing to add {0} with {1} px font size(s); only {2} left other files in this change "
                "(a moved file's sizes may follow it, new ones may not)".format(path, got, pool))
    if refusals:
        return None, refusals
    return {path: count for path, count in actual.items() if count > 0}, []


def check(root, base=None, env=None, notes=None):
    """Failure lines: files over the baseline, or above their merge-base count. notes receives the lowerable ones."""
    path = Path(root) / BASELINE_REL
    if not path.is_file():
        return ["web font ratchet: baseline missing ({0}); refusing to invent counts for existing sizes".format(BASELINE_REL)]
    actual, baseline = scan(root), parse_baseline(path.read_text(encoding="utf-8"), BASELINE_REL)
    if notes is not None:
        notes.extend(lowerable(actual, baseline))
    try:
        merge_base = ratchet_base.resolve(root, base, env)
        return violations(actual, baseline) + base_violations(actual, baseline, merge_base)
    except ratchet_base.BaseError as error:
        raise FontError("web font ratchet: " + str(error))


def update(root):
    path = Path(root) / BASELINE_REL
    if not path.is_file():
        raise FontError("web font ratchet: no baseline at {0}; refusing to create one".format(BASELINE_REL))
    planned, refusals = plan_update(parse_baseline(path.read_text(encoding="utf-8"), BASELINE_REL), scan(root))
    if refusals:
        return refusals
    rendered = render(planned)
    if path.read_text(encoding="utf-8") != rendered:
        path.write_text(rendered, encoding="utf-8")
        print("web font ratchet: lowered the baseline to {0} file(s)".format(len(planned)))
    else:
        print("web font ratchet: baseline already matches ({0} file(s))".format(len(planned)))
    return []


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", default=str(ROOT), help="repository root (default: this checkout)")
    parser.add_argument("--base", help="base ref for the merge-base limit (default: CI_BASE_SHA; with neither, "
                                       "only the baseline holds)")
    parser.add_argument("--update", action="store_true", help="lower the baseline; refuses to raise or to add a file")
    parser.add_argument("--init", action="store_true", help="write the first baseline from the tree (only when none exists)")
    args = parser.parse_args(argv)
    try:
        if args.init:
            path = Path(args.root) / BASELINE_REL
            if path.exists():
                raise FontError("web font ratchet: {0} exists; use --update".format(BASELINE_REL))
            path.write_text(render(scan(args.root)), encoding="utf-8")
            print("web font ratchet: wrote the first baseline")
            return 0
        notes = []
        problems = update(args.root) if args.update else check(args.root, args.base, notes=notes)
    except FontError as error:
        print(error, file=sys.stderr)
        return 2
    if problems:
        print("\n".join(problems), file=sys.stderr)
        return 1
    if not args.update:
        ratchet_base.print_notice("web font ratchet", notes, "python3 scripts/ci/web_font_ratchet.py --update")
        total = sum(parse_baseline((Path(args.root) / BASELINE_REL).read_text(encoding="utf-8"), BASELINE_REL).values())
        print("web font ratchet: {0} hard-coded px font size(s) in the baseline, none above it".format(total))
    return 0


if __name__ == "__main__":
    sys.exit(main())
