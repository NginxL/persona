#!/usr/bin/env python3
"""Isolated whole-install restore drill: real `personad backup`, read-only acceptance, nothing started.

It builds a synthetic install in a disposable PostgreSQL container (the checkout's real migrations, synthetic keys,
knowledge base, tasks, approvals, manager outbox and client operations), runs the caller's static Linux personad
for `backup create`, `verify` and `restore`, and then checks the restored install offline: every database session is
`default_transaction_read_only=on`, and the restored hub and built-in manager are never started (starting them would
recover client operations, expire interactions and drain the outbox, which is the state under test).

It takes no hub URL, database URL, runtime directory or bundle: everything it touches it creates, on an `--internal`
Docker network with no published ports, and removes again on success, failure, timeout and interruption. The printed
report has phase timings, check results and state counts only (docs/server/server.md "Restore drill").
"""
import argparse
import datetime
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import shutil
import signal
import struct
import subprocess
import sys
import tarfile
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
MIGRATIONS = ROOT / "server" / "internal" / "db" / "migrations"
sys.path.insert(0, str(Path(__file__).resolve().parent))
from server_test_env import clean_environment  # noqa: E402

DEFAULT_IMAGE = "postgres:18-alpine"
LABEL = "persona.restore-drill"
# Faults the drill's own tests inject to prove each failure is caught; never needed for a real drill.
FAULTS = ("restore-fails", "kb-drift", "approval-resolved", "outbox-settled", "client-op-settled",
          "readonly-bypassed", "timeout")
MIGRATION_NAME = re.compile(r"^(\d+)_[a-z0-9_]+\.sql$")
IMAGE_NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._/:@-]*$")  # a reference, never an option
READ_ONLY_SQLSTATE = "25006"
ELF_MACHINES = {"amd64": 62, "arm64": 183}  # Docker's architecture names: ELF e_machine
KEY_FINGERPRINT_LABEL = b"persona secret.key fingerprint v1"  # secretkey.Fingerprint
SERVICE_APPLICATIONS = ("persona-hub", "persona-manager")  # db.AppHub, db.AppManager


class DrillError(Exception):
    """A drill step failed; the message is already safe to print."""


class DrillTimeout(DrillError):
    pass


# ---------------------------------------------------------------------------------------------------------------------
# Inputs


RUNTIME_MARKERS = ("hub.json", "secret.key")


def runtime_roots(environment=os.environ):
    """Runtime directories named by the environment: the drill accepts nothing from inside them."""
    roots = [Path(environment.get("HOME", "~")).expanduser() / ".persona"]
    if environment.get("PERSONA_HOME"):
        roots.append(Path(environment["PERSONA_HOME"]).expanduser())
    return [root.resolve() for root in roots]


def inside(path, roots):
    """path is in a runtime directory: one of roots, or any directory holding a hub.json or secret.key (which is also
    how an install from before the rename is recognised)."""
    if any(path == root or root in path.parents for root in roots):
        return True
    return any((directory / marker).is_file() for directory in (path, *path.parents)
               if directory != Path(directory.anchor) for marker in RUNTIME_MARKERS)


def static_linux_executable(path):
    """Why path is not a static Linux ELF executable, or None. A dynamic one would not run in the Alpine client image."""
    try:
        with open(path, "rb") as source:
            header = source.read(64)
            if len(header) < 64 or header[:4] != b"\x7fELF":
                return "is not an ELF executable"
            if header[4] != 2 or header[5] != 1:
                return "is not a 64-bit little-endian ELF executable"
            if header[7] not in (0, 3):
                return "is not a Linux ELF executable"
            if struct.unpack_from("<H", header, 18)[0] not in ELF_MACHINES.values():
                return "is for an architecture the drill does not know"
            phoff, = struct.unpack_from("<Q", header, 32)
            phentsize, phnum = struct.unpack_from("<HH", header, 54)
            if phentsize < 4:
                return "has no readable program headers"
            for index in range(phnum):
                source.seek(phoff + index * phentsize)
                entry = source.read(4)
                if len(entry) < 4:
                    return "has a truncated program header table"
                if struct.unpack("<I", entry)[0] == 3:  # PT_INTERP: needs a dynamic loader
                    return "is dynamically linked; build it with CGO_ENABLED=0"
    except OSError as error:
        return "cannot be read (" + error.__class__.__name__ + ")"
    return None


def elf_machine(path):
    with open(path, "rb") as source:
        header = source.read(20)
    return struct.unpack_from("<H", header, 18)[0]


