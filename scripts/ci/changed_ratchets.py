#!/usr/bin/env python3
"""What main's per-file ratchets will say about the files a change touches once it merges (#7977).

scripts/ci/guards.sh fails when a Go test file has more fixed time.Sleep calls than scripts/ci/sleep_baseline.json
allows (sleep_ratchet.py), or a web/src file more hard-coded px font sizes than scripts/ci/web_font_baseline.json
allows (web_font_ratchet.py). Those scan the whole tree: seconds, and a failure in any file. This reads only the files
the change touches and the baseline entries it edits, from Git objects, as the merge leaves them: the change's version,
merged three ways (git merge-file) with the base's where the base changed the file too. No checkout is needed. Each
ratchet's own counting and messages apply, and only what the merge adds is reported. A count ratchet fails only when a
count rises (docs/operations/testing.md, "Count ratchets"), so a merge is refused only when a file ends further above
its baseline than the base has it: a file under its baseline merges, as the checkers pass it. A file the base already
has above its baseline is the base's failure, unless the change moves it further. A file the two sides change in
conflicting ways is left to the merge itself, which refuses it. Holding a changed file to its count at the base, as
the checkers do with --base, is not done here: a change that raises a count back into the room a fall left under the
baseline merges.

scripts/hooks/merge-pr.sh runs it before merging, with its trusted copy of this script and of the ratchets:

  python3 scripts/ci/changed_ratchets.py --base <base sha> --head <head sha> [--root <repository>]

Exit codes: 0 the merge adds no ratchet failure, 1 it adds one (a line per file), 2 the check could not run.
"""
import argparse
import os
from pathlib import Path
import subprocess
import sys
import tempfile

sys.path.insert(0, str(Path(__file__).resolve().parent))
import sleep_ratchet  # noqa: E402
import web_font_ratchet  # noqa: E402

GIT_TIMEOUT = 120  # seconds for one git command; a large change lists and reads its files in a few
LS_TREE_CHUNK = 500  # paths per git ls-tree call, well under any command-line limit
CONFLICT = object()  # a file the two sides change in ways a merge cannot combine


class CheckError(Exception):
    """The check could not be run: git failed, or a baseline is malformed."""


class Ratchet:
    """One per-file ratchet: which files it counts, how, its baseline, and its own messages."""

    def __init__(self, name, baseline, covers, count, parse, violations, errors, decode, raised=None):
        self.name = name
        self.baseline = baseline
        self.covers = covers
        self.count = count
        self.parse = parse
        self.violations = violations
        self.errors = errors
        self.decode = decode
        self.raised = raised


def _sleep_covers(path):
    """The files sleep_ratchet.scan counts: _test.go files under its roots, outside .git directories."""
    parts = path.split("/")
    return (any(path.startswith(root + "/") for root in sleep_ratchet.ROOTS) and parts[-1].endswith("_test.go")
            and ".git" not in parts[:-1])


def _font_covers(path):
    """The files web_font_ratchet.scan counts: sources under web/src, tests and node_modules excluded."""
    parts = path.split("/")
    return (path.startswith(web_font_ratchet.SOURCE + "/") and os.path.splitext(path)[1] in web_font_ratchet.SUFFIXES
            and not web_font_ratchet.is_test(parts[-1]) and "node_modules" not in parts)


RATCHETS = (
    Ratchet("fixed sleeps", sleep_ratchet.BASELINE_REL, _sleep_covers, sleep_ratchet.count_sleeps,
            sleep_ratchet.parse_baseline, sleep_ratchet.count_violations, sleep_ratchet.SleepError,
            lambda data: data.decode("utf-8", "backslashreplace"), raised=sleep_ratchet.raised_against),
    # The font ratchet has no never-raise check against an earlier commit, so none is applied here either.
    Ratchet("web font sizes", web_font_ratchet.BASELINE_REL, _font_covers, web_font_ratchet.count_sizes,
            web_font_ratchet.parse_baseline, web_font_ratchet.violations, web_font_ratchet.FontError,
            lambda data: data.decode("utf-8", "replace")),
)


