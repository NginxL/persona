#!/usr/bin/env python3
"""Start a Persona Core hub and manager built without any module, and check the core alone runs (#13978).

Builds personad, persona-hub, persona-manager and persona-node with -tags persona_core_only into a temporary directory, starts a
disposable Postgres 18 container on a private port, writes a hub.json for a temporary home (a token of its own, no
computers) and runs `persona-hub run` with a clean environment, so nothing of this machine's own hub (~/.persona,
PERSONA_HOME) is read. The hub starts the persona-manager beside it, as an installed hub does. Then it checks:

- /api/v1/health answers ok, and an authenticated request (/api/v1/managers) is served;
- the manager connects and says `modules: loaded none` (internal/assistant/modules.go);
- every extension program a module provides (builtinext.AddProgram in server/internal/modules/all) is an unknown
  subcommand of personad and persona-manager (usage, exit 2), its built-in extension is not in /api/v1/extensions, and
  that extension's route (/api/v1/extensions/<id>/-/assistant-data) answers 404;
- every device command a module's node half provides (commandport.AddCommand in server/internal/modules/all/node) is
  an unknown subcommand of personad and persona-node, and their usage does not list it;
- every connector a module registers on every hub (its port provided in server/internal/modules/all, ALWAYS_CONNECTORS)
  is not in /api/v1/connectors?all=1, and its route (/api/v1/connectors/<id>/auth) answers 404;
- every built-in extension a connector module adds (MODULE_EXTENSIONS) is not in /api/v1/extensions;
- SIGTERM stops the hub and the manager within the timeout, exit 0.

--full runs the same against a default build and expects the opposite (every module loaded, the programs, extensions
and routes there), which shows the checks are not vacuous. Prints one verdict line: PASS, FAIL or TIMEOUT.

    python3 scripts/ci/core-only-smoke.py [--full] [--keep]

Needs Go, Docker (DOCKER names another client, e.g. a wrapper) and Python 3.
"""
import argparse
import json
import os
import re
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SERVER = ROOT / "server"
PROGRAMS = ("personad", "persona-hub", "persona-manager", "persona-node")
DOCKER = os.environ.get("DOCKER", "docker")


class Failed(Exception):
    pass


class TimedOut(Exception):
    pass