def parse_args(argv, environment=os.environ):
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter, allow_abbrev=False)
    parser.add_argument("--execute", action="store_true",
                        help="run the drill (it starts Docker containers); without it nothing runs")
    parser.add_argument("--binary", help="static Linux personad to drill (copied; never a runtime directory's)")
    parser.add_argument("--docker", default="docker",
                        help="the docker command, split like a shell would (for example \"sudo -n docker\")")
    parser.add_argument("--image", default=DEFAULT_IMAGE,
                        help="local PostgreSQL image for the server and the clients; never pulled (default: %(default)s)")
    parser.add_argument("--work-dir", help="parent of the drill's private temporary directory (default: TMPDIR)")
    parser.add_argument("--report", help="also write the JSON report to this new file")
    parser.add_argument("--timeout", type=float, default=900, help="whole drill, in seconds (default: %(default)s)")
    parser.add_argument("--step-timeout", type=float, default=300,
                        help="each command, in seconds (default: %(default)s)")
    parser.add_argument("--checks", choices=("full", "verify-only"), default="full",
                        help="verify-only stops after the second backup verify (what backup verify alone proves)")
    parser.add_argument("--fault", choices=FAULTS, help="inject a fault (the drill's own tests)")
    args = parser.parse_args(argv)
    if not args.execute:
        parser.error("the drill starts Docker containers and runs nothing without --execute")
    if not args.binary:
        parser.error("--binary is required: a static Linux personad built from this checkout")
    roots = runtime_roots(environment)
    binary = Path(args.binary).expanduser().resolve()
    if inside(binary, roots):
        parser.error("--binary is inside a Persona runtime directory; copy a build of this checkout elsewhere")
    if not binary.is_file():
        parser.error("--binary is not a file")
    reason = static_linux_executable(binary)
    if reason:
        parser.error("--binary " + reason)
    args.binary = binary
    if not IMAGE_NAME.match(args.image):
        parser.error("--image is not an image reference")
    try:
        args.docker = shlex.split(args.docker)
    except ValueError:
        parser.error("--docker cannot be split into a command")
    if not args.docker:
        parser.error("--docker is empty")
    if args.work_dir:
        work = Path(args.work_dir).expanduser().resolve()
        if not work.is_dir():
            parser.error("--work-dir is not a directory")
        if inside(work, roots):
            parser.error("--work-dir is inside a Persona runtime directory")
        args.work_dir = work
    elif inside(Path(tempfile.gettempdir()).resolve(), roots):
        parser.error("TMPDIR is inside a Persona runtime directory; pass --work-dir")
    if args.report:
        report = Path(args.report).expanduser().resolve()
        if report.exists():
            parser.error("--report exists; the drill never overwrites a file")
        if not report.parent.is_dir():
            parser.error("--report's directory does not exist")
        if inside(report, roots):
            parser.error("--report is inside a Persona runtime directory")
        args.report = report
    if not 30 <= args.timeout <= 7200 or not 1 <= args.step_timeout <= 3600:
        parser.error("--timeout must be 30..7200 seconds and --step-timeout 1..3600")
    if args.fault == "readonly-bypassed" and args.checks != "full":
        parser.error("--fault readonly-bypassed needs --checks full")
    if not MIGRATIONS.is_dir():
        parser.error("this checkout has no server/internal/db/migrations")
    return args


# ---------------------------------------------------------------------------------------------------------------------
# Pure checks (unit-tested without Docker)


def tree_digest(root):
    """SHA-256 over every regular file's relative path and content; None when root is missing."""
    root = Path(root)
    if not root.is_dir():
        return None
    digest = hashlib.sha256()
    count = 0
    for path in sorted(p for p in root.rglob("*") if p.is_file() and not p.is_symlink()):
        relative = path.relative_to(root).as_posix().encode()
        content = path.read_bytes()
        digest.update(len(relative).to_bytes(8, "big") + relative + len(content).to_bytes(8, "big") + content)
        count += 1
    return {"sha256": digest.hexdigest(), "files": count}


def key_fingerprint(key):
    return hmac.new(key, KEY_FINGERPRINT_LABEL, hashlib.sha256).hexdigest()[:32]


def approval_counts(approvals):
    counts = {}
    for row in approvals or []:
        counts[row["status"]] = counts.get(row["status"], 0) + 1
    return dict(sorted(counts.items()))


def state_counts(hub, manager):
    """Aggregate numbers for the report: no ids, titles, payloads or bodies."""
    ops = {}
    for row in hub.get("client_ops") or []:
        ops[row["state"]] = ops.get(row["state"], 0) + 1
    outbox = {}
    for row in manager.get("outbox") or []:
        outbox[row["kind"]] = outbox.get(row["kind"], 0) + 1
    tasks = manager.get("tasks") or []
    links = manager.get("agent_links") or []
    return {
        "approvals": approval_counts(manager.get("approvals")),
        "client_ops": dict(sorted(ops.items())),
        "outbox": dict(sorted(outbox.items())),
        "tasks": {"open": sum(1 for t in tasks if not t["done_at"]), "done": sum(1 for t in tasks if t["done_at"])},
        "agent_links": {"active": sum(1 for link in links if not link["released_at"]),
                        "released": sum(1 for link in links if link["released_at"])},
    }


def compare_states(expected_hub, expected_manager, hub, manager):
    """The acceptance checks over two snapshots: [(name, ok, detail)], detail never holding row content."""
    checks = []

    def same(name, left, right, detail=""):
        checks.append((name, left == right, detail if left == right else "differs from the backed-up state"))

    same("hub_schema_identity", expected_hub.get("schema"), hub.get("schema"),
         "%d migrations" % len(hub.get("schema") or []))
    same("manager_schema_identity", expected_manager.get("schema"), manager.get("schema"),
         "%d migrations" % len(manager.get("schema") or []))
    same("manager_sealing_key_record", expected_manager.get("sealing_key"), manager.get("sealing_key"))
    same("tasks", expected_manager.get("tasks"), manager.get("tasks"), "%d tasks" % len(manager.get("tasks") or []))
    same("agent_links", expected_manager.get("agent_links"), manager.get("agent_links"),
         "%d links" % len(manager.get("agent_links") or []))
    same("approvals", expected_manager.get("approvals"), manager.get("approvals"),
         json.dumps(approval_counts(manager.get("approvals")), sort_keys=True))
    pending = [row for row in manager.get("approvals") or [] if row["status"] == "pending"]
    untouched = all(row["resolution"] is None and row["resolved_at"] is None and row["user_notified_at"] is None
                    and row["auto_authorized_at"] is None for row in pending)
    expected_pending = sum(1 for row in expected_manager.get("approvals") or [] if row["status"] == "pending")
    checks.append(("pending_approvals_unresolved", bool(pending) and untouched and len(pending) == expected_pending,
                   "%d pending, none resolved, notified or authorized" % len(pending) if untouched
                   else "a pending approval was resolved, notified or authorized"))
    same("outbox_order", expected_manager.get("outbox"), manager.get("outbox"),
         "%d items in seq order" % len(manager.get("outbox") or []))
    same("outbox_sequence", expected_manager.get("outbox_next_seq"), manager.get("outbox_next_seq"),
         "no sequence value used")
    same("client_ops", expected_hub.get("client_ops"), hub.get("client_ops"),
         json.dumps(state_counts(hub, manager)["client_ops"], sort_keys=True))
    ids = {row["id"] for row in hub.get("client_ops") or []}
    expected_ids = {row["id"] for row in expected_hub.get("client_ops") or []}
    checks.append(("client_ops_no_new_ids", ids == expected_ids, "%d operation ids" % len(ids)))
    return checks


