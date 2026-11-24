#!/usr/bin/env python3
"""What check-fast's failures are, the command that reruns each, and whether the merge base fails them too (#12733).

    python3 scripts/ci/check_fast_compare.py rerun --work <log dir> --job <n> [--root <repo>]
    python3 scripts/ci/check_fast_compare.py compare --work <log dir> --base <sha> --changed <file> [--root <repo>]
        [--base-ref <ref>] [--tip <sha>] [--timeout-go <s>] [--timeout-other <s>] [--timeout-bin <timeout>]

scripts/test/check-fast.sh records each step that fails or times out in its log directory, as job.<n>.step.<m>: NUL-
separated fields, the status (FAIL or TIMEOUT), the exit code, the directory it ran in (relative to the repository),
the byte range of its output in job.<n>.steps.log, its description, then its command. A failed step is narrowed to
what failed in it:

  go test          the named tests that failed, by package (--- FAIL: lines, each package's FAIL line after them);
                   a package that failed without a named test (a build failure, a panic outside a test) stays whole.
                   check-fast runs it through scripts/test/go_test_partitioned.py, whose output is go test's with
                   each partition's printed whole, so it is read the same way (#12765)
  python -m unittest
                   the failing test ids (FAIL: and ERROR: lines; setUpClass names its class)
  anything else    the command itself, a guard: the privacy scan, a ratchet, gofmt, go vet (the Windows one
                   through env, which it keeps), a typecheck, a checker script

rerun prints the command that reruns just that, one line per failing package, suite or command of job <n>, for the
summary's "rerun:" lines. A timed-out step reruns whole.

compare reruns the failures of every job in a detached worktree of the merge base (<log dir>/compare/base, made with
git worktree add and removed with git worktree remove --force by that path); the ones that pass there once more in
this working tree, each on its own; and with --tip the ones that also fail at the base at the base ref's tip
(<log dir>/compare/tip). It prints one line per failing test, suite or command:

  ALREADY   also fails at <base>
  NEW       passes at <base> and fails again when rerun here on its own, so likely caused by this change
  FLAKY     passes at <base> and when rerun here on its own: it failed only in the full run (load, timing, test
            order), so the change is not shown to cause it
  EDITED    not attributable, because this change edits that test's files (a _test.go or testdata/ file of the
            package, the suite or a test module it imports, a file the command names)
  UNKNOWN   not compared, and why (no named test, a web step that needs its own npm ci, a timeout, no such test)
  TIP-PASS  passes at the tip: main fixed it after the merge base; merging the base ref brings the fix
  TIP-FAIL  also fails at the tip
  TIP-?     not compared at the tip, and why (a timeout, no such test there)

then one line that counts them. The rules are the PR watcher's (main_failure_comparison in
scripts/operations/pr_watch_repair.py): only named tests are compared, and a test whose files the change edits is not
attributed. Like the watcher's, the comparison is by name: ALREADY says the same test fails at the base, not that it
fails the same way. A guard's command is compared by its exit code, and the lines of its output the base's does not
have are named, since the change may add to a failure main already has. A go command's package patterns whose
directory a commit does not have (./internal/<new>/..., a package the change adds) are left out of its rerun there,
since go fails on such a pattern before it checks anything; a command left with none of its patterns is UNKNOWN, as a
named test whose package the base does not have is. This file does not import the watcher: it runs where
check-fast runs, with no watcher configuration. It decides nothing from hosted CI, whose runs are hours old.

Go tests rerun with -count=1, as every go test of check-fast's does, and in a go slot (scripts/heavy_slot.py, as
check-fast's own go steps; PERSONA_HEAVY_SLOTS=0 turns the slots off). Each rerun has the timeout of its check-fast
step. The exit code is 0 when the comparison ran, whatever it found, and 2 when it could not (no record, no checkout).
"""
from __future__ import annotations

import argparse
import os
from pathlib import Path
import posixpath
import re
import shlex
import signal
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
HEAVY_SLOT = ROOT / "scripts" / "heavy_slot.py"
TIMEOUT_CODES = (124, 137)  # GNU timeout's own code, and its -k kill (or the OOM killer)

