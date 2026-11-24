#!/usr/bin/env python3
"""Name freezes, and the names open PRs add to them (docs/server/architecture.md "Registering a domain's entries").

A name freeze keeps a list that nearly every change used to edit from growing. The names the list may have are kept in
a file: the ones it had when the freeze landed, then the ones the PRs open that day were adding, each with its PRs. The
list's check fails on any other name and reports a listed one that has gone as a notice. This module holds what the
freezes share: the file's format and its rewrite, and the pending names, read from the open PRs' heads.
scripts/mock/mock_hub_switches.py uses it for mock_hub.py's MOCK_* switches. Run as a script, it does the server's
central lists, whose check is server/internal/archguard/central_lists_test.go:

    python3 scripts/test/name_freezes.py pending            # the names open PRs add to each list (gh, git, go)
    python3 scripts/test/name_freezes.py pending --write    # the lists' files rewritten, the day a freeze lands

`pending` reads each open PR's head, fetched from origin by its commit (objects only: no ref or FETCH_HEAD is written),
and reports the names it adds that its merge base with --base did not have, leaving out the ones --base has. A draft
whose head commit is more than --idle-days old is left out. The archguard test itself reads the names
(-central-list-names), so they are written in the form the freeze compares.

`--write` rewrites each list's file. For a list --base does not have yet, the day its freeze lands, that is its
leading comment, every name --base has, then the pending ones: a listed pending name --base now has joins the first
part (its PR merged), one whose PRs have all closed comes off, one whose PR is still open stays, and the names found
now are added. Once a list is on --base, --write only prunes it: it keeps the listed names --base still has (a
pending one whose PR merged among them) and the pending ones whose PRs are still open, under their header, and drops
the rest. It adds no name. A name --base has that the list lacks (a red main, or a branch given as --base) is
reported as an error, exit status 1, and stays off the list: it is not how a later PR gets a name on a list.
"""
import argparse
from datetime import date, datetime, timedelta, timezone
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]

# An entry: the name, then a pending one's PRs (server/internal/archguard/central_lists_test.go reads the same format).
ENTRY = re.compile(r"(?P<name>\S(?:.*?\S)??)(?:[ \t]+(?P<prs>#\d+(?:[ \t]+#\d+)*))?")

MOVED_SHOWN = 20  # names changes() shows for a part; the rest it counts

PENDING_HEADER = """\
# Pending: the names the PRs open when this freeze landed were adding, each with its PRs
# (python3 scripts/test/name_freezes.py pending --write, {day}; drafts idle more than {idle} days left out).
# A name stays when its PR merges; a rerun of --write drops one whose PRs have all closed. No PR opened later gets a
# line here."""


class Entry:
    """A line of a list's file: the name, its line and, for a pending one, the open PRs that added it (["#4512"])."""

    def __init__(self, line, name, prs):
        self.line, self.name, self.prs = line, name, prs


