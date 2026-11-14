"""Exercise the unified model settings mock without a simulator or real providers."""
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import unittest
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


class MockHubTests(unittest.TestCase):
    mock_env = {}

    def setUp(self):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        self.base = f"http://127.0.0.1:{port}"
        self.log = tempfile.TemporaryFile()
        env = {k: v for k, v in os.environ.items() if not k.startswith("MOCK_")}
        self.process = subprocess.Popen(
            ["python3", "-I", str(Path(__file__).with_name("mock_hub.py"))],
            env=dict(env, PORT=str(port), **self.mock_env), stdout=self.log, stderr=self.log)
        self.addCleanup(self.stop)
        for _ in range(60):
            if self.process.poll() is not None:
                raise RuntimeError("mock hub exited before startup")
            try:
                with urlopen(self.base + "/api/v1/health", timeout=1):
                    return
            except (URLError, TimeoutError):
                time.sleep(0.1)
        raise RuntimeError("mock hub startup timed out")

    def stop(self):
        self.process.terminate()
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(timeout=5)
        self.log.close()

    def request(self, path, body=None, method=None, language=None):
        headers = {"Content-Type": "application/json"}
        if language:
            headers["Accept-Language"] = language
        request = Request(self.base + path,
                          data=None if body is None else json.dumps(body).encode(),
                          method=method or ("GET" if body is None else "PATCH"), headers=headers)
        with urlopen(request, timeout=3) as response:
            return json.load(response)

    def assert_status(self, code, path, body=None, method=None, language=None):
        with self.assertRaises(HTTPError) as error:
            self.request(path, body, method, language)
        with error.exception as response:
            self.assertEqual(response.code, code)
            return json.load(response)


class ModelConfigMockTests(MockHubTests):

    def test_catalog(self):
        catalog = self.request("/api/v1/model-config")
        ids = [p["id"] for p in catalog["purposes"]]
        self.assertEqual(ids, ["assistant", "agent.new", "kb", "inbox.triage"])
        self.assertTrue(all(p["category_id"] == p["id"] and p["uses"] for p in catalog["purposes"]))

    def test_save_conflict_and_restore(self):
        path = "/api/v1/model-config/purposes/agent.new"
        original = self.request(path)["purpose"]
        saved = self.request(path, {"revision": original["revision"], "model": "model-mock-test"})["purpose"]
        self.assertEqual(saved["effective"]["model"], "model-mock-test")
        self.assertEqual(saved["revision"], original["revision"] + 1)
        with self.assertRaises(HTTPError) as error:
            self.request(path, {"revision": original["revision"], "model": "stale"})
        self.assertEqual(error.exception.code, 409)
        restored = self.request(path, {"revision": saved["revision"], "model": None})["purpose"]
        self.assertNotIn("model", restored["override"])
        self.assertEqual(restored["effective"]["model"], "model-chat")

    def test_uses_follow_category_and_cannot_be_edited(self):
        category_path = "/api/v1/model-config/purposes/kb"
        original = self.request(category_path)["purpose"]
        self.request(category_path, {"revision": original["revision"], "model": "processing-model"})
        for pid in ("translate", "inbox.draft", "kb.compress", "assistant.title"):
            path = "/api/v1/model-config/purposes/" + pid
            purpose = self.request(path)["purpose"]
            self.assertEqual(purpose["category_id"], "kb")
            self.assertEqual(purpose["effective"]["model"], "processing-model")
            self.assertEqual(purpose["override"], {})
            self.assertEqual(purpose["fields"], [])
            with self.assertRaises(HTTPError) as error:
                self.request(path, {"revision": purpose["revision"], "model": "individual"})
            self.assertEqual(error.exception.code, 400)
        self.assertEqual(len(self.request("/api/v1/model-config")["purposes"]), 4)

    def test_unknown_purpose_is_not_created(self):
        for body in (None, {"revision": 0, "model": "x"}):
            with self.assertRaises(HTTPError) as error:
                self.request("/api/v1/model-config/purposes/unknown", body)
            self.assertEqual(error.exception.code, 404)


    def test_meeting_purposes_are_unknown_without_the_flag(self):
        for pid in ("meeting.room", "meeting.minutes"):
            path = "/api/v1/model-config/purposes/" + pid
            self.assert_status(404, path)
            for method in ("PATCH", "PUT"):
                self.assert_status(404, path, {"revision": 0, "model": "unused"}, method)
        purposes = self.request("/api/v1/model-config")["purposes"]
        self.assertEqual([p["id"] for p in purposes], ["assistant", "agent.new", "kb", "inbox.triage"])
        self.assertFalse(any(u["name"] in ("会中对话", "会议纪要") for p in purposes for u in p["uses"]))