GO_TEST_RESULT = re.compile(r"^--- (PASS|FAIL|SKIP): (\S+)")
GO_PACKAGE_RESULT = re.compile(r"^(ok|FAIL)\s+(\S+)(?:\s+(.*))?$")
GO_SELF_TIMEOUT = "panic: test timed out after"
PY_FAILURE = re.compile(r"^(FAIL|ERROR): (\S+) \(([^)]*)\)", re.M)
PY_FIXTURES = ("setUpClass", "tearDownClass", "setUpModule", "tearDownModule")
PY_IMPORT = re.compile(r"^\s*(?:from\s+(test_\w+)\s+import\b|import\s+(test_\w+)\b)", re.M)
DURATION = re.compile(r"\b\d+(?:\.\d+)?(?:ms|s)\b")
# Commands that need an install of their own at the base (web/node_modules); they are not rerun there.
INSTALLERS = ("npm", "npx", "bun")
# The runner check-fast hands its go test commands to (python3 -B ../scripts/test/go_test_partitioned.py go test ...).
GO_RUNNER = "go_test_partitioned.py"


def go_command(argv):
    """The go command argv runs: argv itself for go, the one it hands the partitioned runner, the one env runs after
    its assignments (check-fast's Windows vet: env GOOS=windows GOARCH=amd64 go vet ..., #12894), or None for another."""
    rest = list(argv)
    if rest[:1] and posixpath.basename(rest[0]) == "env":
        rest = rest[1:]
        while rest and "=" in rest[0] and not rest[0].startswith("-"):
            rest = rest[1:]
        return rest if rest[:1] == ["go"] else None
    if rest[:1] and posixpath.basename(rest[0]).startswith("python"):
        rest = rest[1:]
        while rest and rest[0].startswith("-"):  # the interpreter's own flags (-B, -I)
            rest = rest[1:]
        if not rest or posixpath.basename(rest[0]) != GO_RUNNER:
            return None
    if rest[:1] and posixpath.basename(rest[0]) == GO_RUNNER:
        rest = rest[1:]
    return rest if rest[:1] == ["go"] else None


def package_directory(pattern):
    """The directory a relative go package pattern reads, relative to where go runs (./internal/foo/... is
    internal/foo, ./... is .), or None for an argument that is not one (a flag, an import path)."""
    if not (pattern in (".", "..") or pattern.startswith(("./", "../"))):
        return None
    wildcard = pattern.find("...")
    if wildcard >= 0:
        pattern = posixpath.dirname(pattern[:wildcard])
    return posixpath.normpath(pattern)


class CompareError(Exception):
    """The comparison cannot be made: a record that cannot be read, a checkout that cannot be made."""


def shell_line(directory, argv):
    """argv as one shell line, run from directory (relative to the repository)."""
    command = " ".join(shlex.quote(argument) for argument in argv)
    return command if directory in ("", ".") else "(cd %s && %s)" % (shlex.quote(directory), command)


def short(sha):
    return sha[:10]


def ref_name(ref):
    for prefix in ("refs/remotes/", "refs/heads/"):
        if ref.startswith(prefix):
            return ref[len(prefix):]
    return ref


class Verdict:
    """What a rerun says about one failing test, suite or command."""

    KINDS = ("ALREADY", "NEW", "FLAKY", "EDITED", "UNKNOWN", "TIP-PASS", "TIP-FAIL", "TIP-?")

    def __init__(self, kind, subject, text):
        if kind not in self.KINDS:
            raise ValueError("unknown verdict " + kind)
        self.kind, self.subject, self.text = kind, subject, text

    def line(self):
        return "%-8s %s: %s" % (self.kind, self.subject, self.text)


class Commit:
    """A commit the failures rerun at, as the verdict lines name it: its sha, and what it is of which ref."""

    def __init__(self, sha, role, ref):
        if not sha:
            raise ValueError("a commit needs its sha")
        self.sha, self.role, self.ref = sha, role, ref

    def __str__(self):
        return "%s (%s %s)" % (short(self.sha), self.role, self.ref)


class WorkingTree:
    """The repository's own working tree, where a failure that passed at the base is rerun on its own: nothing to
    check out or remove."""

    def __init__(self, root):
        self.path = Path(root)
        self.commit = "this working tree"  # what the verdicts of a rerun here name it

    def within(self, directory):
        return self.path / directory


class Timeouts:
    def __init__(self, go=300, other=120):
        self.go, self.other = int(go), int(other)

    def go_inner(self):
        """go test's own -timeout: inside the step's, so a hung test panics with a stack instead of being killed."""
        return max(self.go - 10, 5)