def guarded_write_rejected(returncode, stderr):
    """A write in a read-only session must fail for that reason (SQLSTATE 25006), not any other."""
    return returncode != 0 and re.search(r"\bERROR:\s+" + READ_ONLY_SQLSTATE + r":", stderr) is not None


URL_PATTERN = re.compile(r"[A-Za-z][A-Za-z0-9+.-]*://\S+")


def redact(text, secrets_list=(), paths=()):
    """What may be printed of a failure: no URL, secret value or private path, one short line."""
    text = str(text)
    for secret in sorted((s for s in secrets_list if s), key=len, reverse=True):
        text = text.replace(secret, "<redacted>")
    for path in sorted((str(p) for p in paths if p), key=len, reverse=True):
        text = text.replace(path, "<drill>")
    text = URL_PATTERN.sub("<url>", text)
    text = " ".join(text.split())
    return text[:300]


def report_is_clean(report, secrets_list):
    """The report holds no secret, URL or key material (a last line of defense under redact)."""
    raw = json.dumps(report)
    if URL_PATTERN.search(raw):
        return False
    return not any(secret and secret in raw for secret in secrets_list)


# ---------------------------------------------------------------------------------------------------------------------
# Processes


class Processes:
    """Runs every command in its own process group with a deadline, and kills the group when it passes."""

    def __init__(self, deadline, step_timeout, environment):
        self.deadline = deadline
        self.step_timeout = step_timeout
        self.environment = environment
        self.running = set()

    def remaining(self, limit=None):
        limit = self.step_timeout if limit is None else min(limit, self.step_timeout)
        return max(0.0, min(limit, self.deadline - time.monotonic()))

    def run(self, command, label, stdin=None, timeout=None, check=True, cleanup=False):
        seconds = (min(timeout or 60, 60) if cleanup else self.remaining(timeout))
        if seconds <= 0:
            raise DrillTimeout(label + " timed out (drill deadline)")
        process = subprocess.Popen(command, stdin=subprocess.PIPE if stdin is not None else subprocess.DEVNULL,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=self.environment,
                                   start_new_session=True)
        self.running.add(process)
        try:
            stdout, stderr = process.communicate(stdin.encode() if stdin is not None else None, timeout=seconds)
        except subprocess.TimeoutExpired:
            self.kill(process)
            raise DrillTimeout(label + " timed out after %.0fs" % seconds) from None
        except BaseException:  # an interrupt: the group runs in its own session and would not see it
            self.kill(process)
            raise
        finally:
            for stream in (process.stdin, process.stdout, process.stderr):
                if stream is not None:
                    stream.close()
            self.running.discard(process)
        result = subprocess.CompletedProcess(command, process.returncode, stdout.decode(errors="replace"),
                                             stderr.decode(errors="replace"))
        if check and result.returncode != 0:
            raise DrillError(label + " failed (exit %d)" % result.returncode, result.stderr)
        return result

    @staticmethod
    def kill(process):
        for sig in (signal.SIGTERM, signal.SIGKILL):
            try:
                os.killpg(process.pid, sig)
            except ProcessLookupError:
                return
            except PermissionError:  # a group this user may not signal (a privileged wrapper): the leader at least
                try:
                    os.kill(process.pid, sig)
                except OSError:
                    pass
            try:
                process.wait(timeout=5)
                return
            except subprocess.TimeoutExpired:
                continue

    def kill_all(self):
        for process in list(self.running):
            self.kill(process)
        self.running.clear()


# ---------------------------------------------------------------------------------------------------------------------
# The drill


