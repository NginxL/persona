#!/usr/bin/env python3
"""Tests for backup_restore_drill.py.

The unit tests need no Docker: a fake docker command stands in for the daemon where processes, timeouts and cleanup
are under test. The integration tests run the real drill only when PERSONA_RESTORE_DRILL_BINARY names a static Linux
personad (and PERSONA_RESTORE_DRILL_DOCKER the docker command, "docker" by default); otherwise they are skipped and
prove nothing about a real restore.
"""
import contextlib
import copy
import io
import json
import os
from pathlib import Path
import re
import shlex
import signal
import struct
import subprocess
import sys
import tempfile
import time
import unittest

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import backup_restore_drill as drill  # noqa: E402

DRILL = HERE / "backup_restore_drill.py"


def elf(interpreter=False, machine=62):
    """A minimal ELF64 header with one program header: PT_LOAD, or PT_INTERP for a dynamically linked program."""
    ident = b"\x7fELF" + bytes([2, 1, 1, 0]) + bytes(8)
    header = ident + struct.pack("<HHIQQQIHHHHHH", 2, machine, 1, 0x400000, 64, 0, 0, 64, 56, 1, 0, 0, 0)
    program = struct.pack("<IIQQQQQQ", 3 if interpreter else 1, 5, 0, 0, 0, 0, 0, 0)
    return header + program


FAKE_DOCKER = r'''
import json, os, sys, time
from pathlib import Path
state, mode, args = Path(sys.argv[1]), sys.argv[2], sys.argv[3:]
with open(state / "calls.log", "a") as log:
    log.write(json.dumps(args) + "\n")
def flag(name):
    return (state / name).exists()
def mark(name, present=True):
    if present:
        (state / name).write_text("")
    elif (state / name).exists():
        (state / name).unlink()
if args[:2] == ["image", "inspect"]:
    sys.exit(0)
if args[0] == "version":
    print("amd64"); sys.exit(0)
if args[:2] == ["network", "create"]:
    mark("network"); sys.exit(0)
if args[:2] == ["network", "rm"]:
    mark("network", False); sys.exit(0)
if args[:2] == ["network", "inspect"]:
    sys.exit(0 if flag("network") else 1)
if args[:2] == ["container", "inspect"]:
    sys.exit(0 if flag("container-" + args[2]) else 1)
if args[0] == "rm":
    mark("container-" + args[-1], False); sys.exit(0)
if args[0] == "run":
    if mode == "fail-run":
        env_file = Path(args[args.index("--env-file") + 1])
        sys.stderr.write("could not start: " + env_file.read_text().strip() + " postgres://postgres:" +
                         env_file.read_text().split("=", 1)[1].strip() + "@db:5432/x\n")
        sys.exit(125)
    mark("container-" + args[args.index("--name") + 1]); print("container-id"); sys.exit(0)
if args[0] == "exec":
    if mode in ("hang-ready", "interrupt"):
        (state / "hung.pid").write_text(str(os.getpid()))
        time.sleep(120)
    sys.exit(2)
if args[0] == "port":
    sys.exit(0)
sys.exit(3)
'''


class Fixture(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="drill-test-"))
        self.addCleanup(lambda: subprocess.run(["rm", "-rf", str(self.tmp)], check=False))
        self.home = self.tmp / "home"
        self.home.mkdir()
        self.environment = {"HOME": str(self.home), "PATH": os.environ.get("PATH", "")}
        self.binary = self.tmp / "personad"
        self.binary.write_bytes(elf())
        self.binary.chmod(0o755)

    def parse(self, *argv):
        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr):
            try:
                return drill.parse_args(list(argv), self.environment), ""
            except SystemExit as exit_:
                self.assertEqual(exit_.code, 2)
                return None, stderr.getvalue()


