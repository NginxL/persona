#!/usr/bin/env python3
"""DSH Markdown local-file fixture (#8953).

Run with --port 0 (or a free port), wait for MARKDOWN_LOCAL_HUB_READY, and pass its
URL as TEST_RUNNER_PERSONA_MARKDOWN_LOCAL_HUB. Every image and document is served
through the stock machine-file API; the fixture adds no runtime file-read RPC.
"""
import argparse
import importlib.util
import os
from pathlib import Path
import signal
import sys
import threading
from urllib.parse import parse_qs, urlparse


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=int(os.environ.get("PORT", "0")))
    args = parser.parse_args()
    if not 0 <= args.port <= 65535:
        parser.error("--port must be between 0 and 65535")

    for key in list(os.environ):
        if key.startswith("MOCK_"):
            del os.environ[key]
    os.environ["PORT"] = str(args.port)
    os.environ["MOCK_SOURCES"] = "1"
    os.environ["MOCK_FILES"] = "1"
    os.environ["MOCK_AGENTS"] = "1"
    sys.dont_write_bytecode = True

    root = Path(__file__).resolve().parents[3]
    spec = importlib.util.spec_from_file_location(
        "markdown_local_files_stock_mock", root / "scripts/mock/mock_hub.py")
    hub = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = hub
    spec.loader.exec_module(hub)

    reports = "/Users/me/code/reports"
    absolute_png = "/tmp/persona-markdown-shot.png"
    unreadable_png = "/tmp/persona-markdown-unreadable.png"
    unreadable_md = "/tmp/persona-markdown-unreadable.md"
    hub.FILES[absolute_png] = hub._bar_png(360, 180)
    hub.FILES[reports + "/shots/relative.png"] = hub._bar_png(480, 240)
    hub.FILES[reports + "/reports/absolute.md"] = "# Absolute local report\n\nAbsolute report body.\n"
    hub.FILES[reports + "/reports/relative.md"] = "# Relative local report\n\nRelative report body.\n"
    # These paths exist for stat, but both established read routes refuse their contents below.
    hub.FILES[unreadable_png] = b"not a readable png"
    hub.FILES[unreadable_md] = b"unreadable markdown fixture"

    missing = "The requested local file does not exist on this computer."
    denied = "Permission denied. Allow Persona to read this folder on the computer."
    unreadable = "The local file exists but its contents could not be read."
    failures = {}
    for path in (reports + "/shots/missing.png", reports + "/reports/missing.md"):
        failures[path] = (404, missing)
    for path in (reports + "/private/denied.png", reports + "/private/denied.md"):
        failures[path] = (403, denied)
    for path in (unreadable_png, unreadable_md):
        failures[path] = (422, unreadable)

    markdown = "\n\n".join([
        "Local Markdown fixture",
        "![Absolute screenshot](" + absolute_png + ")",
        "![Relative screenshot](shots/relative.png)",
        "[Absolute report](" + reports + "/reports/absolute.md)",
        "[Relative report](reports/relative.md)",
        "![Missing screenshot](shots/missing.png)",
        "![Denied screenshot](private/denied.png)",
        "![Unreadable screenshot](" + unreadable_png + ")",
        "Before ![Inline denied screenshot](private/denied.png) after",
        "[Missing report](reports/missing.md)",
        "[Denied report](private/denied.md)",
        "[Unreadable report](" + unreadable_md + ")",
        "![Lone denied screenshot](private/denied.png)",
        "[Web report](https://example.com/report)",
    ])
    hub.ITEMS[hub.DSH_SESSION] = [
        hub.it("user", "text", 5, text="Show local images and open local report links"),
        hub.it("assistant", "text", 2, text=markdown),
    ]
    session = next(s for s in hub.SESSIONS if s["session_id"] == hub.DSH_SESSION)
    session.update(status="idle", state="idle", pending_interactions=0,
                   last_text="Local Markdown images and links are ready.")
    for interactions in (hub.INTERACTIONS, hub.MOCK_INTERACTIONS):
        interactions[:] = [x for x in interactions if x.get("session_id") != hub.DSH_SESSION]

    original_get = hub.H.do_GET
    reads = {}
    reads_lock = threading.Lock()

    def get(self):
        url = urlparse(self.path)
        query = parse_qs(url.query)
        if url.path == "/mock/markdown-local-reads":
            asked = query.get("path", [""])[0]
            cwd = query.get("cwd", [""])[0]
            full = hub.file_path(asked, cwd)
            with reads_lock:
                count = reads.get(full, 0)
            return self.send_json({"path": full, "count": count})

        machine_file = url.path in (
            "/api/v1/machines/studio/file", "/api/v1/machines/studio/file/download")
        if machine_file:
            asked = query.get("path", [""])[0]
            cwd = query.get("cwd", [""])[0]
            full = hub.file_path(asked, cwd)
            with reads_lock:
                reads[full] = reads.get(full, 0) + 1
            failure = failures.get(full)
            if failure:
                status, message = failure
                return self.send_json({"error": message, "code": "local_file_fixture"}, status)

        if url.path == "/api/v1/machines/studio/stat":
            cwd = query.get("cwd", [""])[0]
            paths = query.get("path", []) + [p for p in query.get("paths", [""])[0].split("\n") if p]
            stats = []
            for asked in paths:
                full = hub.file_path(asked, cwd)
                kind = "denied" if full in failures and failures[full][0] == 403 else hub.file_kind(full)
                stats.append({"path": asked, "kind": kind if full else "missing"})
            return self.send_json({"stats": stats})
        return original_get(self)

    hub.H.do_GET = get
    server = hub.ThreadingHTTPServer(("127.0.0.1", args.port), hub.H)
    server.daemon_threads = True
    hub.PORT = server.server_port
    hub.ENDPOINTS.append(hub.endpoint("127.0.0.1", hub.PORT, "http", "lan"))
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    print(f"MARKDOWN_LOCAL_HUB_READY url=http://127.0.0.1:{hub.PORT} pid={os.getpid()}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
