"""An Agent whose background work stalled, still Working, for the stall hint (#13744, #13842).

MOCK_BG_STALLED=1: the blog's agent has a test run in the background that used no CPU and wrote nothing for hours; its
turn is over and it stays Working, with background_stalled_since set, so the Agents row and the chat header show the
stall.
"""
import os

HELP = "MOCK_BG_STALLED=1  The blog's background test run stalled hours ago; the Agent stays Working with the stall hint (#13744)"


def register(d):
    d.help(HELP)
    if os.environ.get("MOCK_BG_STALLED") != "1":
        return
    hub = d.hub
    for s in hub.SESSIONS:
        if s["session_id"] == hub.BLOG:
            s.update(status="done", state="working", state_since=hub.ago(hours=4), seen_at=None, background_working=True,
                     background_waiting=False, background_stalled_since=hub.ago(hours=3, minutes=5),
                     last_text="迁移完成，测试在后台跑着。",
                     background=[{"id": "bq7ci42wa", "kind": "shell", "description": "跑全部测试", "command": "pnpm test",
                                  "started_at": hub.ago(hours=4)}])