class Drill:
    def __init__(self, args, environment=None):
        self.args = args
        self.run_id = secrets.token_hex(6)
        self.network = "persona-drill-" + self.run_id
        self.postgres = "persona-drill-pg-" + self.run_id
        self.containers = []
        self.network_created = False
        self.root = None
        self.password = secrets.token_hex(24)
        self.hub_token = secrets.token_hex(24)
        self.hub_db = "drill_" + self.run_id
        self.manager_db = self.hub_db + "_manager"
        self.hub_key = secrets.token_bytes(32)
        self.manager_key = secrets.token_bytes(32)
        self.secrets = [self.password, self.hub_token, self.hub_key.hex(), self.manager_key.hex()]
        environment = clean_environment(dict(os.environ if environment is None else environment))
        self.processes = Processes(time.monotonic() + args.timeout, args.step_timeout, environment)
        self.phases = []
        self.checks = []
        self.report = {
            "drill": "persona-whole-install-restore", "run": self.run_id, "verdict": "FAIL", "checks_mode": args.checks,
            "fault": args.fault, "services_started": False, "phases": self.phases, "checks": self.checks,
        }
        self.cli_count = 0

    # -- docker helpers

    def docker(self, *arguments, label, stdin=None, check=True, timeout=None, cleanup=False):
        return self.processes.run([*self.args.docker, *arguments], label, stdin=stdin, check=check, timeout=timeout,
                                  cleanup=cleanup)

    def removed(self, kind, name):
        """Remove a container or network (best effort, never raising) and say whether it is gone."""
        try:
            if kind == "container":
                self.docker("rm", "--force", "--volumes", name, label="remove container", check=False, cleanup=True)
            else:
                self.docker("network", "rm", name, label="remove network", check=False, cleanup=True)
            return self.docker(kind, "inspect", name, label="inspect", check=False, cleanup=True).returncode != 0
        except (DrillError, OSError, KeyboardInterrupt):
            return False

    def psql(self, database, sql, label, read_only=True, check=True):
        """psql inside the server container over its loopback; read-only sessions refuse every write. Errors are
        verbose, so they carry their SQLSTATE."""
        options = ["--env", "PGOPTIONS=-c default_transaction_read_only=on"] if read_only else []
        return self.docker("exec", "--interactive", *options, self.postgres, "psql", "--no-psqlrc", "--quiet",
                           "--tuples-only", "--no-align", "--set", "ON_ERROR_STOP=1", "--set", "VERBOSITY=verbose",
                           "--host", "127.0.0.1",
                           "--username", "postgres", "--dbname", database, "--file", "-",
                           label=label, stdin=sql, check=check)

    def query_json(self, database, sql, label):
        output = self.psql(database, sql, label).stdout.strip()
        try:
            return json.loads(output)
        except ValueError:
            raise DrillError(label + " did not return JSON") from None

    def personad(self, label, *arguments, entrypoint=None):
        """The caller's personad in a throwaway client container on the internal network (pg_dump/pg_restore 18)."""
        self.cli_count += 1
        name = "persona-drill-cli-%s-%d" % (self.run_id, self.cli_count)
        self.containers.append(name)
        command = ["run", "--rm", "--pull", "never", "--name", name, "--label", LABEL + "=" + self.run_id,
                   "--network", self.network, "--user", "%d:%d" % (os.getuid(), os.getgid()),
                   "--volume", str(self.root) + ":/drill", "--volume", str(self.root / "bin") + ":/drill-bin:ro",
                   "--env", "HOME=/drill/cli-home", "--env", "TMPDIR=/drill/tmp",
                   "--entrypoint", entrypoint or "/drill-bin/personad", self.args.image, *arguments]
        try:
            return self.docker(*command, label=label)
        finally:
            # --rm removes it when it exits; after a timeout the client is gone but the container may run on.
            if self.removed("container", name):
                self.containers.remove(name)

    # -- phases

    def phase(self, name, function):
        started = time.monotonic()
        entry = {"name": name, "ok": False}
        self.phases.append(entry)
        try:
            result = function()
            entry["ok"] = True
            return result
        finally:
            entry["seconds"] = round(time.monotonic() - started, 2)

    def check(self, name, ok, detail=""):
        self.checks.append({"name": name, "ok": bool(ok), "detail": detail})

    def setup(self):
        parent = str(self.args.work_dir) if self.args.work_dir else None
        self.root = Path(tempfile.mkdtemp(prefix="persona-restore-drill-", dir=parent))
        os.chmod(self.root, 0o700)
        for name in ("bin", "cli-home", "tmp", "bundle", "target/home"):
            (self.root / name).mkdir(parents=True, mode=0o700)
        shutil.copyfile(self.args.binary, self.root / "bin" / "personad")
        (self.root / "bin" / "personad").chmod(0o755)
        os.chmod(self.root / "bin", 0o755)
        if self.docker("image", "inspect", self.args.image, label="image", check=False).returncode != 0:
            raise DrillError("the image is not on this machine; the drill never pulls (docker pull it first)")
        architecture = self.docker("version", "--format", "{{.Server.Arch}}", label="docker architecture").stdout
        if ELF_MACHINES.get(architecture.strip()) != elf_machine(self.args.binary):
            raise DrillError("--binary is not built for the Docker server's architecture (" + architecture.strip() + ")")
        self.docker("network", "create", "--internal", "--label", LABEL + "=" + self.run_id, self.network,
                    label="internal network")
        self.network_created = True
        self.containers.append(self.postgres)
        environment = self.root / "postgres.env"  # the password stays out of every command line
        write_private(environment, "POSTGRES_PASSWORD=" + self.password + "\n")
        self.docker("run", "--detach", "--pull", "never", "--name", self.postgres, "--label", LABEL + "=" + self.run_id,
                    "--network", self.network, "--network-alias", "db", "--env-file", str(environment),
                    "--volume", str(MIGRATIONS) + ":/drill-migrations:ro", self.args.image, label="database container")
        environment.unlink()
        deadline = time.monotonic() + min(90, self.processes.remaining())
        while True:
            try:
                ready = self.docker("exec", self.postgres, "psql", "--no-psqlrc", "--host", "127.0.0.1", "--username",
                                    "postgres", "--dbname", "postgres", "--tuples-only", "--command", "SELECT 1",
                                    label="database readiness", check=False, timeout=10).returncode == 0
            except DrillTimeout:  # one slow attempt while the server starts; the loop's own deadline decides
                ready = False
            if ready:
                break
            if time.monotonic() >= deadline:
                raise DrillTimeout("database readiness timed out")
            time.sleep(0.5)
        published = self.docker("port", self.postgres, label="published ports").stdout.strip()
        self.check("no_published_ports", published == "", "the database container publishes no port")
        if published:
            raise DrillError("the database container publishes a port")

    def migration_script(self, part):
        lines = ["CREATE TABLE IF NOT EXISTS schema_migrations (version int NOT NULL, name text NOT NULL, "
                 "applied_at timestamptz NOT NULL DEFAULT now(), part text);",
                 "CREATE UNIQUE INDEX IF NOT EXISTS schema_migrations_version_part "
                 "ON schema_migrations (version, (coalesce(part, '')));"]
        files = sorted(p.name for p in (MIGRATIONS / part).iterdir() if p.suffix == ".sql")
        if not files:
            raise DrillError("no " + part + " migrations in this checkout")
        for name in files:
            match = MIGRATION_NAME.match(name)
            if not match:
                raise DrillError("unexpected migration file name")
            # What db.migrateSet does on a new database: each file in a transaction, recorded with its part.
            lines += ["BEGIN;", "\\i /drill-migrations/%s/%s" % (part, name),
                      "INSERT INTO schema_migrations (version, name, part) VALUES (%d, '%s', '%s');"
                      % (int(match.group(1)), name, part), "COMMIT;"]
        return "\n".join(lines) + "\n", len(files)

    def fixture(self):
        for database in (self.hub_db, self.manager_db):
            self.psql("postgres", "CREATE DATABASE %s;" % database, "create database", read_only=False)
        for database, part in ((self.hub_db, "hub"), (self.manager_db, "manager")):
            script, _ = self.migration_script(part)
            self.psql(database, script, part + " migrations", read_only=False)
        self.psql(self.hub_db, HUB_FIXTURE, "hub fixture", read_only=False)
        self.psql(self.manager_db, MANAGER_FIXTURE.replace("{fingerprint}", key_fingerprint(self.manager_key)),
                  "manager fixture", read_only=False)
        source = self.root / "source" / "home"
        (source / "managers" / "assistant").mkdir(parents=True, mode=0o700)
        write_private(source / "hub.json", json.dumps({
            "database_url": "postgres://postgres:%s@db:5432/%s?sslmode=disable" % (self.password, self.hub_db),
            "token": self.hub_token, "update": {"url": "off"}, "node_packages": {"url": "off"},
            "auto_update_nodes": False}))
        write_private(source / "managers" / "assistant" / "manager.json", json.dumps({"database_name": self.manager_db}))
        write_private(source / "secret.key", self.hub_key)
        write_private(source / "managers" / "assistant" / "secret.key", self.manager_key)
        for relative, content in KNOWLEDGE_BASE.items():
            write_private(source / "data" / relative, content)
        self.expected_kb = tree_digest(source / "data")
        self.expected_hub, self.expected_manager = self.snapshot()
        self.report["data_point"] = {"markers_written_at": utc_now()}
        self.report["fixture"] = state_counts(self.expected_hub, self.expected_manager)

    def snapshot(self):
        return (self.query_json(self.hub_db, HUB_SNAPSHOT, "hub snapshot"),
                self.query_json(self.manager_db, MANAGER_SNAPSHOT, "manager snapshot"))

    def backup(self):
        if self.args.fault == "timeout":  # a client that never finishes: the step deadline must stop and remove it
            self.personad("backup create", "3600", entrypoint="sleep")
        output = self.personad("backup create", "backup", "create", "--home", "/drill/source/home", "--output",
                               "/drill/bundle/install.backup", "--writes-quiesced")
        if "created and verified" not in output.stdout:
            raise DrillError("backup create did not report a verified bundle")
        bundle = self.root / "bundle" / "install.backup"
        with tarfile.open(bundle) as archive:
            manifest = json.load(archive.extractfile("manifest.json"))
        self.report["data_point"]["backup_created_at"] = manifest.get("created_at")
        self.report["bundle"] = {"bytes": bundle.stat().st_size, "files": len(manifest.get("files") or []),
                                 "format": manifest.get("format")}

    def verify(self, home, label):
        output = self.personad(label, "backup", "verify", "--home", home, "--bundle", "/drill/bundle/install.backup")
        if "verified" not in output.stdout:
            raise DrillError(label + " did not report a verified bundle")

    def disaster(self):
        """What the restore must undo: the work in flight moved on, and the runtime directory is gone."""
        self.psql(self.manager_db, MANAGER_DISASTER, "manager disaster", read_only=False)
        self.psql(self.hub_db, HUB_DISASTER, "hub disaster", read_only=False)
        hub, manager = self.snapshot()
        changed = hub != self.expected_hub and manager != self.expected_manager
        self.check("disaster_changed_state", changed, "both databases differ from the backup before the restore")
        if not changed:
            raise DrillError("the simulated disaster changed nothing")
        shutil.rmtree(self.root / "source")
        if self.args.fault == "restore-fails":
            damage_member(self.root / "bundle" / "install.backup", "db/manager.dump")

    def restore(self):
        output = self.personad("backup restore", "backup", "restore", "--home", "/drill/target/home", "--bundle",
                               "/drill/bundle/install.backup", "--writes-quiesced")
        if "restored and verified" not in output.stdout:
            raise DrillError("backup restore did not report a verified restore")
        preserved = sum(1 for line in output.stdout.splitlines()
                        if line.startswith(("previous hub database preserved", "previous built-in manager database")))
        self.check("previous_databases_preserved", preserved == 2, "%d predecessors kept" % preserved)
        if preserved != 2:
            raise DrillError("restore did not keep both previous databases")

    def inject(self):
        """A fault after a successful restore: the acceptance below must catch it."""
        fault = self.args.fault
        if fault == "kb-drift":
            path = self.root / "target" / "home" / "data" / "notes" / "plan.md"
            path.write_bytes(path.read_bytes() + b"\ndrift\n")
        elif fault == "approval-resolved":
            self.psql(self.manager_db, "UPDATE interactions SET status = 'resolved', resolution = '{}'::jsonb, "
                      "resolved_at = now() WHERE id = 'drill-approval-1';", "inject", read_only=False)
        elif fault == "outbox-settled":
            self.psql(self.manager_db, "DELETE FROM manager_outbox WHERE id = 'drill-outbox-1';", "inject",
                      read_only=False)
        elif fault == "client-op-settled":
            self.psql(self.hub_db, "UPDATE client_ops SET state = 'done', result = '{}'::jsonb, updated_at = now() "
                      "WHERE state = 'pending';", "inject", read_only=False)

    def accept(self):
        target = self.root / "target" / "home"
        mode = self.query_json(self.hub_db, "SELECT to_json(current_setting('default_transaction_read_only'));",
                               "session mode")
        self.check("read_only_sessions", mode == "on", "default_transaction_read_only=" + str(mode))
        if mode != "on":
            raise DrillError("the acceptance session is not read-only")
        hub, manager = self.snapshot()
        for name, ok, detail in compare_states(self.expected_hub, self.expected_manager, hub, manager):
            self.check(name, ok, detail)
        self.report["restored"] = state_counts(hub, manager)
        manager_key = (target / "managers" / "assistant" / "secret.key").read_bytes()
        hub_key = (target / "secret.key").read_bytes()
        self.check("manager_key_opens_its_database",
                   manager.get("sealing_key") == [key_fingerprint(manager_key)] and manager_key == self.manager_key,
                   "restored secret.key matches the sealing_key fingerprint")
        self.check("hub_key_restored", hub_key == self.hub_key, "restored hub secret.key matches the backed-up key")
        kb = tree_digest(target / "data")
        self.check("knowledge_base_digest", kb == self.expected_kb, "%d files" % ((kb or {}).get("files") or 0))
        self.check("config_parses", config_parses(target, self.hub_db, self.manager_db),
                   "hub.json and manager.json name the restored databases")
        # The guarded write: an approval resolved through the read-only session must be refused for that reason.
        before = self.query_json(self.manager_db, APPROVAL_ROW, "approval before write")
        write = self.psql(self.manager_db, "UPDATE interactions SET status = 'resolved' WHERE id = 'drill-approval-1';",
                          "guarded write", read_only=self.args.fault != "readonly-bypassed", check=False)
        rejected = guarded_write_rejected(write.returncode, write.stderr)
        self.check("guarded_write_refused", rejected, "refused with SQLSTATE " + READ_ONLY_SQLSTATE if rejected
                   else "a write went through the acceptance session")
        after = self.query_json(self.manager_db, APPROVAL_ROW, "approval after write")
        self.check("guarded_row_unchanged", before == after and after is not None, "the approval row is as restored")
        sessions = self.query_json("postgres", SERVICE_SESSIONS, "service sessions")
        self.check("no_service_sessions", sessions == 0, "no persona-hub or persona-manager connection")
        failed = [c["name"] for c in self.checks if not c["ok"]]
        if failed:
            raise DrillError("acceptance failed: " + ", ".join(failed))

    def cleanup(self):
        result = {}
        self.processes.kill_all()
        removed = [self.removed("container", name) for name in reversed(self.containers)]
        result["containers"] = "removed" if all(removed) else "left behind"
        if self.network_created:
            result["network"] = "removed" if self.removed("network", self.network) else "left behind"
        else:
            result["network"] = "not created"
        if self.root is not None:
            shutil.rmtree(self.root, ignore_errors=True)
            result["temporary_files"] = "removed" if not self.root.exists() else "left behind"
        else:
            result["temporary_files"] = "not created"
        result["ok"] = "left behind" not in result.values()
        return result

    def run(self):
        verdict, error = "FAIL", None
        try:
            self.phase("setup", self.setup)
            self.phase("fixture", self.fixture)
            self.phase("backup_create", self.backup)
            self.phase("backup_verify", lambda: self.verify("/drill/source/home", "backup verify"))
            self.phase("disaster", self.disaster)
            self.phase("backup_restore", self.restore)
            self.phase("restored_verify", lambda: self.verify("/drill/target/home", "restored backup verify"))
            if self.args.fault:
                self.phase("fault", self.inject)
            if self.args.checks == "full":
                self.phase("read_only_acceptance", self.accept)
            verdict = "PASS"
        except DrillTimeout as exc:
            verdict, error = "TIMEOUT", exc.args[0]
        except DrillError as exc:
            error = exc.args[0]
            error += error_detail(exc.args[1] if len(exc.args) > 1 else "")
        except (OSError, ValueError, KeyError, TypeError, tarfile.TarError) as exc:
            error = exc.__class__.__name__ + ": " + str(exc)
        except KeyboardInterrupt:
            verdict, error = "INTERRUPTED", "interrupted"
        finally:
            shield_signals()
            cleanup_started = time.monotonic()
            cleanup = self.cleanup()
            self.phases.append({"name": "cleanup", "ok": cleanup["ok"],
                                "seconds": round(time.monotonic() - cleanup_started, 2)})
            self.report["cleanup"] = cleanup
            if verdict == "PASS" and not cleanup["ok"]:
                verdict, error = "FAIL", "cleanup left resources behind"
        self.report["verdict"] = verdict
        if error:
            self.report["error"] = redact(error, self.secrets, [self.root] if self.root else [])
            failed = [p["name"] for p in self.phases if not p["ok"]]
            self.report["failed_phase"] = failed[0] if failed else None
        if not report_is_clean(self.report, self.secrets):
            self.report = {"drill": self.report["drill"], "run": self.run_id, "verdict": "FAIL",
                           "error": "the report held a secret or URL and was withheld", "services_started": False}
        return self.report