class ArgumentSafetyTest(Fixture):
    def test_nothing_runs_without_execute(self):
        args, error = self.parse("--binary", str(self.binary))
        self.assertIsNone(args)
        self.assertIn("without --execute", error)

    def test_a_static_binary_is_accepted(self):
        args, error = self.parse("--execute", "--binary", str(self.binary), "--docker", "sudo -n docker")
        self.assertEqual(error, "")
        self.assertEqual(args.docker, ["sudo", "-n", "docker"])
        self.assertEqual(args.checks, "full")

    def test_production_inputs_have_no_flag(self):
        for flag in ("--database-url", "--hub-url", "--home", "--bundle", "--manager-database-url", "--token"):
            with self.subTest(flag=flag):
                args, error = self.parse("--execute", "--binary", str(self.binary), flag, "postgres://x@example.com/p")
                self.assertIsNone(args)
                self.assertIn("unrecognized arguments", error)

    def test_abbreviated_flags_are_refused(self):
        args, error = self.parse("--exec", "--binary", str(self.binary))
        self.assertIsNone(args)

    def test_a_runtime_directory_binary_is_refused(self):
        # ~/.persona, and any directory with a hub.json or secret.key (an install under another or an older name).
        for root, marker in ((self.home / ".persona", None), (self.home / "old-install", "hub.json"),
                             (self.home / "other", "secret.key")):
            with self.subTest(root=root.name):
                binary = root / "bin" / "personad"
                binary.parent.mkdir(parents=True)
                binary.write_bytes(elf())
                if marker:
                    (root / marker).write_text("{}")
                args, error = self.parse("--execute", "--binary", str(binary))
                self.assertIsNone(args)
                self.assertIn("runtime directory", error)

    def test_tmpdir_in_a_runtime_directory_is_refused(self):
        runtime = self.home / ".persona" / "tmp"
        runtime.mkdir(parents=True)
        previous = drill.tempfile.tempdir
        drill.tempfile.tempdir = str(runtime)
        try:
            args, error = self.parse("--execute", "--binary", str(self.binary))
        finally:
            drill.tempfile.tempdir = previous
        self.assertIsNone(args)
        self.assertIn("TMPDIR", error)

    def test_persona_home_counts_as_a_runtime_directory(self):
        runtime = self.tmp / "elsewhere"
        (runtime / "bin").mkdir(parents=True)
        binary = runtime / "bin" / "personad"
        binary.write_bytes(elf())
        self.environment["PERSONA_HOME"] = str(runtime)
        args, error = self.parse("--execute", "--binary", str(binary))
        self.assertIsNone(args)
        self.assertIn("runtime directory", error)
        args, error = self.parse("--execute", "--binary", str(self.binary), "--work-dir", str(runtime))
        self.assertIsNone(args)
        self.assertIn("--work-dir", error)

    def test_a_dynamic_or_foreign_binary_is_refused(self):
        dynamic = self.tmp / "dynamic"
        dynamic.write_bytes(elf(interpreter=True))
        script = self.tmp / "script"
        script.write_text("#!/bin/sh\n")
        foreign = self.tmp / "foreign"
        foreign.write_bytes(elf(machine=40))  # 32-bit ARM's e_machine
        for binary, reason in ((dynamic, "dynamically linked"), (script, "not an ELF"), (foreign, "architecture")):
            with self.subTest(reason=reason):
                args, error = self.parse("--execute", "--binary", str(binary))
                self.assertIsNone(args)
                self.assertIn(reason, error)

    def test_an_existing_report_is_never_overwritten(self):
        report = self.tmp / "report.json"
        report.write_text("{}")
        args, error = self.parse("--execute", "--binary", str(self.binary), "--report", str(report))
        self.assertIsNone(args)
        self.assertIn("never overwrites", error)
        args, error = self.parse("--execute", "--binary", str(self.binary), "--report", str(self.tmp / "no" / "r.json"))
        self.assertIsNone(args)
        self.assertIn("does not exist", error)

    def test_limits(self):
        for extra in (["--timeout", "5"], ["--step-timeout", "0"], ["--image", "bad image"], ["--docker", ""],
                      ["--fault", "readonly-bypassed", "--checks", "verify-only"], ["--fault", "unknown"]):
            with self.subTest(extra=extra):
                args, _ = self.parse("--execute", "--binary", str(self.binary), *extra)
                self.assertIsNone(args)


