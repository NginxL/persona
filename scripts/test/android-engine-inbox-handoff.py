#!/usr/bin/env python3
"""Verify an Android Inbox handoff against a real default manager, node, Codex and local model.

Requires the Android SDK/JDK, Go, Docker, Codex CLI, Node 22+, and an already installed
Ollama model on 127.0.0.1:11434 with Responses support. Uses a fresh Codex home and
synthetic data. --report writes hashes and counts without prompts, tokens or local paths.
"""
import argparse
from contextlib import ExitStack
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import tempfile
from urllib.parse import quote
import uuid

from android_handoff_fixture import ROOT, build_tools, lines, postgres, start, start_hub, stop, until


MACHINE = "fixture-engine"
ITEM = "mail:engine-handoff"


def digest(value):
    return hashlib.sha256(value.encode()).hexdigest()


def engine(stack, work, model, codex):
    profile = work / "home" / ".codex"
    profile.mkdir(parents=True, mode=0o700)
    project = work / "project"
    project.mkdir(mode=0o700)
    (profile / "config.toml").write_text(
        'model_provider = "fixture"\nmodel = ' + json.dumps(model) + '\n[model_providers.fixture]\n'
        'name = "Local fixture"\nwire_api = "responses"\nbase_url = "http://127.0.0.1:11434/v1"\n'
        'requires_openai_auth = false\nsupports_websockets = false\n')
    node = shutil.which("node")
    codex = shutil.which(codex)
    if not codex or not node:
        raise RuntimeError("Both Codex CLI and Node are required")
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    endpoint = f"ws://127.0.0.1:{port}"
    env = dict(HOME=str(profile.parent), CODEX_HOME=str(profile), PATH=str(Path(node).parent) + os.pathsep + os.defpath,
               NO_PROXY="localhost,127.0.0.1", OTEL_SDK_DISABLED="true")
    codex_process = start(stack, "codex", [codex, "app-server", "--listen", endpoint], work, env)

    def listening():
        if codex_process.poll() is not None:
            raise RuntimeError("The isolated Codex app-server exited")
        with socket.create_connection(("127.0.0.1", port), timeout=1):
            return True

    until("Codex app-server", listening)
    seed = start(stack, "seed", [node, ROOT / "scripts/test/android-inbox-engine.mjs", "seed", endpoint, project, model], work, env)

    def seeded():
        if seed.poll() is not None:
            raise RuntimeError("The local model did not complete the seed turn")
        return json.loads((project / "engine-ready.json").read_text())

    ready = until("the seed turn", seeded, seconds=180)
    # Validate the UUID before using the thread ID in either a path or an HTTP URL.
    thread = str(uuid.UUID(ready["thread"]))

    def turns_after_seed():
        result = subprocess.check_output([node, ROOT / "scripts/test/android-inbox-engine.mjs", "read", endpoint, thread],
                                         cwd=project, env=env, text=True, timeout=15)
        rows = json.loads(result)
        if not rows or rows[0]["id"] != ready["seed"] or rows[0]["status"] != "completed":
            raise RuntimeError("The engine transcript has no completed fixture seed")
        return rows[1:]

    return profile, endpoint, thread, turns_after_seed