def error_detail(stderr):
    """The line that says what failed: psql's (verbose) ERROR line, else the last line."""
    lines = [line for line in stderr.splitlines() if line.strip()]
    errors = [line for line in lines if re.match(r"^(psql:.*)?ERROR:", line.strip())]
    chosen = errors[-1] if errors else (lines[-1] if lines else "")
    return ": " + chosen if chosen else ""


def damage_member(bundle, member):
    """Flip one byte in the middle of a bundle member's content, so its SHA-256 no longer matches the manifest."""
    with tarfile.open(bundle) as archive:
        info = archive.getmember(member)
        offset = info.offset_data + info.size // 2
    with open(bundle, "r+b") as damaged:
        damaged.seek(offset)
        byte = damaged.read(1)
        damaged.seek(offset)
        damaged.write(bytes([byte[0] ^ 0xFF]))


def shield_signals():
    """From cleanup on, SIGINT and SIGTERM are ignored so they cannot cut it short (main thread only)."""
    try:
        for sig in (signal.SIGINT, signal.SIGTERM):
            signal.signal(sig, signal.SIG_IGN)
    except ValueError:
        pass


def write_private(path, content):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    data = content.encode() if isinstance(content, str) else content
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "wb") as output:
        output.write(data)


def config_parses(home, hub_db, manager_db):
    from urllib.parse import urlsplit
    try:
        hub = json.loads((home / "hub.json").read_text())
        manager = json.loads((home / "managers" / "assistant" / "manager.json").read_text())
        url = urlsplit(hub["database_url"])
        return url.hostname == "db" and url.path == "/" + hub_db and manager.get("database_name") == manager_db
    except (OSError, ValueError, KeyError, TypeError, AttributeError):
        return False