class Checkout:
    """A throwaway detached worktree of one commit, removed by its explicit path when the comparison is done."""

    def __init__(self, root, path, commit, run):
        self.root, self.path, self.commit, self.run = Path(root), Path(path), commit, run

    def __enter__(self):
        if self.path.exists():
            raise CompareError("%s is in the way; remove it with git worktree remove --force %s" % (self.path, self.path))
        self.path.parent.mkdir(parents=True, exist_ok=True)
        code, output = self.run(["git", "-C", str(self.root), "worktree", "add", "--detach", "--quiet", str(self.path),
                                 self.commit.sha], cwd=self.root, timeout=600)
        if code:
            self.remove()  # a checkout cut short can leave the worktree registered
            raise CompareError("git worktree add %s failed: %s" % (short(self.commit.sha), last_line(output)))
        return self

    def __exit__(self, *_):
        self.remove()
        return False

    def remove(self):
        code, output = self.run(["git", "-C", str(self.root), "worktree", "remove", "--force", str(self.path)],
                                cwd=self.root, timeout=600)
        if code and self.path.exists():
            print("check-fast --compare-main: could not remove %s (%s); remove it with git worktree remove --force %s"
                  % (self.path, last_line(output), self.path), flush=True)

    def within(self, directory):
        return self.path / directory


def last_line(text):
    lines = [line for line in (text or "").splitlines() if line.strip()]
    return lines[-1].strip()[:300] if lines else "no output"


def output_lines(text, *roots):
    """The lines of a command's output that say what it found: no slot lines, the checkout's path made the same as the
    repository's, durations left out, so one run's lines can be looked for in another's."""
    lines = []
    for line in (text or "").splitlines():
        line = line.rstrip()
        if not line.strip() or line.startswith("heavy_slot: ") or line.startswith("+ "):
            continue
        for root in roots:
            line = line.replace(str(root), "<repository>")
        lines.append(DURATION.sub("<time>", line))
    return lines


class Failure:
    """One failed piece of a step: what reruns it, what counts as its test files, and how it compares at a commit."""

    def subjects(self):
        raise NotImplementedError

    def rerun(self):
        raise NotImplementedError

    def test_files(self, changed):
        """The changed paths that make this failure the change's own test: comparing it with the base proves nothing."""
        return []

    def not_comparable(self):
        """Why this failure cannot be compared with a commit at all, or "" when it can."""
        return ""

    def compare(self, checkout, run, timeouts, log):
        """One Verdict per subject at checkout.commit (ALREADY, NEW or UNKNOWN)."""
        raise NotImplementedError

    def only(self, subjects):
        """The same failure narrowed to some of its subjects, or None for none of them."""
        return self if any(subject in subjects for subject in self.subjects()) else None


class GoTests(Failure):
    """The named Go tests that failed in one package of a module, rerun with -run '^(A|B)$'."""

    def __init__(self, module, package, names):
        if not names or not package.startswith("."):
            raise ValueError("a Go failure needs a package and the tests that failed in it")
        self.module, self.package, self.names = module, package, list(dict.fromkeys(names))

    def directory(self):
        """The package's directory, relative to the repository."""
        return posixpath.normpath(posixpath.join(self.module, self.package))

    def subjects(self):
        return ["%s %s" % (self.directory(), name) for name in self.names]

    def pattern(self):
        return "^(%s)$" % "|".join(re.escape(name) for name in self.names)

    def argv(self):
        return ["go", "test", "-count=1", "-run", self.pattern(), self.package]

    def rerun(self):
        return shell_line(self.module, self.argv())

    def test_files(self, changed):
        # A _test.go file is compiled into its own package's tests only; testdata/ holds what they read.
        directory = self.directory()
        return [path for path in changed
                if (posixpath.dirname(path) == directory and path.endswith("_test.go"))
                or path.startswith(directory + "/testdata/")]

    def only(self, subjects):
        names = [name for name, subject in zip(self.names, self.subjects()) if subject in subjects]
        return GoTests(self.module, self.package, names) if names else None

    def compare(self, checkout, run, timeouts, log):
        at = checkout.commit
        if not checkout.within(self.directory()).is_dir():
            return [Verdict("UNKNOWN", subject, "not compared: %s has no %s" % (at, self.directory()))
                    for subject in self.subjects()]
        argv = ["go", "test", "-count=1", "-v", "-timeout", "%ds" % timeouts.go_inner(), "-run", self.pattern(),
                self.package]
        code, output = run(argv, cwd=checkout.within(self.module), timeout=timeouts.go, slot="go")
        where = log(self.subjects()[0], output)
        results = {}
        for line in output.splitlines():
            match = GO_TEST_RESULT.match(line)  # top-level tests only: a subtest's line is indented
            if match:
                results[match.group(2)] = match.group(1)
        built = not re.search(r"\[(?:build|setup) failed\]", output)
        verdicts = []
        for name, subject in zip(self.names, self.subjects()):
            result = results.get(name)
            if result == "FAIL":
                verdicts.append(Verdict("ALREADY", subject, "also fails at %s" % at))
            elif result == "PASS":
                verdicts.append(Verdict("NEW", subject, "passes at %s, so likely caused by this change" % at))
            elif result == "SKIP":
                verdicts.append(Verdict("UNKNOWN", subject, "not compared: skipped at %s" % at))
            elif code in TIMEOUT_CODES or GO_SELF_TIMEOUT in output:
                verdicts.append(Verdict("UNKNOWN", subject, "not compared: the rerun at %s timed out (%s)" % (at, where)))
            elif not built:
                verdicts.append(Verdict("UNKNOWN", subject, "not compared: %s does not build at %s (%s)"
                                        % (self.directory(), at, where)))
            elif code == 0:
                verdicts.append(Verdict("UNKNOWN", subject, "not compared: %s has no test %s" % (at, name)))
            else:
                verdicts.append(Verdict("UNKNOWN", subject, "not compared: no result for it at %s (%s)" % (at, where)))
        return verdicts


