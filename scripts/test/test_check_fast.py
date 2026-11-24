"""check-fast.sh runs the scripts/ unittests a change needs (#3968), and the scripts/test suites that guard docs,
markdown, apple/ and deploy/docker/; Ctrl-C, SIGTERM and SIGHUP stop the checks it started; the per-path table it reads
its plan from (scripts/ci/check_fast_map.py, #11535) answers one line per path and names only checks it knows. The
server and web checks' inputs are tested in test_check_fast_inputs.py, split off so each file stays well under the
per-suite limit of scripts/ci/python-tests.py on a loaded machine (#11017).

Each test builds a throwaway repository with check-fast.sh, its table and stubs for the repository-wide checks it
always picks for a scripts/ change (the privacy scan, the extension schema test, the PR workflow test), commits a base,
makes a change on top and runs check-fast.sh --base against the base. The default-base tests run it without --base,
against refs/remotes/origin/* refs they write themselves.
"""
import importlib.util
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parents[2]
PASSING = "import unittest\n\nclass T(unittest.TestCase):\n    def test_ok(self):\n        pass\n"
FAILING = "import unittest\n\nclass T(unittest.TestCase):\n    def test_broken(self):\n        self.fail('broken on purpose')\n"
# The scripts/test suites that guard files outside scripts/; stubbed passing unless a test breaks one.
GUARD_SUITES = ("test_doc_paths.py", "test_doc_go_paths.py", "test_doc_sections.py", "test_doc_wire.py",
                "test_apple_l10n.py", "test_docker_entrypoint.py")
# A python3 that records each call ("<cwd>|<args>") instead of running anything.
FAKE_PYTHON = '#!/bin/sh\nprintf "%s|%s\\n" "$PWD" "$*" >> "$FAKE_PYTHON_LOG"\n'
# check-fast.sh, the per-path table it reads its plan from (#11535), the helper that names a failure's rerun and
# compares it with the merge base (#12733), the partitioned Go runner (#12765) and the gofmt script its gofmt job runs
# (#12894): what a throwaway repository needs to plan and report.
CHECK_FAST_FILES = ("scripts/test/check-fast.sh", "scripts/ci/check_fast_map.py", "scripts/ci/check_fast_compare.py",
                    "scripts/ci/affected_drift.py", "scripts/test/go_test_partitioned.py", "scripts/test/go_partitions.py",
                    "scripts/ci/gofmt.py")
# A go for the --compare-main tests: TestRed fails everywhere, TestMine only outside the merge base's checkout, where
# go also hangs when FAKE_GO_HANG names a pid file; the repo-wide guards and go vet pass. Each call is logged with its
# directory.
FAKE_GO_COMPARE = r"""#!/bin/sh
printf '%s|%s\n' "$PWD" "$*" >> "$FAKE_GO_LOG"
case "$*" in test*./internal/foo*) ;; *) exit 0 ;; esac
case "$PWD" in
  */compare/base/*)
    if [ -n "${FAKE_GO_HANG:-}" ]; then echo $$ > "$FAKE_GO_HANG.tmp"; mv "$FAKE_GO_HANG.tmp" "$FAKE_GO_HANG"; exec sleep 60; fi
    echo '--- FAIL: TestRed (0.00s)'; echo '--- PASS: TestMine (0.00s)'; echo FAIL
    printf 'FAIL\texample.com/m/server/internal/foo\t0.1s\n'; exit 1 ;;
esac
echo '--- FAIL: TestRed (0.00s)'; echo '--- FAIL: TestMine (0.00s)'; echo FAIL
printf 'FAIL\texample.com/m/server/internal/foo\t0.1s\n'; exit 1
"""


def copy_check_fast(repo):
    """Copy check-fast.sh and its table into the throwaway repository at repo."""
    for name in CHECK_FAST_FILES:
        (Path(repo) / name).parent.mkdir(parents=True, exist_ok=True)
        shutil.copy(ROOT / name, Path(repo) / name)


