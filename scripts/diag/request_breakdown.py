"""Aggregate-only source sizes at explicit local request or transcript boundaries.

No provider tokenizer, network access, or runtime logging. Raw data never enters a result row.
"""
import collections
import hashlib
import json
import re


SOURCES = ("instructions", "tool_schemas", "snapshot_or_events", "history", "current_message",
           "tool_results", "attachments", "other_content")
CONTEXT = re.compile(r"<persona-context\b[^>]*>.*?</persona-context>", re.S)
ATTACHMENTS = {"image", "input_image", "image_url", "document", "input_file", "file", "local_image"}
TOOL_OUTPUTS = {"tool_result", "function_call_output", "custom_tool_call_output"}


def size(value):
    """UTF-8 text bytes; structured values use deterministic compact JSON, not wire bytes."""
    text = value if isinstance(value, str) else json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    return len(text.encode("utf-8"))


def counter(value):
    return value if type(value) is int and value >= 0 else None


class Inventory:
    def __init__(self):
        self.bytes = collections.Counter()
        self.counts = collections.Counter()
        self.largest = None

    def add(self, category, value):
        n = size(value)
        self.bytes[category] += n
        self.counts[category] += 1
        if category == "tool_results" and (self.largest is None or n > self.largest["bytes"]):
            # A stable source category, never the runtime's arbitrary tool name or call id.
            self.largest = {"category": "tool_results", "bytes": n}

    def content(self, value, category):
        if isinstance(value, str):
            if category in ("current_message", "history"):
                matches = list(CONTEXT.finditer(value))
                for match in matches:
                    self.add("snapshot_or_events", match.group())
                rest = CONTEXT.sub("", value)
                if rest:
                    self.add(category, rest)
            else:
                self.add(category, value)
        elif isinstance(value, list):
            for block in value:
                self.content(block, category)
        elif isinstance(value, dict):
            kind = value.get("type")
            kind = kind if isinstance(kind, str) else None
            if kind in ATTACHMENTS:
                self.add("attachments", value)
            elif kind in TOOL_OUTPUTS:
                self.tool_result(value.get("content", value.get("output", "")))
            elif kind in ("text", "input_text", "output_text") and isinstance(value.get("text"), str):
                self.content(value["text"], category)
            else:
                self.add("tool_results" if category == "tool_results" else "other_content", value)
        elif value is not None:
            self.add("tool_results" if category == "tool_results" else "other_content", value)

    def tool_result(self, value):
        # A result may contain text plus an image. Keep each byte in only one source bucket.
        result = Inventory()
        result.content(value, "tool_results")
        n = result.bytes["tool_results"]
        self.bytes.update(result.bytes)
        self.counts.update(result.counts)
        if self.largest is None or n > self.largest["bytes"]:
            self.largest = {"category": "tool_results", "bytes": n}

    def row(self, known):
        sources = {key: {"bytes": self.bytes[key] if key in known else None,
                         "items": self.counts[key] if key in known else None} for key in SOURCES}
        maximum = max(self.bytes.values(), default=0)
        return {"sources": sources, "observed_content_bytes": sum(self.bytes.values()),
                "dominant_sources": [k for k in SOURCES if self.bytes[k] == maximum and maximum],
                "largest_tool_result": self.largest}


def usage_row(usage, runtime):
    usage = usage if isinstance(usage, dict) else {}
    cached = usage.get("input_tokens_details") or {}
    cached = cached if isinstance(cached, dict) else {}
    return {"input_tokens": counter(usage.get("input_tokens")),
            "cache_read_input_tokens": counter(usage.get("cache_read_input_tokens") if runtime == "anthropic"
                                                else cached.get("cached_tokens")),
            "cache_creation_input_tokens": counter(usage.get("cache_creation_input_tokens")),
            "output_tokens": counter(usage.get("output_tokens"))}