class MeetingModelMockTests(MockHubTests):
    mock_env = {"MOCK_MEETING_MODELS": "1", "MOCK_PROVIDERS": "1", "MOCK_MODEL_DEFAULTS": "1"}

    def add_api(self, protocol):
        name = "meeting_" + protocol
        self.request("/api/v1/launchers", {"name": name, "kind": "api", "api": protocol,
                     "base_url": "https://api.example.com", "model": "meeting-default"}, method="POST")
        return "launcher." + name

    def test_catalog_and_localized_shapes(self):
        # The hub's names and groups (assistant-models name_*, group_assistant, group_processing) in each language.
        machine = self.request("/api/v1/assistant")["machine_id"]
        chinese = {p["id"]: p for p in self.request("/api/v1/model-config")["purposes"]}
        for language, names, groups, parents in (
                ("en", ("In-meeting conversation", "Meeting minutes"), ("Main chat", "Processing"), ("Main chat", "Processing")),
                ("zh-Hans", ("会中对话", "会议纪要"), ("主对话", "信息处理"), ("主对话", "信息处理")),
                ("ja", ("会議中の会話", "議事録"), ("メインチャット", "情報処理"), ("メインチャット", "情報処理"))):
            with self.subTest(language=language):
                catalog = self.request("/api/v1/model-config", language=language)
                self.assertEqual([p["id"] for p in catalog["purposes"]],
                                 ["assistant", "agent.new", "kb", "inbox.triage", "meeting.room", "meeting.minutes"])
                rows = {p["id"]: p for p in catalog["purposes"]}
                for (pid, category, kind), name, group, parent in zip(
                        (("meeting.room", "assistant", "launcher"), ("meeting.minutes", "kb", "api")), names, groups, parents):
                    own = self.request("/api/v1/model-config/purposes/" + pid, language=language)["purpose"]
                    self.assertEqual(own, rows[pid])
                    self.assertEqual((own["kind"], own["category_id"], own["parent"]), (kind, category, category))
                    self.assertEqual((own["name"], own["group"], own["inherited_from"]), (name, group, parent))
                    self.assertEqual((own["revision"], own["override"]), (0, {}))
                    self.assertEqual(own["effective"], rows[category]["effective"])
                    self.assertEqual(own["fields"], ["provider", "model", "effort"])
                    self.assertEqual(own["machine_id"], machine)  # the minutes too, as every API purpose
                    self.assertTrue(own["description"])
                    self.assertNotIn("uses", own)
                # The categories' rows keep the mock's one language and their fields: the flag only adds the uses.
                for category in ("assistant", "kb"):
                    self.assertEqual(rows[category], chinese[category])
                self.assertIn({"name": "会中对话"}, rows["assistant"]["uses"])
                self.assertEqual(rows["kb"]["uses"][-2:], [{"name": "会议纪要"}, {"name": "会中对话不可用时的会议问答"}])
                self.assertEqual(len(rows["kb"]["uses"]), 7)
                self.assertEqual(rows["kb"]["fields"], ["provider", "model"])
                self.assertEqual(rows["inbox.triage"]["fields"], ["provider", "model"])
                self.assertEqual(rows["meeting.room"]["launch"], "headless")
                self.assertNotIn("launch", rows["meeting.minutes"])
                self.assertEqual(rows["meeting.minutes"]["provider_ids"], rows["kb"]["provider_ids"])
                providers = rows["meeting.room"]["provider_ids"]
                for provider in ("launcher.claude", "launcher.cc_bash", "launcher.gw_full", "legacy.ai"):
                    self.assertIn(provider, providers)
                for provider in ("launcher.codex", "launcher.cc_main"):
                    self.assertNotIn(provider, providers)
                # Each launcher's agent as the hub's viewAgent sends it: none for a Claude Code alias.
                agents = {p["id"]: p.get("agent") for p in catalog["providers"] if p["kind"] == "launcher"}
                self.assertEqual((agents["launcher.claude"], agents["launcher.codex"], agents["launcher.cc_bash"]), ("claude", "codex", None))

    def test_own_revisions_sparse_updates_conflicts_and_reset(self):
        api = self.add_api("messages")
        for pid, category, second_provider in (("meeting.room", "assistant", "launcher.claude"),
                                               ("meeting.minutes", "kb", api)):
            with self.subTest(purpose=pid):
                path, parent_path = "/api/v1/model-config/purposes/" + pid, "/api/v1/model-config/purposes/" + category
                parent = self.request(parent_path)["purpose"]
                parent = self.request(parent_path, {"revision": parent["revision"], "model": "parent-model", "effort": "low"})["purpose"]
                own = self.request(path)["purpose"]
                self.assertEqual((own["revision"], own["override"]), (0, {}))
                self.assertEqual(own["effective"], parent["effective"])
                saved = self.request(path, {"revision": 0, "provider": "legacy.ai", "model": "meeting-model", "effort": "high"}, "PUT")["purpose"]
                self.assertEqual(saved["revision"], 1)
                self.assertEqual(saved["override"], {"provider": "legacy.ai", "model": "meeting-model", "effort": "high"})
                sparse = self.request(path, {"revision": 1, "effort": "low"})["purpose"]
                self.assertEqual(sparse["revision"], 2)
                self.assertEqual(sparse["override"], dict(saved["override"], effort="low"))
                for method in ("PATCH", "PUT"):
                    self.assert_status(409, path, {"revision": 1, "model": "stale"}, method)
                    self.assertEqual(self.request(path)["purpose"], sparse)
                cleared = self.request(path, {"revision": 2, "model": None}, "PUT")["purpose"]
                self.assertEqual(cleared["override"], {"provider": "legacy.ai", "effort": "low"})
                self.assertEqual(cleared["effective"]["model"], "model-fast")
                changed = self.request(path, {"revision": 3, "model": "provider-specific", "effort": "high"})["purpose"]
                self.assertEqual(changed["override"]["model"], "provider-specific")
                self.assertNotIn("inherited_from", changed, "set apart, it follows nothing")
                changed = self.request(path, {"revision": 4, "provider": second_provider})["purpose"]
                self.assertEqual(changed["override"], {"provider": second_provider})
                self.assertNotIn("effort", changed["effective"])
                restored = self.request(path, {"revision": 5, "provider": None, "model": None, "effort": None}, "PUT")["purpose"]
                self.assertEqual((restored["revision"], restored["override"]), (6, {}))
                self.assertEqual(restored["effective"], parent["effective"])
                self.assertEqual(restored["inherited_from"], parent["name"])
                self.assertEqual(self.request(parent_path)["purpose"], parent, "the category record is untouched")
                parent = self.request(parent_path, {"revision": parent["revision"], "model": "later-parent"})["purpose"]
                following = self.request(path)["purpose"]
                self.assertEqual(following["effective"], parent["effective"])
                self.assertEqual(following["revision"], 6, "category edits do not advance the own revision")

    def test_room_refuses_other_runtimes_and_protocols_without_storing(self):
        messages, responses, chat = (self.add_api(protocol) for protocol in ("messages", "responses", "chat"))
        path = "/api/v1/model-config/purposes/meeting.room"
        own = self.request(path, language="en")["purpose"]
        self.assertIn(messages, own["provider_ids"])
        for provider in ("launcher.codex", responses, chat):
            self.assertNotIn(provider, own["provider_ids"])
            for method in ("PATCH", "PUT"):
                error = self.assert_status(400, path, {"revision": 0, "provider": provider, "model": "not-saved"}, method, "en")
                self.assertIn("runs only on Claude Code", error["error"])
                self.assertEqual(self.request(path, language="en")["purpose"], own)
        self.assert_status(400, path, {"revision": 0, "provider": "launcher.cc_main"})
        for language, reason in (("zh-Hans", "只能用 Claude Code"), ("ja", "Claude Code でのみ動きます")):
            error = self.assert_status(400, path, {"revision": 0, "provider": "launcher.codex"}, "PUT", language)
            self.assertIn(reason, error["error"])
        for provider in ("launcher.claude", "launcher.gw_full", messages):
            own = self.request(path, {"revision": own["revision"], "provider": provider}, "PUT")["purpose"]
            self.assertEqual(own["effective"]["provider"], provider)
            self.assertNotIn("runtime", own["effective"])

    def test_runtime_is_rejected_and_never_inherited(self):
        main_path = "/api/v1/model-config/purposes/assistant"
        main = self.request(main_path, {"revision": 0, "runtime": "agentsdk", "model": "main-model"})["purpose"]
        self.assertEqual(main["effective"]["runtime"], "agentsdk")
        room = self.request("/api/v1/model-config/purposes/meeting.room")["purpose"]
        self.assertEqual(room["effective"], {k: v for k, v in main["effective"].items() if k != "runtime"})
        for pid in ("meeting.room", "meeting.minutes"):
            path = "/api/v1/model-config/purposes/" + pid
            own = self.request(path)["purpose"]
            self.assertNotIn("runtime", own["fields"])
            for method in ("PATCH", "PUT"):
                for runtime in ("agentsdk", "cli", None):
                    self.assert_status(400, path, {"revision": 0, "runtime": runtime}, method)
                    self.assertEqual(self.request(path)["purpose"], own)
        self.assertEqual(self.request(main_path)["purpose"], main)

    def test_room_explains_codex_inheritance_and_an_override_fixes_it(self):
        self.request("/api/v1/model-config/purposes/assistant", {"revision": 0, "provider": "launcher.codex", "model": "main-model"})
        path = "/api/v1/model-config/purposes/meeting.room"
        own = self.request(path, language="en")["purpose"]
        self.assertEqual(own["effective"], {"provider": "launcher.codex", "model": "main-model"})
        self.assertIn("codex isn't Claude Code", own["status"])
        configured = self.request(path, {"revision": 0, "provider": "launcher.claude"}, language="en")["purpose"]
        self.assertNotIn("isn't Claude Code", configured["status"])
        reset = self.request(path, {"revision": 1, "provider": None}, language="en")["purpose"]
        self.assertEqual(reset["effective"], own["effective"])
        self.assertEqual(reset["status"], own["status"])

    def test_a_model_alone_still_follows_the_category(self):
        for pid, parent in (("meeting.room", "Main chat"), ("meeting.minutes", "Processing")):
            path = "/api/v1/model-config/purposes/" + pid
            own = self.request(path, {"revision": 0, "model": "only-model"}, "PUT", "en")["purpose"]
            self.assertEqual((own["override"], own["inherited_from"]), ({"model": "only-model"}, parent))
            own = self.request(path, {"revision": 1, "provider": "legacy.ai"}, "PATCH", "en")["purpose"]
            self.assertNotIn("inherited_from", own)

    def test_minutes_fields_follow_the_api_protocol(self):
        chat, responses = (self.add_api(protocol) for protocol in ("chat", "responses"))
        path = "/api/v1/model-config/purposes/meeting.minutes"
        own = self.request(path, {"revision": 0, "provider": chat}, "PUT")["purpose"]
        self.assertEqual(own["fields"], ["provider", "model"])
        own = self.request(path, {"revision": 1, "provider": responses, "effort": "high"})["purpose"]
        self.assertEqual(own["fields"], ["provider", "model", "effort"])
        self.assertEqual(own["override"], {"provider": responses, "effort": "high"})