def ci_is_suite():
    """is_suite of scripts/ci/python-tests.py: the rule CI's Scripts tests job picks its suites by."""
    saved = list(sys.path)  # the module puts scripts/ci and scripts/test on sys.path for its own imports
    try:
        spec = importlib.util.spec_from_file_location("ci_python_tests", ROOT / "scripts/ci/python-tests.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
    finally:
        sys.path[:] = saved
    return module.is_suite


@unittest.skipUnless(shutil.which("timeout") or shutil.which("gtimeout"), "check-fast.sh needs GNU timeout")
class ScriptsRepoTestCase(unittest.TestCase):
    """The throwaway repository the scripts/ tests run check-fast.sh in, and its helpers; no tests of its own.
    test_check_fast_guards.py's tests use it too."""

    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="check-fast-test-"))
        self.addCleanup(shutil.rmtree, self.repo)
        (self.repo / "scripts/test").mkdir(parents=True)
        copy_check_fast(self.repo)
        self.write("scripts/privacy_check.py", "print('clean')\n")
        self.write("scripts/test/test_extension_schema.py", "")
        self.write("scripts/test/pr_needs_issue_test.mjs", "")
        self.write("scripts/tools/other.py", "")
        self.write("scripts/tools/test_other.py", FAILING)  # not related to the changes below: must not run
        self.write("scripts/tools/gone.py", "")
        self.write("scripts/tools/test_gone.py", PASSING)
        for name in GUARD_SUITES:
            self.write(f"scripts/test/{name}", PASSING)
        self.git("init", "-q")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base")
        self.git("tag", "base")

    def write(self, path, text):
        file = self.repo / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)

    def git(self, *args):
        subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True)

    def break_suite(self, name):
        """Make scripts/test/<name> fail and commit it into the base, so only the change made afterwards selects it."""
        self.commit_on_top(f"scripts/test/{name}", FAILING, "base")

    def check_fast(self, *args, path=None, env=None, base="base"):
        # TMPDIR inside the throwaway repo: a failed run keeps its log directory, and the cleanup removes it.
        # base=None runs without --base, so the script picks its default base.
        env = dict(os.environ, TMPDIR=str(self.repo), **({"PATH": path} if path else {}), **(env or {}))
        args = args or (("--base", base) if base else ())
        run = subprocess.run(["bash", "scripts/test/check-fast.sh", *args], cwd=self.repo,
                             capture_output=True, text=True, timeout=300, env=env)
        return run.returncode, run.stdout + run.stderr

    def commit_on_top(self, path, text, tag):
        """Commit one more file on top of HEAD and point tag at the new commit."""
        self.write(path, text)
        self.git("add", path)
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", tag)
        self.git("tag", "-f", tag)

    def toolchain_without_node_and_go(self):
        # A PATH with only the tools check-fast.sh itself needs, so node and go are missing on any machine. It lives
        # outside the repository, so a diff-mode run does not count it as a change.
        if getattr(self, "bin_dir", None):
            return str(self.bin_dir)
        self.bin_dir = Path(tempfile.mkdtemp(prefix="check-fast-bin-"))
        self.addCleanup(shutil.rmtree, self.bin_dir)
        bin_dir = self.bin_dir
        for tool in ("bash", "python3", "timeout", "gtimeout", "git", "mktemp", "rm", "dirname", "basename", "find",
                     "sort", "tee", "tail", "grep", "sed", "awk", "tr", "wc", "cut", "cat", "env"):
            found = shutil.which(tool)
            if found:
                (bin_dir / tool).symlink_to(found)
        return str(bin_dir)