def request_row(entry):
    """Explicit local exports: an Anthropic Messages or OpenAI Responses request object."""
    runtime, request = entry.get("runtime"), entry.get("request")
    if runtime not in ("anthropic", "responses") or not isinstance(request, dict):
        return None
    inv = Inventory()
    instructions = request.get("system" if runtime == "anthropic" else "instructions")
    if instructions is not None:
        inv.content(instructions, "instructions")
    if "tools" in request:
        inv.add("tool_schemas", request["tools"])
    messages = request.get("messages" if runtime == "anthropic" else "input", [])
    if isinstance(messages, str):
        inv.content(messages, "current_message")
    elif isinstance(messages, list):
        last_user = max((i for i, m in enumerate(messages) if isinstance(m, dict) and m.get("role") == "user"), default=-1)
        for i, msg in enumerate(messages):
            if not isinstance(msg, dict):
                inv.add("other_content", msg)
            elif msg.get("type") in TOOL_OUTPUTS:
                inv.tool_result(msg.get("output", msg.get("content", "")))
            elif msg.get("role") in ("system", "developer"):
                inv.content(msg.get("content", ""), "instructions")
            elif "content" in msg:
                inv.content(msg["content"], "current_message" if i == last_user else "history")
            else:
                inv.add("other_content", msg)
    row = inv.row(set(SOURCES))
    lifecycle = entry.get("lifecycle")
    row.update({"boundary": runtime + "_request_content", "lifecycle": lifecycle if lifecycle in
                ("fresh", "followup", "resume", "compaction") else "unknown",
                "provider_usage": usage_row(entry.get("usage"), runtime),
                "request_wire_bytes": None, "provider_context_tokens": None})
    return row


class Breakdown:
    def __init__(self, since=None):
        self.since = since
        self.rows = []

    def add_file(self, path):
        inv = Inventory()
        runtime = "claude"
        observed = False
        compacted = False
        seen = set()
        instructions_seen = False
        with open(path, errors="replace") as stream:
            for line in stream:
                try:
                    entry = json.loads(line)
                except ValueError:
                    continue
                if not isinstance(entry, dict):
                    continue
                kind = entry.get("type")
                payload = entry.get("payload")
                payload = payload if isinstance(payload, dict) else {}
                if kind == "session_meta":
                    runtime = "codex"
                    # Metadata may predate --since; it is context, not a new message.
                    value = payload.get("base_instructions")
                    if isinstance(value, dict):
                        value = value.get("text")
                    if isinstance(value, str):
                        inv.add("instructions", value)
                        instructions_seen = True
                    continue
                if self.since and str(entry.get("timestamp", "")) < self.since:
                    continue
                if kind == "request":
                    row = request_row(entry)
                    if row is not None:
                        self.rows.append(row)
                    continue
                if entry.get("isSidechain"):
                    continue
                if kind == "compacted" or (kind == "system" and entry.get("subtype") == "compact_boundary"):
                    compacted = True
                if runtime == "codex":
                    # event_msg mirrors response_item; measuring both doubles content. Never reconstruct retention.
                    if kind != "response_item":
                        continue
                    if payload.get("type") in TOOL_OUTPUTS:
                        inv.tool_result(payload.get("output", ""))
                    elif payload.get("type") == "message":
                        inv.content(payload.get("content", ""), "instructions" if payload.get("role") in
                                    ("system", "developer") else "history")
                    else:
                        inv.add("other_content", payload)
                    observed = True
                elif kind in ("user", "assistant"):
                    msg = entry.get("message")
                    if not isinstance(msg, dict):
                        continue
                    # Claude's assistant response can be written once per content block / streaming update.
                    # Keep unique content blocks, without returning their ids or text.
                    content = msg.get("content", "")
                    blocks = content if isinstance(content, list) else [content]
                    identity = (msg.get("id") or entry.get("uuid")) if kind == "assistant" else None
                    for block in blocks:
                        if isinstance(identity, str) and identity:
                            digest = hashlib.sha256(json.dumps(block, sort_keys=True, ensure_ascii=False).encode()).digest()
                            key = (identity, digest)
                            if key in seen:
                                continue
                            seen.add(key)
                        inv.content(block, "history")
                    observed = True
        if observed:
            known = set(SOURCES) - {"instructions", "tool_schemas", "current_message"}
            # Recorded instructions are a partial inventory, not the runtime's complete request instructions.
            row = inv.row(known)
            row.update({"boundary": runtime + "_transcript_inventory", "lifecycle": "contains_compaction" if compacted else "unknown",
                        "observed_instruction_bytes": inv.bytes["instructions"] if instructions_seen or inv.counts["instructions"] else None,
                        "retained_request_history_bytes": None, "request_wire_bytes": None, "provider_context_tokens": None})
            self.rows.append(row)

    def summary(self):
        return {"measurement": "utf8_content_and_canonical_json_bytes", "rows": self.rows,
                "limits": ["not_provider_tokens_or_wire_size", "transcript_inventory_is_not_retained_request_context",
                           "runtime_owned_instructions_and_schemas_may_be_unavailable",
                           "attachment_bytes_are_serialized_references_or_data_not_decoded_media"]}
