#!/usr/bin/env python3
"""Verify the actual Assistant trays using only synthetic HTTP/WS data.

Build web first. Run with a loopback production preview URL and --shots <directory>; --text-size large uses the largest Web text-size setting.
The test covers wide/narrow light/dark, the two entries (#3387: Needs you and In progress, each thing in one only) and
Finished (#10922: what finished in the last 24 hours, seen or not, the unseen ones as Just finished),
their capsule size and selected state, stable live task rows, every open task that does not wait on the user in In
progress (a stopped one too, saying why it is not done, #3180) with inactive ones folded and finished ones excluded from open counts, a task's agents never listed by
themselves, an Agent opened by hand in the entry its state says, the plan waiting for Start, decisions and draft sends in
Needs you; and the main conversation's open tasks across both entries equal to the To-dos page's "Not done" (#3331).
"""
import argparse
import copy
import json
import re
from pathlib import Path
from urllib.parse import urlparse

from playwright.sync_api import expect, sync_playwright

AT = "2026-11-10T08:00:00Z"
MAIN = "demo-main"
MACHINE = {"id": "demo", "name": "Computer A", "online": True, "os": "linux"}
TITLES = ["Assistant layout · web", "Extension migration", "Release verification", "Assistant layout · mobile", "Diet card"]
PLAN = {"id": "demo-plan", "conversation": MAIN, "created_at": AT, "state": "started", "tasks": [{"title": title, "prompt": "Run synthetic checks", "machine": "demo", "cwd": "/demo/project", "todo_id": f"demo-task-{i}"} for i, title in enumerate(TITLES)]}
OPEN_PLAN = {"id": "demo-next-plan", "conversation": MAIN, "created_at": AT, "state": "open", "tasks": [{"title": "Prepare the next demo", "prompt": "Prepare a synthetic demo", "machine": "demo", "cwd": "/demo/project"}]}
SESSIONS = [{"machine_id": "demo", "session_id": MAIN, "title": "Persona", "state": "idle", "status": "idle", "cwd": "/demo", "updated_at": AT, "started_by_persona": True, "capabilities": {"send": True, "interrupt": True}}] + [{"machine_id": "demo", "session_id": f"worker-{i}", "title": title, "state": ["working", "working", "needs_you", "unread", "idle"][i], "status": ["working", "working", "needs_you", "unread", "idle"][i], "cwd": "/demo/project", "updated_at": AT, "agent": "claude", "todo": {"id": f"demo-task-{i}", "title": title}, "plan": {"done": 3, "total": 4, "current": "Run the remaining checks"}} for i, title in enumerate(TITLES)]
TODOS = [{"id": f"demo-task-{i}", "title": title, "origin": MAIN, "plan_id": PLAN["id"], "created_at": AT, "updated_at": AT, "agents": [{"machine_id": "demo", "session_id": f"worker-{i}", "linked_at": AT}], "pr_url": f"https://github.com/example/demo/pull/{2801 + i}", "supervision": {"supervisor": MAIN, "state": "accepted" if i == 3 else "needs_user" if i == 2 else "", "progress": {"kind": "blocked" if i == 2 else "progress", "step": i + 1, "steps": 5, "text": "Waiting for a decision" if i == 2 else "Run the demo checks", "at": AT}}, **({"done_at": AT} if i == 3 else {})} for i, title in enumerate(TITLES)]
# #10922: the accepted task was opened already (seen_at after it finished): Finished still lists it, as Accepted, not new.
TODOS[3]["seen_at"] = "2026-11-10T08:05:00Z"
# #3331: a task with no move for 48 hours and nothing running is shelved (folded, not counted, as on the To-dos page); the
# main conversation watches every to-do, one another conversation handed out too.
STALE = "2026-11-07T08:00:00Z"
TODOS += [{"id": "demo-stale", "title": "Old investigation", "origin": MAIN, "created_at": STALE, "updated_at": STALE},
          {"id": "demo-elsewhere", "title": "Task from another chat", "origin": "demo-other", "created_at": AT, "updated_at": AT}]