class CheckFastCompareTests(ScriptsRepoTestCase):
    """A failure's rerun line, and --compare-main's verdicts against the merge base (#12733); the helper's own rules
    are tested in scripts/ci/test_check_fast_compare.py."""

    def go_repo(self):
        """A server package in the base, a fake go first on PATH, and a change to the package's code."""
        self.commit_on_top("server/go.mod", "module example.com/m/server\n", "base")
        self.commit_on_top("server/internal/foo/foo_test.go", "package foo\n", "base")
        self.commit_on_top("server/internal/foo/foo.go", "package foo\n", "base")
        self.write("server/internal/foo/foo.go", "package foo\n\n// changed\n")
        bin_dir = Path(self.toolchain_without_node_and_go())
        for tool in ("mv", "sleep", "ps"):  # the fake go's when it hangs, and on_signal's to find what to stop
            if shutil.which(tool) and not (bin_dir / tool).exists():
                (bin_dir / tool).symlink_to(shutil.which(tool))
        (bin_dir / "go").write_text(FAKE_GO_COMPARE)
        (bin_dir / "go").chmod(0o755)
        self.go_log = bin_dir / "go.log"
        # The go timeout the reruns' -timeout comes from, whatever the caller's environment sets (#12733).
        self.go_env = {"FAKE_GO_LOG": str(self.go_log), "CHECK_FAST_TIMEOUT_GO_TEST": "300"}
        return str(bin_dir)

    def worktrees(self):
        listed = subprocess.run(["git", "-C", str(self.repo), "worktree", "list", "--porcelain"], capture_output=True,
                                text=True, check=True).stdout
        return [line for line in listed.splitlines() if line.startswith("worktree ")]

    def test_a_failed_suite_prints_the_rerun_of_its_failing_tests(self):
        self.write("scripts/tools/other.py", "# changed\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/tools/test_other.py", out)
        self.assertIn("         rerun: (cd scripts/tools && python3 -B -m unittest -q test_other.T.test_broken)\n", out)
        self.assertNotIn("--compare-main", out)

    def test_compare_main_says_which_failures_the_merge_base_has(self):
        path = self.go_repo()
        code, out = self.check_fast("--base", "base", "--compare-main", path=path, env=self.go_env)
        self.assertEqual(1, code, out)  # a failure the base has is still a failure
        self.assertIn("         rerun: (cd server && go test -count=1 -run '^(TestRed|TestMine)$' ./internal/foo)", out)
        base = subprocess.run(["git", "-C", str(self.repo), "rev-parse", "--short=10", "base"], capture_output=True,
                              text=True, check=True).stdout.strip()
        self.assertIn("ALREADY  server/internal/foo TestRed: also fails at %s (merge base with base)" % base, out)
        self.assertIn("NEW      server/internal/foo TestMine: passes at %s (merge base with base) and fails again when "
                      "rerun here on its own, so likely caused by this change" % base, out)
        self.assertIn("check-fast --compare-main: 2 failure(s) against %s: 1 ALREADY" % base, out)
        self.assertTrue(out.rstrip().splitlines()[-1].startswith("check-fast: "), out)  # the run's verdict stays last
        calls = self.go_log.read_text().splitlines()
        reruns = [line for line in calls if "/compare/base/" in line]
        self.assertEqual(1, len(reruns), reruns)
        self.assertIn("|test -count=1 -v -timeout 290s -run ^(TestRed|TestMine)$ ./internal/foo", reruns[0])
        # TestMine passed at the base, so it ran once more here, on its own, before being called this change's.
        here = [line for line in calls if "/compare/" not in line and " -v " in line]
        self.assertEqual(1, len(here), calls)
        self.assertTrue(here[0].endswith("/server|test -count=1 -v -timeout 290s -run ^(TestMine)$ ./internal/foo"),
                        here)
        self.assertEqual(1, len(self.worktrees()), out)  # the base's checkout is gone again
        self.assertFalse([p for p in self.repo.glob("check-fast.*/compare/base") if p.exists()], out)

    def test_compare_main_does_not_attribute_a_test_the_change_edits(self):
        path = self.go_repo()
        self.write("server/internal/foo/foo_test.go", "package foo\n\n// changed\n")
        code, out = self.check_fast("--base", "base", "--compare-main", path=path, env=self.go_env)
        self.assertEqual(1, code, out)
        self.assertIn("EDITED   server/internal/foo TestRed: not attributable, because this change edits that test's "
                      "files (server/internal/foo/foo_test.go)", out)
        self.assertNotIn("/compare/base/", self.go_log.read_text())

    def test_compare_main_after_a_clean_run_compares_nothing(self):
        self.write("scripts/tools/test_new.py", PASSING)
        code, out = self.check_fast("--base", "base", "--compare-main")
        self.assertEqual(0, code, out)
        self.assertIn("check-fast --compare-main: no check failed; nothing to compare", out)

    def test_compare_main_is_refused_on_a_dry_run(self):
        code, out = self.check_fast("--dry-run", "--compare-main")
        self.assertEqual(2, code, out)
        self.assertIn("a dry run has none", out)

    @unittest.skipUnless(shutil.which("ps"), "the test lists processes with ps")
    def test_a_signal_during_the_comparison_removes_the_checkout(self):
        path = self.go_repo()
        hang = self.repo / "go-at-base.pid"
        proc = subprocess.Popen(["bash", "scripts/test/check-fast.sh", "--base", "base", "--compare-main"],
                                cwd=self.repo, env=dict(os.environ, TMPDIR=str(self.repo), PATH=path,
                                                        FAKE_GO_LOG=str(self.go_log), FAKE_GO_HANG=str(hang)),
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, start_new_session=True,
                                preexec_fn=default_signals)
        try:
            deadline = time.monotonic() + 60
            while not hang.exists():
                self.assertIsNone(proc.poll(), "check-fast.sh exited before the rerun at the base started")
                self.assertLess(time.monotonic(), deadline, "the rerun at the base never started")
                time.sleep(0.1)
            self.assertEqual(2, len(self.worktrees()))
            proc.send_signal(signal.SIGTERM)
            out, _ = proc.communicate(timeout=30)
            self.assertEqual(143, proc.returncode, out)
            self.assertEqual(1, len(self.worktrees()), out)
            self.assertFalse([p for p in self.repo.glob("check-fast.*/compare/base") if p.exists()], out)
        finally:
            if proc.poll() is None:
                os.killpg(proc.pid, signal.SIGKILL)
                proc.wait()
            proc.stdout.close()
            if hang.exists():
                try:
                    os.kill(int(hang.read_text()), signal.SIGKILL)
                except (ProcessLookupError, PermissionError, ValueError):
                    pass


