"""Request source accounting, visibility boundaries, and output privacy; synthetic inputs only."""
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import assistant_token_report as report
from request_breakdown import request_row


def exported(lifecycle="fresh"):
    return {"type": "request", "runtime": "anthropic", "lifecycle": lifecycle, "request": {
        "system": "abc", "tools": [{"name": "example", "input_schema": {"type": "object"}}],
        "messages": [
            {"role": "user", "content": "old"},
            {"role": "assistant", "content": [{"type": "text", "text": "reply"}]},
            {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "private-call", "content": "12345"}]},
            {"role": "user", "content": [
                {"type": "text", "text": "<persona-context>状況</persona-context>新"},
                {"type": "image", "source": {"type": "url", "url": "https://example.com/image.png"}}]}]},
        "usage": {"input_tokens": 50, "cache_read_input_tokens": 30, "output_tokens": 4}}


class RequestTests(unittest.TestCase):
    def test_known_sizes_partition_content_once(self):
        e = exported()
        row = request_row(e)
        sizes = {k: v["bytes"] for k, v in row["sources"].items()}
        # Independent serialization specification, including non-ASCII UTF-8.
        compact = lambda v: len(json.dumps(v, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode())
        self.assertEqual(sizes, {"instructions": 3, "tool_schemas": compact(e["request"]["tools"]),
                                "snapshot_or_events": len("<persona-context>状況</persona-context>".encode()),
                                "history": 8, "current_message": 3, "tool_results": 5,
                                "attachments": compact(e["request"]["messages"][-1]["content"][1]), "other_content": 0})
        self.assertEqual(row["observed_content_bytes"], sum(sizes.values()))
        self.assertEqual(row["largest_tool_result"], {"category": "tool_results", "bytes": 5})
        self.assertEqual(row["provider_usage"]["cache_read_input_tokens"], 30)
        self.assertIsNone(row["provider_usage"]["cache_creation_input_tokens"])

    def test_one_source_control_and_oversized_result(self):
        before = request_row(exported())
        changed = exported()
        changed["request"]["messages"][2]["content"][0]["content"] = "z" * 10000
        after = request_row(changed)
        self.assertEqual(after["dominant_sources"], ["tool_results"])
        self.assertEqual(after["largest_tool_result"]["bytes"], 10000)
        self.assertEqual(after["observed_content_bytes"] - before["observed_content_bytes"], 9995)
        for source in before["sources"]:
            if source != "tool_results":
                self.assertEqual(before["sources"][source], after["sources"][source])

    def test_responses_history_instructions_results_and_image(self):
        row = request_row({"runtime": "responses", "request": {"instructions": "abc", "input": [
            {"role": "developer", "content": [{"type": "input_text", "text": "de"}]},
            {"role": "user", "content": "old"},
            {"type": "function_call", "name": "private-tool", "arguments": "private-args"},
            {"type": "function_call_output", "call_id": "private-call", "output": "return"},
            {"role": "user", "content": [{"type": "input_text", "text": "new"}]}]},
            "usage": {"input_tokens": 20, "input_tokens_details": {"cached_tokens": 10}, "output_tokens": 3}})
        self.assertEqual(row["sources"]["instructions"]["bytes"], 5)
        self.assertEqual(row["sources"]["history"]["bytes"], 3)
        self.assertEqual(row["sources"]["current_message"]["bytes"], 3)
        self.assertEqual(row["sources"]["tool_results"]["bytes"], 6)
        self.assertGreater(row["sources"]["other_content"]["bytes"], 0)
        self.assertEqual(row["provider_usage"]["cache_read_input_tokens"], 10)
        self.assertIsNone(row["request_wire_bytes"])
        self.assertNotIn("private", json.dumps(row))

    def run_lines(self, lines, since=None):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "private-session-id.jsonl"
            path.write_text("\n".join(json.dumps(e) for e in lines))
            r = report.Report(since, request_breakdown=True)
            r.add_file(str(path))
            return r.summary()

    def test_lifecycle_and_unknown_usage_are_preserved(self):
        entries = [exported(k) for k in ("fresh", "followup", "resume", "compaction")]
        entries[0].pop("usage")
        entries[1]["usage"]["cache_creation_input_tokens"] = 0
        result = self.run_lines(entries)
        rows = result["request_breakdown"]["rows"]
        self.assertEqual([r["lifecycle"] for r in rows], ["fresh", "followup", "resume", "compaction"])
        self.assertIsNone(rows[0]["provider_usage"]["input_tokens"])
        self.assertEqual(rows[1]["provider_usage"]["cache_creation_input_tokens"], 0)
        self.assertEqual(rows[2]["provider_usage"]["cache_read_input_tokens"], 30)

    def test_claude_inventory_is_not_current_retained_history(self):
        result = self.run_lines([
            {"type": "user", "uuid": "u", "message": {"content": "hello"}},
            {"type": "assistant", "message": {"id": "a", "content": [{"type": "text", "text": "reply"}]}},
            {"type": "assistant", "message": {"id": "a", "content": [{"type": "text", "text": "reply"}]}},
            {"type": "user", "message": {"content": [{"type": "tool_result", "content": "result"}]}},
            {"type": "system", "subtype": "compact_boundary"},
            {"type": "user", "isCompactSummary": True, "message": {"content": "summary"}},
            {"type": "user", "isSidechain": True, "message": {"content": "do not count"}},
        ])
        row = result["request_breakdown"]["rows"][0]
        self.assertEqual(row["boundary"], "claude_transcript_inventory")
        self.assertEqual(row["lifecycle"], "contains_compaction")
        self.assertEqual(row["sources"]["history"]["bytes"], 17)
        self.assertEqual(row["sources"]["tool_results"]["bytes"], 6)
        for field in ("instructions", "tool_schemas", "current_message"):
            self.assertIsNone(row["sources"][field]["bytes"])
        self.assertIsNone(row["retained_request_history_bytes"])

    def test_claude_inventory_counts_repeated_user_entries(self):
        repeated = {"type": "user", "uuid": "user", "message": {"content": "again"}}
        result = self.run_lines([
            repeated,
            {"type": "assistant", "message": {"id": "response", "content": "reply"}},
            repeated,
            {"type": "assistant", "message": {"id": "response", "content": "reply"}},
        ])
        row = result["request_breakdown"]["rows"][0]
        self.assertEqual(row["sources"]["history"]["bytes"], 15)

    def test_codex_rollout_avoids_mirrors_and_keeps_metadata_before_since(self):
        result = self.run_lines([
            {"type": "session_meta", "payload": {"base_instructions": {"text": "abc"}}},
            {"type": "response_item", "timestamp": "2026-11-17", "payload": {"type": "message", "role": "user", "content": "old"}},
            {"type": "response_item", "timestamp": "2026-11-18", "payload": {"type": "message", "role": "user", "content": "hello"}},
            {"type": "event_msg", "timestamp": "2026-11-18", "payload": {"type": "user_message", "message": "hello"}},
            {"type": "response_item", "timestamp": "2026-11-18", "payload": {"type": "function_call_output", "output": "result"}},
            {"type": "compacted", "timestamp": "2026-11-18", "payload": {"message": "summary"}},
        ], since="2026-11-18")
        row = result["request_breakdown"]["rows"][0]
        self.assertEqual(row["boundary"], "codex_transcript_inventory")
        self.assertEqual(row["observed_instruction_bytes"], 3)
        self.assertEqual(row["sources"]["history"]["bytes"], 5)
        self.assertEqual(row["sources"]["tool_results"]["bytes"], 6)
        self.assertIsNone(row["sources"]["tool_schemas"]["bytes"])

    def test_private_values_never_leave_either_output_format(self):
        entry = exported("private-lifecycle")
        entry["request"]["system"] = "private-instruction"
        entry["request"]["tools"][0]["name"] = "private-tool"
        entry["request"]["messages"][0]["content"] = "private-message"
        entry["request"]["messages"][2]["content"][0]["content"] = "private-result"
        entry["request"]["messages"][-1]["content"][1]["source"]["url"] = "https://example.com/private-attachment"
        entry["usage"]["input_tokens"] = "private-counter"
        entry["usage"]["output_tokens"] = True
        result = self.run_lines([entry])
        with redirect_stdout(io.StringIO()) as output:
            report.print_text(result)
        self.assertNotIn("private", json.dumps(result))
        self.assertNotIn("private", output.getvalue())
        self.assertIsNone(result["request_breakdown"]["rows"][0]["provider_usage"]["output_tokens"])

    def test_result_with_attachment_is_disjoint(self):
        e = exported()
        e["request"]["messages"][2]["content"][0]["content"] = [
            {"type": "text", "text": "abc"}, {"type": "text", "text": "defg"},
            {"type": "image", "source": {"data": "AAAA"}}]
        row = request_row(e)
        self.assertEqual(row["largest_tool_result"]["bytes"], 7)
        self.assertEqual(row["sources"]["tool_results"]["bytes"], 7)
        self.assertEqual(row["sources"]["attachments"]["items"], 2)

    def test_structured_tool_output_counts_as_result_not_an_envelope(self):
        e = exported()
        value = {"private-key": "private-value", "count": 12}
        e["request"]["messages"][2]["content"][0]["content"] = value
        row = request_row(e)
        expected = len(json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode())
        self.assertEqual(row["sources"]["tool_results"]["bytes"], expected)
        self.assertEqual(row["largest_tool_result"]["bytes"], expected)
        self.assertNotIn("private", json.dumps(row))

    def test_cli_opt_in_default_unchanged_and_since(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "capture.jsonl"
            entry = exported()
            entry["timestamp"] = "2026-11-18"
            path.write_text(json.dumps(entry))
            command = [sys.executable, str(Path(report.__file__)), "--json", str(path)]
            default = json.loads(subprocess.check_output(command))
            self.assertNotIn("request_breakdown", default)
            enabled = json.loads(subprocess.check_output(command + ["--request-breakdown"]))
            self.assertEqual(len(enabled["request_breakdown"]["rows"]), 1)
            excluded = json.loads(subprocess.check_output(command + ["--request-breakdown", "--since", "2026-11-19"]))
            self.assertEqual(excluded["request_breakdown"]["rows"], [])

    def test_documented_capture_fixture(self):
        r = report.Report(request_breakdown=True)
        r.add_file(str(Path(__file__).with_name("testdata") / "request_breakdown.jsonl"))
        rows = r.summary()["request_breakdown"]["rows"]
        self.assertEqual([row["lifecycle"] for row in rows], ["fresh", "followup", "resume", "compaction"])
        self.assertIsNone(rows[0]["provider_usage"]["input_tokens"])
        self.assertEqual(rows[1]["provider_usage"]["cache_read_input_tokens"], 30)
        self.assertEqual(rows[2]["dominant_sources"], ["tool_results"])
        self.assertEqual(rows[2]["largest_tool_result"]["bytes"], 1300)
        self.assertEqual(rows[3]["sources"]["history"]["bytes"], 0)


if __name__ == "__main__":
    unittest.main()
