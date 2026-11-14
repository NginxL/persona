#!/usr/bin/env python3
"""Isolated Apple composer fixtures (#2200), reusing only the repository's made-up hub data.

    PORT=<unused port> python3 scripts/mock/composer_hub.py

Pair a fresh simulator with this hub. ComposerKeyboardTests needs these short/long GPT fixtures;
never run it against a production hub. This entry keeps mock_hub.py free of composer-specific changes.
A message sent here lands in its chat's transcript a moment later (mock_hub.py MOCK_ECHO_SENT), as the agent taking it.
"""
import os
from pathlib import Path
import runpy
import signal
import sys
from http.server import ThreadingHTTPServer


AGENT = "a1f3c2d4-0000-4000-8000-000000000001"
ASSISTANT = "b2e4d6f8-0000-4000-8000-000000000001"
SHORT_MODEL = "gpt-6.1-sol"
LONG_MODEL = "gpt-6.1-sol-codex-mini-preview-2026-11-05"


def configure(mock):
    """Change only the two composer chats and two starter choices; keep wire ids and other fixture state intact."""
    for session in mock["SESSIONS"] + mock["ASSISTANT"]:
        if session["session_id"] == AGENT:
            session.update(model=LONG_MODEL, agent="codex")
        elif session["session_id"] == ASSISTANT:
            session.update(model=SHORT_MODEL, agent="codex")
    mock["LAUNCHERS"][0]["model"] = SHORT_MODEL
    mock["LAUNCHERS"][1]["model"] = "gpt-6.1"


def main():
    # Without its transcript copy the app keeps a message sent to the working Agent chat queued for up to 16 minutes
    # (Outbox.sentWait), across launches: in the next test its queue strip took what the keyboard, the quotes and a long
    # draft leave of the chat on an iPhone, and a drag there no longer reached the chat (#7683).
    os.environ.setdefault("MOCK_ECHO_SENT", "1")
    mock = runpy.run_path(str(Path(__file__).with_name("mock_hub.py")))
    configure(mock)
    loopback = "127.0.0.1"
    server = ThreadingHTTPServer((loopback, int(os.environ.get("PORT", "18760"))), mock["H"])
    server.daemon_threads = True
    # runpy returns a copy of the module dictionary; functions retain the original globals.
    mock["endpoint"].__globals__["PORT"] = server.server_port
    mock["ENDPOINTS"].append(mock["endpoint"](loopback, server.server_port, "http", "lan"))

    def stop(*_):
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, stop)
    print("RESULT: isolated composer mock listening on %d" % server.server_port, flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError) as error:
        print("RESULT: isolated composer mock failed: %s" % error, file=sys.stderr)
        sys.exit(1)
