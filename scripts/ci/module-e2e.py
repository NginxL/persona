#!/usr/bin/env python3
"""The module e2e gate: switch modules off and on against a test hub and the real web app in headless Chrome (#14417).

A module move (#13430) merges only after this passes on its branch. From a checkout (default: this one) it builds
personad, persona-hub, persona-manager and persona-node (a default build) and the web app (`npm run build`), starts a
disposable postgres:18-alpine container on a private port and `persona-hub run` in a temporary home with a clean
environment, its own hub.json and token, and the web app served by that hub, the start-up of
scripts/ci/core-only-smoke.py. Nothing of this machine's own hub (~/.persona, PERSONA_HOME, its ports, its database)
is read or touched. Then, for every module id in --modules:

- on: the hello has `modules` and module.<id>; the manager's `modules: loaded …` line has a server module; its
  server-side parts answer (SERVER below); in Chrome, light and dark, its entry (WEB below) is there and opens;
- off: the `modules` settings row {"disabled": [...]} is written into the disposable database and the hub restarted:
  the flag is gone from the hello while the marker stays, the manager leaves the module out, its extension, connector
  and routes are absent (404), its entry is gone, an address of its page answers like a removed page, and none of its
  chunks is requested;
- back on: the row is deleted and the hub restarted: everything returns;
- older hub: against the mock hub (scripts/mock/mock_hub.py) without the `modules` marker every module is on, the
  composer's voice button included; with the marker and without the module's flags they are off.

In every phase the core works: health ok, an authenticated request answers, the app loads, the main chat's composer
and the Agents page render, and the browser logs no page or console error. Prints one PASS / FAIL line per module and
check, writes the evidence (JSON) and the light and dark screenshots to local paths (never commit them), and ends with
one verdict line: PASS, FAIL or TIMEOUT; exits non-zero on anything but PASS.

    python3 scripts/ci/module-e2e.py --modules voice,health,recording [--checkout <dir>] [--shots <dir>]
        [--evidence <file>] [--no-build-web] [--no-mock] [--keep] [--timeout <seconds>]

Needs Go, Node with web/node_modules (npm ci), Chrome for playwright-core (the ms-playwright cache), Docker (DOCKER
names another client, e.g. a wrapper that runs `sg docker -c …`) and Python 3.

Adding a module: give it a row in SERVER (what the hub has of it) and in WEB (what the web app shows of it). A module
without a row is still checked for its hello flag and the manager's module list, and the run says it has no row.
"""
import argparse
import importlib.util
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
from pathlib import Path

HERE = Path(__file__).resolve()
ROOT = HERE.parents[2]
_spec = importlib.util.spec_from_file_location("core_only_smoke", HERE.parent / "core-only-smoke.py")
smoke = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(smoke)
Failed, TimedOut, http, run = smoke.Failed, smoke.TimedOut, smoke.http, smoke.run

# What the hub has of a module, checked against the test hub. Every path is fetched with the hub's token.
#   extensions: ids in /api/v1/extensions while on, absent off
#   connectors: ids in /api/v1/connectors?all=1 while on, absent off
#   routes: paths that answer (anything but 404) while on and 404 off
#   seed: requests made once while the module is first on, so the web app has something of it to show: [method, path, body]
# A client-only module (modapi.ClientModules) has an empty row: only its hello flag.
RECORDING_ID = "6f1c2a3b-0000-4000-8000-00000000e2e1"
SERVER = {
    "voice": {"extensions": ["voice"], "routes": ["/api/v1/extensions/voice/-/assistant-data"]},
    "volc": {"extensions": ["volc"], "routes": ["/api/v1/extensions/volc/-/assistant-data"]},
    "health": {},
    "glasses": {},
    "knowledge-ui": {},
    "recording": {
        "routes": ["/api/v1/recordings"],
        "seed": [["PUT", f"/api/v1/recordings/{RECORDING_ID}", {
            "revision": 1, "started_at": "2026-11-20T09:00:00Z", "duration": 60, "language": "en", "device": "phone",
            "title": "Module gate sample recording", "complete": True, "kind": "note",
            "segments": [{"start": 0, "end": 5, "text": "A made-up sample line for the module gate."}]}]],
    },
    "github": {"connectors": ["github"], "routes": ["/api/v1/connectors/github/auth"]},
    "imessage": {"extensions": ["imessage"]},
}

