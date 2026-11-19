"""scripts/heavy_slot.py: machine-wide slots for heavy builds (#10714).

Every run gets a slot directory of its own (PERSONA_HEAVY_SLOT_DIR) and an environment without the caller's
PERSONA_HEAVY_* settings, so a test never waits on, or holds, a real build's slot. The commands are small programs
that write what they see into files; a holder keeps its slot until the test creates a release file.
"""
import contextlib
import importlib.util
import io
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parents[2]
HELPER = ROOT / "scripts/heavy_slot.py"
SPEC = importlib.util.spec_from_file_location("heavy_slot", HELPER)
heavy_slot = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(heavy_slot)

# A command that logs "<name> start <marker>", waits for <release> ("-": no wait; at most 20 s), then logs "<name> end".
HOLDER = """import os, sys, time
log, name, release = sys.argv[1:4]
with open(log, "a") as f:
    f.write(f"{name} start {os.environ.get('PERSONA_HEAVY_SLOT_HELD', '')}".rstrip() + "\\n")
deadline = time.monotonic() + 20
while release != "-" and not os.path.exists(release) and time.monotonic() < deadline:
    time.sleep(0.02)
with open(log, "a") as f:
    f.write(f"{name} end\\n")
"""
# How long the signal tests wait for a step that takes well under a second on an idle machine, and how long their
# command sleeps when no signal comes: ten times the wait, so a TERM that never arrived cannot pass for a slow machine.
SIGNAL_WAIT = 60
UNSIGNALLED_SLEEP = 10 * SIGNAL_WAIT
# A command that writes <ready> once its SIGTERM handler is in place and exits 42 when the TERM comes; without one it
# sleeps UNSIGNALLED_SLEEP seconds and exits 3.
TERM_CATCHER = f"""import signal, sys, time
def caught(signum, frame):
    open(sys.argv[2], "w").write("TERM")
    sys.exit(42)
signal.signal(signal.SIGTERM, caught)
open(sys.argv[1], "w").write("ready")
time.sleep({UNSIGNALLED_SLEEP})
sys.exit(3)
"""
# The same, for a caller that ignores SIGTERM: the command inherits the ignore and only reports what it got.
TERM_IGNORER = TERM_CATCHER.replace("signal.signal(signal.SIGTERM, caught)",
                                    "assert signal.getsignal(signal.SIGTERM) == signal.SIG_IGN")


def default_signals():
    """The test runner may have been started with TERM or HUP ignored (check-fast's job subshells are), and an ignored
    signal stays ignored in every child: give the helper the dispositions of a command started from a terminal."""
    for number in (signal.SIGTERM, signal.SIGHUP, signal.SIGINT):
        signal.signal(number, signal.SIG_DFL)


def ignore_term():
    default_signals()
    signal.signal(signal.SIGTERM, signal.SIG_IGN)


