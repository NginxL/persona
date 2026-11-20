#!/usr/bin/env python3
"""Local source fixture for MessagingAvatarUITests. All names, conversations and pictures are synthetic."""
import argparse
import base64
import os
import signal
import threading
from http.server import ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlsplit

os.environ.update(MOCK_LARK="1", MOCK_MESSAGING="1")
import mock_hub as base


ROOM = "media/studio.png?v=2"
PERSON = "https://portraits.example.invalid/avery.png"
PREFIX = "/api/v1/messaging/lark/"


def picture(row, ref):
    path = base.avatar_path("lark", ref) + "?messaging=1"
    if ref.startswith("media/"):
        path += "&account=1"
    return dict(row, avatar=ref, avatar_url=path)


def draw(ref, size):
    # The two geometrical motifs remain recognisable at the application's list and chat sizes.
    def pixel(x, y):
        u, v = 2 * x / size - 1, 2 * y / size - 1
        if ref == ROOM:
            rgb = (251, 209, 120) if (u + .32) ** 2 + (v + .34) ** 2 < .045 else (51, 132, 129)
            if v > .13 + .16 * u: rgb = (121, 202, 190)
            if v > .53 - .13 * u: rgb = (32, 92, 111)
        else:
            rgb = (167, 86, 109)
            if abs(u) + abs(v) < .60: rgb = (253, 208, 138)
            if abs(u) + abs(v) < .31: rgb = (151, 64, 96)
        return (*rgb, 255)
    return base._png_rgba(size, size, pixel)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port-file", required=True, type=Path)
    parser.add_argument("--timeout", default=3600, type=int)
    args = parser.parse_args()
    hits = []
    lock = threading.Lock()

    class Handler(base.H):
        def do_GET(self):
            url = urlsplit(self.path)
            q = parse_qs(url.query)
            if url.path == "/mock/messaging-avatars":
                with lock:
                    return self.send_json({"reads": list(hits)})
            if url.path in (PREFIX + "conversations", PREFIX + "pinned"):
                items = [picture({"id": "studio", "name": "River Studio", "kind": "group", "unread": 0,
                                  "preview": "The river study is ready.", "preview_sender": "Avery"}, ROOM),
                         picture({"id": "avery", "name": "Avery", "kind": "direct", "unread": 0,
                                  "preview": "See you at the studio."}, PERSON),
                         {"id": "robin", "name": "Robin", "kind": "direct", "unread": 0,
                          "preview": "I saved the sketch."}]
                return self.send_json({"account": "1", "conversations": [] if url.path.endswith("pinned") else items})
            if url.path == PREFIX + "conversations/studio/messages":
                items = []
                for i, (name, line) in enumerate([("Avery", "The river study is ready."),
                                                  ("Robin", "I saved the sketch.")], 1):
                    person = {"name": name, "id": name.lower(), "kind": "user"}
                    items.append({"id": "portrait-" + str(i), "position": str(i), "mine": False,
                                  "created_at": base.ago(minutes=4-i), "sender": picture(person, PERSON) if i == 1 else person,
                                  "parts": [{"kind": "text", "text": line}], "text": line})
                return self.send_json({"account": "1", "has_older": False, "messages": items})
            if url.path.startswith("/api/v1/avatars/lark/") or url.path == PREFIX + "media/studio.png":
                legacy = url.path.startswith(PREFIX)
                value = ROOM
                if not legacy:
                    seg = url.path.split("/")[-1]
                    try:
                        value = base64.urlsafe_b64decode(seg + "=" * (-len(seg) % 4)).decode()
                    except (ValueError, UnicodeError):
                        value = ""
                if value not in (ROOM, PERSON):
                    return self.send_json({"error": "unknown fixture picture"}, 404)
                if not legacy and value == ROOM and q.get("account") != ["1"]:
                    return self.send_json({"error": "unknown fixture account"}, 404)
                size = next((n for n in (64, 128, 256, 512) if n >= int(q.get("size", ["128"])[0])), 512)
                with lock:
                    hits.append({"picture": "room" if value == ROOM else "person", "route": "media" if legacy else "avatars",
                                 "size": size, "account": q.get("account", [""])[0]})
                data = draw(value, size)
                self.send_response(200)
                self.send_header("Content-Type", "image/png")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                return self.wfile.write(data)
            return super().do_GET()

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.daemon_threads = True
    base.PORT = server.server_port
    base.ENDPOINTS.append(base.endpoint("127.0.0.1", server.server_port, "http", "lan"))
    args.port_file.write_text(str(server.server_port))
    def stop(*_):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGALRM, stop)
    signal.alarm(args.timeout)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