# What the web app shows of a module (web/e2e/module-gate.mjs). An entry:
#   name; hubs: where it is checked ("test": the hub this gate starts, no model or computer; "mock": the mock hub, which
#   has conversations); page: the address it lives on; ready: what shows that page rendered, on or off; selector (CSS)
#   or text (a regular expression): the entry itself, visible while on, absent off; opens: what clicking it shows while
#   on (selector, text or url); off_page: an address of the module's own that answers like a removed page while off.
# chunks: regular expressions for the module's own files as the production build names them (web/dist/assets) and as
#   the dev server serves them; none may be requested while off. chunk_when_on: the hubs where one must load while on.
NOT_FOUND = r"no page|not found|doesn.t exist|isn.t here|no longer"
WEB = {
    "voice": {
        "entries": [
            {"name": "Settings › Extensions row", "hubs": ["test"], "page": "/settings?tab=extensions",
             "ready": {"selector": "[data-extension-row]"}, "selector": "[data-extension-row='voice']",
             "opens": {"url": r"voice"}},
            {"name": "composer voice button", "hubs": ["mock"], "page": "/", "ready": {"selector": "textarea"},
             "selector": "[data-testid='composer-voice']"},
        ],
        "chunks": [r"/assets/(slots|VoiceCall|VoiceBar|VoiceParts)-[\w-]+\.js", r"/src/modules/voice/"],
        "chunk_when_on": ["mock"],  # its composer slot mounts in a conversation, which only the mock hub has
    },
    "health": {
        "entries": [
            {"name": "sidebar entry", "hubs": ["test", "mock"], "page": "/", "ready": {"selector": "a[href='/inbox']"},
             "selector": "a[href='/health']", "opens": {"url": r"/health"}, "off_page": {"path": "/health", "text": NOT_FOUND}},
        ],
        "chunks": [r"/assets/(Health|HealthReports|HealthIntakePage)-[\w-]+\.js", r"/src/modules/health/"],
        "chunk_when_on": ["test", "mock"],
    },
    "recording": {
        "entries": [
            {"name": "Knowledge › Meetings recording", "hubs": ["test"], "page": "/kb/meetings",
             "ready": {"text": r"^Meetings$"}, "text": r"Module gate sample recording",
             "opens": {"selector": "main :text('A made-up sample line')"}},
        ],
        "chunks": [r"/assets/RecordingDetail-[\w-]+\.js", r"/src/modules/recording/"],
        "chunk_when_on": ["test"],
    },
}
# Console lines that are not the app's errors. The test hub has no computer, so the assistant's memory (kept on the
# assistant's computer) answers 409; the mock hub answers 404 for what it does not script. Chrome cancels loads in
# flight with ERR_NETWORK_CHANGED when the machine's interfaces change (another Docker container starting or stopping);
# the page checks around it still have to pass.
NETWORK_CHANGED = r"net::ERR_NETWORK_CHANGED"
IGNORE_CONSOLE = {"test": [r"status of 409 .*/api/v1/assistant/memory", NETWORK_CHANGED],
                  "mock": [r"status of 404", r"status of 501", NETWORK_CHANGED]}

PROGRAMS = smoke.PROGRAMS
DEADLINE = [0.0]
# Whether a chunk an off module's page fetches before that page's hello fails the run (--strict-before-hello) or is a
# WARN: the web draws every module as on until the hello comes (docs/web/web.md "When a module is there").
STRICT_BEFORE_HELLO = [False]


def remaining(limit):
    left = DEADLINE[0] - time.monotonic()
    if left <= 0:
        raise TimedOut("the gate's --timeout")
    return min(limit, left)