@unittest.skipIf(heavy_slot.fcntl is None, "heavy build slots need fcntl (not on Windows)")
class HeavySlotTests(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="heavy-slot-test-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.slots = self.tmp / "slots"
        self.log = self.tmp / "log.txt"
        self.release = self.tmp / "release"
        (self.tmp / "holder.py").write_text(HOLDER)
        self.env = {key: value for key, value in os.environ.items() if not key.startswith("PERSONA_HEAVY_SLOT")}
        self.env.update(PERSONA_HEAVY_SLOT_DIR=str(self.slots), PERSONA_HEAVY_SLOTS_GO="1",
                        PERSONA_HEAVY_SLOTS_WEB="1", PERSONA_HEAVY_SLOT_TIMEOUT="20")
        self.procs = []
        self.addCleanup(self.stop_all)

    def stop_all(self):
        # Each helper leads a process group of its own (start_new_session), which its command shares: stop both. Only
        # a helper not yet reaped: until then its pid, and so its group's id, cannot belong to another process.
        self.release.touch()
        for proc in self.procs:
            if proc.poll() is None:
                try:
                    os.killpg(proc.pid, signal.SIGKILL)
                except (ProcessLookupError, PermissionError):
                    pass
            proc.communicate()

    def command(self, kind, *command):
        return [sys.executable, str(HELPER), "run", "--kind", kind, "--", *command]

    def holder(self, name, release="-"):
        return [sys.executable, str(self.tmp / "holder.py"), str(self.log), name, release]

    def spawn(self, kind, *command, env=None, preexec_fn=default_signals, **kwargs):
        options = dict(stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        options.update(kwargs)
        proc = subprocess.Popen(self.command(kind, *command), env={**self.env, **(env or {})}, preexec_fn=preexec_fn,
                                start_new_session=True, **options)
        self.procs.append(proc)
        return proc

    def run_helper(self, kind, *command, env=None, input=""):
        proc = self.spawn(kind, *command, env=env, stdin=subprocess.PIPE)
        out, err = proc.communicate(input, timeout=30)
        return proc.returncode, out, err

    def start_holder(self, kind, name):
        """Start a command that holds a slot of the kind until the release file appears; return once it runs."""
        proc = self.spawn(kind, *self.holder(name, str(self.release)))
        self.wait_for(lambda: f"{name} start" in self.log_text(), f"{name} never started")
        return proc

    def log_text(self):
        return self.log.read_text() if self.log.exists() else ""

    def events(self):
        """The log without the markers: "A start", "A end", ..."""
        return [" ".join(line.split()[:2]) for line in self.log_text().splitlines()]

    def wait_for(self, condition, message, seconds=15):
        deadline = time.monotonic() + seconds
        while not condition():
            for proc in self.procs:
                if proc.poll() not in (None, 0):
                    self.fail(f"{message}: a helper exited {proc.returncode}: {proc.communicate()}")
            self.assertLess(time.monotonic(), deadline, message)
            time.sleep(0.02)

    def test_one_slot_runs_two_commands_of_a_kind_one_after_the_other(self):
        first = self.start_holder("go", "A")
        err = self.tmp / "second.err"
        with err.open("w") as stderr:
            second = self.spawn("go", *self.holder("B"), stderr=stderr)
        # The second one says it waits, and does not start while the first holds the only slot.
        self.wait_for(lambda: "waiting" in err.read_text(), "the second command never waited")
        self.assertEqual("heavy_slot: waiting for a go slot (1 busy, up to 20s)\n", err.read_text())
        self.assertEqual(["A start"], self.events())
        self.release.touch()
        _, first_err = first.communicate(timeout=20)
        second.wait(timeout=20)
        self.assertEqual((0, 0), (first.returncode, second.returncode), (first_err, err.read_text()))
        self.assertEqual(["A start", "A end", "B start", "B end"], self.events())
        self.assertRegex(err.read_text().splitlines()[-1], r"^heavy_slot: got a go slot after \d+s$")
        self.assertEqual("", first_err)  # a free slot runs the command without a word
        # Each command sees the marker that makes a nested call re-entrant: its kind and the holder's pid.
        self.assertIn(f"A start go:{first.pid}", self.log_text())
        self.assertIn(f"B start go:{second.pid}", self.log_text())

    def test_commands_of_different_kinds_do_not_wait_for_each_other(self):
        self.start_holder("go", "A")
        code, _, err = self.run_helper("web", *self.holder("B"))
        self.assertEqual((0, ""), (code, err))
        self.assertEqual(["A start", "B start", "B end"], self.events())

    def test_a_nested_call_of_the_same_kind_runs_at_once(self):
        # With one slot, a nested call that waited for its own kind would wait for itself until the timeout.
        nested = self.command("go", sys.executable, "-c", "import os; print(os.environ['PERSONA_HEAVY_SLOT_HELD'])")
        proc = self.spawn("go", *nested)
        out, err = proc.communicate(timeout=10)
        self.assertEqual((0, ""), (proc.returncode, err))
        self.assertEqual(f"go:{proc.pid}\n", out)

    def test_a_nested_call_of_another_kind_never_waits(self):
        # A go holder waiting for a web slot could deadlock with a web holder waiting for a go slot: it runs anyway.
        self.start_holder("web", "A")
        nested = self.command("web", sys.executable, "-c", "print('inner')")
        code, out, err = self.run_helper("go", *nested)
        self.assertEqual((0, "inner\n"), (code, out), err)
        self.assertRegex(err, r"^heavy_slot: no free web slot: running anyway inside the go \(pid \d+\) slot "
                              r"it was called from \(a nested call never waits\)\n$")

    def test_a_nested_call_takes_a_free_slot_of_another_kind(self):
        nested = self.command("web", *self.holder("B", str(self.release)))
        outer = self.spawn("go", *nested)
        self.wait_for(lambda: "B start" in self.log_text(), "the nested command never started")
        self.assertIn(f"B start go:{outer.pid},web:", self.log_text())
        code, _, err = self.run_helper("web", "true", env={"PERSONA_HEAVY_SLOT_TIMEOUT": "0"})
        self.assertEqual(124, code, err)  # the nested call holds the only web slot

    def test_a_marker_whose_holder_exited_counts_for_nothing(self):
        self.start_holder("go", "A")
        gone = subprocess.Popen([sys.executable, "-c", "pass"])
        gone.wait()
        code, _, err = self.run_helper("go", "true", env={"PERSONA_HEAVY_SLOT_HELD": f"go:{gone.pid}",
                                                          "PERSONA_HEAVY_SLOT_TIMEOUT": "0"})
        self.assertEqual(124, code, err)

    def test_a_wait_past_the_timeout_exits_124_without_running_the_command(self):
        self.start_holder("go", "A")
        ran = self.tmp / "ran"
        code, out, err = self.run_helper("go", sys.executable, "-c", f"open({str(ran)!r}, 'w')",
                                         env={"PERSONA_HEAVY_SLOT_TIMEOUT": "1"})
        self.assertEqual((124, ""), (code, out), err)
        self.assertFalse(ran.exists())
        self.assertEqual(["heavy_slot: waiting for a go slot (1 busy, up to 1s)",
                          f"heavy_slot: timed out after 1s waiting for a go slot (1 busy); did not run {sys.executable} "
                          "(PERSONA_HEAVY_SLOT_TIMEOUT sets the wait)"], err.splitlines())

    def test_the_off_switch_execs_the_command_unchanged(self):
        # yes | head: a command started from a shell dies of SIGPIPE quietly; one left with the SIGPIPE Python ignores
        # for itself would print "Broken pipe".
        script = 'echo "pid $$"; echo "held [${PERSONA_HEAVY_SLOT_HELD:-}]"; cat; yes | head -n 1; echo err >&2; exit 7'
        direct = subprocess.run(["sh", "-c", script], env=self.env, input="from stdin\n", capture_output=True,
                                text=True, timeout=30)
        self.assertEqual(["held []", "from stdin", "y"], direct.stdout.splitlines()[1:])
        self.assertEqual(("err\n", 7), (direct.stderr, direct.returncode))
        for switch in ({"PERSONA_HEAVY_SLOTS": "0"}, {"PERSONA_HEAVY_SLOTS_GO": "0"}):
            with self.subTest(switch=switch):
                proc = self.spawn("go", "sh", "-c", script, env=switch, stdin=subprocess.PIPE)
                out, err = proc.communicate("from stdin\n", timeout=30)
                # exec: the command runs as the helper's own process; nothing else differs from running it directly.
                self.assertEqual(f"pid {proc.pid}", out.splitlines()[0])
                self.assertEqual(direct.stdout.splitlines()[1:], out.splitlines()[1:])
                self.assertEqual((direct.stderr, direct.returncode), (err, proc.returncode))
        self.assertFalse(self.slots.exists())  # no slot was taken

    def test_a_command_in_a_slot_keeps_its_stdio_and_signal_actions(self):
        code, out, err = self.run_helper("go", "sh", "-c", "cat; yes | head -n 1; echo err >&2", input="from stdin\n")
        self.assertEqual((0, "from stdin\ny\n", "err\n"), (code, out, err))

    def test_the_command_is_everything_after_the_first_double_dash(self):
        code, out, err = self.run_helper("go", "sh", "-c", 'printf "%s\\n" "$@"', "sh", "--", "-x", "--kind")
        self.assertEqual((0, "--\n-x\n--kind\n", ""), (code, out, err))
        # Without the -- there is no command: a usage error, nothing run.
        done = subprocess.run([sys.executable, str(HELPER), "run", "--kind", "go", "true"], env=self.env,
                              capture_output=True, text=True, timeout=30)
        self.assertEqual(125, done.returncode, done.stderr)

    def test_the_exit_status_is_the_commands(self):
        for code in (0, 1, 3, 124, 255):
            with self.subTest(code=code):
                self.assertEqual(code, self.run_helper("go", "sh", "-c", f"exit {code}")[0])
        # A command killed by a signal: the helper dies by the same signal.
        self.assertEqual(-signal.SIGTERM, self.run_helper("go", "sh", "-c", "kill -TERM $$")[0])
        self.assertEqual(127, self.run_helper("go", "no-such-command-heavy-slot")[0])
        # The only slot is free again after each of them: no wait allowed.
        self.assertEqual(0, self.run_helper("go", "true", env={"PERSONA_HEAVY_SLOT_TIMEOUT": "0"})[0])

    def test_sigterm_to_the_helper_reaches_the_command_and_frees_the_slot(self):
        (self.tmp / "catcher.py").write_text(TERM_CATCHER)
        ready, caught = self.tmp / "ready", self.tmp / "caught"
        proc = self.spawn("go", sys.executable, str(self.tmp / "catcher.py"), str(ready), str(caught))
        self.wait_for(ready.exists, "the command never installed its handler", seconds=SIGNAL_WAIT)
        proc.send_signal(signal.SIGTERM)
        try:
            proc.communicate(timeout=SIGNAL_WAIT)
        except subprocess.TimeoutExpired:
            # The command sleeps ten times as long as this wait unless the TERM reaches it: say which side stalled.
            if caught.exists():
                self.fail(f"the command caught the TERM, but the helper had not exited {SIGNAL_WAIT}s later")
            self.fail(f"the TERM never reached the command: it was still sleeping {SIGNAL_WAIT}s later")
        self.assertEqual("TERM", caught.read_text())
        self.assertEqual(42, proc.returncode)  # the command's own exit status after it handled the TERM
        self.assertEqual(0, self.run_helper("go", "true", env={"PERSONA_HEAVY_SLOT_TIMEOUT": "0"})[0])

    def test_a_signal_the_caller_ignores_stays_ignored(self):
        # check-fast.sh's job subshells ignore TERM, so that a TERM to its process group cannot end a check before the
        # script stops it in order; the helper must not undo that by passing the TERM on.
        (self.tmp / "ignorer.py").write_text(TERM_IGNORER)
        ready, caught = self.tmp / "ready", self.tmp / "caught"
        proc = self.spawn("go", sys.executable, str(self.tmp / "ignorer.py"), str(ready), str(caught),
                          preexec_fn=ignore_term)
        self.wait_for(ready.exists, "the command never started", seconds=SIGNAL_WAIT)
        proc.send_signal(signal.SIGTERM)
        time.sleep(0.3)
        self.assertIsNone(proc.poll())
        self.assertFalse(caught.exists())

    def test_status_names_the_holder(self):
        holder = self.start_holder("go", "A")
        done = subprocess.run([sys.executable, str(HELPER), "status"], env=self.env, capture_output=True, text=True,
                              timeout=30)
        self.assertEqual(0, done.returncode, done.stderr)
        self.assertIn("go: 1 of 1 busy", done.stdout)
        self.assertIn(f"slot 0: pid {holder.pid}, ", done.stdout)
        self.assertIn("web: 0 of 1 busy", done.stdout)

    def test_bad_settings_stop_with_a_message(self):
        for env, name in (({"PERSONA_HEAVY_SLOTS_GO": "two"}, "PERSONA_HEAVY_SLOTS_GO"),
                          ({"PERSONA_HEAVY_SLOTS": "yes"}, "PERSONA_HEAVY_SLOTS"),
                          ({"PERSONA_HEAVY_SLOT_TIMEOUT": "-1"}, "PERSONA_HEAVY_SLOT_TIMEOUT")):
            with self.subTest(env=env):
                code, _, err = self.run_helper("go", "true", env=env)
                self.assertEqual(125, code)
                self.assertIn(name, err)
        self.assertEqual(125, self.run_helper("nope", "true")[0])


class HeavySlotDefaultsTests(unittest.TestCase):
    def test_default_counts_follow_the_cores(self):
        self.assertEqual({"xcode": 2, "gradle": 2, "go": 3, "web": 3},
                         {kind: heavy_slot.default_count(kind, 14) for kind in heavy_slot.CORES_PER_SLOT})
        self.assertEqual({1}, {heavy_slot.default_count(kind, 1) for kind in heavy_slot.CORES_PER_SLOT})

    def test_the_default_directory_is_a_per_user_cache(self):
        home = os.path.expanduser("~")
        path = heavy_slot.slot_dir({"XDG_CACHE_HOME": "/cache"})
        if sys.platform == "darwin":
            self.assertEqual(os.path.join(home, "Library", "Caches", "persona", "slots"), path)
        else:
            self.assertEqual(os.path.join("/cache", "persona", "slots"), path)
            self.assertEqual(os.path.join(home, ".cache", "persona", "slots"), heavy_slot.slot_dir({}))
        self.assertEqual("/elsewhere", heavy_slot.slot_dir({"PERSONA_HEAVY_SLOT_DIR": "/elsewhere"}))

    def test_without_fcntl_it_runs_the_command_unwrapped_with_a_note(self):
        saved = heavy_slot.fcntl
        heavy_slot.fcntl = None
        self.addCleanup(setattr, heavy_slot, "fcntl", saved)
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            code = heavy_slot.run("go", [sys.executable, "-c", "raise SystemExit(5)"], env={})
        self.assertEqual(5, code)
        self.assertEqual("heavy_slot: this platform has no fcntl (Windows): running without a slot\n", err.getvalue())


class HeavySlotCallersTests(unittest.TestCase):
    def test_ci_and_the_pr_watcher_take_no_slot(self):
        # A hosted runner runs one job, so a slot there would only make that job's own steps wait for each other, and
        # the PR and main watchers' checks must not queue behind local builds. None of them runs a script that takes a
        # slot today, which is why CI does not set PERSONA_HEAVY_SLOTS=0 (docs/operations/testing.md "Heavy build
        # slots"); a lane or a watcher step that starts to run one sets it there. The watchers' steps live in their
        # scripts and example configs, and in the skills that run them (their scripts and the SKILL.md an Agent
        # follows); the lanes they call are the CI scripts above.
        wrapped = ("heavy_slot.py", "app/sim.sh", "app/android.sh", "check-fast.sh", "scripts/sim.sh",
                   "scripts/android.sh")
        groups = {
            "CI": (".github/workflows/*.yml", "scripts/ci/*.sh", "scripts/ci/*.py"),
            "the operations scripts, the PR and main watchers among them": (
                "scripts/operations/*.py", "scripts/operations/*watch*.json", "scripts/operations/*watch*.service"),
            "the PR watcher's skill": (".claude/skills/pr-review-merge-loop/**/*",),
            "the main watcher's skill": (".claude/skills/main-verify-fix-loop/**/*",),
        }
        callers = []
        for group, patterns in groups.items():
            files = sorted({path for pattern in patterns for path in ROOT.glob(pattern)
                            if path.is_file() and not path.name.startswith("test_")})
            # A moved directory must not leave its group scanning nothing.
            self.assertTrue(files, f"no files for {group}: {patterns}")
            for path in files:
                text = path.read_text(encoding="utf-8")
                if any(name in text for name in wrapped) and "PERSONA_HEAVY_SLOTS" not in text:
                    callers.append(str(path.relative_to(ROOT)))
        self.assertEqual([], callers)

if __name__ == "__main__":
    unittest.main()
