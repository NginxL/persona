#!/usr/bin/env python3
"""The MOCK_* switches scripts/mock/mock_hub.py reads, frozen (#12737).

mock_hub.py is the file nearly every mock change edits, and most of what it has gained came with a new MOCK_* switch,
so it takes no new one. The names it may read are the ones in mock_hub_switches.txt beside this file; test_mock_hub.py
fails on any other, and on an environment read whose name it cannot see. A new switch, or a new value on an existing
one, goes into a domain module on the registry, scripts/mock/mock_domains.py (docs/operations/testing.md "Mock hub
domains"). A switch that moves into a domain module comes off the list; until then the test prints a notice.

    python3 scripts/mock/mock_hub_switches.py pending            # the names open PRs add to mock_hub.py (gh, git)
    python3 scripts/mock/mock_hub_switches.py pending --write    # the whole list rewritten, the day the freeze lands

`pending` reads each open PR's head, fetched from origin by its commit (objects only: no ref or FETCH_HEAD is written),
and reports the names its mock_hub.py reads that its merge base with --base did not, leaving out the ones --base reads.
A draft whose head commit is more than --idle-days old is left out.

`--write` rewrites both parts of the list on the day the freeze lands, after origin/main is merged in: the names --base
reads, then the pending ones. A listed pending name --base now reads joins the first part (its PR merged), one whose PRs
have all closed comes off, one whose PR is still open stays, and the names found now are added. Once the list is on
--base it writes nothing: it is not how a later PR gets a name on the list.

The list's format, its rewrite and the open PRs' heads are shared with the server's name freezes, in
scripts/test/name_freezes.py.
"""
import argparse
import ast
from datetime import date
from pathlib import Path
import re
import subprocess
import sys

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.path.insert(0, str(ROOT / "scripts" / "test"))
import name_freezes  # noqa: E402
HUB = HERE / "mock_hub.py"
LIST = HERE / "mock_hub_switches.txt"
HUB_PATH = "scripts/mock/mock_hub.py"  # in the repository, for git show
LIST_PATH = "scripts/mock/mock_hub_switches.txt"

NAME = re.compile(r"MOCK_[A-Z0-9_]+")
KEYED = {"get", "setdefault", "pop"}  # os.environ.<method>(name, ...)
COPIES = {"copy", "items"}  # the whole environment, passed on to a child process
HINT = ('mock_hub.py takes no new MOCK_* switch (scripts/mock/mock_hub_switches.txt). Put the switch, and what it '
        'serves, in a domain module: register(d) and one line in DOMAINS in scripts/mock/mock_domains.py '
        '(docs/operations/testing.md "Mock hub domains"). A new value on an existing switch is a new switch too: '
        'register the variant in a domain module whose route answers first and returns False otherwise.')


class Reads:
    """What a mock_hub.py source reads from the environment: names ({MOCK_* name: its first line}) and unseen
    ([(line, code)]: a read whose name is not a string literal, or a use of os.environ this cannot follow)."""

    def __init__(self, names, unseen):
        self.names, self.unseen = names, unseen