class Repository:
    """Read-only Git access: changed paths, blobs and three-way merges of single files."""

    def __init__(self, root):
        self.root = str(root)
        self.cache = {}

    def run(self, args, data=None):
        try:
            result = subprocess.run(["git", "-C", self.root] + args, input=data, capture_output=True,
                                    timeout=GIT_TIMEOUT)
        except subprocess.TimeoutExpired:
            raise CheckError("changed ratchets: git {0} timed out".format(" ".join(args[:3])))
        if result.returncode:
            detail = result.stderr.decode("utf-8", "replace").strip()
            raise CheckError("changed ratchets: git {0} failed{1}".format(" ".join(args[:3]),
                                                                           ": " + detail if detail else ""))
        return result.stdout

    def commit(self, rev):
        return self.run(["rev-parse", "--verify", "--quiet", "--end-of-options", rev + "^{commit}"]).decode().strip()

    def merge_base(self, base, head):
        return self.run(["merge-base", base, head]).decode().strip()

    def changed(self, old, new):
        """Paths that differ between two commits, each side of a rename on its own."""
        out = self.run(["diff", "--name-only", "--no-renames", "-z", old, new, "--"])
        return [name for name in out.decode("utf-8", "surrogateescape").split("\0") if name]

    def renames(self, old, new):
        """{new path: old path} for the files git's rename detection sees as moved between two commits."""
        out = self.run(["diff", "--name-status", "-M", "-z", old, new, "--"])
        fields = out.decode("utf-8", "surrogateescape").split("\0")
        moved, at = {}, 0
        while at < len(fields) and fields[at]:
            if fields[at].startswith(("R", "C")):
                if fields[at].startswith("R"):
                    moved[fields[at + 2]] = fields[at + 1]
                at += 3
            else:
                at += 2
        return moved

    def blobs(self, rev, paths):
        """{path: bytes} for the paths that are files at rev; a path that is absent or not a file is missing."""
        wanted = [path for path in paths if (rev, path) not in self.cache]
        oids = {}
        for start in range(0, len(wanted), LS_TREE_CHUNK):
            chunk = wanted[start:start + LS_TREE_CHUNK]
            out = self.run(["ls-tree", "-r", "-z", "--full-tree", rev, "--"] + chunk)
            for record in out.decode("utf-8", "surrogateescape").split("\0"):
                if not record:
                    continue
                meta, path = record.split("\t", 1)
                _, kind, oid = meta.split(" ")
                if kind == "blob":
                    oids[path] = oid
        if oids:
            contents = self._cat(list(oids.values()))
            for path, oid in oids.items():
                self.cache[(rev, path)] = contents[oid]
        for path in wanted:
            self.cache.setdefault((rev, path), None)
        return {path: self.cache[(rev, path)] for path in paths if self.cache[(rev, path)] is not None}

    def _cat(self, oids):
        """{oid: bytes} through one git cat-file --batch."""
        unique = list(dict.fromkeys(oids))
        out = self.run(["cat-file", "--batch"], data="".join(oid + "\n" for oid in unique).encode())
        contents, at = {}, 0
        for oid in unique:
            end = out.index(b"\n", at)
            header = out[at:end].split()
            if len(header) != 3 or header[1] != b"blob":
                raise CheckError("changed ratchets: git cat-file could not read blob {0}".format(oid))
            size = int(header[2])
            contents[oid] = out[end + 1:end + 1 + size]
            at = end + 1 + size + 1  # the content, then the newline cat-file adds after it
        return contents

    def merged(self, path, ancestor, ours, theirs):
        """The file as the merge leaves it: bytes, None when the merge removes it, or CONFLICT."""
        if ours == theirs or ancestor == theirs:
            return ours
        if ancestor == ours:
            return theirs
        if None in (ancestor, ours, theirs):
            return CONFLICT  # added on both sides, or changed on one and removed on the other
        with tempfile.TemporaryDirectory(prefix="changed-ratchets-") as temp:
            names = []
            for label, data in (("ours", ours), ("base", ancestor), ("theirs", theirs)):
                name = os.path.join(temp, label)
                with open(name, "wb") as out:
                    out.write(data)
                names.append(name)
            try:
                result = subprocess.run(["git", "merge-file", "-p", "--quiet"] + names, capture_output=True,
                                        timeout=GIT_TIMEOUT)
            except subprocess.TimeoutExpired:
                raise CheckError("changed ratchets: git merge-file timed out on " + path)
        # The exit status is the number of conflicts; a negative one (shown as 255 or more) is an error.
        if result.returncode == 0:
            return result.stdout
        if 0 < result.returncode < 128:
            return CONFLICT
        raise CheckError("changed ratchets: git merge-file failed on {0}: {1}".format(
            path, result.stderr.decode("utf-8", "replace").strip()))


