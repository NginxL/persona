"""Isolated Git, process and history fixtures for Web development updates."""
import contextlib
import fcntl
import gzip
import importlib.util
import io
import json
import os
import signal
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import time
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("webdev", ROOT / "server/internal/update/webdev.py")
webdev = importlib.util.module_from_spec(spec)
spec.loader.exec_module(webdev)


class DevelopmentFixtures(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="persona-web-dev-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / "tools"
        self.bin.mkdir()
        self.env = patch.dict(os.environ, {"PATH": str(self.bin) + os.pathsep + os.environ["PATH"],
                                          "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull})
        self.env.start()
        self.addCleanup(self.env.stop)

    def tool(self, name, source):
        path = self.bin / name
        path.write_text("#!" + sys.executable + "\n" + source)
        path.chmod(0o755)
        return str(path)

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.repo), *args], text=True, stderr=subprocess.DEVNULL).strip()

    def test_project_main_and_fixed_compile_candidate(self):
        self.repo = self.root / "remote"
        self.repo.mkdir()
        self.git("init", "-b", "main")
        self.git("config", "user.name", "Fixture")
        self.git("config", "user.email", "fixture@example.com")
        for relative, content in {"server/build-tags.txt":"public", "web/package.json":"{}"}.items():
            file = self.repo / relative
            file.parent.mkdir(parents=True, exist_ok=True)
            file.write_text(content)
        self.git("add", ".")
        self.git("commit", "-m", "First fixture")
        (self.repo / "earlier").write_text("previous history")
        self.git("add", ".")
        self.git("commit", "-m", "Selected fixture")
        repository = patch.object(webdev, "REPOSITORY", self.repo.as_uri())
        repository.start()
        self.addCleanup(repository.stop)
        resolve_work = self.root/"resolve"
        resolve_work.mkdir()
        selected = webdev.resolve({"work": str(resolve_work)})
        self.assertEqual(selected["branch"], "main")
        self.assertEqual(selected["updated_at"], self.git("show", "-s", "--format=%cI", selected["commit"]))
        (self.repo / "newer").write_text("remote advanced after checking")
        self.git("add", ".")
        self.git("commit", "-m", "Second fixture")
        trace = self.root / "compile.jsonl"
        os.environ["FIXTURE_TRACE"] = str(trace)
        self.addCleanup(os.environ.pop, "FIXTURE_TRACE", None)
        fixture = '''import json, os, pathlib, subprocess, sys
args = sys.argv[1:]
with open(os.environ['FIXTURE_TRACE'], 'a') as log: log.write(json.dumps([pathlib.Path(sys.argv[0]).name, args])+'\\n')
if pathlib.Path(sys.argv[0]).name == 'npm' and args == ['run', 'build:development'] and pathlib.Path.cwd().name == 'web':
    pathlib.Path('dist').mkdir(); pathlib.Path('dist/index.html').write_text('fixture web')
if pathlib.Path(sys.argv[0]).name == 'go' and args[0] == 'build':
    pathlib.Path(args[args.index('-o')+1]).write_text(subprocess.check_output(['git','rev-parse','HEAD'], text=True))
'''
        self.tool("go", fixture)
        self.tool("npm", fixture)
        work = self.root / "build"
        work.mkdir()
        with patch.object(webdev, "HISTORY_LIMIT", 1):
            built = webdev.build({"platforms":["linux-amd64", "darwin-arm64"], "work":str(work), "candidate":selected,
                                  "version":"web-dev-fixture", "build":202611130001, "platform":"linux-amd64"})
        self.assertEqual(built["ancestors"], [selected["commit"]])
        self.assertEqual(subprocess.check_output(["git", "-C", str(work/"source"), "rev-list", "--count", "HEAD"], text=True).strip(), "1")
        self.assertEqual((work / "personad-linux-amd64").read_text().strip(), selected["commit"])
        self.assertNotEqual(selected["commit"], self.git("rev-parse", "HEAD"))
        self.assertEqual({p.name for p in Path(built["artifacts"]).glob("*.gz")}, {
            "personad-linux-amd64.gz", "personad-darwin-arm64.gz",
            "persona-hub-linux-amd64.gz", "persona-manager-linux-amd64.gz"})
        commands = [json.loads(line) for line in trace.read_text().splitlines()]
        self.assertFalse(any("test" in args or "vet" in args for _, args in commands))
        self.git("branch", "-m", "other")
        with self.assertRaisesRegex(ValueError, "project main branch"):
            webdev.resolve({"work": str(resolve_work)})

    def history_fixture(self):
        runtime = self.root / "runtime"
        (runtime / "data").mkdir(parents=True)
        (runtime / "hub.json").write_text(json.dumps({"database_url":"fixture-only"}))
        (runtime / "secret.key").write_bytes(b"fixture-key")
        (runtime / "data/history.txt").write_text("synthetic history")
        (runtime / "web").mkdir()
        (runtime / "web/index.html").write_bytes(b"saved web override")
        binary = self.root / "personad"
        binary.write_bytes(b"fixture installed program")
        database = self.root / "database.json"
        database.write_text('{"messages":["synthetic history"]}')
        self.env_db = patch.dict(os.environ, {"FIXTURE_DB":str(database)})
        self.env_db.start()
        self.addCleanup(self.env_db.stop)
        self.tool("pg_dump", "import os,pathlib,sys\np=pathlib.Path(sys.argv[sys.argv.index('--file')+1]);p.write_bytes(b'PGDMP'+pathlib.Path(os.environ['FIXTURE_DB']).read_bytes())\n")
        self.tool("pg_restore", "import pathlib,sys\nassert pathlib.Path(sys.argv[-1]).read_bytes().startswith(b'PGDMP')\nprint('fixture archive list')\n")
        events = self.root / "events"
        hook = self.tool("writer-hook", "import pathlib,sys\np=pathlib.Path(sys.argv[1]);p.write_text((p.read_text() if p.exists() else '')+sys.argv[2]+'\\n')\n")
        return {"runtime": str(runtime), "hub_database":"effective-fixture-database", "executables":{"personad":str(binary)}, "candidate":{"commit":"a"*40}, "config":{"backup":{
            "quiesce":[hook,str(events),"pause"], "resume":[hook,str(events),"resume"]}}}

    def wait_file(self, path, process):
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if path.exists():
                return
            if process.poll() is not None:
                self.fail("helper exited before " + path.name)
            time.sleep(.01)
        self.fail("timed out waiting for " + path.name)

    def test_cancellation_resumes_writers_and_reaps_child_group(self):
        for phase, cancel_signal in (("quiesce", signal.SIGINT), ("dump", signal.SIGTERM),
                                     ("resume", signal.SIGINT), ("failed-resume", signal.SIGINT),
                                     ("stubborn-dump", signal.SIGINT)):
            with self.subTest(phase=phase), tempfile.TemporaryDirectory(dir=self.root) as directory:
                # Each cancellation gets an independent synthetic installation.
                previous_root = self.root
                self.root = Path(directory)
                child_group = None
                try:
                    request = self.history_fixture()
                    paused, entered, resumed, interrupted = [self.root / n for n in ("paused", "entered", "resumed", "interrupted")]
                    blocker = self.tool("blocker", f'''import os, pathlib, signal, sys, time
def interrupt(*_):
    pathlib.Path({str(interrupted)!r}).write_text('SIGINT')
    raise SystemExit(9)
signal.signal(signal.SIGINT, interrupt)
if {phase == 'stubborn-dump'!r}:
    signal.signal(signal.SIGINT, signal.SIG_IGN)
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
pathlib.Path({str(entered)!r}).write_text(str(os.getpid()))
while True: time.sleep(.01)
''')
                    pause = self.tool("pause", f"import pathlib\npathlib.Path({str(paused)!r}).touch()\n")
                    resume = self.tool("resume", f'''import pathlib, time
if {phase == 'resume'!r}: pathlib.Path({str(entered)!r}).touch()
time.sleep(.4)
if {phase == 'failed-resume'!r}: raise SystemExit(7)
pathlib.Path({str(paused)!r}).unlink(missing_ok=True)
pathlib.Path({str(resumed)!r}).touch()
''')
                    request["config"]["backup"].update(quiesce=[pause], resume=[resume])
                    if phase == "quiesce":
                        request["config"]["backup"]["quiesce"] = [self.tool("pause-block", f"import pathlib,os\npathlib.Path({str(paused)!r}).touch()\nos.execv({blocker!r},[{blocker!r}])\n")]
                    elif phase != "resume":
                        self.tool("pg_dump", Path(blocker).read_text().split("\n", 1)[1])
                    request.update(operation="backup", result=str(self.root/"result.json"))
                    with subprocess.Popen([sys.executable, str(ROOT/"server/internal/update/webdev.py")],
                                          stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                          text=True, start_new_session=True) as process:
                        try:
                            process.stdin.write(json.dumps(request))
                            process.stdin.close()
                            self.wait_file(entered, process)
                            if phase != "resume":
                                child_group = int(entered.read_text())
                            process.send_signal(cancel_signal)
                            if phase == "resume":
                                time.sleep(.05)
                                process.send_signal(signal.SIGTERM)
                            self.assertNotEqual(process.wait(timeout=10), 0)
                        finally:
                            if process.poll() is None:
                                process.kill()
                            process.wait(timeout=3)
                    store = Path(request["runtime"])/"update/development-backups"
                    self.assertFalse((self.root/"result.json").exists())
                    self.assertTrue(any(store.glob(".incomplete-*")))
                    if phase == "failed-resume":
                        self.assertTrue(paused.exists())
                        self.assertTrue((store/"coordination-pending.json").exists())
                    else:
                        self.assertTrue(resumed.exists(), "cancellation interrupted resume")
                        self.assertFalse(paused.exists())
                        self.assertFalse((store/"coordination-pending.json").exists())
                    if phase != "resume":
                        if phase != "stubborn-dump":
                            self.assertTrue(interrupted.exists(), "child did not get graceful SIGINT")
                        with self.assertRaises(ProcessLookupError):
                            os.killpg(int(entered.read_text()), 0)
                    with self.assertRaisesRegex(ValueError, "coordination|incomplete"):
                        webdev.snapshot(request)
                finally:
                    if child_group is not None:
                        try:
                            os.killpg(child_group, signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                    self.root = previous_root

    def test_three_verified_snapshots_and_failure_retention(self):
        request = self.history_fixture()
        store = Path(request["runtime"]) / "update/development-backups"
        receipts = []
        for _ in range(5):
            receipts.append(webdev.snapshot(request))
        snapshots = sorted(p for p in store.iterdir() if webdev.NAME.fullmatch(p.name))
        self.assertEqual(len(snapshots), 3)
        self.assertEqual([p.name for p in snapshots], [r["id"] for r in receipts[-3:]])
        self.assertEqual(receipts[-1]["retained"], 3)
        manifest = webdev.verify(snapshots[-1])
        self.assertEqual(manifest["databases"], 1)
        self.assertEqual(json.loads((snapshots[-1]/"effective-databases.json").read_text()),["effective-fixture-database"])
        self.assertIn("personad",manifest["programs"])
        with tarfile.open(snapshots[-1]/"files.tar.gz") as archive:
            self.assertEqual(archive.extractfile("root-0/data/history.txt").read(), b"synthetic history")
            self.assertEqual(archive.extractfile("root-0/secret.key").read(), b"fixture-key")
            self.assertEqual(archive.extractfile("root-0/web/index.html").read(), b"saved web override")
            self.assertFalse(any("update/development-backups" in m.name for m in archive))
        unrelated = store / "manual-backup"
        unrelated.mkdir()
        (unrelated/"keep").write_text("untouched")
        self.tool("pg_dump", "raise SystemExit(7)\n")
        with self.assertRaises(subprocess.CalledProcessError):
            webdev.snapshot(request)
        self.assertEqual(sorted(p.name for p in snapshots), sorted(p.name for p in store.iterdir() if webdev.NAME.fullmatch(p.name)))
        self.assertTrue((unrelated/"keep").exists())
        self.assertTrue((self.root/"events").read_text().endswith("pause\nresume\n"))
        self.assertFalse((store/"coordination-pending.json").exists())

    def test_builtin_quiesce_custom_directory_and_retention(self):
        request = self.history_fixture()
        runtime = Path(request["runtime"])
        # The Hub paused the writers itself: no hooks, no coordination marker.
        directory = runtime / "backups/web-dev"
        request["config"]["backup"] = {"builtin": True, "directory": str(directory), "retain": 2}
        receipts = [webdev.snapshot(request) for _ in range(3)]
        snapshots = sorted(p for p in directory.iterdir() if webdev.NAME.fullmatch(p.name))
        self.assertEqual(len(snapshots), 2)
        self.assertEqual([p.name for p in snapshots], [r["id"] for r in receipts[-2:]])
        self.assertEqual(receipts[-1]["retained"], 2)
        self.assertFalse((runtime / "update/development-backups").exists())
        self.assertFalse((directory / "coordination-pending.json").exists())
        # A folder inside the runtime is never archived into the next snapshot.
        with tarfile.open(snapshots[-1] / "files.tar.gz") as archive:
            self.assertFalse(any("backups/web-dev" in member.name for member in archive))
        # The folder must be private.
        public = runtime / "public-snapshots"
        public.mkdir()
        public.chmod(0o755)
        request["config"]["backup"]["directory"] = str(public)
        with self.assertRaisesRegex(ValueError, "private"):
            webdev.snapshot(request)
        # Relative paths and an out-of-range retention are refused.
        for broken in ({"directory": "relative/dir"}, {"directory": str(directory), "retain": 51}):
            request["config"]["backup"] = dict(broken, builtin=True)
            with self.assertRaises(ValueError):
                webdev.snapshot(request)
        # Without the built-in pause, hooks are still required.
        request["config"]["backup"] = {}
        with self.assertRaisesRegex(ValueError, "quiesce"):
            webdev.snapshot(request)

    def test_store_may_not_be_or_contain_a_collected_root(self):
        request = self.history_fixture()
        runtime = Path(request["runtime"])
        # The store and everything below it leave the archive: a store that is
        # the runtime, or holds it, would drop what the snapshot is for.
        for store in (runtime, runtime.parent):
            request["config"]["backup"] = {"builtin": True, "directory": str(store)}
            with self.assertRaisesRegex(ValueError, "may not be the runtime or contain"):
                webdev.snapshot(request)
        self.assertFalse((runtime / "update" / "development-backups").exists())

    def test_apply_backup_stages_only_saved_programs_and_preserves_live_data(self):
        request = self.history_fixture()
        request.update(from_version="web-dev-old", from_commit="b"*40, from_build=202611120001,
                       from_updated_at="2026-11-12T10:00:00Z", platform="linux-amd64")
        node_dir = self.root / "nodes"
        node_dir.mkdir()
        node = node_dir / "personad-linux-arm64"
        node.write_bytes(b"old arm64 node")
        (node_dir / "personad-linux-arm64.version").write_text("web-dev-old\n")
        (node_dir / "personad-windows-amd64").write_bytes(b"another version")
        (node_dir / "personad-windows-amd64.version").write_text("other-version")
        (node_dir / "personad-darwin-arm64").symlink_to(node)
        request["node_directory"] = str(node_dir)
        receipt = webdev.snapshot(request)
        manifest = webdev.verify(Path(request["runtime"])/"update/development-backups"/receipt["id"])
        self.assertEqual(set(manifest["nodes"]), {"linux-arm64"})
        for _ in range(2):
            webdev.snapshot(request)
        runtime = Path(request["runtime"])
        current = {runtime/"data/history.txt": b"new messages", runtime/"secret.key": b"current key",
                   self.root/"database.json": b"current database", self.root/"personad": b"current program",
                   runtime/"web/index.html": b"current web override"}
        for file, content in current.items():
            file.write_bytes(content)
        versions = webdev.backups(request)["backups"]
        self.assertEqual(len(versions), 3)
        self.assertEqual(versions[0]["commit"], "b"*40)  # Saved version, not the attempted target.
        self.assertEqual(versions[0]["updated_at"], request["from_updated_at"])
        self.assertNotIn("effective-fixture-database", json.dumps(versions))
        work = self.root/"prepare"
        work.mkdir()
        # Preparation must neither restore a database nor pause current writers.
        self.tool("pg_restore", "raise SystemExit('must not restore data')\n")
        events = (self.root/"events").read_bytes()
        built = webdev.prepare_backup(dict(request, work=str(work), backup_id=receipt["id"],
                                           candidate={"commit":"b"*40}, version="web-dev-old", build=202611120001,
                                           platforms=["linux-amd64", "linux-arm64"]))
        artifacts = Path(built["artifacts"])
        self.assertEqual(gzip.decompress((artifacts/"personad-linux-amd64.gz").read_bytes()), b"fixture installed program")
        self.assertEqual(gzip.decompress((artifacts/"personad-linux-arm64.gz").read_bytes()), b"old arm64 node")
        self.assertEqual((self.root/"events").read_bytes(), events)
        for file, content in current.items():
            self.assertEqual(file.read_bytes(), content)
        with self.assertRaisesRegex(ValueError, "required Node platform"):
            webdev.prepare_backup(dict(request, work=str(work), backup_id=receipt["id"],
                                       platforms=["darwin-arm64"], candidate=None))
        self.tool("pg_restore", "import pathlib,sys\nassert pathlib.Path(sys.argv[-1]).read_bytes().startswith(b'PGDMP')\n")
        webdev.snapshot(request)  # Rotating the selected oldest snapshot keeps staged programs usable.
        self.assertFalse((runtime/"update/development-backups"/receipt["id"]).exists())
        self.assertEqual(gzip.decompress((artifacts/"personad-linux-amd64.gz").read_bytes()), b"fixture installed program")
        with self.assertRaises(ValueError):
            webdev.prepare_backup(dict(request, work=str(work), backup_id="../outside"))
        snapshot = runtime/"update/development-backups"/versions[0]["id"]
        (snapshot/"program-personad.gz").write_bytes(b"corrupt")
        with self.assertRaisesRegex(ValueError, "checksum"):
            webdev.prepare_backup(dict(request, work=str(work), backup_id=snapshot.name))

    def test_resume_failure_and_unknown_coordination_block_retry(self):
        request = self.history_fixture()
        request["config"]["backup"]["resume"] = [self.tool("resume-fails", "raise SystemExit(2)\n")]
        with self.assertRaises(subprocess.CalledProcessError):
            webdev.snapshot(request)
        with self.assertRaisesRegex(ValueError,"coordination needs recovery"):
            webdev.snapshot(request)

    def test_backup_and_resume_failures_both_reach_private_log(self):
        request = self.history_fixture()
        self.tool("pg_dump", "raise SystemExit(7)\n")
        request["config"]["backup"]["resume"] = [self.tool("resume-fails", "raise SystemExit(2)\n")]
        log = io.StringIO()
        with contextlib.redirect_stderr(log), self.assertRaises(subprocess.CalledProcessError) as caught:
            webdev.snapshot(request)
        self.assertEqual(caught.exception.returncode, 2)
        self.assertEqual(caught.exception.__cause__.returncode, 7)
        self.assertIn("snapshot failed: CalledProcessError (exit 7)", log.getvalue())
        self.assertIn("resume failed: CalledProcessError (exit 2)", log.getvalue())
        self.assertNotIn("effective-fixture-database", log.getvalue())
        store = Path(request["runtime"])/"update/development-backups"
        self.assertTrue((store/"coordination-pending.json").exists())
        self.assertEqual(list(store.glob("snapshot-*")), [])

    def test_snapshot_and_prepare_report_lock_contention(self):
        request = self.history_fixture()
        receipt = webdev.snapshot(request)
        store = Path(request["runtime"])/"update/development-backups"
        with (store/".lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            for operation, payload in [(webdev.snapshot, request), (webdev.prepare_backup, dict(request, backup_id=receipt["id"]))]:
                with self.subTest(operation=operation.__name__), self.assertRaisesRegex(ValueError, "another backup or prepare is in progress"):
                    operation(payload)

    def test_real_writer_is_quiesced_for_the_whole_snapshot(self):
        request = self.history_fixture()
        runtime = Path(request["runtime"])
        pause, paused, ready = self.root/"pause", self.root/"paused", self.root/"writer-ready"
        writer = self.root/"writer.py"
        writer.write_text('''import pathlib,sys,time
root,pause,paused,db,ready=map(pathlib.Path,sys.argv[1:])
counter=0
while True:
    if pause.exists():
        paused.touch()
        while pause.exists(): time.sleep(.002)
        paused.unlink(missing_ok=True)
    counter+=1
    (root/'data/history.txt').write_text(str(counter))
    db.write_text(str(counter))
    if counter == 1: ready.touch()
    time.sleep(.002)
''')
        process = subprocess.Popen([sys.executable,str(writer),str(runtime),str(pause),str(paused),str(self.root/"database.json"),str(ready)])
        self.addCleanup(lambda: (process.terminate(),process.wait(timeout=3)))
        # The byte comparison requires the writer's first completed paired write.
        self.wait_file(ready, process)
        hook = self.tool("coordinate", '''import pathlib,sys,time
pause,paused=map(pathlib.Path,sys.argv[1:3])
if sys.argv[3]=='pause': pause.touch()
else: pause.unlink(missing_ok=True)
for _ in range(1000):
    if paused.exists()==(sys.argv[3]=='pause'): break
    time.sleep(.002)
else: raise SystemExit(3)
''')
        request["config"]["backup"].update(quiesce=[hook,str(pause),str(paused),"pause"],resume=[hook,str(pause),str(paused),"resume"])
        receipt = webdev.snapshot(request)
        path = runtime / "update/development-backups" / receipt["id"]
        with tarfile.open(path/"files.tar.gz") as archive:
            history = archive.extractfile("root-0/data/history.txt").read()
        self.assertEqual((path/"database-0.dump").read_bytes()[5:], history)
        self.assertFalse(pause.exists())
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if process.poll() is not None:
                self.fail("writer exited before resuming")
            live_history = (runtime/"data/history.txt").read_bytes()
            live_database = (self.root/"database.json").read_bytes()
            if live_history not in (b"", history) and live_history == live_database:
                break
            time.sleep(.01)
        else:
            self.fail("writer did not resume paired writes")


if __name__ == "__main__":
    unittest.main()