class Format:
    """A freeze list's file. Comment lines start with #; an entry is one name, and a pending one is followed by its
    PRs (NAME #4512). The names the list had when its freeze landed come first, then the pending ones, and names stay
    sorted and unique within each part.

    what and example name an entry in messages ("a MOCK_* name", "MOCK_X #123"), valid says whether a name is one,
    header is the pending part's comment ({day} and {idle} filled in), and label names the first part in changes()."""

    def __init__(self, what="a name", example="NAME #123", valid=None, header=PENDING_HEADER, label="names"):
        self.what, self.example, self.valid, self.header, self.label = what, example, valid, header, label

    def read(self, text):
        """(entries, problems) of a list's text."""
        entries, problems = [], []
        for number, raw in enumerate(text.splitlines(), 1):
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            match = ENTRY.fullmatch(line)
            if match is None or self.valid is not None and not self.valid(match["name"]):
                problems.append("line %d is neither %s nor a pending one with its PRs (%s): %r"
                                % (number, self.what, self.example, raw))
                continue
            entries.append(Entry(number, match["name"], (match["prs"] or "").split()))
        seen = {}
        for entry in entries:
            if entry.name in seen:
                problems.append("line %d repeats %s (line %d)" % (entry.line, entry.name, seen[entry.name]))
            seen.setdefault(entry.name, entry.line)
        first_pending = next((i for i, e in enumerate(entries) if e.prs), len(entries))
        problems += ["line %d (%s) has no PR after the pending entries: those come last" % (e.line, e.name)
                     for e in entries[first_pending:] if not e.prs]
        for part in (entries[:first_pending], entries[first_pending:]):
            problems += ["line %d (%s) is out of order: it belongs before %s (keep the names, and the pending ones, "
                         "sorted)" % (e.line, e.name, before.name) for before, e in zip(part, part[1:])
                         if e.name < before.name]
        return entries, problems

    def rewrite(self, text, today, found, still_open, day, idle_days):
        """The list's text rewritten the day its freeze lands: its leading comment, every name the base has
        (`today`), then the pending entries under a fresh header. Those are the listed pending names the base does not
        have whose PRs are not all closed, with the PRs still open (`still_open`, a set of numbers), and `found`'s
        ({name: [PR numbers]}). A listed pending name the base has is one of its names now, since its PR merged, so no
        name the base has is dropped. Only for a list the base does not have yet: every name the base has goes in."""
        lines = text.splitlines()
        header = self.header.format(day=day, idle=idle_days).splitlines()
        return self._compose(self._lead(lines), today, self._waiting(text, today, still_open, found), header)

    def prune(self, text, today, still_open):
        """(text, unlisted) for a list the base has already: its text pruned, and the names the base has that it
        lacks. The first part keeps the listed names, of either part, that the base still has, so a pending one whose
        PR merged joins it and one that has left the base comes off. The pending part keeps its header and the listed
        pending names the base does not have yet whose PRs are still open (`still_open`). No name is added: a name
        the base has that the list lacks is one the freeze fails on, and its entry belongs in its domain's seam."""
        lines, entries = text.splitlines(), self.read(text)[0]
        listed = {e.name for e in entries}
        names = [e.line for e in entries if not e.prs]
        pending = [e.line for e in entries if e.prs]
        header = []
        if names and pending:  # what sits between the last name and the first pending entry
            header = lines[names[-1]:pending[0] - 1]
            while header and not header[0].strip():
                header.pop(0)
            while header and not header[-1].strip():
                header.pop()
        return (self._compose(self._lead(lines), today & listed, self._waiting(text, today, still_open), header),
                sorted(today - listed))

    @staticmethod
    def _lead(lines):
        """The comment a list's file starts with, less the blank lines that end it."""
        lead = []
        for line in lines:
            if line.strip() and not line.lstrip().startswith("#"):
                break
            lead.append(line)
        while lead and not lead[-1].strip():
            lead.pop()
        return lead

    def _waiting(self, text, today, still_open, found=None):
        """{name: open PR numbers}: the listed pending names the base does not have whose PRs are not all closed,
        and found's names the base does not have."""
        waiting = {}
        for entry in self.read(text)[0]:
            open_ones = {int(pr[1:]) for pr in entry.prs} & still_open
            if entry.prs and entry.name not in today and open_ones:
                waiting[entry.name] = open_ones
        for name, numbers in (found or {}).items():
            if name not in today:
                waiting.setdefault(name, set()).update(numbers)
        return waiting

    @staticmethod
    def _compose(lead, names, waiting, header):
        """A list's text: the leading comment, the names, then the pending entries, when there are any, after a
        blank line and header (when there is one)."""
        lines = lead + sorted(names)
        if waiting:
            lines += ([""] + header) if header else []
            lines += ["%s %s" % (name, " ".join("#%d" % n for n in sorted(waiting[name]))) for name in sorted(waiting)]
        return "\n".join(lines) + "\n"

    def changes(self, old, new):
        """What a rewrite changed, for the person landing the freeze: one line for each part of the list."""
        def parts(text):
            entries = self.read(text)[0]
            return {e.name for e in entries if not e.prs}, {e.name for e in entries if e.prs}

        (old_names, old_waiting), (new_names, new_waiting) = parts(old), parts(new)

        def line(label, before, after):
            moved = ["+" + name for name in sorted(after - before)] + ["-" + name for name in sorted(before - after)]
            if len(moved) > MOVED_SHOWN:  # a list's first write adds every name
                moved = moved[:MOVED_SHOWN] + ["(%d more)" % (len(moved) - MOVED_SHOWN)]
            return "%s: %d" % (label, len(after)) + "".join(" " + name for name in moved)

        return [line(self.label, old_names, new_names), line("pending", old_waiting, new_waiting)]


# ---- the open PRs' heads ----