class Change:
    """A head to merge onto a base, with the paths each side changed since their merge base."""

    def __init__(self, repo, base, head):
        self.repo = repo
        self.base = repo.commit(base)
        self.head = repo.commit(head)
        self.ancestor = repo.merge_base(self.base, self.head)
        self.touched = repo.changed(self.ancestor, self.head)
        self.moved_on = set(repo.changed(self.ancestor, self.base))

    def as_merged(self, paths):
        """{path: bytes, None or CONFLICT} for each path, as the merge of head onto base leaves it."""
        theirs = self.repo.blobs(self.head, paths)
        ours = self.repo.blobs(self.base, paths)
        both = [path for path in paths if path in self.moved_on]
        ancestor = self.repo.blobs(self.ancestor, both)
        result = {}
        for path in paths:
            if path in self.moved_on:
                result[path] = self.repo.merged(path, ancestor.get(path), ours.get(path), theirs.get(path))
            else:
                result[path] = theirs.get(path)
        return result


def worse(got, allowed, base_got, base_allowed):
    """Whether a file ends further above its baseline than the base has it. Under the baseline is never worse."""
    delta, base_delta = got - allowed, base_got - base_allowed
    return delta > max(base_delta, 0)


def ratchet_failures(change, ratchet):
    """(failure lines, notes) for one ratchet on the files the change touches and the entries it edits."""
    rel = ratchet.baseline
    base_text = change.repo.blobs(change.base, [rel]).get(rel)
    if base_text is None:
        return [], []  # the base has no such ratchet
    merged_text = change.as_merged([rel])[rel]
    if merged_text is CONFLICT:
        return [], ["{0}: conflicts with the base; {1} not checked".format(rel, ratchet.name)]
    if merged_text is None:
        return ["{0}: the merge removes the {1} baseline".format(rel, ratchet.name)], []
    try:
        base_counts = ratchet.parse(ratchet.decode(base_text), change.base[:12] + ":" + rel)
        merged_counts = ratchet.parse(ratchet.decode(merged_text), "the merge's " + rel)
        texts = change.repo.blobs(change.head, [rel])
        head_counts = ratchet.parse(ratchet.decode(texts[rel]), change.head[:12] + ":" + rel) if rel in texts else {}
        texts = change.repo.blobs(change.ancestor, [rel])
        ancestor_counts = (ratchet.parse(ratchet.decode(texts[rel]), change.ancestor[:12] + ":" + rel)
                           if rel in texts else {})
    except ratchet.errors as error:
        raise CheckError(str(error))
    edited = {path for path in set(head_counts) | set(ancestor_counts)
              if head_counts.get(path, 0) != ancestor_counts.get(path, 0)}
    paths = sorted({path for path in change.touched if ratchet.covers(path)} | edited)
    merged = change.as_merged(paths)
    base_files = change.repo.blobs(change.base, paths)
    failures, notes = [], []
    for path in paths:
        text = merged[path]
        if text is CONFLICT:
            notes.append("{0}: conflicts with the base; {1} not checked".format(path, ratchet.name))
            continue
        counted = ratchet.covers(path)
        got = ratchet.count(ratchet.decode(text)) if counted and text is not None else 0
        allowed = merged_counts.get(path, 0)
        base_got = ratchet.count(ratchet.decode(base_files[path])) if counted and path in base_files else 0
        if worse(got, allowed, base_got, base_counts.get(path, 0)):
            failures.extend(ratchet.violations({path: got}, {path: allowed}))
    if ratchet.raised:
        current = {path: merged_counts[path] for path in sorted(edited) if path in merged_counts}
        # A file the change only moved keeps the entry it had (sleep_ratchet.carry_renames): moving is no raise.
        moved = {new: old for new, old in change.repo.renames(change.base, change.head).items()
                 if ratchet.covers(new) and ratchet.covers(old)}
        failures.extend(ratchet.raised(current, sleep_ratchet.carry_renames(base_counts, moved), change.base[:12]))
    return failures, notes


def check(root, base, head):
    """(failure lines, notes, number of files checked) for merging head onto base."""
    change = Change(Repository(root), base, head)
    failures, notes, checked = [], [], set()
    for ratchet in RATCHETS:
        found, skipped = ratchet_failures(change, ratchet)
        failures.extend(found)
        notes.extend(skipped)
        checked.update(path for path in change.touched if ratchet.covers(path))
    return failures, notes, len(checked)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--base", required=True, help="the commit the change merges onto")
    parser.add_argument("--head", required=True, help="the change's head commit")
    parser.add_argument("--root", default=".", help="repository with both commits (default: the working directory)")
    args = parser.parse_args(argv)
    try:
        failures, notes, checked = check(args.root, args.base, args.head)
    except CheckError as error:
        print(error, file=sys.stderr)
        return 2
    for note in notes:
        print("changed ratchets: " + note, file=sys.stderr)
    if failures:
        print("\n".join(failures), file=sys.stderr)
        return 1
    print("changed ratchets: {0} changed file(s) under the fixed-sleep and web font ratchets; the merge adds no "
          "failure".format(checked))
    return 0


if __name__ == "__main__":
    sys.exit(main())
