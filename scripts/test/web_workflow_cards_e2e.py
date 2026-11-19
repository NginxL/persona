#!/usr/bin/env python3
"""End-to-end check of the web's workflow run cards (#3434, #5030). In an agent's chat each Workflow call that launched
a run the hub lists is the run's task card (after cindy's AgentTaskCard), inside the turn's folded work summary until the
reader opens it: the workflow mark, the run's name and its status word (运行中 / 已完成 / 失败, only 失败 red), then
"Workflow · tokens · tool uses · 已运行 … / the time it took", "<phase> · done/total Agent" and a strip of a square per
agent (at most 40, then "+n"; a 185-agent run's earlier agents the hub no longer lists drawn from its counts). Its head
opens the Background tasks side panel at its run's detail (#5421, cindy's); there the run's card has its counts,
a progress bar moved by scaleX, one block per phase (a long block's last 12 rows with "更早的 n 个没有列出" and
"还有 n 个"), and agent rows that only read (they open no conversation). A call whose run the hub no longer lists stays a
plain row. A change moves the card in place (the detail's counts roll, not with reduced motion); the running bar says
"后台 workflow · <phase> · N/M+ · 还有 n 个" from the run's start, its count whole at 390 wide in English and Japanese
too; the top bar's Background tasks button opens the panel's list, a run's detail there has "在对话中查看" bringing the
call into view; the sidebar row's count.
Light / dark × 1280 / 390.

    MOCK_WORKFLOW_CARDS=1 PORT=18772 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18772 npx vite --port 5182 --strictPort &)
    python3 scripts/test/web_workflow_cards_e2e.py http://127.0.0.1:5182 --hub http://127.0.0.1:18772 [--shots <dir>]

The mock hub's GET /mock/workflow?step=0..3 moves the small run on (an agent done and a new one, the next phase,
completed) and pushes the session. Prints one line per check and a verdict; exits 1 on the first failure.
"""
import argparse
import json
import os
import re
import sys
import urllib.request

from playwright.sync_api import expect, sync_playwright

CHAT = "/s/macbook/a1f3c2d4-0000-4000-8000-0000000000e1"
SIZES = {"wide": {"width": 1280, "height": 860}, "narrow": {"width": 390, "height": 844}}
OVERFLOW = "() => document.documentElement.scrollWidth - document.documentElement.clientWidth"
# each part of a card's bar: its transform and whether a width was set on it
PARTS = """(card) => [...card.querySelectorAll('[data-workflow-progress] [data-part]')].map((p) => ({
  part: p.dataset.part, transform: p.style.transform, width: p.style.width, transition: getComputedStyle(p).transitionProperty }))"""
# the strip's squares by state, and whether any of them reaches past the card
STRIP = """(card) => { const box = card.getBoundingClientRect();
  const cells = [...card.querySelectorAll('[data-workflow-strip] [data-cell]')];
  return { cells: cells.map((c) => c.dataset.cell), more: card.querySelector('[data-strip-more]')?.textContent ?? '',
    outside: cells.filter((c) => { const r = c.getBoundingClientRect(); return r.right > box.right + 0.5 || r.left < box.left - 0.5; }).length }; }"""
META = "(card) => [...card.querySelectorAll('[data-task-meta] > *')].map((e) => e.textContent)"


def ok(msg):
    print("ok  " + msg, flush=True)


def fail(msg):
    print("FAIL " + msg, flush=True)
    sys.exit(1)


def step(hub, n):
    with urllib.request.urlopen(f"{hub}/mock/workflow?step={n}", timeout=10) as r:
        return json.load(r)


def center(el):
    """Brings an element to the middle of the window (not to its top, under the sticky work-group titles)."""
    el.evaluate("(e) => e.scrollIntoView({ block: 'center' })")
    el.page.wait_for_timeout(250)


def in_summary(card):
    return card.evaluate("(el) => Boolean(el.closest('[data-work-group]'))")


