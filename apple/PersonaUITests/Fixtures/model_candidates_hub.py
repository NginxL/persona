#!/usr/bin/env python3
"""Extension candidate editor UI fixture (#10402), layered on the normal mock hub.

PORT=19402 python3 apple/PersonaUITests/Fixtures/model_candidates_hub.py
Set TEST_RUNNER_PERSONA_CANDIDATE_HUB=http://127.0.0.1:19402 for ExtensionModelCandidateUITests.
"""
import importlib.util
import json
import os
from pathlib import Path
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[3]
os.environ.setdefault("PORT", "19402")
spec = importlib.util.spec_from_file_location("model_candidates_mock", ROOT / "scripts/mock/mock_hub.py")
mock = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mock)

EXTENSION = "extension.editor-fixture"
ROUTE = "/api/v1/model-config/purposes/" + EXTENSION
PROVIDERS = [
    dict(id="launcher.atlas", name="Atlas Gateway", kind="api", api="responses", has_key=True, model="atlas-standard"),
    dict(id="launcher.orbit", name="Orbit Gateway", kind="launcher", api="messages", has_key=True, model="orbit-base"),
    dict(id="launcher.terminal", name="Terminal only", kind="launcher"),
    dict(id="launcher.hidden", name="Hidden provider", kind="api", hidden=True, has_key=True),
]
DIRECT = {p["id"]: p for p in PROVIDERS[:2]}
REQUESTED = [dict(last_used="2026-11-19T07:08:09.123456789Z", models=[
    dict(model="atlas-small", provider="launcher.atlas", provider_name="Atlas Gateway", available=True),
    dict(model="orbit-archive", available=False, problem="No provider offers this model."),
])]
EXAMPLE = dict(mode="custom", custom=[dict(provider="launcher.orbit", model=""), dict(provider="launcher.atlas", model="atlas-small")])
revision, binding, selection, requested = 7, {}, None, REQUESTED


def detail():
    custom = []
    for choice in (selection or {}).get("custom", []):
        p = DIRECT[choice["provider"]]
        custom.append(dict(model=choice["model"] or p["model"], configured_model=choice["model"],
                           provider=p["id"], provider_name=p["name"], available=True))
    policy = dict(mode=(selection or {}).get("mode", "extension"), custom=custom, requested=requested,
                  modes=["extension", "off", "custom"], most=2, direct_provider_ids=list(DIRECT))
    return dict(purpose=dict(id=EXTENSION, name="Reading helper", kind="api", group="Extensions", parent="kb", category_id="kb",
                             override=binding, effective={"provider": "launcher.atlas", "model": "atlas-standard", **binding},
                             revision=revision, fields=["provider", "model", "effort"], provider_ids=[p["id"] for p in PROVIDERS],
                             candidates=policy), providers=PROVIDERS)


class Handler(mock.H):
    def do_GET(self):
        global revision, binding, selection, requested
        path = urlsplit(self.path).path
        with mock.MODEL_CONFIG_LOCK:
            if path == ROUTE:
                return self.send_json(detail())
            if path == "/mock/candidate-editor/reset":
                revision, binding, selection, requested = 7, {}, None, REQUESTED
                return self.send_json({"fixture": EXTENSION})
            if path == "/mock/candidate-editor/empty":
                requested = []
                return self.send_json({"fixture": EXTENSION})
            if path == "/mock/candidate-editor/custom":
                revision += 1
                selection = EXAMPLE
                return self.send_json({"fixture": EXTENSION})
            if path == "/api/v1/model-config":
                catalog = mock.listed_model_configuration(mock.cron_lang(self.headers.get("Accept-Language")))
                return self.send_json({**catalog, "purposes": catalog["purposes"] + [detail()["purpose"]],
                                       "providers": catalog["providers"] + PROVIDERS})
        return super().do_GET()

    def do_PATCH(self):
        global revision, selection
        if urlsplit(self.path).path != ROUTE:
            return super().do_PATCH()
        body = json.loads(self.body())
        with mock.MODEL_CONFIG_LOCK:
            if body.get("revision") != revision:
                return self.send_json({"error": "Changed elsewhere"}, 409)
            if "candidates" in body:
                candidate = body["candidates"]
                if candidate and candidate.get("mode") == "custom":
                    rows = candidate.get("custom", [])
                    if not 1 <= len(rows) <= 2 or any(row.get("provider") not in DIRECT for row in rows):
                        return self.send_json({"error": "Invalid custom list"}, 400)
                selection = candidate
            for field in ("provider", "model", "effort"):
                if field in body:
                    if body[field] is None:
                        binding.pop(field, None)
                    else:
                        binding[field] = body[field]
            revision += 1
            return self.send_json(detail())


if __name__ == "__main__":
    with mock.ThreadingHTTPServer(("127.0.0.1", mock.PORT), Handler) as server:
        server.daemon_threads = True
        mock.ENDPOINTS.append(mock.endpoint("127.0.0.1", mock.PORT, "http", "lan"))
        print(f"Candidate model mock hub on 127.0.0.1:{mock.PORT}", flush=True)
        server.serve_forever()
