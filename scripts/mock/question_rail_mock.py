"""The question rail's states in the mock hub (#13021, docs/web/web.md "User-input rail (#1739, #2060)"), a domain of
scripts/mock/mock_domains.py: with MOCK_QUESTION_RAIL=1 the main conversation holds six fictional questions in English
(two read, three answered and unread, two of them follow-ups drawn shorter, one at work) plus one queued behind it, and
GET / POST /api/v1/sessions/{mid}/{sid}/question-reads keep the marks (server/internal/assistant/question_reads.go).
"""
import os

MODE = os.environ.get("MOCK_QUESTION_RAIL", "")
HUB = None  # mock_hub.py's globals (mock_domains.Hub), from register
READS = {}  # (machine, session) -> {"since": time, "ids": [entry id]}


def _answer(title, points):
    return "**%s**\n\n" % title + "\n\n".join("%d. %s" % (i + 1, x) for i, x in enumerate(points))


QUESTIONS = [
    (180, "Plan the Garden app's Q4 launch", _answer("A launch plan with three milestones", [
        "Private beta in the second week of October, with the twenty testers from the waiting list.",
        "Public beta at the end of October, once the sync issues from the private beta are fixed.",
        "Release in the middle of November, with the store page, the help pages and the launch post ready a week before.",
        "Each milestone has an owner and a checklist in the project notes; the riskiest part is the photo import."])),
    (150, "> a launch plan with three milestones\nCan the private beta move a week earlier?", _answer("Yes, with one change", [
        "Moving the private beta to the first week of October leaves five days for the sign-up form.",
        "The onboarding screens are ready; the photo import would ship behind a switch for the first testers.",
        "The public beta and the release keep their dates, so nothing else moves."])),
    (120, "Draft the Docs site outline", _answer("An outline with five sections", [
        "Getting started: install, first garden, first reminder.",
        "Guides: watering plans, seasons, sharing a garden with family.",
        "Troubleshooting: sync, notifications, photos that do not import.",
        "Release notes, newest first.",
        "Contact and feedback."])),
    (90, "Summarize the beta feedback from this week", _answer("Twelve testers answered", [
        "Most liked the watering reminders and asked for a weekly summary.",
        "Three found the photo import slow on large libraries.",
        "Two asked for a dark app icon.",
        "Nobody reported a crash this week."])),
    (60, "> an outline with five sections\nAdd a section for the API reference", _answer("Added as section six", [
        "API reference: authentication, gardens, plants and reminders, one page each.",
        "Each page starts with a short example and ends with the error codes.",
        "The navigation keeps it last, after Contact and feedback, until the API is public."])),
]


def build():
    """The main conversation's questions, its state at work with one message queued, and the marks: questions one and
    four were opened, and marks began three and a half hours ago."""
    hub, main = HUB, HUB.ASK1
    for s in hub.ASSISTANT:
        if s["session_id"] == main:
            s.update(status="working", state="working", state_since=hub.ago(minutes=1), seen_at=None,
                     queued=["Then send the summary to the Docs site team"])
    items = []
    for ago_min, q, a in QUESTIONS:
        items += [hub.it("user", "text", ago_min, text=q), hub.it("assistant", "text", ago_min - 3, text=a)]
    items.append(hub.it("user", "text", 2, text="Compare the two pricing page drafts"))
    hub.ITEMS[main] = items
    users = [x["id"].split("#")[0] for x in items if x["role"] == "user"]
    READS.clear()
    READS[("server", main)] = {"since": hub.ago(minutes=210), "ids": [users[0], users[3]]}


def _book(req):
    return READS.setdefault((req.match["mid"], req.match["sid"]), {"since": HUB.ago(minutes=0), "ids": []})


def get_reads(h, req):
    r = _book(req)
    return h.send_json({"since": r["since"], "ids": list(reversed(r["ids"]))})


def mark_reads(h, req):
    ids = (req.json() or {}).get("ids") or []
    if not isinstance(ids, list) or len(ids) > 200 or not all(isinstance(x, str) and 0 < len(x) <= 256 for x in ids):
        return h.send_json({"error": "invalid ids"}, 400)
    r = _book(req)
    r["ids"] += [x for x in dict.fromkeys(ids) if x not in r["ids"]]
    return h.send_json({"ok": True})


def register(d):
    global HUB
    HUB = d.hub
    d.help("MOCK_QUESTION_RAIL=1  the main chat's question rail states: six fictional questions, one at work, one "
           "queued, read marks kept (#13021)")
    if not MODE:
        return
    build()
    path = r"/api/v1/sessions/(?P<mid>[^/]+)/(?P<sid>[^/]+)/question-reads"
    d.route("GET", path, get_reads)
    d.route("POST", path, mark_reads)
    d.on_reset(build)
