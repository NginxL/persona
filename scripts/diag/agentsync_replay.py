#!/usr/bin/env python3
"""How often the app pulls chats in the background (AgentSync), over a recording of a real hub's pushes
(server/scripts/pushrec, docs/operations/testing.md "Push replay"): before the working-agent gap (a pull per transcript change, merged
over the 300 ms plan) and after (a working agent's chat at most every AgentSync.workingGap, at once when not working).
Prints counts only; the recording stays on the machine it was made on.

    python3 scripts/diag/agentsync_replay.py ~/push.jsonl
"""
import json
import sys

GAP, PLAN = 60.0, 0.3


def main(path):
    before = after = changes = working_changes = 0
    first = last = None
    baseline, pulled, held = {}, {}, {}
    sessions = set()
    for t, k, working, changed in replay_events(path):
        if changed:
            if first is None:
                first = t
            last = t
            changes += 1
            working_changes += working
            sessions.add(k)
            if t - baseline.get(k, -1e9) > PLAN:
                before, baseline[k] = before + 1, t
        for hk, at in list(held.items()):  # held chats whose turn came
            if at <= t:
                after, pulled[hk] = after + 1, at
                del held[hk]
        if k in held:
            if not working:
                after, pulled[k] = after + 1, t
                del held[k]
            continue
        if not changed:
            continue
        lp = pulled.get(k)
        if working and lp is not None and t - lp < GAP:
            held[k] = lp + GAP
        elif t - pulled.get(k, -1e9) > PLAN:
            after, pulled[k] = after + 1, t
    after += len(held)
    if not changes:
        sys.exit("no transcript changes in the recording")
    mins = (last - first) / 60
    if mins > 0:
        pulls = "pulls %.1f/min before, %.1f/min after" % (before / mins, after / mins)
    else:
        pulls = "pulls %d before, %d after (per-minute rates unavailable)" % (before, after)
    print("%d transcript changes over %.1f min in %d sessions (%d while working): %s"
          % (changes, mins, len(sessions), working_changes, pulls))


def replay_events(path):
    """Yield transcript changes and transitions that could release a held pull."""
    rev, working = {}, {}
    with open(path) as recording:
        for line in recording:
            r = json.loads(line)
            t, m = r["t"] / 1000, r["msg"]
            if m.get("type") == "sessions":
                for s in m.get("sessions") or []:
                    k = (m["machine_id"], s["session_id"])
                    # AgentSync plans with state; older recordings only carry status.
                    active = s.get("state", s.get("status")) == "working"
                    stopped = working.get(k, False) and not active
                    working[k] = active
                    current = s.get("rev")
                    changed = current is not None and k in rev and current != rev[k]
                    if current is not None:
                        rev[k] = current
                    if changed or stopped:
                        yield t, k, active, changed
            elif m.get("type") == "revs":
                for sid, current in (m.get("revs") or {}).items():
                    k = (m["machine_id"], sid)
                    if k in rev and rev[k] != current:
                        yield t, k, working.get(k, False), True
                    rev[k] = current


if __name__ == "__main__":
    main(sys.argv[1])