# Agents opened by hand (no task): one works, one asks. The main conversation lists them by what they need.
SESSIONS += [{"machine_id": "demo", "session_id": "manual-work", "title": "Explore the crash logs", "state": "working", "status": "working", "cwd": "/demo/project", "updated_at": AT, "agent": "claude", "activity": "Reading the logs"},
             {"machine_id": "demo", "session_id": "manual-ask", "title": "Clean up the build cache", "state": "needs_you", "status": "blocked", "pending_interactions": 1, "cwd": "/demo/project", "updated_at": AT, "agent": "claude"}]
# The stopped task's agents all stopped and the hub's completion check found it waiting (#3180): its row says why it is not done.
TODOS[4]["check"] = {"verdict": "waiting", "reason": "Waiting for CI to finish", "at": AT, "work_since": AT}
ITEMS = [{"id": "demo-user", "seq": 1, "role": "user", "kind": "text", "text": "Simplify the conversation and dispatch the work.", "ts": AT}, {"id": "demo-answer", "seq": 2, "role": "assistant", "kind": "text", "text": "I split the work into separate tasks. What needs you is in Needs you; each running task has one live row in In progress.", "ts": AT}]
FEED = [{"id": "demo-plan-event", "conversation": MAIN, "at": AT, "kind": "plan", "ref": PLAN["id"]}] + [{"id": f"demo-task-event-{i}", "conversation": MAIN, "at": AT, "kind": "task", "ref": f"demo-task-{i}"} for i in range(len(TITLES))] + [{"id": "demo-review-event", "conversation": MAIN, "at": AT, "kind": "review", "ref": "demo-task-3", "body": {"verdict": "accepted", "note": "Synthetic acceptance result"}}, {"id": "demo-report-event", "conversation": MAIN, "at": AT, "kind": "report", "ref": "demo-task-0", "body": {"kind": "progress", "text": "Synthetic progress report"}}, {"id": "demo-receipt", "conversation": MAIN, "at": AT, "kind": "receipt", "ref": "demo-task-3", "body": {"state": "done", "title": TITLES[3], "summary": "Synthetic completion receipt"}}, {"id": "demo-draft", "conversation": MAIN, "at": AT, "kind": "message", "ref": "demo-draft-ref", "body": {"sender": "Demo sender", "chat": "Demo group", "text": "When are the checks ready?", "draft": "The demo checks are ready for review."}}]
CARD_METADATA = [{"id": f"plan:demo-plan:{i}", "plan_id": PLAN["id"], "index": i, "todo_id": f"demo-task-{i}", "at": AT, "updated_at": AT, "state": "working"} for i in range(len(TITLES))]
ASK = {"id": "demo-permission", "machine_id": "demo", "session_id": "worker-2", "kind": "permission", "agent": "claude", "tool_name": "Bash", "tool_input": {"command": "npm test", "description": "Run the synthetic checks"}, "created_at": AT}
MANUAL_ASK = {"id": "demo-manual-question", "machine_id": "demo", "session_id": "manual-ask", "kind": "question", "agent": "claude", "created_at": AT, "questions": [{"question": "Remove the old build cache?", "options": [{"label": "Remove it"}, {"label": "Keep it"}]}]}