def reads(source):
    """The MOCK_* names `source` reads through os.environ.get / setdefault / pop, os.environ[...], os.getenv or a
    membership test on os.environ, and any other string that is a whole MOCK_* name (a copy of the environment read
    later, a helper given the name). An `import os as …` or `from os import environ, getenv` is followed."""
    tree = ast.parse(source)
    modules, environs, getenvs = {"os"}, set(), set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            modules |= {alias.asname or alias.name for alias in node.names if alias.name == "os"}
        elif isinstance(node, ast.ImportFrom) and node.module == "os":
            environs |= {alias.asname or alias.name for alias in node.names if alias.name == "environ"}
            getenvs |= {alias.asname or alias.name for alias in node.names if alias.name == "getenv"}

    def named(node, attr, aliases):
        """node is os.environ / os.getenv (attr), or a name it was imported as."""
        if isinstance(node, ast.Attribute):
            return node.attr == attr and isinstance(node.value, ast.Name) and node.value.id in modules
        return isinstance(node, ast.Name) and node.id in aliases

    names, unseen = {}, []

    def code(node):
        text = ast.unparse(node)
        return text if len(text) <= 160 else text[:157] + "..."

    def found(node):
        names[node.value] = min(names.get(node.value, node.lineno), node.lineno)

    def key(node, use):
        """The name a read takes, recorded when it is a literal MOCK_* one; unseen when it is not a literal."""
        if not (isinstance(node, ast.Constant) and isinstance(node.value, str)):
            unseen.append((use.lineno, code(use)))
        elif node.value.startswith("MOCK_"):
            found(node)

    # One pass that carries each node's parent: mock_hub.py has about 340,000 nodes, and a map of every node's parent
    # took several times as long as this.
    stack = [(tree, None)]
    while stack:
        node, parent = stack.pop()
        stack.extend((child, node) for child in ast.iter_child_nodes(node))
        if isinstance(node, ast.Constant):
            if isinstance(node.value, str) and NAME.fullmatch(node.value):
                found(node)
        elif isinstance(node, ast.Attribute) and named(node.value, "environ", environs):
            # os.environ.<attr>: a keyed read, a copy of the whole environment, or something this cannot follow.
            called = isinstance(parent, ast.Call) and parent.func is node
            if called and node.attr in KEYED:
                key(parent.args[0] if parent.args else None, parent)
            elif not (called and node.attr in COPIES and not parent.args) and node.attr != "update":
                unseen.append((node.lineno, code(parent if called else node)))
        elif named(node, "environ", environs):
            if isinstance(parent, ast.Attribute) and parent.value is node:
                continue  # os.environ.<attr>, above
            if isinstance(parent, ast.Subscript) and parent.value is node:
                key(parent.slice, parent)
            elif isinstance(parent, ast.Compare) and any(c is node for c in parent.comparators):
                at = next(i for i, c in enumerate(parent.comparators) if c is node)
                if isinstance(parent.ops[at], (ast.In, ast.NotIn)):
                    key(parent.left if at == 0 else parent.comparators[at - 1], parent)
                else:
                    unseen.append((node.lineno, code(parent)))
            elif not (isinstance(parent, ast.Call) and isinstance(parent.func, ast.Name) and parent.func.id == "dict"
                      and len(parent.args) == 1 and parent.args[0] is node and not parent.keywords
                      or isinstance(parent, ast.keyword) and parent.arg == "env"
                      or isinstance(parent, ast.Dict) and any(v is node and k is None
                                                              for k, v in zip(parent.keys, parent.values))):
                # Not a copy for a child process (dict(os.environ), env=os.environ, {**os.environ}).
                unseen.append((node.lineno, code(parent)))
        elif named(node, "getenv", getenvs):
            if isinstance(parent, ast.Call) and parent.func is node:
                key(parent.args[0] if parent.args else None, parent)
            else:
                unseen.append((node.lineno, code(parent)))
    return Reads(names, sorted(set(unseen)))


PENDING_HEADER = """\
# Pending: the names the PRs open when the freeze landed were adding to mock_hub.py, each with its PRs
# (python3 scripts/mock/mock_hub_switches.py pending --write, {day}; drafts idle more than {idle} days left
# out). A name stays when its PR merges; delete it once its PRs have all closed unmerged. No PR opened later gets a
# line here."""

# mock_hub_switches.txt: one MOCK_* name a line, a pending one followed by its PRs (MOCK_X #4512); the names mock_hub.py
# read when the freeze landed, then the pending ones, each part sorted.
FORMAT = name_freezes.Format(what="a MOCK_* name", example="MOCK_X #123", valid=NAME.fullmatch, header=PENDING_HEADER,
                             label="names mock_hub.py reads")
read_list = FORMAT.read  # (entries, problems) of the list's text
rewrite = FORMAT.rewrite  # the list's text rewritten as the freeze lands
changes = FORMAT.changes  # what a rewrite changed, one line for each part


