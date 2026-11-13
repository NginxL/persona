"""Recorded model streams replayed as the node would send them (#6651).

A trace (`stream_traces/<name>.json`) is a real reply and the time each `text_delta` of it arrived from
`claude -p --output-format stream-json --include-partial-messages`. `deltas` turns it into the node's `delta` sends:
`lead` is the node before #6651 (a send only when a token comes more than 80 ms after the last send), `trail` the node
after it (the same, plus a send 80 ms after the last one when newer text is waiting). `/mock/say?trace=<name>&node=…`
replays them to the app, so the app's pacing is measured on the cadence a phone really gets.
"""
import json
import os

EVERY_MS = 80  # server/internal/node/assistant.go: deltaEvery
DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "stream_traces")


def load(name):
    with open(os.path.join(DIR, os.path.basename(name) + ".json"), encoding="utf-8") as f:
        return json.load(f)


def deltas(tokens, node="trail"):
    """[(ms, characters so far)] the node sends for tokens [[ms, characters], …]; the last is the whole text (message_stop)."""
    out, last, total, timer = [], -10**9, 0, None
    for t, n in tokens:
        if timer is not None and timer <= t:  # the trailing send fired before this token came
            if out[-1][1] != total:
                out.append((timer, total))
            last, timer = timer, None
        total += n
        if t - last > EVERY_MS:
            out.append((t, total))
            last, timer = t, None
        elif node == "trail" and timer is None:
            timer = last + EVERY_MS
    if not out or out[-1][1] != total:  # message_stop sends the whole text at once
        out.append((tokens[-1][0] if tokens else 0, total))
    return out