def exercise(stack, work, android, model, codex="codex"):
    binary = build_tools(work, "handoff-observer")
    db_origin, sql = postgres(stack)
    profile, endpoint, thread, engine_turns = engine(stack, work, model, codex)
    machine = dict(id=MACHINE, name="Fixture computer", kind="local", botmux=False, herdr_autostart=False,
                   codex_discovery=False, codex_homes=[str(profile)],
                   codex_servers=[dict(id="fixture", home=str(profile), endpoint=endpoint)], usage_windows=False)
    base, token, env, health, _, api, restart = start_hub(stack, work, binary, [machine], db_origin)
    dbs = sql("hub_fixture", "SELECT datname FROM pg_database WHERE NOT datistemplate AND datname <> 'postgres'").splitlines()
    db, = [db for db in dbs if sql(db, "SELECT to_regclass('inbox_items')") == "inbox_items"]
    now = datetime.now(timezone.utc).isoformat()
    item = dict(id=ITEM, source="mail", kind="mail", account="fixture", account_name="Fixture mail", title="Synthetic handoff",
                preview="Engine integration", count=1, tier="action", started_at=now, updated_at=now, last="1", triaged_at="1")
    sql(db, "INSERT INTO inbox_items(id,data,updated_at) VALUES ('" + ITEM + "', '" + json.dumps(item) + "'::jsonb, now())")
    restart()
    until("the item on the default manager", lambda: api("inbox/" + quote(ITEM, safe="")).get("id") == ITEM)

    def has_thread():
        sessions = api("sessions")
        if isinstance(sessions, dict):
            sessions = sessions["sessions"]
        return any(s["machine_id"] == MACHINE and s["session_id"] == thread for s in sessions)

    until("the isolated node's real thread", has_thread, seconds=90)
    start(stack, "observer", [binary / "handoff-observer", "observe-refusal", base, work], work, env)
    observed = until("the HTTP observer", lambda: (work / "observer-url").read_text())
    first, second = "LAKE" + secrets.token_hex(3).upper(), "PINE" + secrets.token_hex(3).upper()
    instruction = f"Reply with just these two tokens joined by a hyphen, in this order: {first} and {second}."
    expected = first + "-" + second
    client = str(uuid.uuid4())
    fixture = work / "fixture.json"
    fixture.write_text(json.dumps(dict(base_url=observed, hub_id=health["hub_id"], token=token, item_id=ITEM,
                                      machine_id=MACHINE, session_id=thread, client_id=client, instruction=instruction, expected=expected)))
    fixture.chmod(0o600)
    with (work / "android.log").open("w") as log:
        gradle = subprocess.Popen([android / "gradlew", "--max-workers=2", ":core:data:testDebugUnitTest", "--tests",
                                   "persona.core.data.DefaultManagerEngineInboxHandoffLiveTest", "--rerun"], cwd=android,
                                  env=dict(os.environ, PERSONA_ANDROID_ENGINE_FIXTURE=str(fixture)),
                                  stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        stack.callback(stop, gradle)

        def rejected():
            if gradle.poll() is not None:
                raise RuntimeError("Android did not reach the refusal checkpoint. Inspect the Gradle test results.")
            return (work / "refused").exists()

        until("the Android refusal", rejected, seconds=900)
        def stored_item():
            return json.loads(sql(db, "SELECT data FROM inbox_items WHERE id='" + ITEM + "'"))
        negatives = lines(work / "requests.jsonl")
        assert [r["status"] for r in negatives] == [409] and negatives[0]["operation_id"] == client, negatives
        before = stored_item()
        assert not before.get("handoffs") and not any(c["kind"] == "agent" for c in before.get("chain", []))
        assert not engine_turns(), "The engine received work before Android retried"
        (work / "allow-retry").touch()
        if gradle.wait(timeout=240) != 0:
            raise RuntimeError("Android did not observe the delivered reply. Inspect the Gradle test results.")

    after = stored_item()
    handoff, = after["handoffs"]
    link, = [c["link"] for c in after["chain"] if c["kind"] == "agent"]
    assert (handoff["op"], handoff["machine_id"], handoff["session_id"]) == (client, MACHINE, thread)
    assert (link["machine_id"], link["session_id"]) == (MACHINE, thread)
    wire = lines(work / "requests.jsonl")
    assert [(r["path"].split("/")[-1], r["status"]) for r in wire] == [("messages", 409), ("messages", 200), ("handoff", 200)], wire
    assert wire[1]["operation_id"] and wire[1]["operation_id"] != client
    def completed_turn():
        turns = engine_turns()
        if len(turns) > 1:
            raise RuntimeError("The engine received the handoff more than once")
        if turns and turns[0]["status"] in {"failed", "interrupted"}:
            raise RuntimeError("The engine did not complete the handoff")
        return turns[0] if turns and turns[0]["status"] == "completed" else None

    final = until("the completed real engine turn", completed_turn)
    assert final["inputs"] == [instruction]
    assert final["answers"] and final["answers"][-1].strip() == expected
    return dict(before_retry={"http_status":409, "turns":0, "handoffs":0, "agent_chain":0}, requests=wire,
                completed={"turn_sha256":digest(final["id"]), "instruction_sha256":digest(instruction), "reply_sha256":digest(expected),
                           "android_history_seq":int((work / "android-result-seq").read_text()), "engine_turns":1,
                           "handoffs":1, "agent_chain":1, "original_client_id":client})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model", required=True, help="installed local Ollama model")
    parser.add_argument("--android-dir", type=Path, default=ROOT / "android")
    parser.add_argument("--report", type=Path)
    parser.add_argument("--codex", default="codex", help="real Codex CLI with a Persona-supported history format")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="persona-engine-") as directory, ExitStack() as stack:
        result = exercise(stack, Path(directory), args.android_dir.resolve(), args.model, args.codex)
    text = json.dumps(result, indent=2)
    print(text)
    if args.report:
        args.report.write_text(text + "\n")


if __name__ == "__main__":
    main()
