#!/usr/bin/env python3
"""Keep the inventory of CJK prompts without language handling from growing.

This is a structural migration guard, not a translation-quality check. A template
handles language when an if or else-if action tests .Language/.Lang, directly or through a
template included with the same data. Existing omissions are recorded by filename
and directory count; replacing an old omission with a new file is not allowed.
There are no source exemptions. --update only removes resolved entries.

Only a new omission fails. A resolved prompt still in the baseline passes with a notice: lowering
the baseline is tidying anyone can do, with --update here or scripts/ci/lower_baselines.py for
every count ratchet (docs/operations/testing.md, "Count ratchets"). With a base given (--base, else
CI_BASE_SHA), a prompt the baseline still lists but the merge base already handles may not lose its
language handling again (scripts/ci/ratchet_base.py). With neither, as on main, the baseline alone
holds the prompts; the check that the baseline itself was not raised still reads origin/main.
"""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
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
SOURCE = "server/internal/prompts"
BASELINE = "scripts/ci/prompt_language_baseline.json"
CJK = re.compile(r"[\u3040-\u30ff\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff]")
COMMENT = re.compile(r"{{-?\s*/\*.*?\*/\s*-?}}", re.S)
ACTION = re.compile(r"{{-?\s*(.*?)\s*-?}}", re.S)
QUOTED = re.compile(r'"(?:\\.|[^"\\])*"|`[^`]*`', re.S)
LANGUAGE = re.compile(r"^(?:else\s+)?if\s+.*\.(?:Language|Lang)\b", re.S)
INCLUDE = re.compile(r'^template\s+"([^"]+)"\s+\.$')


def scan(root):
    return scan_texts({p.relative_to(root).as_posix(): p.read_text(encoding="utf-8") for p in root.rglob("*.md")})


def scan_texts(texts):
    """The omissions among templates given as {name relative to SOURCE: text}, by directory."""
    direct, includes = set(), {}
    for name, text in texts.items():
        actions = ACTION.findall(COMMENT.sub("", text))
        if any(LANGUAGE.match(QUOTED.sub('""', a.strip())) for a in actions):
            direct.add(name)
        includes[name] = [m.group(1) + ".md" for a in actions if (m := INCLUDE.fullmatch(a.strip()))]
    localized = set(direct)
    while True:
        expanded = localized | {name for name, refs in includes.items() if any(ref in localized for ref in refs)}
        if expanded == localized:
            break
        localized = expanded
    counts = {}
    for name, text in sorted(texts.items()):
        if CJK.search(text) and name not in localized:
            counts.setdefault(str(Path(name).parent), []).append(name)
    return counts


def decode_baseline(data):
    if not isinstance(data, dict):
        raise ValueError("baseline must be a directory map")
    result = {}
    for directory, entry in data.items():
        if not isinstance(directory, str) or not isinstance(entry, dict) or set(entry) != {"count", "files"}:
            raise ValueError("invalid baseline entry")
        files = entry["files"]
        if not isinstance(files, list) or any(not isinstance(f, str) for f in files):
            raise ValueError("invalid baseline files")
        if type(entry["count"]) is not int or entry["count"] != len(files) or files != sorted(set(files)):
            raise ValueError("baseline count or ordering does not match files")
        if any(Path(f).is_absolute() or ".." in Path(f).parts or str(Path(f).parent) != directory or not f.endswith(".md") for f in files):
            raise ValueError("baseline path is outside its directory")
        if files:
            result[directory] = files
    return result


def encoded(counts):
    return {directory: {"count": len(files), "files": files} for directory, files in sorted(counts.items())}


def increases(actual, baseline):
    return sorted(f for directory, files in actual.items() for f in files if f not in baseline.get(directory, []))


def lowered(actual, baseline):
    added = increases(actual, baseline)
    if added:
        raise ValueError("new unlocalized prompts: " + ", ".join(added))
    return actual