class PythonTests(Failure):
    """The failing test ids of one unittest suite, rerun from its directory as check-fast runs the suite."""

    def __init__(self, directory, module, ids, python="python3", root=None):
        if not ids or not module:
            raise ValueError("a unittest failure needs its suite and the ids that failed")
        self.directory, self.module, self.ids, self.python = directory, module, list(dict.fromkeys(ids)), python
        self.root = Path(root or ROOT)

    def suite(self):
        return posixpath.normpath(posixpath.join(self.directory, self.module + ".py"))

    def subjects(self):
        return ["%s %s" % (self.directory, test) for test in self.ids]

    def argv(self):
        return [self.python, "-B", "-m", "unittest", "-q"] + self.ids

    def rerun(self):
        return shell_line(self.directory, self.argv())

    def test_files(self, changed):
        # The suite, and the test modules beside it that it imports (test_check_fast_guards.py uses
        # test_check_fast.py's repository and fixtures).
        files = {self.suite()}
        source = self.root / self.suite()
        try:
            text = source.read_text(encoding="utf-8")
        except (OSError, UnicodeDecodeError):
            text = ""
        for match in PY_IMPORT.finditer(text):
            files.add(posixpath.join(self.directory, (match.group(1) or match.group(2)) + ".py"))
        return [path for path in changed if path in files]

    def only(self, subjects):
        ids = [test for test, subject in zip(self.ids, self.subjects()) if subject in subjects]
        return PythonTests(self.directory, self.module, ids, self.python, self.root) if ids else None

    def compare(self, checkout, run, timeouts, log):
        at = checkout.commit
        if not checkout.within(self.suite()).is_file():
            return [Verdict("UNKNOWN", subject, "not compared: %s has no %s" % (at, self.suite()))
                    for subject in self.subjects()]
        code, output = run(self.argv(), cwd=checkout.within(self.directory), timeout=timeouts.other)
        where = log(self.subjects()[0], output)
        failed, unloaded = unittest_failures(output)
        verdicts = []
        for test, subject in zip(self.ids, self.subjects()):
            if any(test == other or test.startswith(other + ".") for other in failed):
                verdicts.append(Verdict("ALREADY", subject, "also fails at %s" % at))
            elif code in TIMEOUT_CODES:
                verdicts.append(Verdict("UNKNOWN", subject, "not compared: the rerun at %s timed out (%s)" % (at, where)))
            elif self.module in unloaded:
                verdicts.append(Verdict("UNKNOWN", subject, "not compared: %s does not load at %s (%s)"
                                        % (self.suite(), at, where)))
            elif test.rsplit(".", 1)[-1] in unloaded:
                verdicts.append(Verdict("UNKNOWN", subject, "not compared: %s has no test %s" % (at, test)))
            elif code and not failed and not unloaded:
                verdicts.append(Verdict("UNKNOWN", subject, "not compared: the suite failed at %s without naming a "
                                                            "test (%s)" % (at, where)))
            else:
                verdicts.append(Verdict("NEW", subject, "passes at %s, so likely caused by this change" % at))
        return verdicts


