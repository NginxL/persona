"""Loopback-only process and PostgreSQL fixtures for the Android Inbox handoff tests."""
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import subprocess
import time
from urllib.error import URLError
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[2]


def until(description, check, seconds=60):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        try:
            result = check()
            if result:
                return result
        except (URLError, TimeoutError, ConnectionError, FileNotFoundError):
            pass  # The isolated process or its bus connection may still be starting.
        time.sleep(0.25)
    raise RuntimeError("Timed out waiting for " + description)


def stop(child):
    if child.poll() is None:
        os.killpg(child.pid, signal.SIGTERM)
        try:
            child.wait(timeout=15)
        except subprocess.TimeoutExpired:
            os.killpg(child.pid, signal.SIGKILL)
            child.wait()


def start(stack, label, args, work, env):
    log = stack.enter_context((work / (label + ".log")).open("ab"))
    child = subprocess.Popen(args, cwd=work, env=env, stdin=subprocess.DEVNULL,
                             stdout=log, stderr=log, start_new_session=True)
    stack.callback(stop, child)
    return child


def request(base, token, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = Request(base + "/api/v1/" + path, data=data, headers={
        "Authorization": "Bearer " + token, "Content-Type": "application/json",
    })
    with urlopen(req, timeout=5) as answer:
        return json.load(answer)


def lines(path):
    return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []


def build_tools(work, observer="ssh"):
    binary = work / "bin"
    binary.mkdir()
    for filename, package in (("personad", "./cmd/personad"), (observer, "./internal/hub/testdata/android-inbox-handoff-node")):
        subprocess.run(["go", "build", "-o", binary / filename, package], cwd=ROOT / "server", check=True)
    return binary


def postgres(stack):
    password = secrets.token_hex(24)
    cid = subprocess.check_output([
        "docker", "run", "-d", "--rm", "--tmpfs", "/var/lib/postgresql", "-p", "127.0.0.1::5432",
        "-e", "POSTGRES_PASSWORD", "-e", "POSTGRES_USER=persona", "-e", "POSTGRES_DB=hub_fixture",
        "postgres:18-alpine",
    ], env=dict(os.environ, POSTGRES_PASSWORD=password), text=True).strip()
    stack.callback(subprocess.run, ["docker", "stop", "--timeout", "5", cid], stdout=subprocess.DEVNULL, check=True)

    def sql(db, statement):
        return subprocess.check_output(["docker", "exec", "-i", cid, "psql", "-XAtq", "-v", "ON_ERROR_STOP=1",
                                        "-U", "persona", "-d", db], input=statement, text=True).strip()

    until("PostgreSQL", lambda: subprocess.run(
        ["docker", "exec", cid, "pg_isready", "-h", "127.0.0.1", "-U", "persona", "-d", "hub_fixture"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0)
    address = subprocess.check_output(["docker", "port", cid, "5432/tcp"], text=True).strip()
    if not address.startswith("127.0.0.1:"):
        raise RuntimeError("The test database must be bound to loopback")
    db_origin = "postgres://persona:" + password + "@" + address + "/"
    return db_origin, sql


def start_hub(stack, work, binary, machines, db_origin):
    with socket.socket() as port:
        port.bind(("127.0.0.1", 0))
        listen = "127.0.0.1:" + str(port.getsockname()[1])
    base = "http://" + listen
    token = secrets.token_hex(32)
    home, runtime, temp = work / "home", work / "hub", work / "tmp"
    for path in (home, runtime, temp):
        path.mkdir(mode=0o700, exist_ok=True)
    (runtime / "hub.json").write_text(json.dumps(dict(
        listen=listen, public_url=base, token=token, name="Inbox fixture hub", mesh={"discovery": False},
        machines=machines, database_url=db_origin + "hub_fixture?sslmode=disable",
        public={"advertise_lan": False, "advertise_ipv6": False}, node_packages={"url": "off"},
    )))
    (runtime / "hub.json").chmod(0o600)
    env = dict(HOME=str(home), PERSONA_HOME=str(runtime), TMPDIR=str(temp), PATH=str(binary) + ":" + os.defpath,
               LANG="en_US.UTF-8", PERSONA_LOCAL_NODE="0", PERSONA_HUB_EDGE="0", PERSONA_SPLIT_PROGRAMS="0")
    cli = str(binary / "personad")
    def run_hub():
        return start(stack, "hub", [cli, "hub"], work, env)

    child = run_hub()
    api = lambda path, body=None: request(base, token, path, body)
    health = until("the isolated hub", lambda: api("health"))

    def online(manager):
        return any(m["id"] == manager and m.get("online") for m in api("managers")["managers"])

    until("the default manager", lambda: online("assistant"))
    def restart():
        nonlocal child
        stop(child)
        child = run_hub()
        until("the restarted manager", lambda: online("assistant"))

    return base, token, env, health, online, api, restart