def utc_now():
    return datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


# ---------------------------------------------------------------------------------------------------------------------
# Synthetic fixture: markers of each kind of work in flight, nothing real.

KNOWLEDGE_BASE = {
    "notes/plan.md": "# Drill plan\nA synthetic note.\n",
    "notes/nested/fact.md": "A synthetic fact.\n",
    "people/example.md": "Example Person, me@example.com\n",
}

HUB_FIXTURE = """
INSERT INTO client_ops (id, device_id, kind, target, digest, state, result, created_at, updated_at) VALUES
  ('00000000-0000-4000-8000-000000000001', 'drill-device', 'drill.send', '{"marker": 1}', 'd1', 'pending', NULL,
   '2026-01-01T00:00:01Z', '2026-01-01T00:00:01Z'),
  ('00000000-0000-4000-8000-000000000002', 'drill-device', 'drill.send', '{"marker": 2}', 'd2', 'unknown', NULL,
   '2026-01-01T00:00:02Z', '2026-01-01T00:00:02Z'),
  ('00000000-0000-4000-8000-000000000003', 'drill-device', 'drill.send', '{"marker": 3}', 'd3', 'pending', NULL,
   '2026-01-01T00:00:03Z', '2026-01-01T00:00:03Z'),
  ('00000000-0000-4000-8000-000000000004', 'drill-device', 'drill.send', '{"marker": 4}', 'd4', 'done', '{"ok": true}',
   '2026-01-01T00:00:04Z', '2026-01-01T00:00:05Z'),
  ('00000000-0000-4000-8000-000000000005', 'drill-device', 'drill.send', '{"marker": 5}', 'd5', 'failed',
   '{"ok": false}', '2026-01-01T00:00:06Z', '2026-01-01T00:00:07Z');
"""