class Command(Failure):
    """A failed command that is not a named test (a guard), or one that is not narrowed (a timeout), rerun whole."""

    def __init__(self, directory, argv, description, reason="", root=None, output=""):
        if not argv:
            raise ValueError("a command failure needs its command")
        self.directory, self.argv, self.description, self.reason = directory, list(argv), description, reason
        self.root, self.output = Path(root or ROOT), output

    def subjects(self):
        return [self.description]

    def rerun(self):
        return shell_line(self.directory, self.argv)

    def not_comparable(self):
        return self.reason

    def paths(self):
        """The repository paths the command names (the checker it runs, the files it reads)."""
        named = []
        for argument in self.argv[1:]:
            if argument.startswith("-") or ("/" not in argument and "." not in argument):
                continue
            path = Path(argument)
            if path.is_absolute():
                try:
                    path = path.relative_to(self.root)
                except ValueError:
                    continue
                named.append(path.as_posix())
            else:
                named.append(posixpath.normpath(posixpath.join(self.directory, argument)))
        return named

    def test_files(self, changed):
        paths = set(self.paths())
        return [path for path in changed if path in paths]

    def packages(self, checkout):
        """(kept, missing): the indexes in argv of the go command's relative package patterns that name a directory
        in this working tree, split by whether checkout has that directory too. Both are empty for another command."""
        command = go_command(self.argv)
        if command is None:
            return [], []
        offset = len(self.argv) - len(command)
        kept, missing = [], []
        for index, argument in enumerate(command[2:], offset + 2):
            directory = package_directory(argument)
            if directory is None or not (self.root / self.directory / directory).is_dir():
                continue  # a flag's value that is a file (-coverprofile ./c.out), or a pattern go fails on here too
            (kept if checkout.within(self.directory).joinpath(directory).is_dir() else missing).append(index)
        return kept, missing

    def compare(self, checkout, run, timeouts, log):
        at = checkout.commit
        subject = self.description
        if not checkout.within(self.directory).is_dir():
            return [Verdict("UNKNOWN", subject, "not compared: %s has no %s" % (at, self.directory))]
        kept, missing = self.packages(checkout)
        absent = [posixpath.normpath(posixpath.join(self.directory, package_directory(self.argv[index])))
                  for index in missing]
        if missing and not kept:
            return [Verdict("UNKNOWN", subject, "not compared: %s has no %s" % (at, ", ".join(absent)))]
        argv = [argument.replace(str(self.root), str(checkout.path)) for index, argument in enumerate(self.argv)
                if index not in missing]
        go = go_command(argv) is not None
        code, output = run(argv, cwd=checkout.within(self.directory), timeout=timeouts.go if go else timeouts.other,
                           slot="go" if go else None)
        where = log(subject, output)
        if code in TIMEOUT_CODES:
            return [Verdict("UNKNOWN", subject, "not compared: the rerun at %s timed out (%s)" % (at, where))]
        if code == 0:
            return [Verdict("NEW", subject, "passes at %s, so likely caused by this change" % at)]
        text = "also fails at %s" % at
        if absent:
            text += " (rerun there without %s, which it does not have)" % ", ".join(absent)
        base_lines = set(output_lines(output, checkout.path))
        extra = [line for line in output_lines(self.output, self.root) if line not in base_lines]
        if extra:
            text += ("; %d line(s) of its output here are not in the base's, so this change may add to it, the first: %s"
                     % (len(extra), extra[0][:160]))
        return [Verdict("ALREADY", subject, text)]


def go_failures(output, module_path):
    """{package: [test names]} of a go test run's output, with the packages that failed without a named test as empty
    lists. go test prints a package's output together, its tests' --- FAIL: lines before its own FAIL line."""
    failed, pending = {}, []
    for line in (output or "").splitlines():
        match = GO_TEST_RESULT.match(line)
        if match:
            if match.group(1) == "FAIL":
                pending.append(match.group(2).split("/")[0])
            continue
        match = GO_PACKAGE_RESULT.match(line)
        if not match:
            continue
        path = match.group(2)
        if path == module_path:
            package = "."
        elif module_path and path.startswith(module_path + "/"):
            package = "./" + path[len(module_path) + 1:]
        else:
            continue
        if match.group(1) == "FAIL":
            failed.setdefault(package, [])
            failed[package] += [name for name in pending if name not in failed[package]]
        pending = []
    return failed


def unittest_failures(output):
    """(ids, unloaded) of a unittest run: the ids of its FAIL and ERROR lines, and the names the loader could not find
    or import (unittest.loader._FailedTest)."""
    ids, unloaded = [], []
    for kind, name, where in PY_FAILURE.findall(output or ""):
        if where.startswith("unittest."):
            unloaded.append(name)
        elif name in PY_FIXTURES or where == name or where.endswith("." + name):
            ids.append(where)  # Python 3.11 and later name the test in the brackets; setUpClass names its class
        else:
            ids.append(where + "." + name)  # Python 3.10 and earlier name the class
    return list(dict.fromkeys(ids)), unloaded