def open_summary(seg, card):
    """Opens the turn's folded work summary (the chat fetches the folds holding the call), until the card shows."""
    expect(card).to_have_count(0)
    seg.locator('[data-work-group] > button[aria-expanded="false"]').first.click()
    expect(card).to_be_visible()
    if not in_summary(card):
        fail("a workflow card should sit inside the turn's work summary")


def strip_of(card, tag, cells, more=""):
    got = card.evaluate(STRIP)
    if got["cells"] != cells or got["more"] != more or got["outside"]:
        fail(f"{tag}: the strip should be {cells} {more!r} inside the card: {got}")


def check_head(card, tag, status, word, meta_first, progress):
    expect(card.locator("[data-task-status]")).to_have_attribute("data-task-status", status)
    expect(card.locator("[data-task-status]")).to_have_text(word)
    red = card.locator("[data-task-status]").evaluate("(e) => e.classList.contains('text-red')")
    if red != (status == "failed"):
        fail(f"{tag}: only a failed run's status word is red ({status}: {red})")
    meta = card.evaluate(META)
    if meta[: len(meta_first)] != meta_first:
        fail(f"{tag}: the second line should start {meta_first}: {meta}")
    expect(card.locator("[data-workflow-progress-line]")).to_have_text(progress)
    if card.locator("[data-workflow-status], [data-workflow-counts], [data-workflow-phases]").count():
        fail(f"{tag}: the detail's head and phases are not drawn in the chat's card")
    return meta


def open_in_panel(page, card, task, tag):
    """The card's head opens the Background tasks panel at its run's detail (#5421)."""
    head = card.locator("button").first
    if head.get_attribute("aria-expanded") is not None or card.get_attribute("data-workflow-opens") != "panel":
        fail(f"{tag}: the card should open the Background tasks panel, not in place")
    head.click()
    panel = page.locator('aside[aria-label="后台任务"]')
    expect(panel).to_have_count(1)
    expect(panel.locator(f'[data-background-detail="{task}"]')).to_have_count(1)
    target = panel.locator(f'[data-workflow-task="{task}"]')
    expect(target).to_be_in_viewport()
    return panel, target


def close_panel(page):
    page.locator('aside[aria-label="后台任务"]').get_by_role("button", name="关闭面板").click()
    expect(page.locator('aside[aria-label="后台任务"]')).to_have_count(0)