class Report:
    def __init__(self):
        self.lines, self.failed, self.warned = [], 0, 0

    def add(self, phase, module, check, ok, detail="", scheme="", warn=False):
        line = {"phase": phase, "module": module, "check": check, "scheme": scheme, "ok": bool(ok), "detail": detail}
        if warn and not ok:
            line["warn"] = True
        self.lines.append(line)
        word = "PASS" if ok else "WARN" if warn else "FAIL"
        if word == "FAIL":
            self.failed += 1
        elif word == "WARN":
            self.warned += 1
        where = f" [{scheme}]" if scheme else ""
        print(f"{word} {phase} {module}: {check}{where}" + (f" ({detail})" if detail and not ok else ""), flush=True)


class Hub:
    """persona-hub run in the temporary home; restarted between phases with the same home, port and database."""

    def __init__(self, bindir, work, home, port, token):
        self.bindir, self.work, self.home, self.port, self.token = bindir, work, home, port, token
        self.base = f"http://127.0.0.1:{port}"
        self.proc, self.starts = None, 0

    def start(self):
        self.starts += 1
        self.log = self.work / f"hub-{self.starts}.log"
        env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": str(self.home), "TMPDIR": str(self.work),
               "LANG": "C.UTF-8", "PERSONA_LOCAL_NODE": "0"}
        with open(self.log, "w") as out:
            self.proc = subprocess.Popen([str(self.bindir / "persona-hub"), "run"], cwd=self.work, env=env, stdout=out,
                                         stderr=subprocess.STDOUT, start_new_session=True)

        def healthy():
            smoke.check(self.proc.poll() is None, f"the hub exited ({self.proc.returncode}) before answering")
            status, body = http(self.base + "/api/v1/health")
            return status == 200 and json.loads(body).get("ok") is True

        smoke.wait_for("hub health", remaining(90), healthy)
        line = re.compile(r"modules: loaded (.+)$", re.M)
        smoke.wait_for("the manager's module list", remaining(120), lambda: line.search(self.log.read_text()) or self.proc.poll() is not None)
        smoke.check(self.proc.poll() is None, f"the hub exited ({self.proc.returncode})")
        loaded = line.search(self.log.read_text()).group(1).strip()
        # the manager's flags reach the hub's hello once it is connected
        smoke.wait_for("the manager's flags in the hello", remaining(60), lambda: "modules" in self.features())
        return loaded

    def features(self):
        status, body = http(self.base + "/api/v1/health")
        return json.loads(body).get("features", []) if status == 200 else []

    def stop(self):
        if self.proc is None or self.proc.poll() is not None:
            return
        os.killpg(self.proc.pid, signal.SIGTERM)
        try:
            code = self.proc.wait(remaining(60))
        except subprocess.TimeoutExpired:
            os.killpg(self.proc.pid, signal.SIGKILL)
            self.proc.wait(10)
            raise TimedOut("hub shutdown")
        smoke.check(code == 0, f"the hub exited {code} on SIGTERM")

    def request(self, method, path, body=None):
        import urllib.request, urllib.error
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method,
                                     headers={"Authorization": f"Bearer {self.token}", "Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                return resp.status, resp.read().decode()
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode()


def psql(container, sql):
    r = run([smoke.DOCKER, "exec", container, "psql", "-v", "ON_ERROR_STOP=1", "-U", "persona", "-d", "persona_manager",
             "-Atc", sql], remaining(30))
    if r.returncode != 0:
        raise Failed("psql: " + r.stderr.strip())
    return r.stdout.strip()


def build_web(checkout, npm_ci):
    web = checkout / "web"
    if npm_ci or not (web / "node_modules").is_dir():
        r = run(["npm", "ci", "--no-audit", "--no-fund"], remaining(900), cwd=web)
        if r.returncode != 0:
            raise Failed("npm ci: " + (r.stderr or r.stdout).strip()[-2000:])
    r = run(["npm", "run", "build"], remaining(900), cwd=web)
    if r.returncode != 0:
        raise Failed("web build: " + (r.stderr or r.stdout).strip()[-2000:])
    return web / "dist"


def build_go(checkout, out):
    env = dict(os.environ, GOFLAGS=(os.environ.get("GOFLAGS", "") + " -trimpath").strip())
    r = run(["go", "build", "-o", str(out) + "/"] + [f"./cmd/{p}" for p in PROGRAMS], remaining(900), cwd=checkout / "server", env=env)
    if r.returncode != 0:
        raise Failed("build: " + (r.stderr or r.stdout).strip()[-2000:])


def server_checks(rep, phase, hub, ids, on, loaded):
    features = hub.features()
    rep.add(phase, "core", "hub health ok", True)
    status, _ = hub.request("GET", "/api/v1/managers")
    rep.add(phase, "core", "an authenticated request answers (/api/v1/managers)", status == 200, f"status {status}")
    rep.add(phase, "core", "the hello has the modules marker", "modules" in features)
    status, body = hub.request("GET", "/api/v1/extensions")
    extensions = {e["id"] for e in json.loads(body).get("extensions", [])} if status == 200 else set()
    status, body = hub.request("GET", "/api/v1/connectors?all=1")
    connectors = {c["id"] for c in (json.loads(body).get("connectors") or [])} if status == 200 else set()
    for id in ids:
        want = on[id]
        rep.add(phase, id, f"hello {'has' if want else 'lacks'} module.{id}", (f"module.{id}" in features) == want)
        if (hub.checkout / "server/internal/modules" / id / "module.json").exists():
            rep.add(phase, id, f"the manager {'loads' if want else 'leaves out'} the module (modules: loaded {loaded})",
                    (id in [m.strip() for m in loaded.split(",")]) == want)
        row = SERVER.get(id)
        if row is None:
            rep.add(phase, id, "has a SERVER row in scripts/ci/module-e2e.py", False, "add one: what the hub has of it")
            continue
        for ext in row.get("extensions", []):
            rep.add(phase, id, f"extension {ext} {'listed' if want else 'absent'}", (ext in extensions) == want, sorted(extensions))
        for con in row.get("connectors", []):
            rep.add(phase, id, f"connector {con} {'registered' if want else 'absent'}", (con in connectors) == want, sorted(connectors))
        for path in row.get("routes", []):
            status, _ = hub.request("GET", path)
            rep.add(phase, id, f"{path} {'answers' if want else 'is 404'}", (status != 404) == want, f"status {status}")


def browser(rep, phase, base, token, hub_kind, ids, on, shots, work, schemes):
    modules = {id: WEB[id] for id in ids if id in WEB}
    for id in ids:
        if id not in WEB and phase == "on":
            print(f"NOTE {id}: no WEB row in scripts/ci/module-e2e.py, so no browser checks for it", flush=True)
    spec = {"base": base, "token": token, "phase": phase, "hub": hub_kind, "expect": {id: on[id] for id in modules},
            "modules": modules, "shots": str(shots), "out": str(work / f"browser-{phase}.json"), "schemes": schemes,
            "ignore_console": IGNORE_CONSOLE[hub_kind], "strict_before_hello": STRICT_BEFORE_HELLO[0],
            "mask_text": socket.gethostname()}
    path = work / f"spec-{phase}.json"
    path.write_text(json.dumps(spec))
    r = run(["node", str(ROOT / "web/e2e/module-gate.mjs"), str(path)], remaining(600), cwd=ROOT / "web")
    if not Path(spec["out"]).exists():
        raise Failed(f"the browser run of {phase} wrote nothing: " + (r.stderr or r.stdout).strip()[-2000:])
    seen = json.loads(Path(spec["out"]).read_text())
    for c in seen["checks"]:
        rep.add(phase, c["module"], c["check"], c["ok"], c["detail"], c["scheme"], c.get("warn", False))
    if r.returncode != 0:
        rep.add(phase, "core", "the browser run finished", False, (r.stderr or r.stdout).strip()[-600:])
    return seen


def mock_phase(rep, phase, ids, flags, dist, shots, work, schemes, procs):
    """The web app (vite preview of the same build) against the mock hub, as an older hub or with module flags."""
    hub_port, web_port = smoke.free_port(), smoke.free_port()
    env = dict(os.environ, PORT=str(hub_port), MOCK_VOICE="1")
    env.pop("MOCK_HUB_MODULES", None)
    if flags is not None:
        env["MOCK_HUB_MODULES"] = ",".join(flags) or "none"
    log = open(work / f"mock-{phase}.log", "w")
    mock = subprocess.Popen([sys.executable, str(ROOT / "scripts/mock/mock_hub.py")], cwd=work, env=env, stdout=log,
                            stderr=subprocess.STDOUT, start_new_session=True)
    procs.append(mock)
    smoke.wait_for("the mock hub", remaining(60), lambda: http(f"http://127.0.0.1:{hub_port}/api/v1/health")[0] == 200)
    wenv = dict(os.environ, PERSONA_HUB_URL=f"http://127.0.0.1:{hub_port}")
    preview = subprocess.Popen(["npx", "vite", "preview", "--outDir", str(dist), "--host", "127.0.0.1", "--port", str(web_port),
                                "--strictPort"], cwd=ROOT / "web", env=wenv, stdout=log, stderr=subprocess.STDOUT,
                               start_new_session=True)
    procs.append(preview)
    smoke.wait_for("vite preview", remaining(60), lambda: http(f"http://127.0.0.1:{web_port}/")[0] == 200)
    on = {id: flags is None for id in ids}
    try:
        browser(rep, phase, f"http://127.0.0.1:{web_port}", "mock", "mock", ids, on, shots, work, schemes)
    finally:
        for p in (preview, mock):
            if p.poll() is None:
                os.killpg(p.pid, signal.SIGTERM)
                try:
                    p.wait(15)
                except subprocess.TimeoutExpired:
                    os.killpg(p.pid, signal.SIGKILL)
                    p.wait(5)


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--modules", required=True, help="comma-separated module ids, e.g. voice,health,recording")
    ap.add_argument("--checkout", type=Path, default=ROOT, help="the checkout to build (default: this one)")
    ap.add_argument("--shots", type=Path, default=Path.home() / "persona-pr-shots/module-e2e", help="screenshots go here")
    ap.add_argument("--evidence", type=Path, help="the JSON evidence file (default: <shots>/evidence.json)")
    ap.add_argument("--no-build-web", action="store_true", help="serve the checkout's existing web/dist")
    ap.add_argument("--npm-ci", action="store_true", help="run npm ci before building the web app")
    ap.add_argument("--no-mock", action="store_true", help="leave out the mock hub phases (older hub, flags off)")
    ap.add_argument("--schemes", default="light,dark")
    ap.add_argument("--strict-before-hello", action="store_true",
                    help="fail on an off module's chunk fetched before the page's hello (a WARN otherwise)")
    ap.add_argument("--keep", action="store_true", help="keep the temporary directory (logs, home)")
    ap.add_argument("--timeout", type=int, default=2400, help="the whole run, in seconds")
    a = ap.parse_args()
    DEADLINE[0] = time.monotonic() + a.timeout
    STRICT_BEFORE_HELLO[0] = a.strict_before_hello
    ids = [m.strip() for m in a.modules.split(",") if m.strip()]
    checkout = a.checkout.resolve()
    shots = a.shots.expanduser().resolve()
    shots.mkdir(parents=True, exist_ok=True)
    evidence = (a.evidence or shots / "evidence.json").expanduser().resolve()
    schemes = [s for s in a.schemes.split(",") if s]
    work = Path(tempfile.mkdtemp(prefix="persona-module-e2e."))
    bindir, home = work / "bin", work / "home"
    container = "persona-module-e2e-" + secrets.token_hex(4)
    rep, hub, procs, verdict = Report(), None, [], "FAIL"
    commit = run(["git", "-C", str(checkout), "rev-parse", "HEAD"], 10).stdout.strip()
    dirty = bool(run(["git", "-C", str(checkout), "status", "--porcelain", "--untracked-files=no"], 10).stdout.strip())
    out = {"commit": commit + ("-dirty" if dirty else ""), "checkout": str(checkout), "modules": ids, "phases": {}}
    started = time.monotonic()
    try:
        build_go(checkout, bindir)
        dist = checkout / "web/dist" if a.no_build_web else build_web(checkout, a.npm_ci)
        smoke.check((dist / "index.html").exists(), f"no web build at {dist}")
        print(f"built {', '.join(PROGRAMS)} and the web app from {checkout} at {out['commit'][:12]} in {time.monotonic() - started:.0f}s", flush=True)
        database = smoke.postgres(container)
        port, token = smoke.free_port(), secrets.token_hex(24)
        (home / ".persona").mkdir(parents=True)
        cfg = home / ".persona/hub.json"
        cfg.write_text(json.dumps({"listen": f"127.0.0.1:{port}", "token": token, "public_url": f"http://127.0.0.1:{port}",
                                   "name": "Module gate hub", "database_url": database, "machines": []}))
        cfg.chmod(0o600)
        shutil.copytree(dist, home / ".persona/web")  # newer than the build's embedded copy, so the hub serves it
        hub = Hub(bindir, work, home, port, token)
        hub.checkout = checkout

        phases = [("on", None), ("off", ids), ("back-on", [])]
        for phase, disabled in phases:
            if disabled is not None:
                hub.stop()
                if disabled:
                    psql(container, "INSERT INTO settings (key, value) VALUES ('modules', '%s'::jsonb) ON CONFLICT (key) DO UPDATE "
                                    "SET value = EXCLUDED.value, updated_at = now()" % json.dumps({"disabled": disabled}))
                else:
                    psql(container, "DELETE FROM settings WHERE key = 'modules'")
            loaded = hub.start()
            row = psql(container, "SELECT value::text FROM settings WHERE key = 'modules'")
            print(f"{phase}: settings row modules = {row or '(none)'}; manager: modules: loaded {loaded}", flush=True)
            on = {id: not disabled for id in ids}
            if phase == "on":
                for id in ids:
                    for method, path, body in SERVER.get(id, {}).get("seed", []):
                        status, text = hub.request(method, path, body)
                        smoke.check(status < 300, f"seeding {id}: {method} {path}: {status} {text[:200]}")
            server_checks(rep, phase, hub, ids, on, loaded)
            seen = browser(rep, phase, hub.base, token, "test", ids, on, shots, work, schemes)
            out["phases"][phase] = {"settings_row": row or None, "manager_line": f"modules: loaded {loaded}",
                                    "hello_module_flags": [f for f in hub.features() if f == "modules" or f.startswith("module.")],
                                    "browser_hello": seen["hello"], "requested": seen["requested"]}
            if phase == "on":
                all_flags = out["phases"]["on"]["hello_module_flags"]
        hub.stop()
        hub = None
        if not a.no_mock:
            mock_phase(rep, "older-hub", ids, None, dist, shots, work, schemes, procs)
            mock_phase(rep, "mock-flags-off", ids, [f for f in all_flags if f != "modules" and f.removeprefix("module.") not in ids],
                       dist, shots, work, schemes, procs)
        verdict = "PASS" if rep.failed == 0 else "FAIL"
        summary = f"{verdict} module e2e gate for {', '.join(ids)} at {out['commit'][:12]}: {len(rep.lines) - rep.failed - rep.warned} checks passed, {rep.failed} failed, {rep.warned} warnings ({time.monotonic() - started:.0f}s)"
    except (Failed, TimedOut) as e:
        verdict = "TIMEOUT" if isinstance(e, TimedOut) else "FAIL"
        logs = sorted(work.glob("hub-*.log"))
        if logs:
            sys.stderr.write(logs[-1].read_text()[-6000:])
        summary = f"{verdict} module e2e gate for {', '.join(ids)} at {out['commit'][:12]}: {e}"
    finally:
        if hub is not None and hub.proc is not None and hub.proc.poll() is None:
            os.killpg(hub.proc.pid, signal.SIGKILL)
            hub.proc.wait(10)
        for p in procs:
            if p.poll() is None:
                os.killpg(p.pid, signal.SIGKILL)
                p.wait(10)
        subprocess.run([smoke.DOCKER, "rm", "-fv", container], capture_output=True, timeout=60)
        if a.keep:
            print(f"kept {work}")
        else:
            shutil.rmtree(work, ignore_errors=True)
    out.update(verdict=verdict, summary=summary, checks=rep.lines, shots=str(shots))
    evidence.parent.mkdir(parents=True, exist_ok=True)
    evidence.write_text(json.dumps(out, indent=2, ensure_ascii=False))
    print(f"evidence: {evidence}; screenshots: {shots}")
    print(summary)
    return 0 if verdict == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