def module_path(directory):
    """The module path of the go.mod in directory, or "" without one."""
    try:
        text = (Path(directory) / "go.mod").read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        return ""
    match = re.search(r"(?m)^module\s+(\S+)", text)
    return match.group(1).strip('"') if match else ""


class Step:
    """A check-fast step that failed or timed out, as check-fast's record_step wrote it."""

    def __init__(self, status, code, directory, argv, output, description):
        if status not in ("FAIL", "TIMEOUT") or not argv:
            raise CompareError("not a failed step's record")
        self.status, self.code, self.directory = status, int(code), directory
        self.argv, self.output, self.description = list(argv), output, description

    @classmethod
    def read(cls, record, log):
        try:
            fields = [os.fsdecode(field) for field in Path(record).read_bytes().split(b"\0")]
            if fields and fields[-1] == "":
                fields.pop()
            status, code, directory, start, end, description = fields[:6]
            data = Path(log).read_bytes()[int(start):int(end)] if Path(log).is_file() else b""
        except (OSError, ValueError) as error:
            raise CompareError("cannot read %s: %s" % (record, error))
        return cls(status, code, directory, fields[6:], data.decode("utf-8", "replace"), description)

    def failures(self, root):
        """What failed in the step, narrowed as far as its output names it."""
        if self.status != "FAIL":
            return [self.whole(root, "it timed out here")]
        argv = self.argv
        # A go test that env runs reruns whole: the named tests' reruns would lose its environment.
        if (go_command(argv) or [])[:2] == ["go", "test"] and posixpath.basename(argv[0]) != "env":
            return self.go_failures(root)
        modules = unittest_modules(argv)
        if modules is not None:
            ids, unloaded = unittest_failures(self.output)
            if len(modules) == 1 and ids and not unloaded:
                return [PythonTests(self.directory, modules[0], ids, argv[0], root)]
            return [self.whole(root, "the suite does not load" if unloaded else "no named test failed in its output")]
        if argv[0] in INSTALLERS:
            return [self.whole(root, "%s needs its own install at the base (web/node_modules); rerun it there by hand"
                               % argv[0])]
        if argv[0] == "node" and (Path(root) / self.directory / "node_modules").is_dir():
            return [self.whole(root, "it needs %s/node_modules at the base; rerun it there by hand" % self.directory)]
        return [self.whole(root)]

    def whole(self, root, reason=""):
        return Command(self.directory, self.argv, self.description, reason, root, self.output)

    def go_failures(self, root):
        module = posixpath.normpath(self.directory)
        failed = go_failures(self.output, module_path(Path(root) / module))
        found = [GoTests(module, package, names) for package, names in sorted(failed.items()) if names]
        for package in sorted(package for package, names in failed.items() if not names):
            directory = posixpath.normpath(posixpath.join(module, package))
            found.append(Command(module, ["go", "test", "-count=1", package], "%s (no named test failed)" % directory,
                                 "no named test failed (a build failure, or a panic outside a test); the PR watcher "
                                 "compares named tests only", root))
        return found or [self.whole(root, "no named test failed in its output")]


def unittest_modules(argv):
    """The suites a python -m unittest command runs, or None for another command."""
    for index in range(len(argv) - 1):
        if argv[index] == "-m" and argv[index + 1] == "unittest":
            return [argument for argument in argv[index + 2:] if not argument.startswith("-")]
    return None


def job_steps(work, job=None):
    """The failed steps check-fast recorded, of one job or of every job, in the order they ran."""
    found = []
    for record in Path(work).glob("job.*.step.*"):
        parts = record.name.split(".")
        if len(parts) != 4 or not parts[1].isdigit() or not parts[3].isdigit():
            continue
        if job is not None and int(parts[1]) != job:
            continue
        found.append((int(parts[1]), int(parts[3]), record))
    return [Step.read(record, Path(work) / ("job.%d.steps.log" % number)) for number, _, record in sorted(found)]


def rerun_lines(steps, root):
    lines = []
    for step in steps:
        for failure in step.failures(root):
            line = failure.rerun()
            if line not in lines:
                lines.append(line)
    return lines