MANAGER_FIXTURE = """
INSERT INTO sealing_key (fingerprint) VALUES ('{fingerprint}');
INSERT INTO todos (id, title, created_at, updated_at) VALUES
  ('drill-task-1', 'Drill task in progress', '2026-01-01T00:00:01Z', '2026-01-01T00:00:01Z');
INSERT INTO todos (id, title, done_at, created_at, updated_at) VALUES
  ('drill-task-2', 'Drill task finished', '2026-01-01T00:00:09Z', '2026-01-01T00:00:02Z', '2026-01-01T00:00:09Z');
INSERT INTO todo_agents (todo_id, machine_id, session_id, linked_at, agent) VALUES
  ('drill-task-1', 'drill-machine', 'drill-session-1', '2026-01-01T00:00:03Z', 'claude');
INSERT INTO todo_agents (todo_id, machine_id, session_id, linked_at, released_at, agent) VALUES
  ('drill-task-2', 'drill-machine', 'drill-session-2', '2026-01-01T00:00:04Z', '2026-01-01T00:00:09Z', 'codex');
INSERT INTO interactions (id, machine_id, session_id, kind, payload, status, created_at, todo_id) VALUES
  ('drill-approval-1', 'drill-machine', 'drill-session-1', 'permission', '{"tool": "drill"}', 'pending',
   '2026-01-01T00:00:05Z', 'drill-task-1'),
  ('drill-approval-2', 'drill-machine', 'drill-session-1', 'plan', '{"plan": "drill"}', 'pending',
   '2026-01-01T00:00:06Z', 'drill-task-1');
INSERT INTO interactions (id, machine_id, session_id, kind, payload, status, resolution, created_at, resolved_at)
  VALUES ('drill-approval-3', 'drill-machine', 'drill-session-2', 'question', '{"q": "drill"}', 'resolved',
   '{"answer": "yes"}', '2026-01-01T00:00:07Z', '2026-01-01T00:00:08Z');
INSERT INTO manager_outbox (id, kind, body, created_at) VALUES
  ('drill-outbox-1', 'push', convert_to('{"marker": 1}', 'UTF8'), '2026-01-01T00:00:01Z'),
  ('drill-outbox-2', 'broadcast', convert_to('{"marker": 2}', 'UTF8'), '2026-01-01T00:00:02Z'),
  ('drill-outbox-3', 'push', convert_to('{"marker": 3}', 'UTF8'), '2026-01-01T00:00:03Z');
"""