def module_programs():
    """The extension programs the built-in modules provide: builtinext.AddProgram("<name>", …) in modules/all."""
    names = []
    for path in sorted((SERVER / "internal/modules/all").glob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        names += re.findall(r'builtinext\.AddProgram\(\s*"([^"]+)"', path.read_text())
    return names


def node_commands():
    """The device commands the modules' node halves provide: commandport.AddCommand("<name>", …) in modules/all/node."""
    names = []
    for path in sorted((SERVER / "internal/modules/all/node").glob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        names += re.findall(r'commandport\.AddCommand\(\s*"([^"]+)"', path.read_text())
    return names


# The connector modules whose connector the manager registers on every hub, unconfigured too: the port the composition
# root provides (server/internal/modules/all) and the connector's id.
ALWAYS_CONNECTORS = {"githubport": "github"}
# The connector modules that add a built-in extension of the extension manager's (its settings form), by port.
MODULE_EXTENSIONS = {"imessageport": "imessage"}


def module_connectors():
    """The always-registered connectors the built-in modules provide: <port>.Provide(…) in modules/all."""
    found = set()
    for path in sorted((SERVER / "internal/modules/all").glob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        for port in re.findall(r"\b(\w+port)\.Provide\(", path.read_text()):
            if port in ALWAYS_CONNECTORS:
                found.add(ALWAYS_CONNECTORS[port])
    return sorted(found)


def module_extensions():
    """The built-in extensions the connector modules add: <port>.Provide(…) in modules/all, MODULE_EXTENSIONS."""
    found = set()
    for path in sorted((SERVER / "internal/modules/all").glob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        for port in re.findall(r"\b(\w+port)\.Provide\(", path.read_text()):
            if port in MODULE_EXTENSIONS:
                found.add(MODULE_EXTENSIONS[port])
    return sorted(found)


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def run(args, timeout, **kw):
    try:
        return subprocess.run(args, timeout=timeout, capture_output=True, text=True, **kw)
    except subprocess.TimeoutExpired:
        raise TimedOut(" ".join(map(str, args[:3])))


def build(tags, out):
    env = dict(os.environ, GOFLAGS=(os.environ.get("GOFLAGS", "") + " -trimpath").strip())
    args = ["go", "build"] + (["-tags", tags] if tags else []) + ["-o", str(out) + "/"] + [f"./cmd/{p}" for p in PROGRAMS]
    r = run(args, 900, cwd=SERVER, env=env)
    if r.returncode != 0:
        raise Failed("build: " + (r.stderr or r.stdout).strip()[-2000:])


def postgres(name):
    r = run([DOCKER, "run", "--detach", "--name", name, "-e", "POSTGRES_USER=persona", "-e", "POSTGRES_PASSWORD=smoke-only",
             "-e", "POSTGRES_DB=persona", "-p", "127.0.0.1::5432", "postgres:18-alpine",
             "-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off"], 180)
    if r.returncode != 0:
        raise Failed("postgres container: " + r.stderr.strip())
    deadline = time.monotonic() + 90
    while run([DOCKER, "exec", name, "pg_isready", "-U", "persona", "-d", "persona"], 10).returncode != 0:
        if time.monotonic() > deadline:
            raise TimedOut("Postgres readiness")
        time.sleep(1)
    # pg_isready answers during the image's first-start init too: wait until the final server takes TCP connections.
    port = run([DOCKER, "port", name, "5432/tcp"], 10).stdout.strip().splitlines()[0].rsplit(":", 1)[1]
    while run([DOCKER, "exec", name, "psql", "-h", "127.0.0.1", "-U", "persona", "-d", "persona", "-c", "select 1"], 10).returncode != 0:
        if time.monotonic() > deadline:
            raise TimedOut("Postgres readiness")
        time.sleep(1)
    return f"postgres://persona:smoke-only@127.0.0.1:{port}/persona?sslmode=disable"


def http(url, token=None):
    req = urllib.request.Request(url, headers={"Authorization": f"Bearer {token}"} if token else {})
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            return resp.status, resp.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


def wait_for(what, seconds, probe):
    deadline = time.monotonic() + seconds
    while True:
        try:
            if probe():
                return
        except (OSError, urllib.error.URLError):
            pass
        if time.monotonic() > deadline:
            raise TimedOut(what)
        time.sleep(1)


def check(cond, why):
    if not cond:
        raise Failed(why)


def subcommands(bindir, full, programs):
    """A module's program is a subcommand of personad and persona-manager only when the module is built in."""
    for prog in ("personad", "persona-manager"):
        for name in programs:
            if full:
                # Running a module's program would start it: personad's usage line for it says it is there.
                if prog == "personad":
                    r = run([str(bindir / prog), "no-such-command"], 30, stdin=subprocess.DEVNULL)
                    check(re.search(rf"personad {re.escape(name)}\b", r.stdout + r.stderr), f"personad's usage lacks {name}")
                continue
            r = run([str(bindir / prog), name], 30, stdin=subprocess.DEVNULL)
            check(r.returncode == 2, f"{prog} {name}: exit {r.returncode}, want 2 (unknown subcommand)")
            check(not re.search(rf"\b{re.escape(prog)} {re.escape(name)}\b", r.stdout + r.stderr),
                  f"{prog} {name}: the usage still lists the module's program")


def device_commands(bindir, full, commands):
    """A node half's device command is a subcommand of personad and persona-node only when the module is built in."""
    for prog in ("personad", "persona-node"):
        usage = run([str(bindir / prog), "no-such-command"], 30, stdin=subprocess.DEVNULL)
        for name in commands:
            listed = re.search(rf"\b(personad|persona-node) {re.escape(name)}\b", usage.stdout + usage.stderr)
            if full:
                check(listed, f"{prog}'s usage lacks {name}")
                continue
            check(not listed, f"{prog}'s usage still lists the module's command {name}")
            r = run([str(bindir / prog), name], 30, stdin=subprocess.DEVNULL)
            check(r.returncode == 2, f"{prog} {name}: exit {r.returncode}, want 2 (unknown subcommand)")


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--full", action="store_true", help="a default build, expecting every module (the control)")
    ap.add_argument("--keep", action="store_true", help="keep the temporary directory (logs, home)")
    a = ap.parse_args()
    kind = "default build" if a.full else "core-only"
    work = Path(tempfile.mkdtemp(prefix="persona-core-smoke."))
    bindir, home = work / "bin", work / "home"
    container = "persona-core-smoke-" + secrets.token_hex(4)
    hub = None
    log = work / "hub.log"
    started = time.monotonic()
    try:
        programs = module_programs()
        connectors = module_connectors()
        module_exts = module_extensions()
        commands = node_commands()
        build("" if a.full else "persona_core_only", bindir)
        print(f"built {', '.join(PROGRAMS)} ({kind}) in {time.monotonic() - started:.0f}s", flush=True)
        subcommands(bindir, a.full, programs)
        device_commands(bindir, a.full, commands)
        database = postgres(container)
        port, token = free_port(), secrets.token_hex(24)
        (home / ".persona").mkdir(parents=True)
        cfg = home / ".persona/hub.json"
        cfg.write_text(json.dumps({"listen": f"127.0.0.1:{port}", "token": token, "public_url": f"http://127.0.0.1:{port}",
                                   "database_url": database, "machines": []}))
        cfg.chmod(0o600)
        # A clean environment: no PERSONA_HOME (or its older name) or other settings of this machine's own hub.
        env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": str(home), "TMPDIR": str(work), "LANG": "C.UTF-8",
               "PERSONA_LOCAL_NODE": "0"}
        with open(log, "w") as out:
            hub = subprocess.Popen([str(bindir / "persona-hub"), "run"], cwd=work, env=env, stdout=out, stderr=subprocess.STDOUT,
                                   start_new_session=True)
        base = f"http://127.0.0.1:{port}"

        def healthy():
            check(hub.poll() is None, f"the hub exited ({hub.returncode}) before answering")
            status, body = http(base + "/api/v1/health")
            return status == 200 and json.loads(body).get("ok") is True

        wait_for("hub health", 90, healthy)
        print(f"hub healthy on 127.0.0.1:{port}", flush=True)
        modules_line = re.compile(r"modules: loaded (.+)$", re.M)
        wait_for("the manager's module list", 120, lambda: modules_line.search(log.read_text()) or hub.poll() is not None)
        check(hub.poll() is None, f"the hub exited ({hub.returncode})")
        loaded = modules_line.search(log.read_text()).group(1).strip()
        if a.full:
            check(loaded != "none", "a default build's manager loaded no module")
        else:
            check(loaded == "none", f"a core-only manager loaded modules: {loaded}")
        status, _ = http(base + "/api/v1/managers", token)
        check(status == 200, f"/api/v1/managers: {status}")
        status, body = http(base + "/api/v1/extensions", token)
        check(status == 200, f"/api/v1/extensions: {status}")
        extensions = {e["id"] for e in json.loads(body).get("extensions", [])}
        for name in programs:
            check((name in extensions) == a.full, f"extension {name} {'missing' if a.full else 'present'}: {sorted(extensions)}")
            status, _ = http(f"{base}/api/v1/extensions/{name}/-/assistant-data", token)
            check(status == (200 if a.full else 404), f"/api/v1/extensions/{name}/-/assistant-data: {status}")
        for name in module_exts:
            check((name in extensions) == a.full, f"extension {name} {'missing' if a.full else 'present'}: {sorted(extensions)}")
        status, body = http(base + "/api/v1/connectors?all=1", token)
        check(status == 200, f"/api/v1/connectors: {status}")
        listed = {c["id"] for c in json.loads(body).get("connectors") or []}
        for name in connectors:
            check((name in listed) == a.full, f"connector {name} {'missing' if a.full else 'present'}: {sorted(listed)}")
            status, _ = http(f"{base}/api/v1/connectors/{name}/auth", token)
            check(status == (200 if a.full else 404), f"/api/v1/connectors/{name}/auth: {status}")
        os.killpg(hub.pid, signal.SIGTERM)
        try:
            code = hub.wait(60)
        except subprocess.TimeoutExpired:
            raise TimedOut("hub shutdown")
        check(code == 0, f"the hub exited {code} on SIGTERM")
        left = run(["pgrep", "-f", str(bindir)], 10).stdout.split()
        check(not left, f"processes left after shutdown: {left}")
        hub = None
        print(f"PASS {kind} smoke: built, hub healthy, modules loaded: {loaded}, module programs "
              f"{'present' if a.full else 'absent'} ({', '.join(programs) or 'none'}), module connectors "
              f"{'present' if a.full else 'absent'} ({', '.join(connectors) or 'none'}), node commands "
              f"{'present' if a.full else 'absent'} ({', '.join(commands) or 'none'}), clean shutdown "
              f"({time.monotonic() - started:.0f}s)")
        return 0
    except (Failed, TimedOut) as e:
        verdict = "TIMEOUT" if isinstance(e, TimedOut) else "FAIL"
        if log.exists():
            sys.stderr.write(log.read_text()[-6000:])
        print(f"{verdict} {kind} smoke: {e}")
        return 1
    finally:
        if hub is not None and hub.poll() is None:
            os.killpg(hub.pid, signal.SIGKILL)
            hub.wait(10)
        subprocess.run([DOCKER, "rm", "-fv", container], capture_output=True, timeout=60)
        if a.keep:
            print(f"kept {work}")
        else:
            shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