def snapshot():
    hub = {"schema": [[1, "0001_init.sql", "hub"]], "client_ops": [
        {"id": "op-1", "state": "pending", "result": None, "updated_at": "t1"},
        {"id": "op-2", "state": "unknown", "result": None, "updated_at": "t2"},
        {"id": "op-3", "state": "done", "result": "r", "updated_at": "t3"}]}
    manager = {"schema": [[1, "0001_init.sql", "manager"]], "sealing_key": ["fingerprint"],
               "tasks": [{"id": "t-1", "done_at": None}, {"id": "t-2", "done_at": "t9"}],
               "agent_links": [{"todo_id": "t-1", "released_at": None}],
               "approvals": [
                   {"id": "a-1", "status": "pending", "resolution": None, "resolved_at": None,
                    "user_notified_at": None, "auto_authorized_at": None},
                   {"id": "a-2", "status": "resolved", "resolution": "r", "resolved_at": "t5",
                    "user_notified_at": "t5", "auto_authorized_at": None}],
               "outbox": [{"seq": 1, "id": "o-1", "kind": "push"}, {"seq": 2, "id": "o-2", "kind": "broadcast"}],
               "outbox_next_seq": 2}
    return hub, manager


def failing(hub, manager):
    expected_hub, expected_manager = snapshot()
    return sorted(name for name, ok, _ in drill.compare_states(expected_hub, expected_manager, hub, manager) if not ok)


