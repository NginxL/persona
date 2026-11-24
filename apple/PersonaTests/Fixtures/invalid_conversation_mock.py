#!/usr/bin/env python3
"""QuietReceiptsUITests' account-sealed conversation variants, without changing the shared mock."""

import copy
import os
import runpy
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
MOCK_DIR = ROOT / "scripts" / "mock"
sys.path.insert(0, str(MOCK_DIR))
os.environ.setdefault("MOCK_TASKS", "1")
os.environ.setdefault("MOCK_TASKS_CUT", "1")
os.environ.setdefault("MOCK_TASKS_INVALID", "1")
os.environ.setdefault("MOCK_TASKS_NO_ASKS", "1")
os.environ.setdefault("MOCK_MANAGERS", "1")
hub = runpy.run_path(str(MOCK_DIR / "mock_hub.py"), run_name="persona_invalid_conversation_mock")

SEALED = ("This conversation was compacted natively by <account>: the compaction is sealed (encrypted) "
          "to that account and can't go to it now")
ORDINARY = "API Error: Connection lost mid-response. The response above may be incomplete."
RELEASE = hub["RELEASE"]
original = next(session for session in hub["SESSIONS"] if session["session_id"] == RELEASE)
original_todo = next(todo for todo in hub["TODOS"]
                     if any(agent["session_id"] == RELEASE for agent in todo.get("agents", [])))
if original.get("turn_error_kind") != "unrecoverable" or original.get("turn_error_code") != "compaction_sealed":
    raise RuntimeError("MOCK_TASKS_INVALID=1 must seed the sealed release session")

# The original is kind-only; copies isolate code-only, legacy raw-only, and an ordinary retryable interruption.
original.update(last_text="Context unavailable", turn_error="Context unavailable")
original.pop("turn_error_code", None)
# Each fixture has one current Agent, so the task row's lead is the session under test rather than a peer's state.
original_todo["agents"] = [agent for agent in original_todo["agents"] if agent["session_id"] == RELEASE]
if len(original_todo["agents"]) != 1:
    raise RuntimeError("the release fixture must have exactly one current Agent")
original_todo["title"] = "整理发布说明 kind"
for todo in hub["TODOS_INIT"]:
    if todo["id"] == original_todo["id"]:
        todo["title"] = original_todo["title"]
        todo["agents"] = copy.deepcopy(original_todo["agents"])
cut = hub.get("CUT_SESSION")
if cut:
    cut.clear()
    cut.update(original)

for name, text, kind, code in [
    ("code", "Context unavailable", None, "compaction_sealed"),
    ("sealed", SEALED, None, None),
    ("ordinary", ORDINARY, None, None),
]:
    session = copy.deepcopy(original)
    todo = copy.deepcopy(original_todo)
    old_todo_id = todo["id"]
    session["session_id"] = RELEASE + "-" + name
    session["title"] = "Previous attempt " + name
    session.update(last_text=text, turn_error=text)
    session.pop("turn_error_kind", None)
    session.pop("turn_error_code", None)
    if kind is not None:
        session["turn_error_kind"] = kind
    if code is not None:
        session["turn_error_code"] = code
    todo["id"] = old_todo_id + "-" + name
    todo["title"] = "整理发布说明 " + name
    for agent in todo["agents"]:
        if agent["session_id"] == RELEASE:
            agent["session_id"] = session["session_id"]
    if session.get("todo"):
        session["todo"].update(id=todo["id"], title=todo["title"])
    hub["SESSIONS"].append(session)
    hub["TODOS"].append(todo)
    hub["TODOS_INIT"].append(copy.deepcopy(todo))

server = hub["ThreadingHTTPServer"](("127.0.0.1", int(os.environ.get("PORT", "0"))), hub["H"])
server.daemon_threads = True
port = server.server_port
hub["PORT"] = port
hub["ENDPOINTS"].append(hub["endpoint"]("127.0.0.1", port, "http", "lan"))
print("invalid-conversation mock hub on 127.0.0.1:%d" % port, flush=True)
try:
    server.serve_forever()
except KeyboardInterrupt:
    pass
finally:
    server.server_close()