def run(args):
    shots = Path(args.shots)
    shots.mkdir(parents=True, exist_ok=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(executable_path=args.chromium, args=["--no-sandbox"])
        try:
            for width in (1280, 390):
                for theme in ("light", "dark"):
                    todos = copy.deepcopy(TODOS)
                    sessions = copy.deepcopy(SESSIONS)
                    todos[2]["ask"] = {"outcome": "decide", "verdict": "Choose the demo rollout", "recommend": "Use the demo rollout", "asked_at": AT}
                    remaining = [copy.deepcopy(ASK), copy.deepcopy(MANUAL_ASK)]
                    writes = []
                    sockets = []
                    context = browser.new_context(viewport={"width": width, "height": 900}, locale="en-US", color_scheme=theme, reduced_motion="reduce")
                    context.add_init_script(f"localStorage.setItem('persona.token', 'demo'); localStorage.setItem('persona.language', 'en'); localStorage.setItem('persona.theme', '{theme}'); localStorage.setItem('persona.text-size', '{args.text_size}');")
                    context.add_init_script("const RealDate = Date; globalThis.Date = class extends RealDate { constructor(...args) { super(...(args.length ? args : ['2026-11-10T08:10:00Z'])); } static now() { return new RealDate('2026-11-10T08:10:00Z').getTime(); } };")

                    def api(route):
                        path = urlparse(route.request.url).path.removeprefix("/api/v1")
                        data = {}
                        if path == "/machines":
                            data = {"machines": [MACHINE]}
                        elif path == "/sessions":
                            data = {"sessions": sessions}
                        elif path == "/interactions":
                            data = {"interactions": remaining}
                        elif path == "/assistant":
                            data = {"machine_id": "demo", "main": MAIN, "conversations": [SESSIONS[0]], "tasks": [], "memory": []}
                        elif path == f"/assistant/conversations/{MAIN}/feed":
                            data = {"items": FEED, "plans": {PLAN["id"]: PLAN, OPEN_PLAN["id"]: OPEN_PLAN}, "task_cards": CARD_METADATA}
                        elif path == "/todos":
                            data = {"todos": todos}
                        elif path.endswith("/items"):
                            data = {"items": ITEMS, "has_more": False}
                        elif re.fullmatch(r"/sessions/demo/[^/]+", path):
                            data = {"session": next(s for s in sessions if s["session_id"] == path.rsplit("/", 1)[-1])}
                        elif path.endswith("/accept") and route.request.method == "POST":
                            body = json.loads(route.request.post_data or "{}")
                            writes.append((path, body))
                            assert body.get("asked_at") == AT, body
                            todos[2].pop("ask", None)
                            todos[2]["supervision"]["state"] = ""
                            data = {"ok": True}
                        elif path.startswith("/interactions/") and route.request.method == "POST":
                            writes.append((path, json.loads(route.request.post_data or "{}")))
                            remaining[:] = [ix for ix in remaining if ix["id"] != path.split("/")[2]]
                            data = {"ok": True}
                        elif route.request.method == "POST" and "message" in path:
                            writes.append((path, json.loads(route.request.post_data or "{}")))
                            data = {"ok": True}
                        elif path in ("/extensions", "/managers"):
                            data = {"extensions": [], "managers": []}
                        route.fulfill(status=200, content_type="application/json", body=json.dumps(data))

                    def socket(ws):
                        sockets.append(ws)
                        ws.send(json.dumps({"type": "hello", "server_version": "demo", "hub_name": "Demo hub", "features": []}))
                        ws.on_message(lambda message: ws.send(json.dumps({"type": "pong"})) if "app_ping" in message else None)

                    context.route("**/api/v1/**", api)
                    context.route_web_socket("**/api/v1/ws**", socket)
                    page = context.new_page()
                    errors = []
                    page.on("pageerror", lambda error: errors.append(str(error)))
                    page.set_default_timeout(15000)
                    page.goto(args.url.rstrip("/") + "/")
                    bar = page.locator("[data-assistant-actionbar]")
                    expect(bar).to_be_visible()
                    expect(page.locator("[data-handoff-cards]")).to_have_count(1)  # the dispatch is a group of hand-off cards (#5096)
                    expect(page.locator("[data-receipt], [data-task-slot]")).to_have_count(0)
                    expect(page.get_by_text("Synthetic acceptance result", exact=True)).to_have_count(0)
                    expect(page.get_by_text("Synthetic progress report", exact=True)).to_have_count(0)
                    expect(page.locator('[data-testid="message-card"]')).to_have_count(0)

                    def capture(name):
                        expect(page.locator("vite-error-overlay")).to_have_count(0)
                        assert not errors, errors
                        if args.text_size == "large":
                            expect(page.locator("html")).to_have_attribute("data-text-size", "large")
                        assert bar.evaluate("e => { const box = e.getBoundingClientRect(); return box.x >= 0 && box.right <= innerWidth && box.bottom <= innerHeight; }")
                        # Reduced motion still runs 1 ms transitions (styles.css); headless Chrome draws frames only on
                        # demand, so let two frames finish them before the shot.
                        page.evaluate("() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))")
                        page.screenshot(path=str(shots / f"{width}-{theme}-{name}.png"))

                    # The entries are the dock's old "All" / task capsules (#2999): one rem-based height, fully round,
                    # content-wide in a row, and the open tray is the selected capsule.
                    entries = bar.locator("[data-tray-entry]")
                    expect(entries).to_have_count(3)
                    assert entries.evaluate_all("es => es.map(e => e.dataset.trayEntry)") == ["needs", "running", "finished"]
                    # Needs you: the release task (its agent's card), the hand-opened Agent's question, the plan waiting
                    # for Start, the draft. In progress: three of the plan's tasks (one asks, one is closed), the other
                    # chat's task and the hand-opened working Agent; the shelved one is folded, not counted.
                    needs_entry = bar.locator('[data-tray-entry="needs"]')
                    running_entry = bar.locator('[data-tray-entry="running"]')
                    finished_entry = bar.locator('[data-tray-entry="finished"]')
                    expect(needs_entry).to_have_text("Needs you4")
                    expect(running_entry).to_have_text("In progress5")
                    # #10922: the accepted task, finished ten minutes ago, is Finished's; its number is not open work
                    expect(finished_entry).to_have_text("Finished1")
                    boxes = entries.evaluate_all("""es => es.map(e => { const box = e.getBoundingClientRect(); const style = getComputedStyle(e);
                        return { h: box.height, w: box.width, top: box.top, radius: parseFloat(style.borderTopLeftRadius), font: style.fontSize,
                                 capsule: 2 * parseFloat(getComputedStyle(document.documentElement).fontSize) }; })""")
                    assert all(b["h"] == b["capsule"] for b in boxes), boxes
                    assert all(b["radius"] >= b["h"] / 2 for b in boxes), boxes
                    assert len({b["top"] for b in boxes}) == 1 and len({b["font"] for b in boxes}) == 1, boxes
                    # Side by side they fit the bar, Finished (#10922) too at the standard text size; at the large one on a
                    # phone it runs past the bar's end, and the entries' scroller brings it into view when it opens (below)
                    bar_width = bar.evaluate("e => e.getBoundingClientRect().width")
                    assert sum(b["w"] for b in boxes[:2]) < bar_width, boxes
                    assert args.text_size == "large" and width < 1280 or sum(b["w"] for b in boxes) < bar_width, boxes
                    assert entries.evaluate_all("es => es.map(e => e.getAttribute('aria-expanded'))") == ["false"] * 3
                    print(f"  entry heights {[b['h'] for b in boxes]} px, fully round, font {boxes[0]['font']}", flush=True)
                    capture("collapsed")
                    # keyboard: the entry takes focus and Enter toggles its tray, as a click does
                    needs_entry.focus()
                    expect(needs_entry).to_be_focused()
                    page.keyboard.press("Enter")
                    expect(needs_entry).to_have_attribute("aria-expanded", "true")
                    expect(bar.locator("section")).to_have_count(1)
                    page.keyboard.press("Enter")
                    expect(needs_entry).to_have_attribute("aria-expanded", "false")
                    expect(bar.locator("section")).to_have_count(0)
                    needs_entry.blur()
                    # #3700, #5096: a hand-off card for each of the plan's tasks, each opening its own Agent (handoff-cards.py), not this tray
                    expect(page.locator("[data-handoff-cards] [data-handoff-card]")).to_have_count(len(TITLES))
                    running_entry.click()
                    expect(running_entry).to_have_attribute("aria-expanded", "true")
                    expect(running_entry).to_have_class(re.compile(r"\bbg-ink\b"))
                    assert running_entry.evaluate("e => e.getBoundingClientRect().height") == boxes[1]["h"]
                    expect(bar.locator("[data-task-slot]")).to_have_count(4)
                    # The asking task and the plan waiting for Start are Needs you's; the finished one is Finished's (#10922).
                    expect(bar.locator('[data-task-slot="plan:demo-plan:2"]')).to_have_count(0)
                    expect(bar.locator('[data-task-slot="plan:demo-next-plan:0"]')).to_have_count(0)
                    expect(bar.locator('[data-task-slot="plan:demo-plan:3"]')).to_have_count(0)
                    expect(bar.locator("[data-progress-closed]")).to_have_count(0)
                    expect(bar.locator('[data-task-slot="task:demo-elsewhere"]')).to_have_count(1)
                    expect(bar.locator('[data-task-slot="task:demo-stale"]')).to_have_count(0)
                    expect(bar.locator("[data-progress-shelved]")).to_have_text("Inactive 1")
                    expect(bar.locator('[data-task-slot="plan:demo-plan:4"] [data-task-mark="idle"]')).to_have_count(1)
                    # the status column is the Agent's own state (#6492): its Agent is idle, not "follow up"
                    expect(bar.locator('[data-task-slot="plan:demo-plan:4"] [data-task-status]')).to_have_text("Idle")
                    # Agents: the hand-opened working one is a row; the tasks' agents are in their tasks' rows only
                    expect(bar.locator("[data-running-agent]")).to_have_count(1)
                    expect(bar.locator('[data-running-agent="demo/manual-work"]')).to_contain_text("Explore the crash logs")
                    expect(bar.get_by_text("Clean up the build cache", exact=False)).to_have_count(0)
                    expect(bar.get_by_text("PR #2801", exact=False)).to_be_visible()
                    capture("running")
                    stopped = bar.locator('[data-task-slot="plan:demo-plan:4"]')
                    stopped.scroll_into_view_if_needed()
                    # its agents stopped and the hub's check found it waiting: the row says why it is not done (#3180)
                    expect(stopped).to_contain_text("Stopped, not done yet · Waiting for CI to finish")
                    expect(stopped).not_to_contain_text("Run the demo checks")
                    capture("running-stopped")
                    row = bar.locator('[data-task-slot="plan:demo-plan:0"]')
                    row.evaluate("e => globalThis.originalTaskRow = e")
                    todos[0]["supervision"]["progress"] = {"kind": "blocked", "step": 3, "steps": 5, "text": "Waiting for demo review", "at": AT}
                    for ws in sockets:
                        ws.send(json.dumps({"type": "todos"}))
                    expect(row.get_by_text("Waiting for demo review", exact=False)).to_be_visible()
                    assert row.evaluate("e => e === globalThis.originalTaskRow")
                    expect(bar.locator('[role="progressbar"] .bg-orange')).to_have_count(1)
                    todos[0]["done_at"] = AT
                    todos[0]["supervision"]["state"] = "accepted"
                    for ws in sockets:
                        ws.send(json.dumps({"type": "todos"}))
                    # Acceptance moves the row to Finished without counting it as open work (#10922): new there, as
                    # Just finished, above the accepted task opened before (the same moment: by id), which reads Accepted.
                    expect(row).to_have_count(0)
                    expect(running_entry).to_have_text("In progress4")
                    expect(finished_entry).to_have_text("Finished2")
                    expect(bar.locator("[data-progress-closed]")).to_have_count(0)
                    capture("accepted-live")
                    finished_entry.click()
                    expect(finished_entry).to_have_attribute("aria-expanded", "true")
                    # the open entry is whole in view, however far the entries' row had to scroll for it
                    assert finished_entry.evaluate("""e => { const b = e.getBoundingClientRect(), r = e.closest("[data-assistant-actionbar]").getBoundingClientRect();
                        return b.left >= r.left - 1 && b.right <= r.right + 1; }""")
                    finished_rows = bar.locator("[data-tray-finished] [data-task-slot]")
                    # its rows come with the tray's first opening, in the Parked rows' chunk (FinishedTaskRows.tsx)
                    expect(finished_rows).to_have_count(2)
                    assert finished_rows.evaluate_all("es => es.map(e => e.dataset.taskSlot)") == ["plan:demo-plan:0", "plan:demo-plan:3"]
                    assert finished_rows.evaluate_all("es => es.map(e => e.dataset.countsOpen)") == ["false", "false"]
                    fresh = finished_rows.nth(0)
                    expect(fresh.locator('[data-task-mark="just_finished"] [data-state="just_finished"]')).to_have_count(1)
                    expect(fresh.locator("[data-task-status]")).to_have_text("Just finished")
                    seen = finished_rows.nth(1)
                    expect(seen.locator('[data-task-mark="done"]')).to_have_count(1)
                    expect(seen.locator("[data-task-status]")).to_have_text("Accepted")
                    capture("finished")
                    running_entry.click()
                    bar.locator("[data-progress-shelved]").click()
                    expect(bar.locator('[data-task-slot="task:demo-stale"]')).to_have_count(1)
                    capture("shelved-unfolded")
                    needs_entry.click()
                    expect(needs_entry).to_have_attribute("aria-expanded", "true")
                    section = bar.locator("section")
                    # the rows with something to answer are folded to a line until opened (#5569)
                    folded = section.locator('button[data-needs-row][aria-expanded="false"]')
                    while folded.count():
                        folded.first.click()
                    # one row per thing: the release task with its agent's card under its name, the hand-opened Agent's
                    # question, the plan with its Start, the draft
                    expect(section.locator('[data-needs-row="task:demo-task-2"]')).to_have_count(1)
                    expect(section.get_by_text(TITLES[2], exact=True)).to_have_count(1)
                    expect(section.get_by_role("button", name="Allow", exact=True)).to_be_visible()
                    expect(section.locator('[data-needs-row="agent:demo/manual-ask"]')).to_contain_text("Remove the old build cache?")
                    next_plan = section.locator('[data-needs-row="plan:demo-next-plan"]')
                    expect(next_plan.locator('button[aria-expanded="true"]')).to_contain_text("Prepare the next demo")
                    expect(next_plan.get_by_role("button", name="Start", exact=True)).to_be_visible()
                    expect(section.locator('[data-testid="message-card"]')).to_have_count(1)
                    # In progress' things are not here
                    expect(section.get_by_text(TITLES[1], exact=True)).to_have_count(0)
                    expect(section.get_by_text("Explore the crash logs", exact=False)).to_have_count(0)
                    capture("needs")
                    assert not writes, writes
                    section.get_by_role("button", name="Allow", exact=True).click()
                    page.wait_for_timeout(150)
                    assert any(body.get("allow") is True for _, body in writes), writes
                    assert not any("message" in path for path, _ in writes), writes
                    # its card answered, the task still waits for the owner's decision: the same one row, now the decision
                    owner = bar.get_by_role("button", name="Take recommendation", exact=True)
                    owner.scroll_into_view_if_needed()
                    expect(bar.locator('[data-brief-row="accept"]')).to_have_count(1)
                    expect(section.get_by_text(TITLES[2], exact=False)).to_have_count(1)
                    capture("needs-owner")
                    owner.click()
                    page.wait_for_timeout(150)
                    assert any(path.endswith("/accept") and body.get("asked_at") == AT for path, body in writes), writes
                    assert not any("message" in path for path, _ in writes), writes
                    # the To-dos page's header counts every open task: In progress' tasks plus the one in Needs you
                    running_tasks = bar.locator('[data-tray-entry="running"]')
                    running_tasks.click()
                    task_rows = bar.locator('[data-task-slot][data-counts-open="true"]').count()
                    # #3331: the badge counts a to-do row only while the To-dos page counts it open (data-counts-open);
                    # rows that the page shelves or closes stay visible but add no number.
                    chip_open_running = bar.locator('section [data-counts-open="true"]').count()
                    needs_entry.click()
                    needs_tasks = bar.locator('section [data-needs-row^="task:"], section [data-brief-row="accept"]').count()
                    chip_open_needs = bar.locator('section [data-counts-open="true"]').count()
                    chip_open = chip_open_running + chip_open_needs
                    page.goto(args.url.rstrip("/") + "/todos")
                    header = page.get_by_text(re.compile(r"^\d+ open · ")).first
                    # a phone-wide page hides the line (max-sm:hidden); it is there all the same
                    expect(header).to_be_attached()
                    open_count = int(re.match(r"(\d+) open", header.text_content()).group(1))
                    assert open_count == chip_open, (open_count, chip_open, header.text_content())
                    assert open_count == task_rows + needs_tasks == 4, (open_count, task_rows, needs_tasks, header.text_content())
                    print(f"  chip open markers {chip_open} ({chip_open_running} in progress + {chip_open_needs} needs you) = To-dos page \"{header.text_content()}\"", flush=True)
                    page.evaluate("() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))")
                    page.screenshot(path=str(shots / f"{width}-{theme}-todos.png"))
                    # Main groups merge waits with explicit holds in Parked; retain the PR row and the separate review tray.
                    review = {"id": "demo-supervisor-review", "title": "Review with stopped Agent", "origin": MAIN, "created_at": AT, "updated_at": AT,
                              "agents": [{"machine_id": "demo", "session_id": "review-stopped", "linked_at": AT}],
                              "supervision": {"supervisor": MAIN, "state": "pending_review", "updated_at": AT, "reviews": []}}
                    pr = "https://github.com/example/demo/pull/42"
                    waiting_merge = {"id": "demo-waiting-merge", "title": "Waiting PR with blockers", "origin": MAIN, "created_at": AT, "updated_at": AT,
                                     "agents": [], "pr_url": pr, "delivery": {"state": "pr"},
                                     "pr_status": {"url": pr, "state": "open", "draft": False, "review": "changes_requested", "checks": "pending",
                                                   "conflicts": True, "checked_at": "2026-11-10T08:09:00Z"}}
                    rework = {"id": "demo-rework-stopped", "title": "Rework with stopped Agent", "origin": MAIN, "created_at": AT, "updated_at": AT,
                              "agents": [{"machine_id": "demo", "session_id": "rework-stopped", "linked_at": AT}],
                              "supervision": {"supervisor": MAIN, "state": "rework", "updated_at": AT, "reviews": [], "round": 2}}
                    parked = {"id": "demo-parked", "title": "Waiting on a reviewer", "origin": MAIN, "created_at": AT, "updated_at": AT,
                              "agents": [],
                              "supervision": {"supervisor": MAIN, "state": "parked", "updated_at": AT, "reviews": [],
                                              "parked": {"reason": "Waiting on a reviewer", "until": "2026-11-20T08:00:00Z", "waiting_on": "someone_else", "count": 1}}}
                    todos[:] = [review, waiting_merge, rework, parked]
                    sessions[:] = [sessions[0]] + [{"machine_id": "demo", "session_id": session, "title": "Stopped demo Agent", "cwd": "/demo",
                                                   "state": "offline", "status": "offline", "updated_at": AT} for session in ("review-stopped", "rework-stopped")]
                    remaining[:] = []
                    page.goto(args.url.rstrip("/") + "/")
                    bar.locator('[data-tray-entry="review"]').click()
                    review_row = bar.locator('[data-task-slot="task:demo-supervisor-review"]')
                    expect(review_row.get_by_text("Review with stopped Agent", exact=True)).to_be_visible()
                    expect(review_row.locator("[data-task-status]")).to_have_text("Supervisor reviewing")
                    expect(review_row.locator('[data-task-mark="stopped"]')).to_have_count(0)
                    capture("supervisor-review")
                    expect(bar.locator('[data-tray-entry="parked"]')).to_contain_text("Parked")
                    expect(bar.locator('[data-tray-entry="pr"]')).to_have_count(0)
                    bar.locator('[data-tray-entry="parked"]').click()
                    merge_row = bar.locator('[data-pr-waiting-row="demo-waiting-merge"]')
                    expect(merge_row.locator("a")).to_have_attribute("href", pr)
                    for blocker in ("Changes requested", "Checks pending", "Conflicts"):
                        expect(merge_row).to_contain_text(blocker)
                    expect(bar.locator("[data-parked-task-row]")).to_have_count(2)
                    expect(bar.locator('[data-parked-task-row="demo-parked"]')).to_contain_text("Waiting on a reviewer")
                    capture("waiting-merge")
                    bar.locator('[data-tray-entry="running"]').click()
                    rework_row = bar.locator('[data-task-slot="task:demo-rework-stopped"]')
                    expect(rework_row.get_by_text("Rework with stopped Agent", exact=True)).to_be_visible()
                    expect(rework_row.locator("[data-task-status]")).to_have_text("Stopped")
                    expect(rework_row.locator("[data-task-todo-state]")).to_have_text("To-do: Rework round 2")
                    expect(bar.locator('[data-task-slot="task:demo-parked"]')).to_have_count(0)
                    capture("agent-state")
                    print(f"PASS {width} {theme} {args.text_size}: Agent-only status; Finished lists seen and new rows outside open counts; supervisor review and waiting-PR blockers retained; draft not sent", flush=True)
                    context.close()
        finally:
            browser.close()
    print("PASS Assistant task tray browser verification", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("url")
    parser.add_argument("--shots", required=True)
    parser.add_argument("--chromium", default="/usr/bin/google-chrome")
    parser.add_argument("--text-size", choices=("standard", "large"), default="standard")
    run(parser.parse_args())