def _git(*args):
    try:
        return subprocess.run(["git", *args], cwd=ROOT, capture_output=True, text=True, timeout=60)
    except subprocess.TimeoutExpired:
        raise ValueError("git " + " ".join(args[:3]) + " timed out")


def base_text(base, path):
    """The file's text at base, or None when base has no such file.

    Whether the file is there comes from the tree, not from reading it: CI's guards checkout is blobless (#12753), so
    the base's copy of a changed baseline is fetched on first read, and a failed fetch must not count as absence.
    """
    listed = _git("ls-tree", "-z", "--full-tree", base, "--", path)
    if listed.returncode:
        raise ValueError("cannot read the tree of ratchet base " + base)
    if not any(entry.partition("\t")[2] == path for entry in listed.stdout.split("\0")):
        return None
    shown = _git("show", base + ":" + path)
    if shown.returncode:
        raise ValueError("cannot read {0}:{1}: {2}".format(base, path, shown.stderr.strip()))
    return shown.stdout


def resolved(actual, baseline):
    """Files the baseline lists that handle language now (or are gone): lowering it would drop them."""
    return sorted(f for directory, files in baseline.items() for f in files if f not in actual.get(directory, []))


def regained(actual, baseline, merge_base):
    """Listed omissions the merge base had already resolved, which the change brings back."""
    if merge_base is None or not any(path.startswith(SOURCE + "/") for path in merge_base.changed()):
        return []  # with no prompt touched since the merge base, it has the same omissions
    names = [name for name in merge_base.files(SOURCE) if name.endswith(".md")]
    texts = merge_base.texts(names, errors="strict")
    before = scan_texts({name[len(SOURCE) + 1:]: text for name, text in texts.items()})
    return sorted(f for directory, files in actual.items() for f in files
                  if f in baseline.get(directory, []) and f not in before.get(directory, []))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--update", action="store_true")
    parser.add_argument("--base", help="base ref (default: CI_BASE_SHA). The merge-base limit needs one; the "
                                       "check that the baseline was not raised falls back to origin/main")
    args = parser.parse_args()
    path = ROOT / BASELINE
    # Only a given base holds a changed prompt to the merge base: origin/main is as fresh as the last fetch, and
    # main verification leaves it behind HEAD (scripts/ci/ratchet_base.py).
    prior_ref = args.base if args.base is not None else os.environ.get("CI_BASE_SHA", "origin/main")
    try:
        baseline = decode_baseline(json.loads(path.read_text(encoding="utf-8")))
        actual = scan(ROOT / SOURCE)
        # Once the baseline exists upstream, editing its JSON cannot raise it.
        if _git("cat-file", "-e", prior_ref).returncode:
            raise ValueError("cannot read ratchet base " + prior_ref)
        prior = base_text(prior_ref, BASELINE)
        if prior is not None:
            lowered(baseline, decode_baseline(json.loads(prior)))
        lowered(actual, baseline)
        back = regained(actual, baseline, ratchet_base.resolve(ROOT, args.base))
        if back:
            raise ValueError("prompts the merge base already localizes lost it again: " + ", ".join(back)
                             + "; the baseline still lists them, but a count may not rise into that room")
        if args.update:
            path.write_text(json.dumps(encoded(actual), ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        elif actual != baseline:
            gone = resolved(actual, baseline)
            print("prompt language ratchet: {0} resolved prompt(s) still in the baseline; lowering it is tidying "
                  "anyone can do, not this change's job: python3 scripts/ci/lower_baselines.py, or python3 "
                  "scripts/ci/prompt_language_ratchet.py --update: {1}".format(len(gone), ", ".join(gone)))
    except (OSError, ValueError, UnicodeDecodeError, ratchet_base.BaseError) as error:
        print("FAIL prompt language ratchet:", error)
        return 1
    print("PASS prompt language ratchet:", sum(map(len, actual.values())), "remaining templates in", len(actual), "directories")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
