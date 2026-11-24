"""A meeting's final pass in progress, for shots of the minutes line (#13461, #13537).

MOCK_MEETING_FINISHING=<done>,<end> (recording seconds) shows every complete meeting recording with no transcription of
its own as a hub-mode meeting whose final pass has done <done> of <end> (docs/assistant/meeting-minutes.md "Gaps and the
final pass"); its minutes stay as MOCK_RECORDING_MINUTES says. A recording with a transcription of its own (the mock's
scripted uplink) answers as without the switch.
"""
import os

HELP = "MOCK_MEETING_FINISHING=<done>,<end>  complete meeting recordings show a final pass at <done> of <end> seconds"
HUB = None  # mock_hub.py's globals (mock_domains.Hub), from register


def finishing(done_end):
    """transcription_view, but a complete meeting recording without a transcription of its own is finishing."""
    done, end = done_end
    plain = HUB.transcription_view

    def view(rid):
        out = plain(rid)
        if out is not None or rid in HUB.TRANSCRIPTIONS or not HUB.meeting_transcription_on():
            return out
        rec = HUB.RECORDINGS.get(rid)
        if isinstance(rec, dict) and rec.get("kind") == "meeting" and rec.get("complete"):
            return {"mode": "hub", "state": "finishing", "gaps": [], "final_done": min(done, end), "end": end}
        return out

    return view


def register(d):
    global HUB
    HUB = d.hub
    d.help(HELP)
    raw = os.environ.get("MOCK_MEETING_FINISHING")
    if not raw:
        return
    done_end = tuple(float(x) for x in raw.split(","))
    d.hub.transcription_view = finishing(done_end)
