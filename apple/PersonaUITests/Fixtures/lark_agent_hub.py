#!/usr/bin/env python3
"""Issue #5925's isolated, synthetic hub fixture, with the AI twin set apart too (#4232); the shared mock stays unchanged."""
import importlib.util
import json
import os
from pathlib import Path
import signal
from http.server import ThreadingHTTPServer
from urllib.parse import urlparse

ROOT = Path(__file__).resolve().parents[3]
spec = importlib.util.spec_from_file_location("persona_model_mock", ROOT / "scripts/mock/mock_hub.py")
mock = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mock)

PARENT = {"revision": 11, "override": {"provider": "launcher.claude", "model": "agents-default", "effort": "medium", "permission_mode": "bypassPermissions"}}
mock.MODEL_BINDINGS["agent.new"] = PARENT
# The uses of Agents the user can set apart, in the hub's order, each with its own record and writes.
CHILDREN = {"inbox.agent": "AI 分身执行", "lark.agent": "飞书 /agent"}
STATES = {pid: {"revision": 0, "override": {}} for pid in CHILDREN}
WRITES = {pid: [] for pid in CHILDREN}
BASE_CATALOG = mock.model_configuration
mock.LAUNCHER_MODELS["demo"] = {"agent": "claude", "models": [
    {"id": "example-model", "name": "示例模型", "efforts": ["low", "medium", "high"]}
]}


def catalog(include_uses=False):
    result = BASE_CATALOG(include_uses=include_uses)
    result["providers"] += [
        {"id": "launcher.demo", "name": "示例启动器", "kind": "launcher", "model": "example-model"},
        {"id": "api.messages", "name": "示例 Messages API", "kind": "api", "api": "messages", "model": "api-default"},
    ]
    parent = next(p for p in result["purposes"] if p["id"] == "agent.new")
    children = []
    for pid, name in CHILDREN.items():
        state = STATES[pid]
        binding = dict(state["override"])
        effective = dict(parent["effective"])
        if binding.get("provider"):
            provider = next(p for p in result["providers"] if p["id"] == binding["provider"])
            effective = {"provider": provider["id"], "model": provider.get("model", "")}
        effective.update(binding)
        if pid == "inbox.agent" and not binding.get("permission_mode"):
            effective["permission_mode"] = "default"
        purpose = {"id": pid, "category_id": "agent.new", "parent": "agent.new", "name": name,
                   "group": "Agents", "kind": "launcher", "override": binding, "effective": effective,
                   "revision": state["revision"], "fields": ["provider", "model", "effort", "permission_mode"],
                   "provider_ids": ["launcher.claude", "launcher.demo", "api.messages"]}
        if not binding:
            purpose["inherited_from"] = "Agents"
        children.append(purpose)
    result["purposes"] = [p for p in result["purposes"] if p["id"] not in CHILDREN] + children
    return result


def audit(pid):
    return {"writes": WRITES[pid], "parent": mock.MODEL_BINDINGS["agent.new"], "state": STATES[pid],
            "others": {other: STATES[other] for other in CHILDREN if other != pid}}


mock.model_configuration = catalog


class Handler(mock.H):
    def do_GET(self):
        path = urlparse(self.path).path
        if path == "/api/v1/model-config/providers/launcher.demo/models":
            return self.send_json(mock.LAUNCHER_MODELS["demo"])
        if path == "/mock/lark-agent" or path.startswith("/mock/model-child/"):
            pid = "lark.agent" if path == "/mock/lark-agent" else path.rsplit("/", 1)[1]
            if pid not in CHILDREN:
                return self.send_json({"error": "unknown use"}, 404)
            with mock.MODEL_CONFIG_LOCK:
                return self.send_json(audit(pid))
        return super().do_GET()

    def do_POST(self):
        if urlparse(self.path).path == "/mock/lark-agent/reset":
            self.body()
            with mock.MODEL_CONFIG_LOCK:
                for pid in CHILDREN:
                    STATES[pid].update(revision=0, override={})
                    WRITES[pid].clear()
            return self.send_json({"ok": True})
        return super().do_POST()

    def do_PATCH(self):
        path = urlparse(self.path).path
        pid = path.rsplit("/", 1)[1]
        if not path.startswith("/api/v1/model-config/purposes/") or pid not in CHILDREN:
            return super().do_PATCH()
        body = json.loads(self.body() or b"{}")
        with mock.MODEL_CONFIG_LOCK:
            state = STATES[pid]
            if body.get("revision") != state["revision"]:
                return self.send_json({"error": "配置已在别处修改"}, 409)
            if set(body) - {"revision", "provider", "model", "effort", "permission_mode"}:
                return self.send_json({"error": "unsupported field"}, 400)
            if pid == "inbox.agent" and body.get("permission_mode") == "plan":
                return self.send_json({"error": "permission mode unsupported"}, 400)
            binding = dict(state["override"])
            if "provider" in body and body["provider"] != binding.get("provider"):
                binding.pop("model", None)
                binding.pop("effort", None)
                binding.pop("permission_mode", None)
            for field in ("provider", "model", "effort", "permission_mode"):
                if field not in body:
                    continue
                if body[field] is None:
                    binding.pop(field, None)
                else:
                    binding[field] = body[field]
            WRITES[pid].append(body)
            state.update(revision=state["revision"] + 1, override=binding)
            result = catalog()
            purpose = next(p for p in result["purposes"] if p["id"] == pid)
            return self.send_json({"purpose": purpose, "providers": result["providers"]})


if __name__ == "__main__":
    port = int(os.environ.get("PORT", "18760"))
    with ThreadingHTTPServer(("127.0.0.1", port), Handler) as server:
        server.daemon_threads = True
        mock.PORT = server.server_port
        mock.ENDPOINTS.append(mock.endpoint("127.0.0.1", server.server_port, "http", "lan"))
        signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
        print("lark-agent fixture ready on port %d" % server.server_port, flush=True)
        try:
            server.serve_forever()
        except KeyboardInterrupt:
            pass