# Each changes what a started hub or manager would change: approvals resolved and notified, the outbox drained and
# refilled, client operations settled and a new one made, tasks finished and released.
MANAGER_DISASTER = """
UPDATE interactions SET status = 'resolved', resolution = '{"drill": "disaster"}', resolved_at = now(),
  user_notified_at = now() WHERE status = 'pending';
DELETE FROM manager_outbox;
INSERT INTO manager_outbox (id, kind, body, created_at) VALUES ('drill-outbox-after', 'push', '\\x00', now());
UPDATE todos SET done_at = now(), updated_at = now() WHERE done_at IS NULL;
UPDATE todo_agents SET released_at = now() WHERE released_at IS NULL;
"""

HUB_DISASTER = """
UPDATE client_ops SET state = 'done', result = '{}', updated_at = now() WHERE state IN ('pending', 'unknown');
INSERT INTO client_ops (id, kind, target, digest) VALUES ('00000000-0000-4000-8000-0000000000ff', 'drill.send', '{}', 'x');
"""

SCHEMA_ROWS = ("(SELECT coalesce(json_agg(json_build_array(version, name, part) ORDER BY version, name, part), '[]')"
               " FROM schema_migrations)")

HUB_SNAPSHOT = """
SELECT json_build_object(
  'schema', %s,
  'client_ops', (SELECT coalesce(json_agg(json_build_object('id', id, 'device_id', device_id, 'kind', kind,
      'target', md5(target::text), 'digest', digest, 'state', state, 'result', md5(result::text),
      'created_at', created_at, 'updated_at', updated_at) ORDER BY created_at, id), '[]') FROM client_ops)
);
""" % SCHEMA_ROWS

MANAGER_SNAPSHOT = """
SELECT json_build_object(
  'schema', %s,
  'sealing_key', (SELECT coalesce(json_agg(fingerprint), '[]') FROM sealing_key),
  'tasks', (SELECT coalesce(json_agg(json_build_object('id', id, 'title', md5(title), 'done_at', done_at,
      'updated_at', updated_at) ORDER BY id), '[]') FROM todos),
  'agent_links', (SELECT coalesce(json_agg(json_build_object('todo_id', todo_id, 'machine_id', machine_id,
      'session_id', session_id, 'released_at', released_at, 'agent', agent) ORDER BY todo_id, machine_id, session_id),
      '[]') FROM todo_agents),
  'approvals', (SELECT coalesce(json_agg(json_build_object('id', id, 'kind', kind, 'todo_id', todo_id,
      'payload', md5(payload::text), 'status', status, 'resolution', md5(resolution::text), 'resolved_at', resolved_at,
      'user_notified_at', user_notified_at, 'auto_authorized_at', auto_authorized_at) ORDER BY created_at, id), '[]')
      FROM interactions),
  'outbox', (SELECT coalesce(json_agg(json_build_object('seq', seq, 'id', id, 'kind', kind, 'body', md5(body),
      'created_at', created_at) ORDER BY seq), '[]') FROM manager_outbox),
  'outbox_next_seq', (SELECT last_value FROM manager_outbox_seq_seq)
);
""" % SCHEMA_ROWS

APPROVAL_ROW = """SELECT row_to_json(r) FROM (SELECT id, status, resolution, resolved_at, user_notified_at
  FROM interactions WHERE id = 'drill-approval-1') r;"""

SERVICE_SESSIONS = ("SELECT to_json(count(*)) FROM pg_stat_activity WHERE application_name IN (%s);"
                    % ", ".join("'%s'" % name for name in SERVICE_APPLICATIONS))


def main(argv=None):
    args = parse_args(sys.argv[1:] if argv is None else argv)
    drill = Drill(args)

    def interrupted(signum, frame):
        shield_signals()  # the first signal stops the drill; later ones cannot cut its cleanup short
        raise KeyboardInterrupt

    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, interrupted)
    report = drill.run()  # returns with both signals ignored, so the report is always printed
    text = json.dumps(report, indent=2, sort_keys=True)
    print(text, flush=True)
    verdict = report["verdict"]
    if args.report:
        try:
            write_private(args.report, text + "\n")
        except OSError as error:
            print("the report could not be written to --report (" + error.__class__.__name__ + ")", flush=True)
            verdict = "FAIL" if verdict == "PASS" else verdict
    print(verdict + ": restore drill " + report["run"] + (" (" + report["error"] + ")" if
                                                          report.get("error") else ""), flush=True)
    return 0 if verdict == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())
