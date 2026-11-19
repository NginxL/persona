#!/usr/bin/env python3
"""Agent-preset UI fixture (#2225); wraps, never edits, scripts/mock/mock_hub.py.

PORT=18925 python3 apple/PersonaUITests/Fixtures/model_profiles_hub.py
Use PERSONA_PRESETS_HUB=http://127.0.0.1:18925 for AgentPresetUITests.
GET /mock/presets?mode=normal selects normal, older, locked-model, unsupported-effort,
blocked-launcher, default-model, start-race, or plan and clears recorded session starts.
In start-race the unlocked slowcc model list waits for GET /mock/presets?release=1.
"""
import copy
import importlib.util
import os
from pathlib import Path
import threading
from urllib.parse import parse_qs, urlparse

os.environ["MOCK_MODEL_DEFAULTS"] = "1"
os.environ["MOCK_MODELS_SECONDS"] = "0"
os.environ["MOCK_TASKS"] = "1"
os.environ["MOCK_TASKS_NO_ASKS"] = "1"
os.environ["MOCK_MANAGERS"] = "0"
root = Path(__file__).resolve().parents[3]
spec = importlib.util.spec_from_file_location("model_profiles_mock", root / "scripts/mock/mock_hub.py")
hub = importlib.util.module_from_spec(spec)
spec.loader.exec_module(hub)
original_get = hub.H.do_GET
mode = "normal"
slow_requests = 0
release_models = threading.Event()
release_models.set()
modes = {"normal", "older", "locked-model", "unsupported-effort", "blocked-launcher", "default-model", "start-race", "plan"}

# The ordinary mock plan has no tier yet. This test-only copy covers every display precedence without changing it.
plan = hub.TASK_PLANS["plan_mock_3"]
plan["tasks"] = [
    {"title": "Tiered review", "prompt": "Review", "machine": "macbook", "cwd": "/Users/me/code/weather",
     "launcher": "claude", "tier": "review", "permission": "acceptEdits"},
    {"title": "Tier without a launcher", "prompt": "Review", "machine": "macbook", "cwd": "/Users/me/code/weather",
     "tier": "review", "permission": "acceptEdits"},
    {"title": "Explicit model wins", "prompt": "Review", "machine": "macbook", "cwd": "/Users/me/code/weather",
     "launcher": "claude", "tier": "review", "model": "gw/model-x", "permission": "acceptEdits"},
    {"title": "Ordinary default model", "prompt": "Document", "machine": "macbook", "cwd": "/Users/me/code/weather",
     "permission": "acceptEdits"},
]

special_tiers = {
    "locked-model": [{"name": "locked", "launcher": "sonnetcc", "model": "opus", "effort": "high",
                      "description": "A model the launcher fixes itself"}],
    "unsupported-effort": [{"name": "deep", "launcher": "claude", "model": "haiku", "effort": "max",
                            "description": "An effort this model does not offer"}],
    "blocked-launcher": [{"name": "blocked", "launcher": "claude", "model": "opus", "effort": "high",
                          "description": "A launcher whose check failed"}],
    "default-model": [{"name": "standard", "launcher": "claude", "model": "default", "effort": "low",
                       "description": "The launcher's own default, chosen explicitly"}],
    "start-race": [{"name": "race", "launcher": "slowcc", "model": "opus", "effort": "high",
                    "description": "Start before the second Claude's model list answers"}],
}


def launchers(handler, path, query):
    """The mock's model-default response with only this fixture's requested tier/health variation."""
    purpose_id = query.get("purpose", ["agent.new"])[0]
    purpose = next((x for x in hub.model_configuration()["purposes"] if x["id"] == purpose_id), None)
    default = None
    if purpose and purpose["kind"] == "launcher":
        binding = purpose["effective"]
        default = {"provider": binding.get("provider", "").removeprefix("launcher."),
                   "model": binding.get("model", ""), "effort": binding.get("effort", "")}
        if purpose.get("runtimes"):
            default["runtime"] = binding.get("runtime") or "cli"
    machine = path.split("/")[4]
    out = {"launchers": copy.deepcopy(hub.machine_launchers(machine)), "default": default}
    if purpose_id == "agent.new":
        out = hub.with_own_service_unavailable(out, machine, handler.headers.get("Accept-Language"))
        if mode == "start-race":
            # A second, unlocked Claude launcher: unlike sonnetcc it can take the preset's model and effort.
            out["launchers"].append({"name": "slowcc", "model": "sonnet", "agent": "claude"})
        if mode != "older":
            out["tiers_profile"] = {"id": "performance", "name": "Performance"}
            out["tiers"] = (special_tiers[mode] if mode in special_tiers else
                            [{"name": name, **tier} for name, tier in sorted(hub.MOCK_PRESETS.items())])
        if mode == "blocked-launcher":
            for launcher in out["launchers"]:
                if launcher["name"] == "claude":
                    launcher["health"] = {"state": "blocked", "kind": "auth", "error": "401 sign-in rejected",
                                          "checked_at": "2026-11-19T00:00:00Z"}
    handler.send_json(out)


def get(self):
    global mode, slow_requests
    url = urlparse(self.path)
    query = parse_qs(url.query)
    if url.path == "/mock/presets":
        if query.get("release") == ["1"]:
            release_models.set()
            return self.send_json({"mode": mode, "released": True, "slow_pending": False})
        if "mode" not in query:
            return self.send_json({"mode": mode, "slow_pending": slow_requests > 0 and not release_models.is_set()})
        requested = query.get("mode", [mode])[0]
        if requested not in modes:
            return self.send_json({"error": "unknown mode"}, 400)
        release_models.set()  # unblock a request left by a failed test before changing fixtures
        mode = requested
        slow_requests = 0
        if mode == "start-race":
            release_models.clear()
        del hub.STARTED[:]
        return self.send_json({"mode": mode, "slow_pending": False})
    if mode == "start-race" and url.path == "/api/v1/machines/macbook/launchers/slowcc/models":
        # A gate, not a timing guess: Start must retain the preset while this unlocked list is still unavailable.
        slow_requests += 1
        if not release_models.wait(timeout=30):
            return self.send_json({"error": "test did not release slowcc models"}, 504)
        return self.send_json(copy.deepcopy(hub.LAUNCHER_MODELS["claude"]))
    if (url.path.startswith("/api/v1/machines/") and url.path.endswith("/launchers")
            and query.get("purpose", ["agent.new"])[0] == "agent.new"):
        return launchers(self, url.path, query)
    return original_get(self)


hub.H.do_GET = get
if __name__ == "__main__":
    server = hub.ThreadingHTTPServer(("127.0.0.1", hub.PORT), hub.H)
    server.daemon_threads = True
    hub.ENDPOINTS.append(hub.endpoint("127.0.0.1", hub.PORT, "http", "lan"))
    print(f"Model profile fixture ready on port {hub.PORT}", flush=True)
    try:
        server.serve_forever()
    finally:
        release_models.set()
        server.server_close()