class CheckFastTableTests(unittest.TestCase):
    """The per-path table check-fast plans from (scripts/ci/check_fast_map.py, #11535): its command line, the order its
    rules are tried in, and that check-fast.sh and the table name the same checks."""

    TABLE = ROOT / "scripts/ci/check_fast_map.py"

    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location("check_fast_map", cls.TABLE)
        cls.table = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.table)

    def run_table(self, data, *args):
        return subprocess.run([sys.executable, str(self.TABLE), *args], input=data, capture_output=True, timeout=60)

    def test_it_prints_the_checks_of_each_path_on_its_own_line(self):
        paths = ["web/src/App.tsx", "notes/todo.txt", "server/cmd/personad/main.go", "integrations/pi/a b.js",
                 "web/src/App.tsx"]
        run = self.run_table("".join(path + "\n" for path in paths).encode())
        self.assertEqual(0, run.returncode, run.stderr)
        self.assertEqual(["web\tsuite:scripts/test/test_doc_sections.py", "unmapped",
                          "go-package:cmd/personad\tgo-guards\tgo-vet:cmd/personad\tgo-fmt"
                          "\tsuite:scripts/test/test_doc_sections.py",
                          "integration:pi\tsuite:scripts/test/test_doc_sections.py",
                          "web\tsuite:scripts/test/test_doc_sections.py", ""], run.stdout.decode().split("\n"))
        self.assertEqual(b"", self.run_table(b"").stdout)

    def test_a_path_that_is_not_utf8_keeps_its_bytes_and_its_line(self):
        run = self.run_table(b"integrations/\xff/x.js\nnotes/\xfe\n")
        self.assertEqual(0, run.returncode, run.stderr)
        self.assertEqual(b"integration:\xff\tsuite:scripts/test/test_doc_sections.py\nunmapped\n", run.stdout)

    def test_help_prints_the_usage_and_anything_else_is_refused(self):
        run = self.run_table(b"", "--help")
        self.assertEqual(0, run.returncode)
        self.assertIn(b"check-fast's per-path table", run.stdout)
        self.assertEqual(2, self.run_table(b"", "--each").returncode)

    def test_the_first_rule_of_a_group_that_matches_wins(self):
        checks = self.table.checks
        # A dedicated script's own rule, not the scripts/* one after it.
        self.assertEqual(["external-refs", "suite:scripts/test/test_doc_sections.py"],
                         checks("scripts/test/test_external_service_refs.py"))
        # server/: a package group, a file directly in plugins/ (the guards run it), anything else a build.
        self.assertEqual(["go-package:internal/hub", "go-guards", "go-vet:internal/hub", "go-fmt",
                          "suite:scripts/test/test_doc_wire.py", "suite:scripts/test/test_doc_sections.py"],
                         checks("server/internal/hub/hub.go"))
        self.assertEqual(["go-guards", "go-vet:.", "go-fmt", "suite:scripts/test/test_doc_sections.py"],
                         checks("server/plugins/plugins.go"))
        self.assertEqual(["server-build", "go-guards", "go-vet:."], checks("server/internal/README.md")[:3])
        # A markdown file in a wire-list package is a doc first: the four doc suites, not the wire list alone.
        self.assertIn("suite:scripts/test/test_doc_paths.py", checks("server/internal/proto/README.md"))
        # name:test_* matches the file name only: a test file is not a slot caller, a file in a test_ directory is.
        heavy = "suite:scripts/test/test_heavy_slot.py"
        self.assertNotIn(heavy, checks("scripts/operations/test_pr_watch.py"))
        self.assertIn(heavy, checks("scripts/operations/test_tools/watch.py"))
        self.assertIn(heavy, checks("scripts/operations/pr_watch.py"))

    def test_go_vet_takes_the_changed_group_and_gofmt_the_go_files_ci_formats(self):
        # go vet checks the whole module, and the path's group for Windows: the whole module again for a path outside
        # every group (go.mod, a file directly in plugins/, sdk/go, which the server imports). gofmt checks the .go
        # files of the trees CI's Go formatting step covers, and nothing else (#12894).
        checks = self.table.checks
        for path, group in (("server/internal/foo/foo.go", "internal/foo"), ("server/cmd/personad/main.go", "cmd/personad"),
                            ("server/plugins/wechat/x.go", "plugins/wechat"),
                            ("server/internal/foo/testdata/a.json", "internal/foo"), ("server/go.mod", "."),
                            ("server/plugins/plugins.go", "."), ("sdk/go/wire/x.go", ".")):
            with self.subTest(path=path):
                self.assertEqual(["go-vet:" + group], [c for c in checks(path) if c.startswith("go-vet")])
        for path in ("server/internal/foo/foo.go", "server/internal/foo/testdata/x.go", "server/go.go", "sdk/go/x.go"):
            with self.subTest(path=path):
                self.assertIn("go-fmt", checks(path))
        for path in ("server/go.mod", "server/internal/foo/a.json", "web/e2e/agents_hub_test.go",
                     "sdk/templates/go/main.go", "sdk/ts/scripts/int64-oracle.go", "docs/x.go"):
            with self.subTest(path=path):
                self.assertNotIn("go-fmt", checks(path))
        for path in ("web/e2e/agents_hub_test.go", "sdk/templates/go/main.go", "docs/x.md"):
            with self.subTest(path=path):
                self.assertEqual([], [c for c in checks(path) if c.startswith("go-vet")])

    def test_check_fast_and_the_table_name_the_same_checks(self):
        text = (ROOT / "scripts/test/check-fast.sh").read_text()
        block = text[text.index('case "$_c" in'):]
        block = block[:block.index("esac")]
        handled = set(re.findall(r"^\s+([a-z-]+)(?::\*)?\)", block, re.M))
        self.assertEqual(set(self.table.CHECKS) | {"suite"}, handled)
        named = {check for group in self.table.TABLE for _, listed in group for check in listed}
        self.assertEqual(set(self.table.CHECKS), {check for check in named if "/" not in check})

    def test_every_suite_the_table_names_is_a_unittest_suite_ci_runs(self):
        is_suite = ci_is_suite()
        suites = self.table.suites()
        self.assertGreater(len(suites), 20)
        for suite in suites:
            with self.subTest(suite=suite):
                self.assertTrue(suite.startswith("scripts/") and Path(suite).name.startswith("test_"), suite)
                self.assertTrue((ROOT / suite).is_file(), suite)
                self.assertTrue(is_suite(ROOT / suite), suite)