class Comparison:
    """The failures of a run, compared with the merge base and, for the ones it fails too, with the tip."""

    def __init__(self, steps, root, changed, checkouts, base, tip=None, run=None, timeouts=None, out=None):
        self.root, self.changed, self.checkouts = Path(root), list(changed), Path(checkouts)
        self.base, self.tip = base, tip
        self.run, self.timeouts = run or Runner(), timeouts or Timeouts()
        self.out = out or sys.stdout
        self.failures = [failure for step in steps if step.status == "FAIL" for failure in step.failures(self.root)]
        self.verdicts = []
        self.logs = 0

    def say(self, text):
        print(text, file=self.out, flush=True)

    def record(self, verdict):
        self.verdicts.append(verdict)
        self.say(verdict.line())

    def log(self, subject, output):
        """Keep a rerun's output beside check-fast's logs; the path for the verdict lines that need it."""
        self.logs += 1
        path = self.checkouts / ("rerun.%d.log" % self.logs)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("# %s\n%s" % (subject, output), encoding="utf-8")
        return "log: %s" % path

    def compare(self):
        pending = []
        for failure in self.failures:
            files = failure.test_files(self.changed)
            if files:
                more = " and %d more" % (len(files) - 2) if len(files) > 2 else ""
                for subject in failure.subjects():
                    self.record(Verdict("EDITED", subject, "not attributable, because this change edits that test's "
                                                           "files (%s%s)" % (", ".join(files[:2]), more)))
            elif failure.not_comparable():
                for subject in failure.subjects():
                    self.record(Verdict("UNKNOWN", subject, "not compared: " + failure.not_comparable()))
            else:
                pending.append(failure)
        already, passed = self.at_base(pending)
        self.confirm(passed)
        if self.tip and self.tip.sha != self.base.sha and already:
            self.at_tip_of(already)
        return self.summary()

    def at_base(self, failures):
        """Rerun failures at the merge base. ALREADY and UNKNOWN are said at once; the ones that pass there are held
        back for confirm. Returns both, each narrowed to those subjects."""
        already, passed = [], []
        if not failures:
            return already, passed
        with self.checkout(self.base, "base", failures) as checkout:
            for failure in failures:
                verdicts = failure.compare(checkout, self.run, self.timeouts, self.log)
                for verdict in verdicts:
                    if verdict.kind != "NEW":
                        self.record(verdict)
                for kind, kept in (("ALREADY", already), ("NEW", passed)):
                    narrowed = failure.only([verdict.subject for verdict in verdicts if verdict.kind == kind])
                    if narrowed is not None:
                        kept.append(narrowed)
        return already, passed

    def confirm(self, failures):
        """A failure that passes at the base is rerun here on its own before it is called this change's: one that
        failed only in the full run (under load, or after another test) passes again, and is FLAKY, not NEW."""
        if not failures:
            return
        here = WorkingTree(self.root)
        count = sum(len(failure.subjects()) for failure in failures)
        self.say("check-fast --compare-main: rerunning %d failure(s) that pass at %s here on their own"
                 % (count, short(self.base.sha)))
        for failure in failures:
            for verdict in failure.compare(here, self.run, self.timeouts, self.log):
                if verdict.kind == "NEW":  # it passes here too
                    self.record(Verdict("FLAKY", verdict.subject, "passes at %s and when rerun here on its own: it "
                                                                  "failed only in the full run (load, timing or test "
                                                                  "order), so this change is not shown to cause it"
                                                                  % self.base))
                elif verdict.kind == "ALREADY":  # it fails here again
                    self.record(Verdict("NEW", verdict.subject, "passes at %s and fails again when rerun here on its "
                                                                "own, so likely caused by this change" % self.base))
                else:
                    self.record(Verdict("NEW", verdict.subject, "passes at %s, so likely caused by this change (the "
                                                                "rerun here said: %s)" % (self.base, verdict.text)))

    def at_tip_of(self, failures):
        """Rerun what also fails at the base at the base ref's tip: has main fixed it since?"""
        with self.checkout(self.tip, "tip", failures) as checkout:
            for failure in failures:
                for verdict in failure.compare(checkout, self.run, self.timeouts, self.log):
                    self.record(self.at_tip(verdict))

    def checkout(self, commit, name, failures):
        path = self.checkouts / name
        count = sum(len(failure.subjects()) for failure in failures)
        self.say("check-fast --compare-main: rerunning %d failure(s) at %s in %s" % (count, commit, path))
        return Checkout(self.root, path, commit, self.run)

    def at_tip(self, verdict):
        if verdict.kind == "NEW":
            return Verdict("TIP-PASS", verdict.subject, "passes at %s: main fixed it after the merge base, so merging "
                                                        "%s brings the fix" % (self.tip, self.tip.ref))
        if verdict.kind == "ALREADY":
            return Verdict("TIP-FAIL", verdict.subject, verdict.text)
        # Not counted with the base's verdicts: the failure already has one.
        return Verdict("TIP-?", verdict.subject, verdict.text)

    def summary(self):
        counts = {kind: sum(1 for verdict in self.verdicts if verdict.kind == kind) for kind in Verdict.KINDS}
        total = sum(counts[kind] for kind in ("ALREADY", "NEW", "FLAKY", "EDITED", "UNKNOWN"))
        line = ("check-fast --compare-main: %d failure(s) against %s: %d ALREADY (failing there too), %d NEW (passing "
                "there, failing here again: likely this change), %d FLAKY (passing there and here on its own), "
                "%d EDITED (not attributable), %d UNKNOWN (not compared)"
                % (total, short(self.base.sha), counts["ALREADY"], counts["NEW"], counts["FLAKY"], counts["EDITED"],
                   counts["UNKNOWN"]))
        if self.tip and self.tip.sha != self.base.sha and (counts["TIP-PASS"] or counts["TIP-FAIL"] or counts["TIP-?"]):
            line += "; at the tip %s: %d TIP-PASS (fixed since), %d TIP-FAIL (still failing), %d TIP-? (not compared)" % (
                short(self.tip.sha), counts["TIP-PASS"], counts["TIP-FAIL"], counts["TIP-?"])
        self.say(line)
        return counts


