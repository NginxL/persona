#!/usr/bin/env python3
"""Keep the inventory of CJK prompts without language handling from growing.

This is a structural migration guard, not a translation-quality check. A template
handles language when an if action tests .Language/.Lang, directly or through a
template included with the same data. Existing omissions are recorded by filename
and directory count; replacing an old omission with a new file is not allowed.
There are no source exemptions. --update only removes resolved entries.
"""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess


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
LANGUAGE = re.compile(r"^if\s+.*\.(?:Language|Lang)\b", re.S)
INCLUDE = re.compile(r'^template\s+"([^"]+)"\s+\.$')


def scan(root):
    texts = {p.relative_to(root).as_posix(): p.read_text(encoding="utf-8") for p in root.rglob("*.md")}
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--update", action="store_true")
    parser.add_argument("--base", default=os.environ.get("CI_BASE_SHA", "origin/main"))
    args = parser.parse_args()
    path = ROOT / BASELINE
    try:
        baseline = decode_baseline(json.loads(path.read_text(encoding="utf-8")))
        actual = scan(ROOT / SOURCE)
        # Once the baseline exists upstream, editing its JSON cannot raise it.
        exists = subprocess.run(["git", "cat-file", "-e", args.base], cwd=ROOT, capture_output=True)
        if exists.returncode:
            raise ValueError("cannot read ratchet base " + args.base)
        prior = subprocess.run(["git", "show", args.base + ":" + BASELINE], cwd=ROOT, capture_output=True, text=True)
        if prior.returncode == 0:
            lowered(baseline, decode_baseline(json.loads(prior.stdout)))
        lowered(actual, baseline)
        if args.update:
            path.write_text(json.dumps(encoded(actual), ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        elif actual != baseline:
            raise ValueError("resolved prompts remain in baseline; run --update to lower it")
    except (OSError, ValueError) as error:
        print("FAIL prompt language ratchet:", error)
        return 1
    print("PASS prompt language ratchet:", sum(map(len, actual.values())), "remaining templates in", len(actual), "directories")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