class AcceptanceTest(unittest.TestCase):
    def test_an_unchanged_restore_passes(self):
        self.assertEqual(failing(*snapshot()), [])

    def test_state_changes_are_caught(self):
        cases = {
            "approval resolved": (lambda h, m: m["approvals"][0].update(status="resolved", resolution="r"),
                                  ["approvals", "pending_approvals_unresolved"]),
            "approval notified": (lambda h, m: m["approvals"][0].update(user_notified_at="t"),
                                  ["approvals", "pending_approvals_unresolved"]),
            "approval expired": (lambda h, m: m["approvals"][0].update(status="expired"),
                                 ["approvals", "pending_approvals_unresolved"]),
            "outbox drained": (lambda h, m: m["outbox"].pop(0), ["outbox_order"]),
            "outbox reordered": (lambda h, m: m["outbox"].reverse(), ["outbox_order"]),
            "outbox sequence used": (lambda h, m: m.update(outbox_next_seq=3), ["outbox_sequence"]),
            "client op settled": (lambda h, m: h["client_ops"][0].update(state="done"), ["client_ops"]),
            "client op acknowledged": (lambda h, m: h["client_ops"][1].update(updated_at="t9"), ["client_ops"]),
            "new operation id": (lambda h, m: h["client_ops"].append({"id": "op-9", "state": "pending"}),
                                 ["client_ops", "client_ops_no_new_ids"]),
            "task finished": (lambda h, m: m["tasks"][0].update(done_at="t"), ["tasks"]),
            "agent released": (lambda h, m: m["agent_links"][0].update(released_at="t"), ["agent_links"]),
            "schema drift": (lambda h, m: m["schema"].append([2, "0002_x.sql", "manager"]), ["manager_schema_identity"]),
            "sealing key replaced": (lambda h, m: m.update(sealing_key=["other"]), ["manager_sealing_key_record"]),
        }
        for name, (change, expected) in cases.items():
            with self.subTest(name):
                hub, manager = copy.deepcopy(snapshot())
                change(hub, manager)
                self.assertEqual(failing(hub, manager), sorted(expected))

    def test_no_pending_approval_is_not_a_pass(self):
        hub, manager = snapshot()
        expected_hub, expected_manager = copy.deepcopy((hub, manager))
        for state in (expected_manager, manager):
            state["approvals"] = [row for row in state["approvals"] if row["status"] != "pending"]
        checks = {name: ok for name, ok, _ in drill.compare_states(expected_hub, expected_manager, hub, manager)}
        self.assertFalse(checks["pending_approvals_unresolved"])

    def test_check_details_hold_counts_not_content(self):
        hub, manager = snapshot()
        manager["approvals"][0]["status"] = "resolved"
        for _, _, detail in drill.compare_states(*snapshot(), hub, manager):
            self.assertNotIn("a-1", detail)
            self.assertNotIn("o-1", detail)

    def test_the_guarded_write_must_fail_as_read_only(self):
        self.assertTrue(drill.guarded_write_rejected(
            1, "ERROR:  25006: cannot execute UPDATE in a read-only transaction\nLOCATION:  PreventCommandIfReadOnly"))
        self.assertFalse(drill.guarded_write_rejected(0, ""))
        self.assertFalse(drill.guarded_write_rejected(1, "ERROR:  42P01: relation \"interactions\" does not exist"))
        self.assertFalse(drill.guarded_write_rejected(1, "psql: error: connection refused"))
        self.assertFalse(drill.guarded_write_rejected(1, "ERROR:  42501: permission denied; not a read-only transaction"))

    def test_tree_digest(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            (base / "a").mkdir()
            (base / "a" / "one.md").write_text("one")
            first = drill.tree_digest(base)
            self.assertEqual(first["files"], 1)
            (base / "a" / "one.md").write_text("two")
            self.assertNotEqual(drill.tree_digest(base), first)
            (base / "a" / "one.md").write_text("one")
            self.assertEqual(drill.tree_digest(base), first)
            (base / "a" / "one.md").rename(base / "one.md")
            self.assertNotEqual(drill.tree_digest(base), first)
        self.assertIsNone(drill.tree_digest(Path(root)))

    def test_key_fingerprint_matches_secretkey(self):
        # Known answer from server/internal/secretkey.Fingerprint for the key 00 01 02 … 1f.
        self.assertEqual(drill.key_fingerprint(bytes(range(32))), "577562a1d8d8b402fa2b69a7ca995e68")
        self.assertNotEqual(drill.key_fingerprint(b"k" * 32), drill.key_fingerprint(b"w" * 32))

    def test_error_detail_is_the_error_line(self):
        verbose = ("psql:<stdin>:3: ERROR:  23505: duplicate key value violates unique constraint\n"
                   "DETAIL:  Key (id)=(x) already exists.\nLOCATION:  _bt_check_unique, nbtinsert.c:666\n")
        self.assertIn("23505", drill.error_detail(verbose))
        self.assertNotIn("LOCATION", drill.error_detail(verbose))
        self.assertEqual(drill.error_detail("first\nSHA-256 mismatch for db/manager.dump\n"),
                         ": SHA-256 mismatch for db/manager.dump")
        self.assertEqual(drill.error_detail(""), "")


class RedactionTest(unittest.TestCase):
    def test_redact(self):
        text = "failed: postgres://postgres:s3cret@db:5432/x at /tmp/persona-restore-drill-abc/bundle token s3cret\nmore"
        clean = drill.redact(text, ["s3cret"], ["/tmp/persona-restore-drill-abc"])
        self.assertNotIn("s3cret", clean)
        self.assertNotIn("postgres://", clean)
        self.assertNotIn("/tmp/persona-restore-drill-abc", clean)
        self.assertNotIn("\n", clean)
        self.assertLessEqual(len(drill.redact("x" * 1000)), 300)

    def test_report_is_clean(self):
        self.assertTrue(drill.report_is_clean({"verdict": "PASS", "checks": [{"detail": "3 files"}]}, ["s3cret"]))
        self.assertFalse(drill.report_is_clean({"error": "https://example.com/x"}, []))
        self.assertFalse(drill.report_is_clean({"error": "token s3cret"}, ["s3cret"]))

    def test_state_counts_hold_no_ids(self):
        counts = drill.state_counts(*snapshot())
        raw = json.dumps(counts)
        for content in ("op-1", "a-1", "o-1", "t-1"):
            self.assertNotIn(content, raw)
        self.assertEqual(counts["approvals"], {"pending": 1, "resolved": 1})
        self.assertEqual(counts["client_ops"], {"done": 1, "pending": 1, "unknown": 1})


class ProcessTest(Fixture):
    """The drill against a fake docker: timeouts, failures and interrupts all end in a complete cleanup."""

    def setUp(self):
        super().setUp()
        # Drill.run leaves SIGINT and SIGTERM ignored for main() to print its report; give them back.
        handlers = {sig: signal.getsignal(sig) for sig in (signal.SIGINT, signal.SIGTERM)}
        self.addCleanup(lambda: [signal.signal(sig, handler) for sig, handler in handlers.items()])
        self.state = self.tmp / "state"
        self.state.mkdir()
        self.work = self.tmp / "work"
        self.work.mkdir()
        (self.tmp / "fake_docker.py").write_text(FAKE_DOCKER)

    def docker(self, mode):
        return shlex.join([sys.executable, str(self.tmp / "fake_docker.py"), str(self.state), mode])

    def argv(self, mode, *extra):
        return ["--execute", "--binary", str(self.binary), "--docker", self.docker(mode), "--work-dir", str(self.work),
                "--timeout", "30", *extra]

    def calls(self):
        return [json.loads(line) for line in (self.state / "calls.log").read_text().splitlines()]

    def assert_cleaned_up(self, report):
        self.assertEqual(report["cleanup"]["ok"], True, report["cleanup"])
        self.assertEqual(list(self.work.iterdir()), [], "temporary files left behind")
        self.assertFalse((self.state / "network").exists(), "network left behind")
        self.assertEqual(list(self.state.glob("container-*")), [], "container left behind")
        self.assertIs(report["services_started"], False)
        pid_file = self.state / "hung.pid"
        if pid_file.exists():
            with self.assertRaises(ProcessLookupError, msg="a timed-out command still runs"):
                for _ in range(50):
                    os.kill(int(pid_file.read_text()), 0)
                    time.sleep(0.1)

    def test_a_hung_step_times_out_and_everything_is_removed(self):
        args = drill.parse_args(self.argv("hang-ready", "--step-timeout", "2"), self.environment)
        started = time.monotonic()
        report = drill.Drill(args, self.environment).run()
        self.assertLess(time.monotonic() - started, 25)
        self.assertEqual(report["verdict"], "TIMEOUT")
        self.assertEqual(report["failed_phase"], "setup")
        self.assert_cleaned_up(report)
        network = [call for call in self.calls() if call[:2] == ["network", "create"]]
        self.assertEqual(len(network), 1)
        self.assertIn("--internal", network[0])
        run = [call for call in self.calls() if call[0] == "run"][0]
        self.assertNotIn("--publish", run)
        self.assertNotIn("-p", run)
        self.assertIn("never", run[run.index("--pull") + 1:run.index("--pull") + 2])
        self.assertFalse(any(drill.URL_PATTERN.search(" ".join(call)) for call in self.calls()))

    def test_a_failed_start_is_redacted_and_cleaned_up(self):
        args = drill.parse_args(self.argv("fail-run"), self.environment)
        instance = drill.Drill(args, self.environment)
        report = instance.run()
        self.assertEqual(report["verdict"], "FAIL")
        self.assertEqual(report["failed_phase"], "setup")
        raw = json.dumps(report)
        self.assertNotIn(instance.password, raw)
        self.assertNotIn("postgres://", raw)
        self.assertNotIn(str(self.work), raw)
        self.assertIn("could not start", report["error"])
        self.assert_cleaned_up(report)
        self.assertFalse(any(instance.password in arg for call in self.calls() for arg in call),
                         "the database password was on a command line")

    def test_a_binary_for_another_architecture_stops_before_any_resource(self):
        arm = self.tmp / "personad-arm64"
        arm.write_bytes(elf(machine=183))
        args = drill.parse_args(["--execute", "--binary", str(arm), "--docker", self.docker("hang-ready"),
                                 "--work-dir", str(self.work), "--timeout", "30"], self.environment)
        report = drill.Drill(args, self.environment).run()
        self.assertEqual(report["verdict"], "FAIL")
        self.assertIn("architecture", report["error"])
        self.assertFalse(any(call[:2] == ["network", "create"] for call in self.calls()))
        self.assert_cleaned_up(report)

    def test_an_interrupt_cleans_up(self):
        report_path = self.tmp / "report.json"
        process = subprocess.Popen([sys.executable, str(DRILL), *self.argv("interrupt", "--report", str(report_path))],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                   env={**self.environment, "PATH": os.environ.get("PATH", "")})
        try:
            deadline = time.monotonic() + 20
            while not (self.state / "hung.pid").exists():
                self.assertLess(time.monotonic(), deadline, "the fake never hung")
                time.sleep(0.05)
            process.send_signal(signal.SIGTERM)
            self.assertEqual(process.wait(timeout=30), 1)
        finally:
            if process.poll() is None:
                process.kill()
        report = json.loads(report_path.read_text())
        self.assertEqual(report["verdict"], "INTERRUPTED")
        self.assert_cleaned_up(report)


def integration():
    binary = os.environ.get("PERSONA_RESTORE_DRILL_BINARY")
    if not binary:
        raise unittest.SkipTest("set PERSONA_RESTORE_DRILL_BINARY to a static Linux personad (and "
                                "PERSONA_RESTORE_DRILL_DOCKER, e.g. \"sudo -n docker\") to run the real drill; "
                                "skipped, so nothing about a real restore was verified")
    return binary, os.environ.get("PERSONA_RESTORE_DRILL_DOCKER", "docker")


class RealDrillTest(unittest.TestCase):
    """The real drill: Docker, PostgreSQL 18 and the caller's personad. Opt-in only."""

    @classmethod
    def setUpClass(cls):
        cls.binary, cls.docker = integration()

    def drill(self, *extra):
        with tempfile.TemporaryDirectory(prefix="drill-it-") as directory:
            report_path = Path(directory) / "report.json"
            result = subprocess.run([sys.executable, str(DRILL), "--execute", "--binary", self.binary, "--docker",
                                     self.docker, "--report", str(report_path), *extra],
                                    capture_output=True, text=True, timeout=1200)
            report = json.loads(report_path.read_text())
        self.assertEqual(report["cleanup"]["ok"], True, report["cleanup"])
        leftovers = subprocess.run([*shlex.split(self.docker), "ps", "--all", "--quiet", "--filter",
                                    "label=" + drill.LABEL + "=" + report["run"]], capture_output=True, text=True,
                                   timeout=60).stdout.strip()
        self.assertEqual(leftovers, "", "containers left behind")
        networks = subprocess.run([*shlex.split(self.docker), "network", "ls", "--quiet", "--filter",
                                   "label=" + drill.LABEL + "=" + report["run"]], capture_output=True, text=True,
                                  timeout=60).stdout.strip()
        self.assertEqual(networks, "", "network left behind")
        raw = json.dumps(report) + result.stdout
        self.assertIsNone(drill.URL_PATTERN.search(raw))
        self.assertIsNone(re.search(r"\b[0-9a-f]{48,}\b", raw), "a secret-looking value was printed")
        self.assertIs(report["services_started"], False)
        return result.returncode, report

    def failed_checks(self, report):
        return sorted(check["name"] for check in report["checks"] if not check["ok"])

    def test_the_restore_passes_every_read_only_check(self):
        code, report = self.drill()
        self.assertEqual((code, report["verdict"]), (0, "PASS"), report.get("error"))
        names = {check["name"] for check in report["checks"]}
        for required in ("read_only_sessions", "pending_approvals_unresolved", "outbox_order", "client_ops",
                         "client_ops_no_new_ids", "tasks", "agent_links", "manager_key_opens_its_database",
                         "knowledge_base_digest", "guarded_write_refused", "guarded_row_unchanged",
                         "no_service_sessions", "previous_databases_preserved"):
            self.assertIn(required, names)
        self.assertEqual(self.failed_checks(report), [])
        self.assertEqual(report["restored"], report["fixture"])
        self.assertTrue(report["data_point"]["backup_created_at"])

    def test_backup_verify_alone_misses_what_the_drill_catches(self):
        # Before: create, verify, restore and verify again all pass over a restore whose approval was resolved.
        code, report = self.drill("--checks", "verify-only", "--fault", "approval-resolved")
        self.assertEqual((code, report["verdict"]), (0, "PASS"), report.get("error"))
        self.assertNotIn("read_only_acceptance", [phase["name"] for phase in report["phases"]])
        # After: the read-only acceptance fails it.
        code, report = self.drill("--fault", "approval-resolved")
        self.assertEqual((code, report["verdict"]), (1, "FAIL"))
        self.assertIn("pending_approvals_unresolved", self.failed_checks(report))

    def test_each_fault_fails_and_cleans_up(self):
        expected = {
            "restore-fails": ("FAIL", "backup_restore", []),
            "kb-drift": ("FAIL", "read_only_acceptance", ["knowledge_base_digest"]),
            "approval-resolved": ("FAIL", "read_only_acceptance", ["approvals", "pending_approvals_unresolved"]),
            "outbox-settled": ("FAIL", "read_only_acceptance", ["outbox_order"]),
            "client-op-settled": ("FAIL", "read_only_acceptance", ["client_ops"]),
            "readonly-bypassed": ("FAIL", "read_only_acceptance", ["guarded_row_unchanged", "guarded_write_refused"]),
            "timeout": ("TIMEOUT", "backup_create", []),
        }
        for fault, (verdict, phase, checks) in expected.items():
            with self.subTest(fault=fault):
                extra = ["--step-timeout", "10"] if fault == "timeout" else []
                code, report = self.drill("--fault", fault, *extra)
                self.assertEqual(code, 1)
                self.assertEqual(report["verdict"], verdict, report.get("error"))
                self.assertEqual(report["failed_phase"], phase)
                self.assertEqual(self.failed_checks(report), checks)


if __name__ == "__main__":
    unittest.main()