def check(source, text):
    """(failures, notices) of a mock_hub.py source against the list's text."""
    found = reads(source)
    entries, problems = read_list(text)
    listed = {e.name: e for e in entries}
    failures = ["mock_hub_switches.txt: " + problem for problem in problems]
    failures += ["mock_hub.py:%d reads %s, which is not on the list. %s" % (line, name, HINT)
                 for name, line in sorted(found.names.items(), key=lambda item: item[1]) if name not in listed]
    failures += ["mock_hub.py:%d reads the environment without a literal name, so the list cannot be checked: %s. "
                 "Name the switch as a literal (os.environ.get(\"MOCK_X\")); for a new one: %s" % (line, text, HINT)
                 for line, text in found.unseen]
    notices = ["mock_hub.py no longer reads %s: delete line %d of scripts/mock/mock_hub_switches.txt" % (e.name, e.line)
               for e in entries if e.name not in found.names and not e.prs]
    waiting = ["%s (%s)" % (e.name, " ".join(e.prs)) for e in entries if e.name not in found.names and e.prs]
    if waiting:
        notices.append("pending, not read by mock_hub.py yet (delete a line once its PRs have closed): "
                       + ", ".join(waiting))
    return failures, notices


# ---- pending: the names open PRs add (name_freezes reads their heads) ----


def names_at(revision):
    """The MOCK_* names mock_hub.py reads at a commit, or None when it has no mock_hub.py there."""
    source = name_freezes.show(revision, HUB_PATH)
    if source is None:
        return None
    try:
        return set(reads(source).names)
    except SyntaxError:
        return None


def pending(base, today, prs, idle_days, log=sys.stderr):
    """{name: [PR numbers]}: the names each counted PR of `prs` (name_freezes.open_prs()) reads at its head that its
    merge base with base did not, without the ones base reads (`today`)."""
    counted = name_freezes.counted(prs, HUB_PATH, idle_days, log=log)
    name_freezes.fetch(counted)
    found = {}
    for pr in counted:
        added = (names_at(pr.head) or set()) - (names_at(name_freezes.merge_base(pr.head, base)) or set()) - today
        for name in added:
            found.setdefault(name, []).append(pr.number)
        print("#%d: %s" % (pr.number, " ".join(sorted(added)) or "no new name"), file=log)
    return {name: sorted(numbers) for name, numbers in sorted(found.items())}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    sub = parser.add_subparsers(dest="command", required=True)
    cmd = sub.add_parser("pending", help="the names open PRs add to mock_hub.py")
    cmd.add_argument("--base", default="origin/main", help="what the names are counted against (default origin/main)")
    cmd.add_argument("--idle-days", type=int, default=14, help="a draft whose head commit is older is left out")
    cmd.add_argument("--write", action="store_true",
                     help="rewrite mock_hub_switches.txt, both parts, the day the freeze lands")
    args = parser.parse_args(argv)
    text = LIST.read_text()
    if args.write:
        if subprocess.run(["git", "cat-file", "-e", "%s:%s" % (args.base, LIST_PATH)], cwd=ROOT,
                          capture_output=True).returncode == 0:
            print("%s has the list already: the freeze has landed, and --write is for the day it lands. Delete the "
                  "lines test_mock_hub.py's notices name by hand." % args.base, file=sys.stderr)
            return 1
        problems = read_list(text)[1]
        if problems:
            print("fix mock_hub_switches.txt first:\n" + "\n".join(problems), file=sys.stderr)
            return 1
    today = names_at(args.base)
    if today is None:
        print("%s has no readable %s" % (args.base, HUB_PATH), file=sys.stderr)
        return 1
    prs = name_freezes.open_prs([HUB_PATH])
    found = pending(args.base, today, prs, args.idle_days)
    for name, numbers in found.items():
        print("%s %s" % (name, " ".join("#%d" % n for n in numbers)))
    print("pending: %d names from %d PRs" % (len(found), len({n for numbers in found.values() for n in numbers})),
          file=sys.stderr)
    if args.write:
        new = rewrite(text, today, found, {pr.number for pr in prs}, date.today().isoformat(), args.idle_days)
        LIST.write_text(new)
        for line in changes(text, new):
            print("mock_hub_switches.txt, " + line, file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