QUERY = """
query($owner: String!, $repo: String!, $endCursor: String) {
  repository(owner: $owner, name: $repo) {
    pullRequests(states: OPEN, first: 40, after: $endCursor) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number isDraft headRefOid
        commits(last: 1) { nodes { commit { committedDate } } }
        files(first: 100) { totalCount nodes { path } }
      }
    }
  }
}
"""


def run(args, timeout, tries=1, cwd=ROOT):
    """args' standard output; the last error after `tries` attempts (GitHub drops connections now and then)."""
    for attempt in range(tries):
        try:
            return subprocess.run(args, cwd=cwd, check=True, capture_output=True, text=True, timeout=timeout).stdout
        except (subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
            if attempt == tries - 1:
                raise RuntimeError("%s failed: %s" % (" ".join(args[:3]), getattr(error, "stderr", "") or error))


class PR:
    """An open PR: its number, whether it is a draft, its head commit and that commit's date, and which of the paths
    open_prs() was asked about it may touch (all of them when it has more files than one page lists)."""

    def __init__(self, number, draft, head, committed, touches):
        self.number, self.draft, self.head, self.committed, self.touches = number, draft, head, committed, touches


def open_prs(paths):
    """Every open PR (PR), with the paths of `paths` it may touch."""
    out = run(["gh", "api", "graphql", "--paginate", "-F", "owner={owner}", "-F", "repo={repo}", "-f",
               "query=" + QUERY, "--jq", ".data.repository.pullRequests.nodes[]"], 300, tries=3)
    return [read_pr(json.loads(line), paths) for line in out.splitlines()]


def read_pr(node, paths):
    """A PR from one of QUERY's nodes."""
    files = node["files"]
    if files["totalCount"] > len(files["nodes"]):
        touches = set(paths)  # more files than one page lists: read its head to see
    else:
        touches = {f["path"] for f in files["nodes"]} & set(paths)
    committed = datetime.fromisoformat(node["commits"]["nodes"][0]["commit"]["committedDate"].replace("Z", "+00:00"))
    return PR(node["number"], node["isDraft"], node["headRefOid"], committed, touches)


def counted(prs, path, idle_days, now=None, log=sys.stderr):
    """The PRs of `prs` that may touch path, less the drafts whose head commit is more than idle_days old."""
    now = now or datetime.now(timezone.utc)
    out = []
    for pr in prs:
        if path not in pr.touches:
            continue
        if pr.draft and now - pr.committed > timedelta(days=idle_days):
            print("#%d: left out, a draft whose head commit is from %s" % (pr.number, pr.committed.date()), file=log)
        else:
            out.append(pr)
    return out


def fetch(prs):
    """Fetches the PRs' heads from origin, by commit: objects only, no ref and no FETCH_HEAD."""
    heads = sorted({pr.head for pr in prs})
    for start in range(0, len(heads), 20):
        run(["git", "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "origin"] + heads[start:start + 20],
            600, tries=3)


def merge_base(head, base):
    """The commit head and base last shared."""
    return run(["git", "merge-base", head, base], 60).strip()


def show(revision, path):
    """path's text at revision, or None when it has no such file there."""
    try:
        return run(["git", "show", "%s:%s" % (revision, path)], 60)
    except RuntimeError:
        return None


# ---- the server's central lists ----


class CentralList:
    """One row of central_lists_test.go's table: its id, its source file and its list file (from the repository's
    root), and what one name is."""

    def __init__(self, id, source, file, what):
        self.id, self.source, self.file, self.what = id, source, file, what


GO_NAMES = ["go", "test", "-count=1", "-run", "^TestCentralListNames$", "./internal/archguard", "-args"]


def central_names(work, sources=None):
    """(lists, names): central_lists_test.go's table of central lists (CentralList), and the names each reads in the
    given sources ({(list id, key): text}), {(list id, key): names, or None for a file that does not parse}. work is a
    directory to use, empty or missing. The archguard test reads the files there, so the names are the ones the freeze
    compares."""
    os.makedirs(work, exist_ok=True)
    for (list_id, key), text in (sources or {}).items():
        os.makedirs(os.path.join(work, list_id), exist_ok=True)
        Path(work, list_id, key + ".go").write_text(text)
    run(GO_NAMES + ["-central-list-names=" + os.path.abspath(work)], 900, cwd=ROOT / "server")
    lists = [CentralList(*line.split("\t")) for line in Path(work, "lists.tsv").read_text().splitlines()]
    names = {}
    for list_id, key in sources or {}:
        path = Path(work, list_id, key + ".names")
        names[(list_id, key)] = set(path.read_text().splitlines()) if path.exists() else None
    return lists, names


CENTRAL = Format()


def central_pending(base, lists, prs, idle_days, work, log=sys.stderr):
    """(today, found) for the central lists: the names each has at base ({id: names}), and the names the counted PRs
    of prs (open_prs()) add to it ({id: {name: [PR numbers]}})."""
    per_list = {l.id: counted(prs, l.source, idle_days, log=log) for l in lists}
    fetch([pr for each in per_list.values() for pr in each])
    sources, bases = {}, {}
    for l in lists:
        text = show(base, l.source)
        if text is not None:
            sources[(l.id, "base")] = text
        for pr in per_list[l.id]:
            if pr.head not in bases:
                bases[pr.head] = merge_base(pr.head, base)
            for revision in (pr.head, bases[pr.head]):
                text = show(revision, l.source)
                if text is not None:
                    sources[(l.id, revision)] = text
    _, names = central_names(work, sources)
    today, found = {}, {}
    for l in lists:
        today[l.id] = names.get((l.id, "base")) or set()
        found[l.id] = {}
        for pr in per_list[l.id]:
            added = (names.get((l.id, pr.head)) or set()) - (names.get((l.id, bases[pr.head])) or set()) - today[l.id]
            for name in added:
                found[l.id].setdefault(name, []).append(pr.number)
            print("%s #%d: %s" % (l.id, pr.number, " ".join(sorted(added)) or "no new name"), file=log)
    return today, {i: {n: sorted(p) for n, p in sorted(f.items())} for i, f in found.items()}


def on_base(base, path):
    return subprocess.run(["git", "cat-file", "-e", "%s:%s" % (base, path)], cwd=ROOT,
                          capture_output=True).returncode == 0


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    sub = parser.add_subparsers(dest="command", required=True)
    cmd = sub.add_parser("pending", help="the names open PRs add to the server's central lists")
    cmd.add_argument("--base", default="origin/main", help="what the names are counted against (default origin/main)")
    cmd.add_argument("--idle-days", type=int, default=14, help="a draft whose head commit is older is left out")
    cmd.add_argument("--write", action="store_true", help="rewrite the lists' files (a list --base has: pruned only)")
    args = parser.parse_args(argv)
    base = run(["git", "rev-parse", "--verify", args.base + "^{commit}"], 60).strip()
    with tempfile.TemporaryDirectory(prefix="name-freezes-") as work:
        lists, _ = central_names(os.path.join(work, "table"))
        prs = open_prs([l.source for l in lists])
        today, found = central_pending(base, lists, prs, args.idle_days, os.path.join(work, "names"))
    for l in lists:
        print("== %s (%s): %d names at %s, %d pending from %d PRs" % (
            l.id, l.source, len(today[l.id]), args.base, len(found[l.id]),
            len({n for numbers in found[l.id].values() for n in numbers})))
        for name, numbers in found[l.id].items():
            print("%s %s" % (name, " ".join("#%d" % n for n in numbers)))
    if not args.write:
        return 0
    failed, still_open = False, {pr.number for pr in prs}
    for l in lists:
        path = ROOT / l.file
        text = path.read_text() if path.exists() else (
            "# The names %s may have (each %s), frozen: server/internal/archguard/central_lists_test.go\n"
            "# fails on any other (docs/server/architecture.md \"Registering a domain's entries\"). Delete a line\n"
            "# once the test's notice names it; add none.\n" % (l.source, l.what))
        problems = CENTRAL.read(text)[1]
        if problems:
            print("fix %s first:\n%s" % (l.file, "\n".join(problems)), file=sys.stderr)
            return 1
        if on_base(base, l.file):
            new, unlisted = CENTRAL.prune(text, today[l.id], still_open)
            note = " (on %s already: pruned, nothing added)" % args.base
        else:
            new, unlisted = CENTRAL.rewrite(text, today[l.id], found[l.id], still_open, date.today().isoformat(),
                                            args.idle_days), []
            note = ""
        path.write_text(new)
        for line in CENTRAL.changes(text, new):
            print("%s, %s%s" % (l.file, line, note), file=sys.stderr)
        for name in unlisted:
            failed = True
            print("%s: %s has %s, which the list does not: --write adds no name to a list that has landed, so the "
                  "freeze still fails on it. Move its entry to its domain's own file (docs/server/architecture.md "
                  "\"Registering a domain's entries\")." % (l.file, args.base, name), file=sys.stderr)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
