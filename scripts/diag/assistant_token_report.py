#!/usr/bin/env python3
"""Where the assistant's tokens go: model usage per round kind, from Claude Code and Codex transcripts.

    scripts/diag/assistant_token_report.py [--since 2026-11-01] [--json] [--request-breakdown] [path ...]

A round starts with a message typed into a conversation: the owner's, or one Persona sends (a supervision event, a
follow-up or scheduled run, a task notice, another conversation's message...). Every model call until the next such
message counts for that round. Paths are transcript files or directories searched for *.jsonl; by default the
assistant directory's projects under ~/.claude*/projects and $CLAUDE_CONFIG_DIR/projects, plus Codex rollouts under
$CODEX_HOME (default ~/.codex) whose recorded working directory is ~/.persona/assistant. Pass paths explicitly for a
different assistant directory. Codex's input is reported net of cache reads, like Persona's usage meter; cache creation
is unknown rather than zero. If an aggregate includes unknown values, it and its shares are unknown.

It also sizes each supervision event's text and the part of it that is the general handling manual (#5665).

Prints counts and sizes only, never message content, so the output can be pasted into an issue.
--request-breakdown adds source byte inventories and explicit local request exports; see
docs/assistant/assistant.md, "Local request source diagnostics" for boundaries and unknowns.
"""
import argparse
import collections
import datetime as dt
import glob
import importlib.util
import json
import os
from pathlib import Path
import re
import sys