class MeetingModelsWithSupervisorMockTests(MockHubTests):
    mock_env = {"MOCK_MEETING_MODELS": "1", "MOCK_SUPERVISE_LISTED": "1", "MOCK_VOICE": "1"}

    def test_meeting_rows_follow_supervisor_and_precede_plugins(self):
        purposes = self.request("/api/v1/model-config")["purposes"]
        self.assertEqual([p["id"] for p in purposes],
                         ["assistant", "agent.new", "kb", "inbox.triage", "assistant.supervise", "meeting.room", "meeting.minutes", "plugin.voice"])
        for purpose in purposes[4:7]:
            self.assertEqual(purpose, self.request("/api/v1/model-config/purposes/" + purpose["id"])["purpose"])


class MeetingModelsLimitMockTests(MockHubTests):
    mock_env = {"MOCK_MEETING_MODELS": "1", "MOCK_MODEL_LIMIT": "1"}

    def test_meeting_limits_follow_the_effective_provider(self):
        kb = self.request("/api/v1/model-config/purposes/kb")["purpose"]
        minutes = self.request("/api/v1/model-config/purposes/meeting.minutes")["purpose"]
        self.assertEqual(minutes["limit"], kb["limit"])
        self.assertEqual(minutes["status"], kb["status"])
        path = "/api/v1/model-config/purposes/meeting.room"
        room = self.request(path, {"revision": 0, "provider": "legacy.ai"})["purpose"]
        self.assertEqual(room["limit"], kb["limit"])
        self.assertEqual(room["status"], kb["status"])
        room = self.request(path, {"revision": 1, "provider": "launcher.claude"})["purpose"]
        self.assertNotIn("limit", room)
        self.assertNotEqual(room["status"], kb["status"])


if __name__ == "__main__":
    unittest.main()
