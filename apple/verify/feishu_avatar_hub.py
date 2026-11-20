#!/usr/bin/env python3
"""Loopback-only Feishu avatar fixture for PersonaUITests/FeishuAvatarUITests.swift."""
import argparse
import base64
import hashlib
import json
import os
import sys
import threading
from http.server import ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, unquote, urlsplit

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "scripts" / "mock"))
os.environ.update(MOCK_LARK="1", MOCK_MESSAGING="0")
import mock_hub as base


def picture(key, slot):
    return base.avatar_path("lark", key) + "?lark_account=" + str(slot)


def draw(key, size):
    def pixel(x, y):
        u = (x + 0.5) / size
        v = (y + 0.5) / size
        if key == "photo/one":
            rgb = (57, 113, 131) if v < 0.55 + 0.22 * u else (121, 175, 155)
            if (u - 0.34)**2 + (v - 0.33)**2 < 0.023:
                rgb = (254, 214, 135)
        else:
            rgb = (175, 89, 107) if abs(u - 0.5) + abs(v - 0.5) > 0.36 else (255, 212, 152)
        return (*rgb, 255)
    return base._png_rgba(size, size, pixel)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port-file", type=Path, required=True)
    parser.add_argument("--production-wire", type=Path, help="optional exported JSON and JPEG from the registered Go gateway fixture")
    args = parser.parse_args()
    reads = []
    missing_paths = []
    unavailable = []
    lock = threading.Lock()

    def exported(name, fallback):
        if args.production_wire:
            return json.loads((args.production_wire / name).read_text())
        return fallback

    class Handler(base.H):
        def do_GET(self):
            url = urlsplit(self.path)
            path = url.path
            q = parse_qs(url.query, keep_blank_values=True)
            if path == "/mock/feishu-avatars":
                with lock:
                    return self.send_json({"reads": list(reads), "missing_paths": list(missing_paths), "unavailable": list(unavailable)})
            if path == "/api/v1/lark/accounts":
                page = exported("accounts.json", {"accounts": [{"id": n, "avatar": "photo/one", "avatar_url": picture("photo/one", n)} for n in [2, 3]], "default": 2})
                for row in page["accounts"]:
                    row.update(name="Field Studio" if row["id"] == 2 else "Field Notes", signed_in=True, unread=0, primary=row["id"] == 2)
                return self.send_json(page)
            if path in ("/api/v1/lark/conversations", "/api/v1/lark/pinned"):
                slot = int(q.get("ol_account", ["2"])[0])
                page = exported("conversations-three.json" if slot == 3 else "conversations-default.json",
                                {"account_id": "user-68", "conversations": [{"avatar": "/agent/v1/media/photo%2Fone", "avatar_url": picture("photo/one", slot)}]})
                room = dict(page["conversations"][0], id="studio", name="River Studio", updated_at=base.ago(minutes=2),
                            is_group=True, unread=0, at_me=False, is_mine=False, muted=False, preview="The river study is ready.")
                quiet = dict(id="quiet-notes" if slot == 3 else "quiet", name="Marsh Notes", updated_at=base.ago(minutes=5), is_group=False, unread=0,
                             at_me=False, is_mine=False, muted=False, preview="Morning sketches")
                page["conversations"] = [] if path.endswith("pinned") else [room, quiet]
                return self.send_json(page)
            if path == "/api/v1/lark/conversations/studio/messages":
                slot = int(q.get("ol_account", ["2"])[0])
                rows = []
                for i, (sender, name, line) in enumerate([("reed", "Reed", "The river study is ready."), ("robin", "Robin", "I saved the sketch.")], 1):
                    row = dict(id="study-" + str(i), position=str(i), sender_id=sender, sender_name=name, sender_type="user",
                               is_mine=False, type="TEXT", text=line, created_at=base.ago(minutes=4 - i),
                               recalled=False, reply_count=0, parts=[dict(kind="text", text=line)])
                    if sender == "reed":
                        row.update(sender_avatar="/api/media/reed", sender_avatar_url=picture("reed", slot))
                    else:
                        row["sender_avatar_url"] = picture("removed", slot)
                    rows.append(row)
                return self.send_json({"account_id": "user-68", "messages": rows, "has_older": False})
            if path == "/api/v1/lark/users/reed":
                profile = exported("profile-two.json", {"avatarUrl": "photo/one", "avatar_url": picture("photo/one", 2)})
                profile.update(displayName="Reed", description="Watercolour studies")
                return self.send_json(profile)
            avatar = "/api/v1/avatars/lark/"
            if path.startswith(("/api/media/", "/agent/v1/media/")):
                # These are upstream spellings in old data. They are not public endpoints of a paired hub.
                with lock:
                    missing_paths.append(path)
                return self.send_json({"error": "unknown hub route"}, 404)
            if path.startswith(avatar) or path.startswith("/api/v1/lark/media/"):
                shared = path.startswith(avatar)
                key = base64.urlsafe_b64decode(path[len(avatar):] + "===").decode() if shared else unquote(path.split("/media/", 1)[1])
                if key not in ("photo/one", "reed"):
                    with lock:
                        unavailable.append(key)
                    return self.send_json({"error": "missing fixture picture"}, 404)
                slot = int(q.get("lark_account" if shared else "ol_account", ["2"])[0])
                size = int(q.get("size", ["128"])[0])
                if shared and (slot not in (2, 3) or size not in (64, 128, 256, 512) or len(q["size"]) != 1):
                    return self.send_json({"error": "unexpected fixture slot or size"}, 400)
                if args.production_wire and shared and key == "photo/one":
                    data = (args.production_wire / f"slot-{slot}-{size}.jpg").read_bytes()
                    mime = "image/jpeg"
                else:
                    data, mime = draw(key, size), "image/png"
                with lock:
                    reads.append(dict(key=key, slot=slot, size=size, shared=shared, sha256=hashlib.sha256(data).hexdigest()))
                self.send_response(200)
                self.send_header("Content-Type", mime)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                return self.wfile.write(data)
            return super().do_GET()

    with ThreadingHTTPServer(("127.0.0.1", 0), Handler) as server:
        server.daemon_threads = True
        base.PORT = server.server_port
        base.ENDPOINTS.append(base.endpoint("127.0.0.1", server.server_port, "http", "lan"))
        args.port_file.write_text(str(server.server_port))
        server.serve_forever()


if __name__ == "__main__":
    main()