USAGE_FIELDS = ("input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "output_tokens")
TIME_FRACTION = re.compile(r"^(.{10}[T ][0-9]{2}:[0-9]{2}:[0-9]{2})[.,]([0-9]+)")

# The leading <persona-context> blocks a message can start with (snapshot, message source, forwarded text).
LEAD_CONTEXT = re.compile(r"(?s)^\s*<persona-context\b([^>]*)>(.*?)</persona-context>\s*")
SOURCE_KIND = re.compile(r'\bkind="message-source"')
SOURCE_KINDS = {"conversation", "patrol", "followup", "supervision", "meeting", "wake", "ask_later", "task_notice", "inbox", "system"}
# Codex records runtime updates as role=user messages, but they are not new user inputs.
CODEX_CONTEXT = re.compile(r"(?s)^\s*<(environment_context|codex_internal_context|turn_aborted)(?:\s[^>]*)?>.*</\1>\s*$")

# First lines of the messages Persona types in (server/internal/prompts/injected.go), for transcripts written before
# messages carried their source. They match the Chinese prompts the hub sends, so they stay in Chinese.
FIRST_LINE_KINDS = [
    ("supervision", re.compile(r"^📋 督办：")),
    ("followup", re.compile(r"^<persona-limit-continue />$")),  # localized usage-limit continuation
    ("followup", re.compile(r"^⏰（Persona 叫你接着做）")),  # limit_continue: the usage limit reset
    ("wake", re.compile(r"^⏰（Persona 叫醒你")),
    ("followup", re.compile(r"^⏰ ")),
    ("ask_later", re.compile(r"^🔔 你 .+ 用 ask_user 问过用户")),
    ("task_notice", re.compile(r"^🔔 任务「")),
    ("inbox", re.compile(r"^📥 收件箱事件")),
    ("system", re.compile(r"^⚠️ 运行你的 node 刚重启过")),  # lost_tasks
    ("conversation", re.compile(r"^这条消息不是用户本人发的，是 Persona 的另一段对话")),
]
# A message relayed from another conversation (server/internal/prompts/assistant/relay.md) opens with a context block
# that names the sending conversation twice; prompts.ParseRelaySource reads it the same way.
RELAY_TITLE = re.compile(r'\btitle="来自对话「')
RELAY_BODY = re.compile(r"(?s)^这条消息不是用户本人发的，是 Persona 的另一段对话（([^）\s]+)）用 \S+ 发来的.*发回 (\S+?)；")
# Where the general handling manual starts in a supervision event (server/internal/prompts/assistant/supervise.md).
MANUAL_START = re.compile(r"(?m)^怎么处理")


def default_paths():
    # Claude Code's config directory is not always ~/.claude (CLAUDE_CONFIG_DIR; launchers keep their own).
    roots = set(glob.glob(os.path.expanduser("~/.claude*/projects")))
    if os.environ.get("CLAUDE_CONFIG_DIR"):
        roots.add(os.path.join(os.path.expanduser(os.environ["CLAUDE_CONFIG_DIR"]), "projects"))
    # Claude Code names a project directory after its path with "/" and "." as "-": ~/.persona/assistant ends in
    # "-persona-assistant".
    return sorted(d for r in roots for d in glob.glob(os.path.join(r, "*-persona-assistant")))


def default_codex_files():
    """Select the assistant's rollouts by their recorded working directory, never all Codex sessions."""
    home = os.path.expanduser(os.environ.get("CODEX_HOME", "~/.codex"))
    assistant = os.path.realpath(os.path.expanduser("~/.persona/assistant"))
    for directory in ("sessions", "archived_sessions"):
        for path in glob.iglob(os.path.join(home, directory, "**", "rollout-*.jsonl"), recursive=True):
            try:
                with open(path, errors="replace") as f:
                    first = json.loads(f.readline())
                meta = first.get("payload") or {}
                if first.get("type") == "session_meta" and meta.get("cwd") and os.path.realpath(meta["cwd"]) == assistant:
                    yield path
            except (OSError, ValueError, AttributeError):
                continue


def transcript_files(paths):
    seen = set()
    for p in paths:
        if os.path.isdir(p):
            candidates = sorted(glob.glob(os.path.join(p, "**", "*.jsonl"), recursive=True))
        elif os.path.isfile(p):
            candidates = (p,)
        else:
            continue
        for candidate in candidates:
            resolved = os.path.realpath(candidate)
            if resolved not in seen:
                seen.add(resolved)
                yield candidate


def message_text(entry):
    """The typed text of a user line, or None when the line is a tool result, meta or a compaction summary."""
    if entry.get("type") != "user" or entry.get("isMeta") or entry.get("isCompactSummary"):
        return None
    content = (entry.get("message") or {}).get("content")
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        if any(isinstance(b, dict) and b.get("type") == "tool_result" for b in content):
            return None
        texts = [b.get("text", "") for b in content if isinstance(b, dict) and b.get("type") == "text"]
        return "\n".join(texts) if texts else None
    return None


def classify(text):
    """The round kind a typed message starts, and the text after its leading context blocks."""
    rest = text
    while True:
        m = LEAD_CONTEXT.match(rest)
        if not m:
            break
        if SOURCE_KIND.search(m.group(1)):
            try:
                meta = json.loads(m.group(2).strip())
            except ValueError:
                meta = {}
            if not isinstance(meta, dict):
                meta = {}
            rest = rest[m.end():]
            if meta.get("author") == "user" or not meta.get("kind"):
                return "owner", rest
            kind = meta["kind"]
            return kind if isinstance(kind, str) and kind in SOURCE_KINDS else "other", rest
        if 'kind="context"' in m.group(1) and RELAY_TITLE.search(m.group(1)):
            body = RELAY_BODY.match(m.group(2).strip())
            if body and body.group(1) == body.group(2):
                return "conversation", rest[m.end():]
        rest = rest[m.end():]
    first = rest.lstrip().split("\n", 1)[0]
    for kind, pattern in FIRST_LINE_KINDS:
        if pattern.search(first):
            return kind, rest
    if rest.lstrip().startswith("<command-name>") or rest.lstrip().startswith("<local-command"):
        return "command", rest
    return "owner", rest


def timestamp_key(value):
    if not isinstance(value, str):
        return None
    # Parse whole seconds with their offset. Keep the decimal fraction separately so Python 3.9 can
    # parse arbitrary widths and a cutoff can still distinguish timestamps within a microsecond.
    match = TIME_FRACTION.match(value)
    fraction = ""
    if match:
        fraction = match[2].rstrip("0")
        value = match[1] + value[match.end():]
    try:
        instant = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None
    if instant.tzinfo is None:
        instant = instant.replace(tzinfo=dt.timezone.utc)
    return instant, fraction


class Report:
    def __init__(self, since=None, request_breakdown=False):
        self.since = timestamp_key(since)
        if since is not None and self.since is None:
            raise ValueError("--since must be an ISO date or timestamp")
        self.kinds = collections.defaultdict(lambda: collections.Counter())
        self.conversations = collections.defaultdict(lambda: collections.Counter())
        self.events = []  # (total bytes, manual bytes) per supervision event
        self.sidechain = collections.Counter()
        self.files = 0
        self.unknown = collections.defaultdict(set)
        self.breakdown = None
        if request_breakdown:
            # The report can also be loaded directly by file path, without this directory on sys.path.
            spec = importlib.util.spec_from_file_location("request_breakdown", Path(__file__).with_name("request_breakdown.py"))
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)
            self.breakdown = module.Breakdown()

    def includes_timestamp(self, value):
        if self.since is None:
            return True
        timestamp = timestamp_key(value)
        return timestamp is not None and timestamp >= self.since

    def add_round(self, kind, text, rest, counted):
        if not counted:
            return
        self.kinds[kind]["rounds"] += 1
        self.kinds[kind]["typed_bytes"] += len(text.encode())
        if kind == "supervision":
            body = rest.encode()
            m = MANUAL_START.search(rest)
            manual = len(rest[m.start():].encode()) if m else 0
            self.events.append((len(text.encode()), len(body), manual))

    def add_file(self, path):
        if self.breakdown is not None:
            self.breakdown.add_file(path, self.includes_timestamp)
        with open(path, errors="replace") as f:
            try:
                first = json.loads(f.readline())
            except ValueError:
                first = {}
        if isinstance(first, dict) and first.get("type") == "session_meta":
            return self.add_codex_file(path)
        self.files += 1
        kind = "before_first_message"
        conv = os.path.splitext(os.path.basename(path))[0]
        # message id -> usage counted so far and whether the response began in the window; Claude Code writes one
        # line per content block. Lines before --since still keep this state and the round kind, but count nothing.
        seen = {}
        with open(path, errors="replace") as f:
            for line_number, line in enumerate(f):
                try:
                    e = json.loads(line)
                except ValueError:
                    continue
                if not isinstance(e, dict):
                    continue
                counted = self.includes_timestamp(e.get("timestamp"))
                if e.get("type") == "user" and e.get("isCompactSummary"):
                    kind = "after_compaction"
                text = message_text(e)
                if text is not None and not e.get("isSidechain"):
                    kind, rest = classify(text)
                    self.add_round(kind, text, rest, counted)
                    continue
                if e.get("type") != "assistant":
                    continue
                msg = e.get("message") or {}
                usage = msg.get("usage") or {}
                if not usage:
                    continue
                # A line without a message id is a response of its own: gateways can leave the id empty, and one
                # requestId can then cover several responses (transcript.RequestUsage counts them the same way).
                key = msg.get("id") or ("line", e.get("uuid") or line_number)
                prev = seen.get(key)
                target = self.sidechain if e.get("isSidechain") else self.kinds[kind]
                if prev is None:
                    seen[key] = prev = {k: usage.get(k) or 0 for k in USAGE_FIELDS}
                    prev["counted"] = counted
                    if not counted:
                        continue
                    target["model_calls"] += 1
                    for k in USAGE_FIELDS:
                        target[k] += usage.get(k) or 0
                        if not e.get("isSidechain"):
                            self.conversations[conv][k] += usage.get(k) or 0
                    continue
                # A later line of the same response: count only what grew (output tokens stream in), and only for a
                # response that began in the window.
                for k in USAGE_FIELDS:
                    grown = (usage.get(k) or 0) - prev[k]
                    if grown > 0:
                        prev[k] += grown
                        if not prev["counted"]:
                            continue
                        target[k] += grown
                        if not e.get("isSidechain"):
                            self.conversations[conv][k] += grown

    def add_codex_file(self, path):
        self.files += 1
        conv = os.path.basename(path)
        kind = "before_first_message"
        total = 0
        pending = None
        seen_ids = set()
        last_event = None

        def record(text, counted):
            nonlocal kind
            kind, rest = classify(text)
            self.add_round(kind, text, rest, counted)

        def flush():
            nonlocal pending
            if pending is not None:
                record(pending[0], pending[1])
                pending = None

        def matches_pending(text):
            # A response carrying images may also contain image wrapper text absent from UserMessage. Compare the
            # complete textual item to an entire raw text part, never to an arbitrary prefix.
            return pending is not None and (pending[0] == text or text in pending[2])

        with open(path, errors="replace") as f:
            for line in f:
                try:
                    e = json.loads(line)
                except ValueError:
                    continue
                if not isinstance(e, dict) or not isinstance(e.get("payload"), dict):
                    continue
                p = e["payload"]
                counted = self.includes_timestamp(e.get("timestamp"))
                if e.get("type") == "response_item" and p.get("type") == "message" and p.get("role") == "user":
                    last_event = None
                    parts = p.get("content") or []
                    texts = [x.get("text", "") for x in parts if isinstance(x, dict) and x.get("type") == "input_text"]
                    text = "\n".join(texts)
                    if text and not CODEX_CONTEXT.match(text):
                        flush()
                        has_image = any(isinstance(x, dict) and x.get("type") in ("input_image", "image") for x in parts)
                        pending = (text, counted, texts if has_image else [])
                elif e.get("type") == "event_msg" and p.get("type") == "item_completed":
                    item = p.get("item") or {}
                    if item.get("type") != "UserMessage" or (item.get("id") and item["id"] in seen_ids):
                        continue
                    if item.get("id"):
                        seen_ids.add(item["id"])
                    text = "\n".join(x.get("text", "") for x in item.get("content", []) if isinstance(x, dict) and x.get("type") == "text")
                    if text and not CODEX_CONTEXT.match(text):
                        if matches_pending(text):
                            counted = pending[1]
                            pending = None
                        else:
                            flush()
                        if last_event != ("event", text):
                            record(text, counted)
                        last_event = ("item", text)
                elif e.get("type") == "event_msg" and p.get("type") == "user_message" and isinstance(p.get("message"), str):
                    text = p["message"]
                    if CODEX_CONTEXT.match(text):
                        continue
                    if matches_pending(text):
                        counted = pending[1]
                        pending = None
                    else:
                        flush()
                    if last_event != ("item", text):
                        record(text, counted)
                    last_event = ("event", text)
                elif e.get("type") == "event_msg" and p.get("type") == "token_count":
                    flush()
                    last_event = None
                    info = p.get("info") or {}
                    current = (info.get("total_token_usage") or {}).get("total_tokens")
                    last = info.get("last_token_usage") or {}
                    if not isinstance(current, int) or current <= total:
                        continue
                    total = current
                    if not counted:
                        continue
                    target = self.kinds[kind]
                    target["model_calls"] += 1
                    self.conversations[conv]["model_calls"] += 1
                    self.unknown[kind].add("cache_creation_input_tokens")
                    inp, cached, out = last.get("input_tokens"), last.get("cached_input_tokens"), last.get("output_tokens")
                    values = {"input_tokens": max(0, inp - cached) if isinstance(inp, int) and isinstance(cached, int) else None,
                              "cache_read_input_tokens": cached if isinstance(cached, int) else None,
                              "output_tokens": out if isinstance(out, int) else None}
                    for key, value in values.items():
                        if value is None:
                            self.unknown[kind].add(key)
                        else:
                            target[key] += value
                            self.conversations[conv][key] += value
                elif e.get("type") == "event_msg" and p.get("type") == "task_started":
                    last_event = None
                    seen_ids.clear()
            flush()

    def summary(self):
        total = collections.Counter()
        for c in self.kinds.values():
            total.update({k: c[k] for k in USAGE_FIELDS + ("model_calls",)})
        kinds = {}
        for kind, c in sorted(self.kinds.items(), key=lambda kv: -sum(kv[1][k] for k in USAGE_FIELDS)):
            row = {k: c[k] for k in ("rounds", "typed_bytes", "model_calls") + USAGE_FIELDS}
            for k in USAGE_FIELDS:
                row[k + "_share"] = round(c[k] / total[k], 3) if total[k] else 0.0
            for key in self.unknown[kind]:
                row[key] = None
            kinds[kind] = row
        events = sorted(self.events)
        ev = {"count": len(events)}
        if events:
            body = sum(b for _, b, _ in events)
            manual = sum(m for _, _, m in events)
            ev.update({"median_bytes": events[len(events) // 2][0], "max_bytes": events[-1][0],
                       "body_bytes": body, "manual_bytes": manual,
                       "manual_share_of_body": round(manual / body, 3) if body else 0.0,
                       "with_manual": sum(1 for _, _, m in events if m)})
        for key in set().union(*self.unknown.values()):
            total[key] = None
            # No category's share of an unknown total can be calculated.
            for row in kinds.values():
                row[key + "_share"] = None
        result = {"files": self.files, "conversations": len(self.conversations), "total": dict(total),
                  "by_kind": kinds, "supervision_events": ev, "sidechain": dict(self.sidechain)}
        if self.breakdown is not None:
            result["request_breakdown"] = self.breakdown.summary()
        return result


def print_text(s):
    def number(value, width):
        return f"{value if value is not None else '?':>{width}}"

    def share(value):
        return "?" if value is None else f"{value:.0%}"

    print(f"{s['files']} transcript files, {s['conversations']} conversations with model calls")
    head = f"{'round kind':22s} {'rounds':>7s} {'calls':>7s} {'input':>12s} {'cache write':>12s} {'cache read':>14s} {'output':>10s}  share(write/read/out)"
    print(head)
    for kind, r in s["by_kind"].items():
        print(f"{kind:22s} {r['rounds']:7d} {r['model_calls']:7d} {number(r['input_tokens'], 12)} "
              f"{number(r['cache_creation_input_tokens'], 12)} {number(r['cache_read_input_tokens'], 14)} {number(r['output_tokens'], 10)}  "
              f"{share(r['cache_creation_input_tokens_share'])}/{share(r['cache_read_input_tokens_share'])}/{share(r['output_tokens_share'])}")
    t = s["total"]
    print(f"{'total':22s} {'':7s} {t.get('model_calls', 0):7d} {number(t.get('input_tokens', 0), 12)} "
          f"{number(t.get('cache_creation_input_tokens', 0), 12)} {number(t.get('cache_read_input_tokens', 0), 14)} {number(t.get('output_tokens', 0), 10)}")
    if s["sidechain"]:
        sc = s["sidechain"]
        print(f"subagents (sidechain): {sc.get('model_calls', 0)} calls, cache write {sc.get('cache_creation_input_tokens', 0)}, "
              f"cache read {sc.get('cache_read_input_tokens', 0)}, output {sc.get('output_tokens', 0)}")
    ev = s["supervision_events"]
    if ev["count"]:
        print(f"supervision events: {ev['count']}, median {ev['median_bytes']} B, largest {ev['max_bytes']} B; "
              f"{ev['with_manual']} carry the handling manual, {ev['manual_share_of_body']:.0%} of their text after the context blocks")
    if "request_breakdown" in s:
        print("Request source sizes (UTF-8 content / canonical JSON bytes; ? = unavailable):")
        for index, row in enumerate(s["request_breakdown"]["rows"], 1):
            print(f"  {index}: {row['boundary']} / {row['lifecycle']}")
            print("    " + ", ".join(f"{key}={value['bytes'] if value['bytes'] is not None else '?'}"
                                       for key, value in row["sources"].items()))
            print(f"    observed content={row['observed_content_bytes']}; largest tool result={row['largest_tool_result']}")
            if "provider_usage" in row:
                print("    observed provider usage: " + json.dumps(row["provider_usage"], sort_keys=True))
        print("  Limits: " + "; ".join(s["request_breakdown"]["limits"]))


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("paths", nargs="*", help="transcript files or directories (default: the assistant directory's projects)")
    ap.add_argument("--since", help="only lines at or after this ISO date or timestamp (UTC if no offset is given)")
    ap.add_argument("--json", action="store_true", help="print JSON")
    ap.add_argument("--request-breakdown", action="store_true",
                    help="include local source byte inventories and explicit request exports; unknowns are not zero")
    args = ap.parse_args(argv)
    try:
        r = Report(args.since, request_breakdown=args.request_breakdown)
    except ValueError as error:
        ap.error(str(error))
    paths = args.paths or [*default_paths(), *default_codex_files()]
    if not paths:
        print("no assistant transcripts found; pass a directory", file=sys.stderr)
        return 1
    for path in transcript_files(paths):
        r.add_file(path)
    s = r.summary()
    if args.json:
        json.dump(s, sys.stdout, indent=2)
        print()
    else:
        print_text(s)
    return 0


if __name__ == "__main__":
    sys.exit(main())
