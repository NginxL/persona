"""Side sessions with replies nobody has seen, for the sidebar's Unread row (#13496).

MOCK_SIDE_UNREAD=1, with MOCK_SIDE_SESSIONS=1: the /btw question was answered and both it and the side chat finished a
reply nobody has seen, while the review itself was seen. Its row is Unread for its sides alone, and the sidebar names the
newer one ("+1" for the other) and opens it in one click. Without MOCK_SIDE_SESSIONS the switch does nothing.
"""
import os

HELP = ("MOCK_SIDE_UNREAD=1  With MOCK_SIDE_SESSIONS=1: the side chat and the answered /btw question each have an unread "
        "reply, the review itself seen (#13496)")


def register(d):
    d.help(HELP)
    hub = d.hub
    if os.environ.get("MOCK_SIDE_UNREAD") != "1" or not hub.MOCK_SIDE_SESSIONS:
        return
    for s in hub.SESSIONS:
        if s["session_id"] in (hub.SIDE_BTW, hub.SIDE_CHAT):
            s.update(status="idle", state="unread", pending_interactions=0, seen_at=hub.ago(minutes=40))
    hub.INTERACTIONS[:] = [ix for ix in hub.INTERACTIONS if ix.get("id") != "ix_side_btw"]  # the answered card