def slot_wait():
    try:
        return max(int(os.environ.get("PERSONA_HEAVY_SLOT_TIMEOUT", "2700")), 0)
    except ValueError:
        return 2700


class Runner:
    """Runs a command under GNU timeout, a go command in a go slot, and returns (exit code, output)."""

    def __init__(self, timeout_bin=None, heavy_slot=HEAVY_SLOT):
        self.timeout_bin, self.heavy_slot = timeout_bin, Path(heavy_slot)

    def __call__(self, argv, cwd, timeout, slot=None):
        command = list(argv)
        if self.timeout_bin:
            command = [self.timeout_bin, "-k", "5s", str(timeout)] + command
        limit = timeout + 30  # a backstop behind GNU timeout's own
        if slot and os.environ.get("PERSONA_HEAVY_SLOTS") != "0" and self.heavy_slot.is_file():
            command = [sys.executable, str(self.heavy_slot), "run", "--kind", slot, "--"] + command
            limit += slot_wait()
        try:
            done = subprocess.run(command, cwd=str(cwd), stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                  stderr=subprocess.STDOUT, timeout=limit)
        except subprocess.TimeoutExpired as error:
            return 124, (error.output or b"").decode("utf-8", "replace")
        except OSError as error:
            return 127, str(error)
        return done.returncode, done.stdout.decode("utf-8", "replace")


def interrupted(number, _frame):
    # TERM and HUP end the run through SystemExit, so the checkout is still removed on the way out.
    raise SystemExit(128 + number)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    commands = parser.add_subparsers(dest="command")
    rerun = commands.add_parser("rerun", help="print the commands that rerun what failed in one job")
    rerun.add_argument("--work", required=True)
    rerun.add_argument("--job", type=int, required=True)
    rerun.add_argument("--root", default=str(ROOT))
    compare = commands.add_parser("compare", help="rerun every job's failures at the merge base")
    compare.add_argument("--work", required=True)
    compare.add_argument("--root", default=str(ROOT))
    compare.add_argument("--base", required=True)
    compare.add_argument("--base-ref", default="the base")
    compare.add_argument("--tip")
    compare.add_argument("--changed", required=True, help="the change's paths, one per line")
    compare.add_argument("--timeout-go", type=int, default=300)
    compare.add_argument("--timeout-other", type=int, default=120)
    compare.add_argument("--timeout-bin")
    args = parser.parse_args(argv)
    if args.command is None:
        parser.print_help()
        return 2
    try:
        if args.command == "rerun":
            for line in rerun_lines(job_steps(args.work, args.job), args.root):
                print(line)
            return 0
        for number in (signal.SIGTERM, signal.SIGHUP):
            signal.signal(number, interrupted)
        steps = job_steps(args.work)
        changed = [line.strip() for line in Path(args.changed).read_text(encoding="utf-8").splitlines() if line.strip()]
        ref = ref_name(args.base_ref)
        base = Commit(args.base, "merge base with", ref)
        tip = Commit(args.tip, "tip of", ref) if args.tip else None
        Comparison(steps, args.root, changed, Path(args.work) / "compare", base, tip,
                   Runner(args.timeout_bin), Timeouts(args.timeout_go, args.timeout_other)).compare()
        return 0
    except (CompareError, OSError) as error:
        if args.command == "rerun":
            print("check_fast_compare: %s" % error, file=sys.stderr)
        else:
            print("check-fast --compare-main: %s; nothing more compared" % error, flush=True)
        return 2


if __name__ == "__main__":
    sys.exit(main())