# A privacy-scan stub that never finishes on its own: it records its own pid, its parent's (the GNU timeout that runs
# it) and a sleeping child's, then waits.
HANGING_CHECK = """import os, subprocess, sys, time
child = subprocess.Popen(["sleep", "60"])
with open(sys.argv[0] + ".pids.tmp", "w") as f:
    f.write(f"{os.getpid()} {os.getppid()} {child.pid}")
os.replace(sys.argv[0] + ".pids.tmp", sys.argv[0] + ".pids")
time.sleep(60)
"""


# The signals the tests send. A child inherits an ignored signal, and a shell can never trap one that was ignored when
# it started, so check-fast.sh starts with them at their defaults whatever this runner got: check-fast runs each suite
# as a background job, which bash starts with SIGINT ignored, and nohup or a detached shell ignores SIGHUP
# (docs/lessons/testing.md, "Signal tests").
SIGNALS = (signal.SIGINT, signal.SIGHUP, signal.SIGTERM)


def default_signals():
    """preexec_fn: SIGINT, SIGHUP and SIGTERM at their defaults in the child, as a terminal starts check-fast.sh."""
    for sig in SIGNALS:
        signal.signal(sig, signal.SIG_DFL)


@unittest.skipUnless(shutil.which("ps"), "the signal tests list processes with ps")
@unittest.skipUnless(shutil.which("timeout") or shutil.which("gtimeout"), "check-fast.sh needs GNU timeout")
class CheckFastSignalTests(unittest.TestCase):
    """Ctrl-C, SIGTERM and SIGHUP to check-fast.sh stop the checks it started instead of leaving them orphaned."""

    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="check-fast-signal-"))
        self.addCleanup(shutil.rmtree, self.repo, True)
        (self.repo / "scripts/test").mkdir(parents=True)
        copy_check_fast(self.repo)
        (self.repo / "scripts/privacy_check.py").write_text(HANGING_CHECK)
        (self.repo / "scripts/test/test_extension_schema.py").write_text("")
        (self.repo / "scripts/test/pr_needs_issue_test.mjs").write_text("")
        for args in (["init", "-q"], ["add", "-A"],
                     ["-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base"],
                     ["tag", "base"]):
            subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True)
        with (self.repo / "scripts/privacy_check.py").open("a") as f:
            f.write("# changed\n")

    def survivors(self, pids):
        """Live processes from this run: any whose command line names the throwaway repo, plus the recorded pids."""
        ps = subprocess.run(["ps", "-A", "-o", "pid=", "-o", "stat=", "-o", "args="], capture_output=True, text=True,
                            check=True).stdout
        left = []
        for line in ps.splitlines():
            pid, stat, args = (line.split(None, 2) + ["", ""])[:3]
            if stat.startswith("Z"):
                continue
            if str(self.repo) in args or int(pid) in pids:
                left.append(line.strip())
        return left

    def interrupt(self, sig, code, group=False):
        pid_file = self.repo / "scripts/privacy_check.py.pids"
        pid_file.unlink(missing_ok=True)  # a test that interrupts twice waits for the second run's check
        # An absolute script path puts the repo into the job subshells' command lines; TMPDIR puts it into tee's.
        # start_new_session keeps a stray signal off the test runner; default_signals undoes what the runner ignores.
        proc = subprocess.Popen(["bash", str(self.repo / "scripts/test/check-fast.sh"), "--base", "base"],
                                cwd=self.repo, env=dict(os.environ, TMPDIR=str(self.repo)), stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT, text=True, start_new_session=True,
                                preexec_fn=default_signals)
        pids = set()
        try:
            deadline = time.monotonic() + 30
            while not pid_file.exists():
                self.assertIsNone(proc.poll(), "check-fast.sh exited before the stub check started")
                self.assertLess(time.monotonic(), deadline, "the stub check never started")
                time.sleep(0.1)
            pids = {int(p) for p in pid_file.read_text().split()}
            if group:
                # A terminal's Ctrl-C or hangup, kill -- -PGID and runners that time out signal the whole process
                # group: the job subshells and tee get the signal too, not just the script.
                os.killpg(proc.pid, sig)
            else:
                proc.send_signal(sig)
            out, _ = proc.communicate(timeout=15)
            self.assertEqual(code, proc.returncode, out)
            self.assertIn("check-fast: interrupted", out)
            self.assertNotIn("Killed", out)  # bash's report of each job subshell it KILLed stays off the output
            left = self.survivors(pids)
            deadline = time.monotonic() + 5
            while left and time.monotonic() < deadline:
                time.sleep(0.2)
                left = self.survivors(pids)
            self.assertEqual([], left, out)
            # The interrupted run keeps its log directory (made under TMPDIR, the throwaway repo).
            self.assertTrue([p for p in self.repo.glob("check-fast.*") if p.is_dir()], out)
        finally:
            if proc.poll() is None:
                os.killpg(proc.pid, signal.SIGKILL)
                proc.wait()
            for pid in pids:
                try:
                    os.kill(pid, signal.SIGKILL)
                except (ProcessLookupError, PermissionError):
                    pass

    def test_sigterm_stops_the_running_checks(self):
        self.interrupt(signal.SIGTERM, 143)

    def test_sigint_stops_the_running_checks(self):
        self.interrupt(signal.SIGINT, 130)

    def test_sighup_stops_the_running_checks(self):
        self.interrupt(signal.SIGHUP, 129)

    def test_sigint_to_the_process_group_stops_the_running_checks(self):
        self.interrupt(signal.SIGINT, 130, group=True)

    def test_sigterm_to_the_process_group_stops_the_running_checks(self):
        self.interrupt(signal.SIGTERM, 143, group=True)

    def test_sighup_to_the_process_group_stops_the_running_checks(self):
        # Closing a terminal or a tmux pane sends SIGHUP to the whole foreground process group.
        self.interrupt(signal.SIGHUP, 129, group=True)

    def test_a_runner_that_ignores_the_signals_does_not_pass_that_on(self):
        # As check-fast runs this suite: a background job with SIGINT ignored, here SIGHUP and SIGTERM too (#11250).
        saved = {sig: signal.signal(sig, signal.SIG_IGN) for sig in SIGNALS}
        try:
            for sig, code in ((signal.SIGINT, 130), (signal.SIGHUP, 129)):
                with self.subTest(signal=sig.name):
                    self.interrupt(sig, code)
        finally:
            for sig, handler in saved.items():
                signal.signal(sig, handler)


if __name__ == "__main__":
    unittest.main()
