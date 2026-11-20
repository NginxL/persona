#!/usr/bin/env python3
"""Switch an ssh computer to Persona Link end to end (#10662, docs/operations/link.md "Switching from SSH").

    python3 scripts/test/link_switch_e2e.py --bin <dir> [--scenario success|rollback|linger_off] [--port-base 18741]
        [--prefix persona-link-switch-e2e] [--image <image>] [--web <web/dist>] [--hold <seconds>]

<dir> holds linux/amd64 builds of personad and persona-link of one version. Everything is disposable: a hub and its
built-in manager under an isolated HOME / PERSONA_HOME, a Postgres container, and a systemd container as the computer
(privileged: systemd needs a writable cgroup), reached over ssh through a wrapper with its own config, key and
known_hosts. The user's ~/.ssh, ~/.persona and services are never read or written. Containers are named with --prefix
and removed at the end, the private key with them.

Scenarios: success (switches and connects); rollback (the computer cannot reach the hub's TLS port, so the switch rolls
back: kind ssh again, the credential revoked, what it installed removed, online over ssh); linger_off (refused at the
check, nothing changed). Ports: base ssh, base+1 hub http, base+2 hub tls, base+3 postgres. Prints a line per step and a
final RESULT line. --hold keeps everything running that many seconds after the checks (screenshots), then cleans up.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

DOCKERFILE = """FROM debian:bookworm
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends systemd systemd-sysv \\
    dbus-user-session libpam-systemd openssh-server ca-certificates iproute2 && rm -rf /var/lib/apt/lists/*
RUN useradd -m -s /bin/bash testuser && mkdir -p /run/sshd && printf '%s\\n' 'PasswordAuthentication no' \\
    'PubkeyAuthentication yes' 'PermitRootLogin no' >> /etc/ssh/sshd_config
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
"""
MACHINE = "e2e-box"
RUNNING = {"checking", "installing", "connecting", "rolling_back"}


def say(*parts):
    print(*parts, flush=True)


def run(args, timeout=60, check=True, **kw):
    r = subprocess.run(args, capture_output=True, text=True, timeout=timeout, **kw)
    if check and r.returncode:
        raise RuntimeError(f"{args[0]} {args[1] if len(args) > 1 else ''} failed (exit {r.returncode}): {r.stderr.strip()[:300]}")
    return r


def wait(check, seconds, label, every=0.25):
    deadline = time.monotonic() + seconds
    last = None
    while time.monotonic() < deadline:
        try:
            v = check()
            if v:
                return v
        except Exception as e:  # noqa: BLE001 - a probe that fails is a probe that has not succeeded yet
            last = e
        time.sleep(every)
    raise TimeoutError(f"{label} (last error: {last})")


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--bin", required=True)
    p.add_argument("--scenario", choices=["success", "rollback", "linger_off", "hub_pause"], default="success")
    p.add_argument("--port-base", type=int, default=18741)
    p.add_argument("--prefix", default="persona-link-switch-e2e")
    p.add_argument("--image", default="")
    p.add_argument("--web", default="")
    p.add_argument("--node-service", action="store_true",
                   help="give the computer its own resident node service first (personad-node): a switch must leave it "
                        "as it was, and a rollback must not remove it")
    p.add_argument("--pause", type=int, default=150, help="hub_pause: seconds the hub stays stopped")
    p.add_argument("--hold", type=int, default=0)
    p.add_argument("--manual", action="store_true",
                   help="start no switch: print READY with the hub's address and the file holding its token, and wait for "
                        "one started from the web app (a browser test clicks it), then check it the same way")
    p.add_argument("--keep-logs", default="", help="copy the scrubbed hub log here")
    a = p.parse_args()
    if sys.platform != "linux":
        p.error("needs Linux and docker")
    bindir = Path(a.bin).resolve()
    for name in ("personad", "persona-link"):
        if not (bindir / name).is_file():
            p.error(f"{bindir}/{name} is missing")
    ssh_port, http_port, tls_port, pg_port = (a.port_base + i for i in range(4))
    # The default bridge's gateway, a host address: the hub listens there, and the computer reaches it from its own
    # network (so cutting that route leaves ssh's replies, which go to its own network's gateway, alone).
    gateway = run(["docker", "network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}"]).stdout.strip()
    if not gateway:
        p.error("the docker bridge network has no gateway address")
    root = Path(tempfile.mkdtemp(prefix="link-switch-e2e-"))
    home, state, nodes, sshdir = root / "home", root / "home/.persona", root / "nodes", root / "ssh"
    for d in (state, nodes, sshdir, root / "sshbin", home / "bin"):
        d.mkdir(parents=True, mode=0o700, exist_ok=True)
    db, box, net = a.prefix + "-db", a.prefix + "-box", a.prefix + "-net"
    made = {"db": False, "box": False, "net": False}
    hub = None
    paused = []  # the hub's slot process while hub_pause has it stopped
    verdict = "FAIL"
    token, password = secrets.token_hex(24), secrets.token_hex(24)
    base = f"http://{gateway}:{http_port}"
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def api(path, method="GET", body=None):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(base + path, data=data, method=method,
                                     headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"})
        try:
            with opener.open(req, timeout=20) as r:
                return r.status, json.loads(r.read() or b"null")
        except urllib.error.HTTPError as e:
            raw = e.read(8192)
            try:
                return e.code, json.loads(raw)
            except ValueError:
                return e.code, {"raw": raw.decode(errors="replace")[:200]}

    def machine():
        _, data = api("/api/v1/machines")
        rows = data.get("machines", []) if isinstance(data, dict) else data
        return next((m for m in rows if m.get("id") == MACHINE), None)

    def box_sh(cmd, user=True, check=True, timeout=60):
        if user:
            cmd = "su - testuser -c " + shq("export XDG_RUNTIME_DIR=/run/user/$(id -u); " + cmd)
        return run(["docker", "exec", box, "sh", "-c", cmd], check=check, timeout=timeout)

    # SIGTERM ends a hold early; otherwise it stops the run with its cleanup (the default would leave the containers).
    stop = {"holding": False, "now": False}

    def on_term(*_):
        if stop["holding"]:
            stop["now"] = True
        else:
            raise SystemExit(1)

    signal.signal(signal.SIGTERM, on_term)
    try:
        version = run([str(bindir / "personad"), "version"]).stdout.strip()
        image = a.image
        if not image:
            image = a.prefix + ":local"
            ctx = root / "image"
            ctx.mkdir()
            (ctx / "Dockerfile").write_text(DOCKERFILE)
            run(["docker", "build", "-q", "-t", image, str(ctx)], timeout=900)
        say(f"STEP build: hub {version}, image {image}")

        # The computer: systemd, sshd, the user testuser with this run's key, linger as the scenario wants.
        run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(sshdir / "key")])
        run(["docker", "network", "create", net])
        made["net"] = True
        run(["docker", "run", "-d", "--name", box, "--hostname", MACHINE, "--network", net, "--privileged",
             "--cgroupns=private", "--tmpfs", "/run", "--tmpfs", "/run/lock", "-p", f"127.0.0.1:{ssh_port}:22", image])
        made["box"] = True
        wait(lambda: run(["docker", "exec", box, "systemctl", "is-system-running"], check=False).stdout.strip()
             in ("running", "degraded"), 60, "the computer's systemd")
        pub = (sshdir / "key.pub").read_text()
        run(["docker", "exec", "-i", box, "sh", "-c",
             "install -d -m 700 -o testuser -g testuser /home/testuser/.ssh /home/testuser/.persona /home/testuser/.persona/bin"
             " && cat > /home/testuser/.ssh/authorized_keys && chown testuser:testuser /home/testuser/.ssh/authorized_keys"
             " && chmod 600 /home/testuser/.ssh/authorized_keys"], input=pub)
        run(["docker", "cp", str(bindir / "personad"), f"{box}:/home/testuser/.persona/bin/personad"])
        run(["docker", "exec", box, "chown", "testuser:testuser", "/home/testuser/.persona/bin/personad"])
        if a.scenario != "linger_off":
            run(["docker", "exec", box, "loginctl", "enable-linger", "testuser"])
            wait(lambda: box_sh("systemctl --user is-system-running", check=False).stdout.strip() in ("running", "degraded"),
                 30, "the computer's user manager")
        unit_before = ""
        if a.node_service:
            unit = ("[Unit]\nDescription=resident node (the computer's own, before the switch)\n[Service]\n"
                    "ExecStart=%h/.persona/bin/personad node --attach\nRestart=always\nRestartSec=2\n"
                    "[Install]\nWantedBy=default.target\n")
            run(["docker", "exec", "-i", box, "su", "-", "testuser", "-c",
                 "mkdir -p ~/.config/systemd/user && cat > ~/.config/systemd/user/personad-node.service"], input=unit)
            box_sh("systemctl --user daemon-reload && systemctl --user enable --now personad-node.service")
            wait(lambda: box_sh("systemctl --user is-active personad-node", check=False).stdout.strip() == "active", 30,
                 "the computer's own node service")
            unit_before = box_sh("sha256sum ~/.config/systemd/user/personad-node.service").stdout.split()[0]
        hostkey = run(["docker", "exec", box, "cat", "/etc/ssh/ssh_host_ed25519_key.pub"]).stdout.split()
        (sshdir / "known_hosts").write_text(f"[127.0.0.1]:{ssh_port} {hostkey[0]} {hostkey[1]}\n")
        (sshdir / "config").write_text(
            f"Host {MACHINE}\n  HostName 127.0.0.1\n  Port {ssh_port}\n  User testuser\n  IdentityFile {sshdir}/key\n"
            f"  IdentitiesOnly yes\n  UserKnownHostsFile {sshdir}/known_hosts\n  StrictHostKeyChecking yes\n"
            "  GlobalKnownHostsFile /dev/null\n")
        wrapper = root / "sshbin/ssh"
        wrapper.write_text(f"#!/bin/sh\nexec /usr/bin/ssh -F {shq(str(sshdir / 'config'))} \"$@\"\n")
        wrapper.chmod(0o700)
        run([str(wrapper), "-o", "BatchMode=yes", MACHINE, "true"], timeout=30)
        say("STEP computer: ssh as testuser works, linger", "off" if a.scenario == "linger_off" else "on")

        # The hub: its own HOME, Postgres, TLS on docker0, the connector build beside the node builds.
        run(["docker", "run", "-d", "--name", db, "-e", "POSTGRES_PASSWORD=" + password, "-e", "POSTGRES_DB=persona",
             "-p", f"127.0.0.1:{pg_port}:5432", "postgres:18-alpine"])
        made["db"] = True
        wait(lambda: run(["docker", "exec", db, "pg_isready", "-U", "postgres", "-d", "persona"], check=False).returncode == 0,
             60, "the database")
        shutil.copy2(bindir / "personad", home / "bin/personad")
        shutil.copy2(bindir / "persona-link", nodes / "persona-link-linux-amd64")
        (nodes / "persona-link-linux-amd64.version").write_text(version + "\n")
        if a.web:
            shutil.copytree(a.web, state / "web")
        config = {"listen": f"{gateway}:{http_port}", "public_url": base, "public": {"tls_listen": f"{gateway}:{tls_port}"},
                  "token": token, "name": "E2E hub",
                  "database_url": f"postgres://postgres:{password}@127.0.0.1:{pg_port}/persona?sslmode=disable",
                  "auto_update_nodes": False, "update": {"url": "off"}, "node_packages": {"url": "off"},
                  "machines": [{"id": MACHINE, "name": "E2E box", "kind": "ssh", "ssh_host": MACHINE, "icon": "server",
                                "herdr_autostart": False, "botmux": False, "usage_windows": False}]}
        (state / "hub.json").write_text(json.dumps(config))
        (state / "hub.json").chmod(0o600)
        spec = importlib.util.spec_from_file_location("server_test_env", Path(__file__).with_name("server_test_env.py"))
        env_mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(env_mod)
        env = env_mod.clean_environment(os.environ)
        for key in ("INVOCATION_ID", "XPC_SERVICE_NAME", "SSH_AUTH_SOCK"):
            env.pop(key, None)
        env.update(HOME=str(home), PERSONA_HOME=str(state), PERSONA_HUB_EDGE="1", PERSONA_NODE_BINARIES=str(nodes),
                   PATH=f"{root / 'sshbin'}:{env.get('PATH', '/usr/bin:/bin')}")
        log = open(root / "hub.log", "wb")
        hub = subprocess.Popen([str(home / "bin/personad"), "hub"], cwd=home, env=env, stdin=subprocess.DEVNULL,
                               stdout=log, stderr=log, start_new_session=True)
        m = wait(lambda: (lambda m: m if m and m.get("online") and m.get("transport") == "ssh" else None)(machine()),
                 120, "the computer online over ssh")
        say(f"STEP hub: {MACHINE} online over ssh, link_switch={json.dumps(m.get('link_switch'))}")

        if a.scenario in ("rollback", "hub_pause"):
            # The computer can no longer reach the hub (its TLS port included); ssh from the hub still works, its
            # replies go to the computer's own network's gateway.
            run(["docker", "exec", box, "ip", "route", "add", "prohibit", f"{gateway}/32"])
            say(f"STEP rollback setup: the computer cannot reach {gateway}")

        ls = m.get("link_switch") or {}
        if a.scenario == "linger_off":
            if not ls.get("available"):
                raise AssertionError(f"link_switch should be offered before the check: {ls}")
        elif not ls.get("available"):
            raise AssertionError(f"link_switch not available: {ls}")

        before_files = box_sh("ls -a ~/.persona ~/.persona/bin ~/.config/systemd/user 2>/dev/null; true").stdout
        if a.manual:
            (root / "token").write_text(token)
            say(f"READY {base} {root / 'token'} {MACHINE}")
            wait(lambda: ((machine() or {}).get("link_switch") or {}).get("phase"), 900, "a switch started from the web app")
            began = time.monotonic()
        else:
            began = time.monotonic()
            status, body = api(f"/api/v1/machines/{MACHINE}/link-switch", "POST", {})
            took = (time.monotonic() - began) * 1000
            say(f"STEP start: POST {status} in {took:.0f} ms: {json.dumps(body)}")
            if status != 202:
                raise AssertionError("the start was not accepted")
            status2, body2 = api(f"/api/v1/machines/{MACHINE}/link-switch", "POST", {})
            say(f"STEP second start: {status2} {json.dumps(body2)}")
            if status2 != 409:
                raise AssertionError("a second start was not refused")
        if a.scenario == "hub_pause":
            # Stop the hub (its slot process: the edge only forwards) right after it began the switch, before the
            # manager's rollback asks it to end it; keep it stopped past the manager's old two-minute give-up.
            log_path = root / "hub.log"
            wait(lambda: " begun " in log_path.read_text(errors="replace") and "link switch" in log_path.read_text(errors="replace"),
                 120, "the hub beginning the switch", every=0.02)
            slot = next(int(c) for c in run(["pgrep", "-P", str(hub.pid)]).stdout.split()
                        if Path(f"/proc/{c}/cmdline").read_bytes().split(b"\0")[1:3] == [b"hub", b"slot"])
            os.kill(slot, signal.SIGSTOP)
            paused.append(slot)
            stopped_at = time.monotonic()
            if " rolled back" in log_path.read_text(errors="replace"):
                raise AssertionError("the hub was stopped too late: the rollback had reached it already")
            say(f"STEP hub stopped after its begin (slot pid {slot}) for {a.pause} s")
            seen = set()
            while time.monotonic() - stopped_at < a.pause:
                time.sleep(5)
                there = box_sh("test -e ~/.persona/bin/persona-link && echo yes || echo no").stdout.strip()
                seen.add(there)
            if seen != {"yes"}:
                raise AssertionError("Persona Link was removed from the computer while the hub could not confirm the rollback")
            say(f"STEP while the hub was stopped ({a.pause} s): Persona Link stayed on the computer")
            os.kill(slot, signal.SIGCONT)
            paused.clear()
            say("STEP hub running again")
        phases = []
        first_phase_ms = None

        def finished():
            nonlocal first_phase_ms
            mm = machine()
            s = (mm or {}).get("link_switch") or {}
            ph = s.get("phase", "")
            if ph and first_phase_ms is None:
                first_phase_ms = (time.monotonic() - began) * 1000
            if ph and (not phases or phases[-1] != ph):
                phases.append(ph)
                say(f"  phase {ph} at {(time.monotonic() - began):.1f} s kind={mm.get('kind')} transport={mm.get('transport')}"
                    f" online={mm.get('online')} error={s.get('error', '')} unconfirmed={s.get('rollback_unconfirmed', False)}")
            return mm if ph in ("succeeded", "failed") else None

        m = wait(finished, 900, "the switch to end", every=0.2)
        ls = m["link_switch"]
        say(f"STEP ended: {ls['phase']} after {(time.monotonic() - began):.1f} s, first phase seen at {first_phase_ms:.0f} ms,"
            f" phases {' > '.join(phases)}")

        if a.scenario == "success":
            if ls["phase"] != "succeeded" or m.get("kind") != "link" or m.get("transport") != "link" or not m.get("online"):
                raise AssertionError(f"not switched: {json.dumps(m)[:600]}")
            active = box_sh("systemctl --user is-active persona-link personad-node", check=False).stdout.split()
            mode = box_sh("stat -c %a ~/.persona/link.json").stdout.strip()
            staged = box_sh("ls ~/.persona/bin/.persona-link-switch-* 2>/dev/null | wc -l").stdout.strip()
            say(f"STEP computer after: services {active}, link.json mode {mode}, staged files {staged}")
            if active != ["active", "active"] or mode != "600" or staged != "0":
                raise AssertionError("the computer is not set up as expected")
            # The node answers over the link: a real call through it.
            st, _ = api(f"/api/v1/machines/{MACHINE}/claude-settings")
            say(f"STEP call over the link: {st}")
        elif a.scenario in ("rollback", "hub_pause"):
            if ls["phase"] != "failed" or ls.get("rollback_unconfirmed"):
                raise AssertionError(f"expected a confirmed rollback: {ls}")
            m = wait(lambda: (lambda m: m if m and m.get("online") and m.get("transport") == "ssh" else None)(machine()),
                     60, "the computer back online over ssh")
            after_files = box_sh("ls -a ~/.persona ~/.persona/bin ~/.config/systemd/user 2>/dev/null; true").stdout
            units = box_sh("systemctl --user list-unit-files 'persona*' --no-legend 2>/dev/null; true").stdout.strip()
            say(f"STEP rolled back: kind {m.get('kind')}, transport {m.get('transport')}, error {ls.get('error')},"
                f" units [{units}]")
            if a.node_service:  # its own node service stays; only Persona Link's unit must be gone
                units = "\n".join(u for u in units.splitlines() if not u.startswith("personad-node"))
            if m.get("kind") != "ssh" or units or "link.json" in after_files or "persona-link" in after_files.replace(".persona-link-switch", "x"):
                raise AssertionError(f"left behind:\n{after_files}")
            if sorted(before_files.split()) != sorted(after_files.split()):
                say("  note: files before/after differ:", sorted(set(after_files.split()) ^ set(before_files.split())))
        else:
            if ls["phase"] != "failed" or ls.get("error") != "linger_off" or m.get("kind") != "ssh":
                raise AssertionError(f"expected a linger_off refusal: {ls}")
            after_files = box_sh("ls -a ~/.persona ~/.persona/bin ~/.config/systemd/user 2>/dev/null; true").stdout
            if sorted(before_files.split()) != sorted(after_files.split()):
                raise AssertionError("the refused check changed files on the computer")
            say("STEP refused at the check, nothing changed on the computer")

        if a.node_service:  # the computer's own node service, as it was before the switch, whatever its outcome
            now = box_sh("sha256sum ~/.config/systemd/user/personad-node.service 2>/dev/null; true").stdout.split()
            active = box_sh("systemctl --user is-active personad-node", check=False).stdout.strip()
            say(f"STEP own node service: unchanged {bool(now) and now[0] == unit_before}, {active}")
            if not now or now[0] != unit_before or active != "active":
                raise AssertionError("the computer's own node service was changed or stopped")
        # The pairing secret never shows: not in the hub's log, the stored record, or the answers.
        text = (root / "hub.log").read_text(errors="replace")
        leaks = [w for w in ("jp_", "persona://link?") if w in text]
        rec = ""
        dbs = run(["docker", "exec", db, "psql", "-U", "postgres", "-d", "persona", "-tAc",
                   "select datname from pg_database where not datistemplate"]).stdout.split()
        for name in dbs:  # the manager keeps its settings in a database of its own
            rec += run(["docker", "exec", db, "psql", "-U", "postgres", "-d", name, "-tAc",
                        "select string_agg(value::text, ' ') from settings where key like 'link_switch.%'"],
                       check=False).stdout.strip()
        if "jp_" in rec or "persona://" in rec:
            leaks.append("record")
        say(f"STEP secrets: log/record leaks {leaks or 'none'}; record present {bool(rec.strip())}")
        if leaks:
            raise AssertionError("the pairing secret leaked")
        if a.hold:
            say(f"HOLD {a.hold} s at {base} (token in {state / 'hub.json'})")
            stop["holding"] = True
            until = time.monotonic() + a.hold
            while time.monotonic() < until and not stop["now"]:
                time.sleep(0.2)
            stop["holding"] = False
        verdict = "PASS"
    except Exception as e:  # noqa: BLE001
        say("ERROR", type(e).__name__, str(e)[:2000])
    finally:
        for pid in paused:  # a stopped process takes no TERM: let it run first
            try:
                os.kill(pid, signal.SIGCONT)
            except ProcessLookupError:
                pass
        if hub is not None:
            try:
                os.killpg(hub.pid, signal.SIGTERM)
                hub.wait(timeout=20)
            except Exception:  # noqa: BLE001
                try:
                    os.killpg(hub.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
        if a.keep_logs and (root / "hub.log").exists():
            Path(a.keep_logs).mkdir(parents=True, exist_ok=True)
            text = (root / "hub.log").read_text(errors="replace").replace(token, "<token>").replace(password, "<password>")
            (Path(a.keep_logs) / f"hub-{a.scenario}.log").write_text(text)
        for kind, name in (("box", box), ("db", db)):
            if made[kind]:
                run(["docker", "rm", "-fv", name], check=False)
        if made["net"]:
            run(["docker", "network", "rm", net], check=False)
        shutil.rmtree(root, ignore_errors=True)
        say(f"RESULT {a.scenario}: {verdict}")
    return 0 if verdict == "PASS" else 1


def shq(s):
    return "'" + s.replace("'", "'\\''") + "'"


if __name__ == "__main__":
    sys.exit(main())
