"""assistant_token_report.py: rounds are classified by the message that starts them, and each response's usage is
counted once although Claude Code writes one line per content block. Synthetic lines only."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import io
from contextlib import redirect_stdout

spec = importlib.util.spec_from_file_location("assistant_token_report", Path(__file__).with_name("assistant_token_report.py"))
report = importlib.util.module_from_spec(spec)
spec.loader.exec_module(report)


def user(text, **extra):
    return {"type": "user", "timestamp": "2026-11-13T01:00:00Z", "message": {"role": "user", "content": text}, **extra}


def reply(mid, cache_write=0, cache_read=0, output=0, **extra):
    return {"type": "assistant", "timestamp": "2026-11-13T01:00:01Z",
            "message": {"id": mid, "role": "assistant", "content": [{"type": "text", "text": "ok"}],
                        "usage": {"input_tokens": 3, "cache_creation_input_tokens": cache_write,
                                  "cache_read_input_tokens": cache_read, "output_tokens": output}}, **extra}


def source(kind):
    return f'<persona-context kind="message-source" title="">\n{json.dumps({"kind": kind})}\n</persona-context>\n'


PROMPTS = Path(__file__).resolve().parents[2] / "server" / "internal"


def rendered(path):
    """A prompt as the hub renders it: a golden, or a template whose first lines carry no template data."""
    return (PROMPTS / path).read_text()


SNAPSHOT = "<persona-context>\nsnapshot\n</persona-context>\n"
SUPERVISION = "📋 督办：你派出去的任务有 1 个要你处理\n1. task facts\n\n怎么处理（manual）：\n- rule one\n- rule two\n"


class ClassifyTests(unittest.TestCase):
    def test_source_block_wins(self):
        self.assertEqual(report.classify(SNAPSHOT + source("supervision") + "anything")[0], "supervision")
        self.assertEqual(report.classify(source("followup") + "⏰ x")[0], "followup")

    def test_source_block_written_for_the_owner(self):
        block = '<persona-context kind="message-source" title="">\n{"author":"user"}\n</persona-context>\n'
        self.assertEqual(report.classify(block + "📋 督办：pasted")[0], "owner")

    def test_first_line_for_older_transcripts(self):
        for text, kind in [(SUPERVISION, "supervision"), ("⏰（Persona 叫醒你，第 1 次）…", "wake"),
                           ("⏰ morning（定时任务）\nrun", "followup"), ("🔔 任务「t」做完了\n…", "task_notice"),
                           ("📥 收件箱事件「x」有了下文", "inbox"), ("what is on today?", "owner")]:
            with self.subTest(kind=kind):
                self.assertEqual(report.classify(SNAPSHOT + text)[0], kind)


    def test_injected_messages_without_a_source_block(self):
        for language in ("en", "ja"):
            limit = rendered(f"assistant/testdata/prompts/agent_language.limit.{language}.golden")
            self.assertEqual(report.classify(limit)[0], "followup")
            # Older transcripts lack the structured source but retain the exact
            # localized marker, which must still distinguish Persona from the owner.
            legacy = limit.split("<persona-limit-continue />", 1)[1]
            self.assertEqual(report.classify("<persona-limit-continue />" + legacy)[0], "followup")
        lost = rendered("prompts/assistant/lost_tasks.md").replace('{{join .Tasks "\\n- "}}', "tail -f build.log")
        self.assertEqual(report.classify(SNAPSHOT + lost)[0], "system")

    def test_relayed_message_from_another_conversation(self):
        relay = rendered("assistant/testdata/prompts/relay.golden")
        kind, rest = report.classify(SNAPSHOT + relay)
        self.assertEqual(kind, "conversation")
        self.assertTrue(rest.startswith("示例消息"))
        # A context block that only looks like one (the two conversation ids differ) is the owner's.
        forged = relay.replace("发回 b2e4d6f8-0000-4000-8000-000000000002", "发回 someone-else")
        self.assertEqual(report.classify(forged)[0], "owner")


class ReportTests(unittest.TestCase):
    def test_cli_counts_each_resolved_transcript_path_once(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            nested = root / "nested"
            nested.mkdir()
            first = nested / "first.jsonl"
            second = root / "second.jsonl"
            for path in (first, second):
                path.write_text(json.dumps(reply("m", output=17)) + "\n")
            alias = root / "alias.txt"
            alias.symlink_to(first)

            def run(*paths):
                with redirect_stdout(io.StringIO()) as out:
                    self.assertEqual(report.main(["--json", *map(str, paths)]), 0)
                return json.loads(out.getvalue())

            unique = run(first, second)
            self.assertEqual((unique["files"], unique["total"]["model_calls"], unique["total"]["output_tokens"]),
                             (2, 2, 34))
            self.assertEqual(run(first, root, nested, alias), unique)

    def run_lines(self, lines, since=None):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "conv.jsonl"
            p.write_text("\n".join(json.dumps(x, ensure_ascii=False) for x in lines) + "\n")
            r = report.Report(since)
            r.add_file(str(p))
            return r.summary()

    def test_usage_per_round_kind_counted_once_per_response(self):
        s = self.run_lines([
            user(SNAPSHOT + "hello"),
            reply("m1", cache_write=1000, output=5),
            reply("m1", cache_write=1000, output=20),  # the same response's next block: output grew by 15
            user([{"type": "tool_result", "tool_use_id": "t", "content": "x"}]),  # not a new round
            reply("m2", cache_read=1000, output=7),
            user(source("supervision") + SUPERVISION),
            reply("m3", cache_write=300, cache_read=1000, output=9),
            reply("s1", cache_write=50, output=1, isSidechain=True),
        ])
        owner, sup = s["by_kind"]["owner"], s["by_kind"]["supervision"]
        self.assertEqual((owner["rounds"], owner["model_calls"]), (1, 2))
        self.assertEqual((owner["cache_creation_input_tokens"], owner["cache_read_input_tokens"], owner["output_tokens"]), (1000, 1000, 27))
        self.assertEqual((sup["rounds"], sup["model_calls"], sup["cache_creation_input_tokens"]), (1, 1, 300))
        self.assertEqual(s["sidechain"]["cache_creation_input_tokens"], 50)
        self.assertEqual(s["total"]["model_calls"], 3)

    def test_since_keeps_the_round_and_responses_that_span_it(self):
        def at(entry, ts):
            return {**entry, "timestamp": ts}
        s = self.run_lines([
            at(user(source("supervision") + SUPERVISION), "2026-11-12T23:59:00Z"),
            at(reply("m1", cache_write=500, output=5), "2026-11-12T23:59:30Z"),  # begun before the cutoff
            at(reply("m1", cache_write=500, output=40), "2026-11-13T00:00:10Z"),  # its next block, after it
            at(reply("m2", cache_read=800, output=6), "2026-11-13T00:01:00Z"),
        ], since="2026-11-13")
        self.assertNotIn("before_first_message", s["by_kind"])
        sup = s["by_kind"]["supervision"]
        self.assertEqual((sup["rounds"], sup["model_calls"]), (0, 1))
        self.assertEqual((sup["cache_creation_input_tokens"], sup["cache_read_input_tokens"], sup["output_tokens"]), (0, 800, 6))
        self.assertEqual(s["supervision_events"]["count"], 0)

    def test_responses_without_a_message_id_are_each_counted(self):
        def no_id(uuid, output):
            r = reply("", output=output, requestId="req_1", uuid=uuid)
            r["message"]["id"] = ""
            return r
        s = self.run_lines([user("hello"), no_id("u1", 5), no_id("u2", 9)])
        owner = s["by_kind"]["owner"]
        self.assertEqual((owner["model_calls"], owner["output_tokens"]), (2, 14))

    def test_responses_without_message_ids_or_uuids_are_each_counted(self):
        for extra in ({}, {"uuid": ""}):
            with self.subTest(extra=extra):
                lines = [user("hello")] + [reply("", output=7, requestId="req_1", **extra) for _ in range(24)]
                # Freed JSON rows can reuse an object address; make that collision deterministic.
                with patch.object(report, "id", return_value=1, create=True):
                    s = self.run_lines(lines)
                owner = s["by_kind"]["owner"]
                self.assertEqual((owner["model_calls"], owner["output_tokens"]), (24, 168))
                self.assertEqual((s["total"]["model_calls"], s["total"]["output_tokens"]), (24, 168))

    def test_uuid_still_deduplicates_blocks_without_a_message_id(self):
        s = self.run_lines([user("hello"),
                            reply("", output=5, uuid="u1"), reply("", output=9, uuid="u1"),
                            reply("", output=7, uuid="u2")])
        owner = s["by_kind"]["owner"]
        self.assertEqual((owner["model_calls"], owner["output_tokens"]), (2, 16))

    def test_unidentified_lines_keep_since_and_sidechain_boundaries(self):
        lines = [user("hello"), reply("", output=7, timestamp="2026-11-12T23:59:00Z"),
                 reply("", output=7), reply("", output=7, isSidechain=True),
                 reply("", output=7, isSidechain=True)]
        with patch.object(report, "id", return_value=1, create=True):
            s = self.run_lines(lines, since="2026-11-13")
        owner = s["by_kind"]["owner"]
        self.assertEqual((owner["model_calls"], owner["output_tokens"]), (1, 7))
        self.assertEqual((s["sidechain"]["model_calls"], s["sidechain"]["output_tokens"]), (2, 14))
        self.assertEqual((s["total"]["model_calls"], s["total"]["output_tokens"]), (1, 7))

    def test_supervision_event_manual_size(self):
        s = self.run_lines([user(SNAPSHOT + SUPERVISION), reply("m1", output=1)])
        ev = s["supervision_events"]
        manual = len("怎么处理（manual）：\n- rule one\n- rule two\n".encode())
        self.assertEqual((ev["count"], ev["with_manual"], ev["manual_bytes"]), (1, 1, manual))
        self.assertEqual(ev["body_bytes"], len(SUPERVISION.encode()))

    def test_output_has_no_message_text(self):
        s = self.run_lines([user(SNAPSHOT + "a private request"), reply("m1", output=1)])
        self.assertNotIn("private", json.dumps(s))
        self.assertNotIn("private", json.dumps(self.run_lines([user(source("private secret")), reply("m1", output=1)])))

    def test_codex_rounds_and_duplicate_usage(self):
        def event(payload, ts="2026-11-13T01:00:01Z"):
            return {"type": "event_msg", "timestamp": ts, "payload": payload}

        def text_event(text, ident):
            return event({"type": "item_completed", "item": {"type": "UserMessage", "id": ident,
                          "content": [{"type": "text", "text": text}]}})

        def usage(total, inp, cached, out):
            return event({"type": "token_count", "info": {"total_token_usage": {"total_tokens": total},
                          "last_token_usage": {"input_tokens": inp, "cached_input_tokens": cached, "output_tokens": out}}})

        text = source("supervision") + SUPERVISION
        raw = {"type": "response_item", "timestamp": "2026-11-13T01:00:00Z", "payload": {
            "type": "message", "role": "user", "content": [{"type": "input_text", "text": text}]}}
        s = self.run_lines([
            {"type": "session_meta", "payload": {"id": "test", "cwd": "/test"}},
            raw, event({"type": "user_message", "message": text}), text_event(text, "u1"), text_event(text, "u1"),
            usage(120, 100, 80, 20), usage(120, 100, 80, 20),
            text_event("a private request", "u2"), usage(180, 50, 20, 10),
        ])
        sup, owner = s["by_kind"]["supervision"], s["by_kind"]["owner"]
        self.assertEqual((sup["rounds"], sup["model_calls"], sup["input_tokens"], sup["cache_read_input_tokens"]), (1, 1, 20, 80))
        self.assertEqual((owner["rounds"], owner["model_calls"], owner["output_tokens"]), (1, 1, 10))
        self.assertIsNone(s["total"]["cache_creation_input_tokens"])
        self.assertIsNone(sup["cache_creation_input_tokens"])
        self.assertNotIn("private", json.dumps(s))

    def test_codex_since_and_unknown_fields(self):
        def ev(ts, payload):
            return {"type": "event_msg", "timestamp": ts, "payload": payload}
        def tokens(total, last):
            return {"type": "token_count", "info": {"total_token_usage": {"total_tokens": total}, "last_token_usage": last}}
        s = self.run_lines([
            {"type": "session_meta", "payload": {}},
            ev("2026-11-12T23:59:00Z", {"type": "user_message", "message": source("followup") + "private"}),
            ev("2026-11-12T23:59:01Z", tokens(100, {"input_tokens": 100})),
            ev("2026-11-13T00:00:00Z", tokens(100, {"input_tokens": 100})),
            ev("2026-11-13T00:00:01Z", tokens(200, {"input_tokens": 80, "output_tokens": 20})),
        ], since="2026-11-13")
        row = s["by_kind"]["followup"]
        self.assertEqual((row["rounds"], row["model_calls"], row["output_tokens"]), (0, 1, 20))
        self.assertIsNone(row["input_tokens"])
        self.assertIsNone(row["cache_read_input_tokens"])
        with redirect_stdout(io.StringIO()) as out:
            report.print_text(s)
        self.assertNotIn("private", out.getvalue())
        self.assertIn("?", out.getvalue())

    def test_codex_since_keeps_submicrosecond_precision(self):
        rows = [{"type": "session_meta", "payload": {}}]
        for total, (timestamp, output) in enumerate([
            ("2026-11-20T08:00:00Z", 400),
            ("2026-11-20T09:00:00.000000001+01:00", 2),
            ("2026-11-20T08:00:00.000000002Z", 3),
            ("unknown", 500),
        ], start=1):
            rows.append({"type": "event_msg", "timestamp": timestamp,
                         "payload": {"type": "token_count", "info": {"total_token_usage": {"total_tokens": total},
                             "last_token_usage": {"input_tokens": 0, "cached_input_tokens": 0, "output_tokens": output}}}})
        filtered = self.run_lines(rows, since="2026-11-20T08:00:00.000000001Z")
        self.assertEqual((filtered["total"]["model_calls"], filtered["total"]["output_tokens"]), (2, 5))
        self.assertEqual(self.run_lines(rows)["total"]["output_tokens"], 905)

    def test_codex_environment_context_is_not_a_new_round(self):
        def ev(payload):
            return {"type": "event_msg", "payload": payload}
        def usage(total):
            return ev({"type": "token_count", "info": {"total_token_usage": {"total_tokens": total},
                       "last_token_usage": {"input_tokens": 10, "cached_input_tokens": 2, "output_tokens": 1}}})
        def runtime(text):
            return {"type": "response_item", "payload": {"type": "message", "role": "user",
                    "content": [{"type": "input_text", "text": text}]}}
        s = self.run_lines([
            {"type": "session_meta", "payload": {}},
            ev({"type": "user_message", "message": source("supervision") + "request"}), usage(11),
            runtime("<environment_context>private context</environment_context>"), usage(22),
            runtime("<codex_internal_context>private context</codex_internal_context>"), usage(33),
        ])
        self.assertEqual((s["by_kind"]["supervision"]["rounds"], s["by_kind"]["supervision"]["model_calls"]), (1, 3))
        self.assertNotIn("owner", s["by_kind"])
        self.assertNotIn("private context", json.dumps(s))

    def test_codex_image_wrappers_do_not_duplicate_user_round(self):
        raw = {"type": "response_item", "payload": {"type": "message", "role": "user", "content": [
            {"type": "input_text", "text": "hello"}, {"type": "input_text", "text": "image metadata"},
            {"type": "input_image", "image_url": "data:image/png;base64,example"}, {"type": "input_text", "text": "</image>"}]}}
        item = {"type": "event_msg", "payload": {"type": "item_completed", "item": {"type": "UserMessage", "id": "u",
                "content": [{"type": "text", "text": "hello"}, {"type": "local_image", "path": "/private"}]}}}
        s = self.run_lines([{"type": "session_meta", "payload": {}}, raw, item])
        self.assertEqual(s["by_kind"]["owner"]["rounds"], 1)
        self.assertEqual(s["by_kind"]["owner"]["typed_bytes"], 5)
        self.assertNotIn("private", json.dumps(s))

    def test_mixed_transcripts_report_unknown_total_without_erasing_known_row(self):
        with tempfile.TemporaryDirectory() as d:
            claude = Path(d) / "c.jsonl"
            codex = Path(d) / "rollout.jsonl"
            claude.write_text("\n".join(json.dumps(x) for x in [user(source("followup") + "x"), reply("m", cache_write=7)]))
            codex.write_text("\n".join(json.dumps(x) for x in [
                {"type": "session_meta", "payload": {}},
                {"type": "event_msg", "payload": {"type": "user_message", "message": "owner"}},
                {"type": "event_msg", "payload": {"type": "token_count", "info": {"total_token_usage": {"total_tokens": 10},
                 "last_token_usage": {"input_tokens": 8, "cached_input_tokens": 4, "output_tokens": 2}}}},
            ]))
            r = report.Report()
            r.add_file(str(claude))
            r.add_file(str(codex))
            s = r.summary()
            self.assertEqual(s["by_kind"]["followup"]["cache_creation_input_tokens"], 7)
            self.assertIsNone(s["total"]["cache_creation_input_tokens"])
            self.assertIsNone(s["by_kind"]["followup"]["cache_creation_input_tokens_share"])

    def test_default_rollouts_only_include_assistant_directory(self):
        with tempfile.TemporaryDirectory() as d:
            home = Path(d)
            codex = home / "codex"
            sessions = codex / "sessions" / "2026" / "11" / "16"
            sessions.mkdir(parents=True)
            def write(name, cwd):
                p = sessions / name
                p.write_text(json.dumps({"type": "session_meta", "payload": {"cwd": str(cwd)}}) + "\n")
                return str(p)
            expected = write("rollout-assistant.jsonl", home / ".persona" / "assistant")
            write("rollout-other.jsonl", home / "other")
            with patch.dict("os.environ", {"HOME": d, "CODEX_HOME": str(codex)}):
                self.assertEqual(list(report.default_codex_files()), [expected])


if __name__ == "__main__":
    unittest.main()