def run(browser, base, hub, token, size, theme, shots, reduce):
    tag = f"{size}/{theme}{'/reduced' if reduce else ''}"
    step(hub, 0)
    ctx = browser.new_context(viewport=SIZES[size], device_scale_factor=2, has_touch=size == "narrow", color_scheme=theme,
                              locale="zh-CN", reduced_motion="reduce" if reduce else "no-preference")
    page = ctx.new_page()
    page.set_default_timeout(15000)
    errors = []
    page.on("pageerror", lambda e: errors.append(str(e)))
    page.goto(base + "/overview?token=" + token)
    page.wait_for_load_state("networkidle")
    page.evaluate(f"localStorage.setItem('persona.theme', '{theme}')")
    prefix = f"workflow-cards-{size}-{theme}{'-reduced' if reduce else ''}"

    def shot(name, el=None):
        if not shots:
            return
        path = os.path.join(shots, f"{prefix}-{name}.png")
        (el or page).screenshot(path=path)

    page.goto(base + CHAT)
    main = page.locator("main")
    small_seg = main.locator("[data-segment]", has_text="小组件也逐块审一下")
    long_seg = main.locator("[data-segment]", has_text="和 App 的功能差异全量审一遍")
    small = main.locator('[data-workflow-call][data-tool-use="toolu_wf_run"]')
    long = main.locator('[data-workflow-call][data-tool-use="toolu_wf_long"]')
    # the hub folded the turns' work: a run's card is in its turn's summary until that opens
    expect(small_seg).to_be_visible()
    open_summary(small_seg, small)
    open_summary(long_seg, long)
    ok(f"{tag}: the running runs' cards sit inside their turns' work summaries, shown once those open")

    meta = check_head(small, tag, "running", "运行中", ["Workflow"], "Verify · 6/9 Agent")
    if small.locator("[data-task-status] svg").count():
        fail(f"{tag}: a running run's status has the spinner, not an outcome's icon")
    if len(meta) != 4 or not meta[1].endswith(" tokens") or not meta[2].endswith("次工具调用") or not meta[3].startswith("已运行 "):
        fail(f"{tag}: the running card's second line: {meta}")
    strip_of(small, tag, ["done"] * 6 + ["running"] * 3)
    expect(small.locator("button").first).to_have_attribute("aria-label", "在后台任务面板中打开 widget-review")
    ok(f"{tag}: the running card: 运行中, {' · '.join(meta)}, Verify · 6/9 Agent, 6 done and 3 running squares")
    center(small)
    shot("running", small)
    shot("running-page")

    # the running bar above the composer: what runs in the background, timed from the run's start
    bar = main.locator('[data-running-background="workflow"]')
    expect(bar).to_be_visible()
    expect(bar.locator("[data-running-verb]")).to_have_text("后台 workflow · Verify · 6/9+ · 还有 1 个")
    elapsed = bar.locator("[data-running-elapsed]").inner_text()
    started = page.evaluate("""async () => { const r = await fetch('/api/v1/sessions', { headers: { Authorization: 'Bearer ' + (localStorage.getItem('persona.token') || 'mock') } });
      const all = (await r.json()).sessions; const s = all.find((x) => x.session_id === '%s');
      return (Date.now() - Date.parse(s.workflows[0].started_at)) / 60000; }""" % CHAT.split("/")[-1])
    minutes = int(m.group(1)) if (m := re.match(r"(\d+)m ", elapsed)) else -1
    if abs(minutes - int(started)) > 1:
        fail(f"{tag}: the running bar should count from the run's start ({started:.1f} min): {elapsed}")
    ok(f"{tag}: the running bar says the background workflow, its phase and count, from the run's start")

    # the long run: 185 agents, the hub lists the newest 60; the strip's first 40 are the earliest (all back), then +145
    meta = check_head(long, tag, "running", "运行中", ["Workflow", "3.1M tokens", "1420 次工具调用"], "Verify · 169/185 Agent")
    strip_of(long, tag, ["done"] * 40, "+145")
    if page.evaluate(OVERFLOW) > 0:
        fail(f"{tag}: the page scrolls sideways")
    ok(f"{tag}: the long run's card: 3.1M tokens, 1420 次工具调用, Verify · 169/185 Agent, 40 squares and +145")
    center(long)
    shot("long", long)

    # its head opens the Background tasks panel at the run; there its Audit block (cut into by the hub's cap) shows its
    # last 12 rows with what is hidden and what is not listed above them; the rows only read
    panel, card = open_in_panel(page, long, "wflong", tag)
    page.wait_for_timeout(300)
    shot("panel-long")
    expect(card.locator("[data-workflow-counts]")).to_have_text("169/185+")
    audit = card.locator('[data-phase="Audit"]')
    expect(audit.locator("[data-phase-count]")).to_have_text("169/180+")
    if audit.locator("[data-agent]").count() == 0:
        audit.locator("button").first.click()  # all back but the running ones: open it
    expect(audit.locator("[data-agent]")).to_have_count(12)
    expect(audit.locator("[data-agent]").first).to_have_attribute("data-agent", "p169")
    expect(audit.locator("[data-phase-unlisted]")).to_have_text("更早的 125 个没有列出")
    expect(audit.locator("[data-phase-more]")).to_have_text("还有 43 个")
    order = audit.evaluate("""(b) => [...b.querySelectorAll('[data-phase-agents] > li')].slice(0, 3).map((li) =>
      li.dataset.agent || (li.hasAttribute('data-phase-unlisted') ? 'unlisted' : 'more'))""")
    if order != ["unlisted", "more", "p169"]:
        fail(f"{tag}: the long block should start with what is not listed, then 还有 n 个, then its rows: {order}")
    audit.locator("[data-phase-more]").click()
    expect(audit.locator("[data-agent]")).to_have_count(55)  # the 43 it said, and the 12
    expect(audit.locator("[data-agent]").first).to_have_attribute("data-agent", "p126")
    expect(audit.locator("[data-phase-unlisted]")).to_be_visible()
    audit.locator("[data-phase-fewer]").click()
    expect(audit.locator("[data-agent]")).to_have_count(12)
    if panel.locator("[data-agent] button, [data-agent] a, [data-agent][role=button]").count():
        fail(f"{tag}: the detail's agent rows should open nothing")
    audit.locator('[data-agent="p170"]').click()
    expect(panel).to_have_count(1)  # nothing opened in its place
    ok(f"{tag}: the long card opens the panel at its run; Audit's last 12 rows, 还有 43 个 opens exactly those; rows only read")
    close_panel(page)

    # the small run in the panel: Verify lit at 2/5+, Scan folded at 4/4, the bar by scaleX
    panel, card = open_in_panel(page, small, "wfrun", tag)
    expect(card.locator("[data-workflow-counts]")).to_have_text("6/9+")
    expect(card.locator("[data-workflow-status]")).to_have_text("在跑")
    expect(card.locator("[data-workflow-now]")).to_contain_text("当前 Verify：verify:")
    expect(card.locator('[data-phase="Verify"][data-phase-current]')).to_have_count(1)
    expect(card.locator('[data-phase="Verify"] [data-phase-count]')).to_have_text("2/5+")
    expect(card.locator('[data-phase="Scan"] [data-phase-count]')).to_have_text("4/4")
    expect(card.locator('[data-phase="Scan"] [data-phase-agents]')).to_have_count(0)  # all back: folded
    expect(card.locator('[data-phase="Verify"] [data-agent]')).to_have_count(5)
    parts = card.evaluate(PARTS)
    if [p["part"] for p in parts] != ["done", "running", "failed", "stopped"] or any(p["width"] for p in parts) \
            or not all("scaleX" in p["transform"] for p in parts) or any("width" in p["transition"] for p in parts):
        fail(f"{tag}: the bar's parts should move by transform: scaleX, never width: {parts}")
    ok(f"{tag}: the small run in the panel: 6/9+, Verify lit at 2/5+, Scan folded at 4/4, the bar by scaleX")
    close_panel(page)

    # a call whose run is over a day old: the row it always was
    old_turn = main.locator("[data-segment]", has_text="上次的文案审计再跑一遍")
    old_turn.locator('[data-work-group] > button[aria-expanded="false"]').first.click()
    gone = old_turn.locator('[data-tool-use="toolu_wf_gone"]')
    expect(gone).to_have_count(1)
    if gone.get_attribute("data-action-row") != "Workflow":
        fail(f"{tag}: an unlisted run's call should stay a plain row")
    ok(f"{tag}: a call with no run listed is the plain row")

    # ended runs: their status word, the time they took, how many came back, a square each; the panel at their run
    for prompt, call, task, status, word, took, progress, cells, name in (
            ("把设置页的文案过一遍", "toolu_wf_done", "wfdone", "completed", "已完成", "22m", "8/8 Agent", ["done"] * 8, "done"),
            ("小组件的快照对比也跑一遍", "toolu_wf_failed", "wffail", "failed", "失败", "8m", "6/6 Agent",
             ["done", "done", "done", "failed", "failed", "done"], "failed")):
        seg = main.locator("[data-segment]", has_text=prompt)
        card = seg.locator(f'[data-workflow-call="{status}"][data-tool-use="{call}"]')
        open_summary(seg, card)
        meta = check_head(card, tag, status, word, ["Workflow"], progress)
        if meta[-1] != took or any(m.startswith("已运行") for m in meta):
            fail(f"{tag}: the {name} run should say the time it took ({took}): {meta}")
        strip_of(card, tag, cells)
        center(card)
        shot(f"{name}", card)
        panel, target = open_in_panel(page, card, task, tag)
        if status == "failed":
            expect(target.locator("[data-workflow-error]")).to_contain_text("2 agents failed")
            expect(target.locator('[data-phase="Snapshot"] [data-phase-mark]')).to_have_attribute("data-phase-mark", "done_failed")
        page.wait_for_timeout(300)
        shot(f"panel-{name}")
        close_panel(page)
        ok(f"{tag}: the {name} run's card: {word}, {took}, {progress}; opens the panel at its run")

    # a change: the detail's count rolls (not with reduced motion), the card's line and squares move on in place
    small.evaluate("(e) => { e.dataset.e2eMark = '1'; }")
    panel, card = open_in_panel(page, small, "wfrun", tag)
    center(card)
    shot("change-before", card)
    step(hub, 1)
    if reduce:
        expect(card.locator("[data-workflow-counts]")).to_have_text("8/10+")
        if panel.locator("[data-rolling], [data-fading]").count():
            fail(f"{tag}: something moved with reduced motion")
        ok(f"{tag}: reduced motion: the new count at once, nothing rolls")
    else:
        expect(card.locator("[data-workflow-counts] [data-rolling]")).to_have_count(1, timeout=3000)
        page.wait_for_timeout(60)
        shot("change-mid", card)
        expect(card.locator("[data-workflow-counts]")).to_have_text("8/10+")
        expect(card.locator("[data-workflow-counts] [data-rolling]")).to_have_count(0)
        ok(f"{tag}: the detail's count rolls to 8/10+ and comes to rest")
    expect(card.locator('[data-phase="Verify"] [data-agent]')).to_have_count(6)
    page.wait_for_timeout(500)
    shot("change-after", card)
    close_panel(page)
    expect(small.locator("[data-workflow-progress-line]")).to_have_text("Verify · 8/10 Agent")
    strip_of(small, tag, ["done"] * 8 + ["running"] * 2)
    step(hub, 2)
    expect(small.locator("[data-workflow-progress-line]")).to_have_text("Report · 10/11 Agent")
    ok(f"{tag}: the card moves on in place: Verify · 8/10, then Report · 10/11")
    step(hub, 3)
    expect(small).to_have_attribute("data-workflow-call", "completed")
    expect(small.locator("[data-task-status]")).to_have_text("已完成")
    expect(small.locator("[data-workflow-progress-line]")).to_have_text("11/11 Agent")
    strip_of(small, tag, ["done"] * 11)
    if small.get_attribute("data-e2e-mark") != "1" or not in_summary(small):
        fail(f"{tag}: the ended run's card should be the same card, where it was")
    if any(m.startswith("已运行") for m in small.evaluate(META)):
        fail(f"{tag}: an ended run says the time it took, not 已运行")
    page.wait_for_timeout(600)
    center(small)
    shot("completed", small)
    shot("completed-page")
    ok(f"{tag}: completed in place: 已完成, 11/11 Agent, 11 squares done, the time it took")

    # The overview lists the runs. Opening a detail or jumping to its call keeps the overview open.
    trigger = page.locator("[data-session-overview-trigger]")
    trigger.click()
    overview = page.locator("[data-session-overview]")
    expect(overview.locator("[data-bg-run]")).to_have_count(4)
    shot("overview")
    overview.locator('[data-bg-run="wffail"] [data-bg-row-button]').click()
    panel = page.locator('aside[aria-label="后台任务"]')
    expect(panel.locator('[data-workflow-card="failed"]')).to_contain_text("2 agents failed")
    panel.locator('[data-workflow-card="failed"] [data-workflow-jump]').click()
    if size == "narrow":
        expect(panel).to_have_count(0)  # it covered the chat
    expect(overview).to_be_visible()
    expect(main.locator('[data-tool-use="toolu_wf_failed"]')).to_be_in_viewport()
    ok(f"{tag}: the overview stays open and the workflow detail can jump to its call")

    if size == "wide":
        # the sidebar's row for this agent, where the sidebar lists agents (its default now lists pages only)
        row = page.locator("nav a[aria-current=page], aside a[aria-current=page]").first
        if row.count() == 0:
            print(f"skip {tag}: the sidebar lists no row for this agent, so its workflow line is not checked here", flush=True)
        else:
            expect(row.locator("[data-workflow-line]")).to_contain_text("· parity-audit · Verify")
            ok(f"{tag}: the sidebar row says the run it waits for")
            shot("sidebar", row)
    if page.evaluate(OVERFLOW) > 0:
        fail(f"{tag}: the page scrolls sideways")
    if errors:
        fail(f"{tag}: page errors: {errors[:3]}")
    ctx.close()


