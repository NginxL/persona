#!/usr/bin/env python3
"""Exercise the Android Inbox outbox against a real loopback hub and two managers.

Requires the server's Go toolchain, Docker, and the Android SDK/JDK used by Gradle.
Pass --report FILE to retain scrubbed HTTP, node and database evidence. Everything
else, including the synthetic pairing and both databases, belongs to this run.
"""
import argparse
from contextlib import ExitStack
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import subprocess
import tempfile
from urllib.parse import quote

from android_handoff_fixture import ROOT, build_tools, lines, postgres, start, start_hub, stop, until


ITEM = "mail:fixture-handoff"
SESSION = "00000000-0000-4000-8000-000000008422"
CLIENT = "10000000-0000-4000-8000-000000008422"
MACHINE = "fixture-named"


def exercise(stack, work, android):
    binary = build_tools(work)
    db_origin, sql = postgres(stack)
    sql("hub_fixture", "CREATE DATABASE named_fixture")
    machines = [dict(id=host, name=host, kind="ssh", ssh_host=host, botmux=False,
                     herdr_autostart=False, usage_windows=False) for host in ("fixture-default", MACHINE)]
    base, token, env, health, online, api, _ = start_hub(stack, work, binary, machines, db_origin)
    cli = str(binary / "personad")
    added = api("managers", {"name": "Fixture second manager"})
    manager = added["manager"]["id"]
    directory = work / "named"
    cmd = [cli, "manager", "join", base, added["code"], "--dir", str(directory),
           "--database-url", db_origin + "named_fixture?sslmode=disable"]
    # Joining prints an installation hint. Neither the code nor the database password belongs in a test report.
    joined = subprocess.run(cmd, cwd=work, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30)
    if joined.returncode:
        raise RuntimeError("The fixture manager could not join")
    run_named = lambda: start(stack, "named", [cli, "manager", "run", "--dir", str(directory)], work, env)
    named = run_named()
    until("both real managers", lambda: online(manager) and online("assistant"))
    until("the named manager's Inbox schema", lambda: sql("named_fixture", "SELECT to_regclass('inbox_items')"))
    # Seed only the second manager. The process reloads its Inbox from its own Postgres on restart.
    stop(named)
    now = datetime.now(timezone.utc).isoformat()
    item = dict(id=ITEM, source="mail", kind="mail", account="fixture", account_name="Fixture mail",
                title="Named manager handoff", preview="Synthetic routing check", count=1, tier="action",
                started_at=now, updated_at=now, last="1", triaged_at="1")
    sql("named_fixture", "INSERT INTO inbox_items(id,data,updated_at) VALUES ('" + ITEM + "', '" +
        json.dumps(item) + "'::jsonb, now())")
    run_named()
    until("the seeded manager", lambda: online(manager))
    moved = api("managers/" + manager + "/machines", {"machine_id": MACHINE})
    if moved.get("manager") != manager:
        raise RuntimeError("The second manager does not own the controlled target")
    detail = "inbox/" + quote(ITEM, safe="") + "?manager=" + manager
    until("the seeded Inbox item", lambda: api(detail).get("id") == ITEM)

    def has_target():
        response = api("sessions")
        rows = response.get("sessions", []) if isinstance(response, dict) else response
        hosts = {row.get("machine_id") for row in rows if row.get("session_id") == SESSION}
        return {MACHINE, "fixture-default"}.issubset(hosts)

    until("both controlled target sessions", has_target)
    start(stack, "observer", [binary / "ssh", "observe", base, work], work, env)
    observed = until("the HTTP observer", lambda: (work / "observer-url").read_text())
    fixture = work / "fixture.json"
    fixture.write_text(json.dumps(dict(base_url=observed, hub_id=health["hub_id"], token=token, manager=manager,
                                      item_id=ITEM, machine_id=MACHINE, session_id=SESSION, client_id=CLIENT)))
    fixture.chmod(0o600)
    subprocess.run([android / "gradlew", ":core:data:testDebugUnitTest", "--tests",
                    "persona.core.data.NamedManagerInboxHandoffLiveTest", "--rerun"], cwd=android,
                   env=dict(os.environ, PERSONA_ANDROID_HANDOFF_FIXTURE=str(fixture)), check=True, timeout=900)
    proof = json.loads((work / "android-proof.json").read_text())
    requests = lines(work / "requests.jsonl")
    path = "/api/v1/sessions/" + MACHINE + "/" + SESSION + "/messages"
    sends = [r for r in requests if r["path"] == path]
    records = [r for r in requests if r["path"] == "/api/v1/inbox/" + ITEM + "/handoff"]
    assert len(sends) == len(records) == 2, requests
    assert all(r["operation_id"] == CLIENT for r in sends), sends
    assert all(manager in (r["manager_header"], r["manager_query"]) for r in records), records
    assert all(r["method"] == "POST" and r["status"] == 200 for r in requests), requests
    for pair in (sends, records):
        assert pair[0]["request_sha256"] == pair[1]["request_sha256"], pair
        assert pair[0]["response_sha256"] == pair[1]["response_sha256"], pair
    assert proof["same_record_result"] and len(proof["attempts"]) == 2, proof
    assert all(a["message_result"] == "accepted" for a in proof["attempts"]), proof
    deliveries = {host: lines(binary / (host + ".jsonl")) for host in ("fixture-default", MACHINE)}
    assert deliveries["fixture-default"] == [] and len(deliveries[MACHINE]) == 1, deliveries
    assert deliveries[MACHINE][0]["client_msg_id"] == CLIENT, deliveries
    # A fresh hub can keep its built-in manager in the hub database or provision a separate manager database.
    dbs = sql("hub_fixture", "SELECT datname FROM pg_database WHERE NOT datistemplate AND datname <> 'postgres'").splitlines()
    owners = [db for db in dbs if sql(db, "SELECT to_regclass('inbox_items')") == "inbox_items"]
    default = [db for db in owners if db != "named_fixture"]
    assert len(default) == 1 and "named_fixture" in owners, owners
    select = "SELECT data FROM inbox_items WHERE id = '" + ITEM + "'"
    assert not sql(default[0], select), "The fixture item leaked into the default manager"
    stored = json.loads(sql("named_fixture", select))
    handoffs = stored.get("handoffs", [])
    chain = [c for c in stored.get("chain", []) if c["kind"] == "agent"]
    assert len(handoffs) == len(chain) == 1, (handoffs, chain)
    entry = handoffs[0]
    assert (entry["op"], entry["machine_id"], entry["session_id"]) == (CLIENT, MACHINE, SESSION), entry
    assert (chain[0]["link"]["machine_id"], chain[0]["link"]["session_id"]) == (MACHINE, SESSION), chain
    return dict(client_id=CLIENT, named_manager=manager, requests=requests, node_deliveries=deliveries,
                persistence={"default": {"fixture_items": 0, "handoffs": 0, "agent_chain": 0},
                             "named": {"fixture_items": 1, "handoffs": 1, "agent_chain": 1, "original_client_id": entry["op"]}},
                replayed_record_matches=proof["same_record_result"])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report", type=Path, help="write the scrubbed verification counts and request receipts")
    parser.add_argument("--android-dir", type=Path, default=ROOT / "android", help="Android checkout being tested")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="persona-inbox-") as directory, ExitStack() as stack:
        result = exercise(stack, Path(directory), args.android_dir.resolve())
    text = json.dumps(result, indent=2)
    print(text)
    if args.report:
        args.report.write_text(text + "\n")


if __name__ == "__main__":
    main()
