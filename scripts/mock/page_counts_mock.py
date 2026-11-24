"""Page reads whose counts come from the same filtered session collection (#4858)."""
import os

HELP = "MOCK_PAGE_COUNTS=1  include a paged session read's counts in its answer (#4858)"
HUB = None  # mock_hub.py's globals (mock_domains.Hub), from register


def page_counts(query):
    page_query = {key: value for key, value in query.items() if key != "with_counts"}
    page, status = HUB.paged_sessions(page_query)
    if status != 200:
        return page, status
    counts_query = {key: value for key, value in page_query.items()
                    if key not in ("since", "cursor", "limit", "summary")}
    counts, _ = HUB.paged_sessions({**counts_query, "counts": ["1"]})
    return {**page, "counts": counts}, 200


def page(h, req):
    if req.query.get("with_counts", [""])[0] != "1":
        return False
    return h.send_json(*page_counts(req.query))


def register(d):
    global HUB
    HUB = d.hub
    d.help(HELP)
    if os.environ.get("MOCK_PAGE_COUNTS") != "1":
        return
    d.feature("sessions_page_counts")
    d.route("GET", "/api/v1/sessions", page)