def bar_in(browser, base, hub, token, locale, shots):
    """The running bar at 390 wide in another language: the words before the count give way, the count stays whole."""
    step(hub, 0)
    ctx = browser.new_context(viewport=SIZES["narrow"], device_scale_factor=2, has_touch=True, color_scheme="light", locale=locale)
    page = ctx.new_page()
    page.set_default_timeout(15000)
    page.goto(base + "/overview?token=" + token)
    page.wait_for_load_state("networkidle")
    page.goto(base + CHAT)
    bar = page.locator('main [data-running-background="workflow"]')
    expect(bar).to_be_visible()
    expect(bar.locator("[data-running-counts]")).to_contain_text("6/9+")
    cut = bar.evaluate("""(b) => {
      const verb = b.querySelector('[data-running-verb]').getBoundingClientRect();
      return ['[data-running-counts]', '[data-running-more]'].map((s) => {
        const e = b.querySelector(s), r = e.getBoundingClientRect();
        return { s, cut: e.scrollWidth > e.clientWidth + 1 || r.right > verb.right + 0.5 || r.left < verb.left - 0.5 };
      }).filter((x) => x.cut).map((x) => x.s);
    }""")
    if cut:
        fail(f"narrow/{locale}: the running bar cuts {cut}")
    if shots:
        page.screenshot(path=os.path.join(shots, f"workflow-cards-narrow-light-{locale}-page.png"))
        bar.screenshot(path=os.path.join(shots, f"workflow-cards-narrow-light-{locale}-bar.png"))
    ok(f"narrow/{locale}: the running bar keeps the count and the other runs whole: {bar.locator('[data-running-verb]').inner_text()}")
    ctx.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--hub", required=True, help="the fake hub (MOCK_WORKFLOW_CARDS=1), for /mock/workflow")
    ap.add_argument("--shots")
    ap.add_argument("--token", default="mock")
    ap.add_argument("--only", choices=list(SIZES), help="one width only")
    a = ap.parse_args()
    if a.shots:
        os.makedirs(a.shots, exist_ok=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(channel="chrome")
        try:
            for size in [a.only] if a.only else SIZES:
                for theme in ("light", "dark"):
                    run(browser, a.base.rstrip("/"), a.hub.rstrip("/"), a.token, size, theme, a.shots, False)
            run(browser, a.base.rstrip("/"), a.hub.rstrip("/"), a.token, a.only or "wide", "light", a.shots, True)
            if a.only != "wide":
                for locale in ("en-US", "ja-JP"):
                    bar_in(browser, a.base.rstrip("/"), a.hub.rstrip("/"), a.token, locale, a.shots)
        finally:
            step(a.hub.rstrip("/"), 0)
            browser.close()
    print("PASS web workflow cards e2e")


if __name__ == "__main__":
    main()
