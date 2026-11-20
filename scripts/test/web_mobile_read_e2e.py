#!/usr/bin/env python3
"""End-to-end check of reading a chat on a phone (#1996): the buttons that only showed on hover (copy code, Mermaid's
tools) are there to tap, a turn's pictures open in the image viewer where two fingers zoom the picture (not the page)
and Back closes it on the chat, a wide table's last column is not a character a line, your bubble keeps a margin on its
left (also while it is being sent, folded when long), the answer is 16 px, a work group's header is 40 tall, a long
press on an answer leaves its menu open after the finger lifts, an edit's diff takes the whole screen (the changes
panel, #9362), and formulas are drawn once KaTeX has loaded for them. On a desktop (1280, a mouse) the sizes stay 15 / 14.

Needs a running web page against the fake hub with these conversations, and Playwright for Python (it drives the
installed Chrome):

    MOCK_CHAT_READ=1 MOCK_SEND_DELAY_MS=20000 MOCK_WORK_GROUPS=1 MOCK_TURN_IMAGES_WEB=1 MOCK_RICH=1 MOCK_MERMAID=1 \\
      PORT=18962 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18962 npx vite --port 5962 --strictPort &)
    uv run --no-project --with playwright==1.58.0 python3 scripts/test/web_mobile_read_e2e.py http://127.0.0.1:5962 [--shots <dir>]

Prints one line per check and a verdict; exits 1 on the first failure. Runs the checks on a 390 × 844 phone, then the
layout ones on 360 × 740 and 844 × 390 (landscape); --shots saves each state there, light and dark.
"""
import argparse
import os
import sys

from playwright.sync_api import expect, sync_playwright

READ = "/s/macbook/a1f3c2d4-0000-4000-8000-0000000c1ead"  # MOCK_CHAT_READ
GROUPS = "/s/macbook/a1f3c2d4-0000-4000-8000-0000000c1e03"  # MOCK_WORK_GROUPS
REVIEW = "/s/macbook/a1f3c2d4-0000-4000-8000-000000000006"  # MOCK_TURN_IMAGES_WEB: nine pictures
RICH = "/s/macbook/a1f3c2d4-0000-4000-8000-00000000000a"  # MOCK_RICH: the wide tables
WIDGET = "/s/macbook/a1f3c2d4-0000-4000-8000-000000000001"  # MOCK_MERMAID
UA = ("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 "
      "(KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1")
PHONES = {"390": (390, 844), "360": (360, 740), "land": (844, 390)}
S = {"base": "", "shots": None}


def ok(msg):
    print("ok  " + msg, flush=True)


def fail(msg):
    print("FAIL " + msg, flush=True)
    sys.exit(1)


def shot(page, name):
    if S["shots"]:
        page.screenshot(path=os.path.join(S["shots"], name + ".png"))


def open_page(browser, w, h, theme, phone=True):
    opts = dict(viewport={"width": w, "height": h}, color_scheme=theme)
    if phone:
        opts.update(device_scale_factor=3, is_mobile=True, has_touch=True, user_agent=UA)
    ctx = browser.new_context(**opts)
    ctx.grant_permissions(["clipboard-read", "clipboard-write"], origin=S["base"])
    page = ctx.new_page()
    page.goto(S["base"] + "/overview?token=mock")
    page.wait_for_selector("aside", state="attached", timeout=15000)
    return ctx, page


def same_chat(path, url):
    """Still on the chat `path` names: its /s/ link, or the /t/ one the page replaced it with (#1996)."""
    return path in url or url.rstrip("/").endswith("/" + path.rstrip("/").rsplit("/", 1)[-1])


def go(page, path):
    """In-app navigation (the history the overlays' Back works on), then the chat drawn."""
    page.evaluate("p => { history.pushState(null, '', p); dispatchEvent(new PopStateEvent('popstate')); }", path)
    expect(page.locator("main [data-segment]").first).to_be_visible(timeout=10000)
    page.wait_for_timeout(400)


def touches(page):
    """Sends touch events through one CDP session (a touch has to start and end on the same one)."""
    cdp = page.context.new_cdp_session(page)

    def send(kind, points):
        cdp.send("Input.dispatchTouchEvent", {"type": kind, "touchPoints": [{"x": x, "y": y, "id": i} for i, (x, y) in enumerate(points)]})

    return send


def center(loc):
    b = loc.bounding_box()
    return b["x"] + b["width"] / 2, b["y"] + b["height"] / 2


def check_sizes(page, tag, body, secondary, group_min=None):
    """The answer's size, a tool row's and, on a touch screen, the work group header's height."""
    go(page, READ)
    fs = page.evaluate("() => getComputedStyle(document.querySelector('main [data-answer] .prose-j')).fontSize")
    if fs != body:
        fail(f"{tag}: the answer is {fs}, not {body}")
    # the row's words (since #10684 a command row leads with the command, "swift test"; its description "跑测试" is the
    # hover title), in the open once the group is open
    open_group(page)
    row = page.locator('main [data-action-row="Bash"] button[title="跑测试"] span', has_text="swift test").first.evaluate("e => getComputedStyle(e).fontSize")
    if row != secondary:
        fail(f"{tag}: a tool row is {row}, not {secondary}")
    # a table and a code block keep their size
    pre = page.evaluate("() => getComputedStyle(document.querySelector('main [data-answer] pre')).fontSize")
    if pre != "12.5px":
        fail(f"{tag}: a code block grew with the text ({pre})")
    ok(f"{tag}: the answer is {body}, a tool row {secondary}, code stays 12.5px")
    if group_min is not None:
        h = main_box(page, "[data-work-group] > button")["height"]
        if h < group_min:
            fail(f"{tag}: the work group header is {h:.0f} tall, under {group_min}")
        ok(f"{tag}: the work group header is {h:.0f} tall")


def open_group(page):
    """Opens the exchange's work group (it remembers being open across visits)."""
    head = page.locator("main [data-work-group] > button").first
    if head.get_attribute("aria-expanded") != "true":
        head.click()
    expect(page.locator("main [data-work-detail] [data-action-row]").first).to_be_visible()
    page.wait_for_timeout(350)  # the fold's opening


def main_box(page, sel):
    return page.locator("main " + sel).first.bounding_box()


def check_bubble(page, tag):
    """Your bubble leaves 12% on its left."""
    go(page, READ)
    sec = main_box(page, "[data-segment]")
    inner = page.locator("main [data-user-bubble] .bg-user-bubble").first.bounding_box()
    gap = inner["x"] - sec["x"]
    if gap < sec["width"] * 0.12 - 1:
        fail(f"{tag}: your bubble starts {gap:.0f}px in, under 12% of {sec['width']:.0f}")
    ok(f"{tag}: your bubble keeps {gap:.0f}px on its left")


def check_pending(page, tag):
    go(page, READ)
    long = "\n".join(f"第 {i} 行：这是一条很长的消息，发出去以后还没送到的时候也要收起来" for i in range(20))
    box = page.locator("main textarea").first
    box.fill(long)
    page.locator('main button[aria-label="发送"], main button[aria-label="排队发送"]').first.click()
    # the bubble being sent; its 发送中… is the chat's one send line under it since #7490
    pend = page.locator("main [data-user-bubble][data-send-state=sending]").last
    expect(pend).to_be_visible()
    expect(page.locator("main [data-message-feedback=sending]")).to_contain_text("发送中")
    folded = pend.locator("[data-folded]")
    expect(folded).to_have_count(1)
    expect(pend.get_by_role("button", name="展开")).to_be_visible()
    # the column the bubble sits in
    sec = pend.evaluate("e => { const r = e.parentElement.getBoundingClientRect(); return { x: r.x, width: r.width }; }")
    gap = folded.bounding_box()["x"] - sec["x"]
    if gap < sec["width"] * 0.12 - 1:
        fail(f"{tag}: the bubble being sent starts {gap:.0f}px in")
    ok(f"{tag}: a long message being sent is folded to ten lines with 展开, {gap:.0f}px in")
    shot(page, f"{tag}-pending")


def check_copy(page, tag):
    go(page, READ)
    btn = page.locator('main [data-answer] button[aria-label="复制代码"]').first
    expect(btn).to_be_visible()
    if float(btn.evaluate("e => getComputedStyle(e).opacity")) < 0.99:
        fail(f"{tag}: copy code is invisible on a touch screen")
    btn.tap()
    clip = page.evaluate("() => navigator.clipboard.readText()")
    if "forecast.hours.prefix(6)" not in clip:
        fail(f"{tag}: tapping copy code put {clip[:40]!r} on the clipboard")
    ok(f"{tag}: copy code shows without hover and a tap copies the block")


def check_math(page, tag):
    go(page, READ)
    expect(page.locator("main [data-answer] .katex").first).to_be_visible(timeout=8000)
    if page.locator("main [data-math-pending]").count():
        fail(f"{tag}: a formula is still TeX after KaTeX loaded")
    if page.locator("main [data-answer] .katex-display").count() != 1:
        fail(f"{tag}: the display formula is not drawn")
    ok(f"{tag}: KaTeX loads for the formulas and draws them")


def leave_end(page):
    """A finger pulls the chat down a little, slowly (no fling): the reader leaves the end, so the chat stops following
    it. A scroll from a script is not the reader leaving: content that grows still pulls the view back to the end."""
    sc = page.locator("main [data-chat-scroll]").bounding_box()
    x, y0 = sc["x"] + sc["width"] / 2, sc["y"] + 40
    cdp = page.context.new_cdp_session(page)
    cdp.send("Input.dispatchTouchEvent", {"type": "touchStart", "touchPoints": [{"x": x, "y": y0}]})
    for i in range(1, 9):
        cdp.send("Input.dispatchTouchEvent", {"type": "touchMove", "touchPoints": [{"x": x, "y": y0 + 10 * i}]})
        page.wait_for_timeout(16)
    page.wait_for_timeout(100)  # held still before lifting: no speed left for a fling
    cdp.send("Input.dispatchTouchEvent", {"type": "touchEnd", "touchPoints": []})
    cdp.detach()
    expect(page.locator("[data-jump-pill]")).to_be_visible()  # away from the end


def check_mermaid(page, tag):
    go(page, WIDGET)
    # each block is drawn on its own a moment after it mounts, each one making the chat taller; at the end the chat
    # follows, which moved the first diagram away under the tap about one run in three. Wait for all of them, leave the
    # end with a finger as a reader would, and only then bring the diagram into view.
    page.wait_for_function("""() => [...document.querySelectorAll('main [data-mermaid]')].every((m) =>
        m.querySelector('[data-mermaid-diagram] svg, details'))""", timeout=10000)
    leave_end(page)
    m = page.locator("main [data-mermaid]").first
    m.scroll_into_view_if_needed()
    diagram = m.locator("[data-mermaid-diagram]")
    expect(diagram.locator("svg").first).to_be_visible()
    tools = m.locator('button[aria-label="放大"]')
    if float(tools.evaluate("e => getComputedStyle(e.parentElement).opacity")) < 0.99:
        fail(f"{tag}: Mermaid's tools are invisible on a touch screen")
    diagram.tap()
    expect(page.get_by_role("dialog", name="图")).to_be_visible()
    page.go_back()
    expect(page.get_by_role("dialog", name="图")).to_have_count(0)
    if not same_chat(WIDGET, page.url):
        fail(f"{tag}: Back left the chat ({page.url})")
    ok(f"{tag}: Mermaid's tools show, a tap on the diagram opens it big and Back closes it")


def check_viewer(page, tag):
    """Each context opens the viewer cold, the first picture after a page load. That open is what broke when #8762 made
    the viewer load on demand: the picture never fitted, so a pinch did nothing, until #11511 fixed the viewer."""
    go(page, REVIEW)
    strip = page.locator("main [data-turn-images] button").first
    strip.scroll_into_view_if_needed()
    strip.tap()
    dlg = page.locator('[role=dialog][aria-modal="true"]')
    expect(dlg).to_be_visible()
    img = dlg.locator("img").first
    expect(img).to_be_visible()
    page.wait_for_timeout(400)
    # the bottom bar ("Download", "Copy path", "Open in Files" ...) fits the screen
    out = dlg.evaluate("""d => { const w = document.documentElement.clientWidth;
      return [...d.querySelectorAll(':scope > div:last-of-type button')].filter(b => { const r = b.getBoundingClientRect(); return r.left < 0 || r.right > w; }).length; }""")
    if out:
        fail(f"{tag}: {out} of the viewer's buttons run off the screen")
    cx, cy = center(img)
    cdp_touch = touches(page)
    cdp_touch("touchStart", [(cx - 30, cy), (cx + 30, cy)])
    for k in range(1, 9):
        cdp_touch("touchMove", [(cx - 30 - k * 12, cy), (cx + 30 + k * 12, cy)])
        page.wait_for_timeout(16)
    cdp_touch("touchEnd", [])
    page.wait_for_timeout(300)
    scale = float(img.evaluate("e => parseFloat(e.style.scale || '1')"))
    vv = page.evaluate("() => visualViewport.scale")
    if scale < 1.5:
        fail(f"{tag}: a pinch zoomed the picture only to {scale:.2f} (a cold first open, see check_viewer)")
    if abs(vv - 1) > 0.01:
        fail(f"{tag}: a pinch zoomed the page ({vv})")
    ok(f"{tag}: two fingers zoom the picture ({scale:.1f}×), the page stays at 1×")
    shot(page, f"{tag}-viewer-pinch")
    page.go_back()
    expect(dlg).to_have_count(0)
    page.wait_for_timeout(300)
    if not same_chat(REVIEW, page.url) or not page.locator("main [data-turn-images]").count():
        fail(f"{tag}: Back left the chat ({page.url})")
    ok(f"{tag}: Back closes the viewer and stays on the chat")


# Every frame from here on: the viewer's picture, once the browser has it and it shows, drawn at its fit in the room;
# and the frames its loading spinner shows in.
VIEWER_FRAMES = """() => {
  const px = (v) => parseFloat(v) || 0;
  const s = window.__viewerFrames = { seen: 0, wrong: [], spinning: 0 };
  const tick = () => {
    if (document.querySelector('[role=dialog][aria-modal="true"] [role=status]')) s.spinning++;
    const img = document.querySelector('[role=dialog][aria-modal="true"] img');
    if (img && img.complete && img.naturalWidth && getComputedStyle(img).visibility !== 'hidden') {
      const area = img.parentElement, cs = getComputedStyle(area);
      const rw = area.clientWidth - px(cs.paddingLeft) - px(cs.paddingRight), rh = area.clientHeight - px(cs.paddingTop) - px(cs.paddingBottom);
      const k = Math.min(rw / img.naturalWidth, rh / img.naturalHeight);
      s.seen++;
      if (Math.abs(px(img.style.width) - img.naturalWidth * k) > 1 || Math.abs(px(img.style.height) - img.naturalHeight * k) > 1)
        s.wrong.push(`${img.naturalWidth}x${img.naturalHeight} drawn ${img.style.width || 'at its own size'} in ${Math.round(rw)}x${Math.round(rh)}`);
    }
    requestAnimationFrame(tick);
  };
  requestAnimationFrame(tick);
}"""

# Each control of the viewer's top row and bottom bar: its drawn size, its touch square (the button grown by
# touch-hit's ::before), its row, and the points 21.5 px out from its middle (and the middle) a tap there misses.
VIEWER_HITS = """d => {
  const px = (v) => parseFloat(v) || 0;
  const rows = [d.querySelector(':scope > div:first-of-type'), d.querySelector(':scope > div:last-of-type')];
  return rows.map((row) => { const box = row.getBoundingClientRect();
    return [...row.querySelectorAll('button')].filter((b) => b.getClientRects().length).map((b) => {
      const r = b.getBoundingClientRect(), pb = getComputedStyle(b, '::before');
      const grown = pb.content !== 'none' && pb.position === 'absolute';
      const sq = grown ? { l: r.left + px(pb.left), t: r.top + px(pb.top), r: r.right - px(pb.right), b: r.bottom - px(pb.bottom) } : { l: r.left, t: r.top, r: r.right, b: r.bottom };
      const cx = r.left + r.width / 2, cy = r.top + r.height / 2;
      const miss = [[0, 0], [0, -21.5], [0, 21.5], [-21.5, 0], [21.5, 0]]
        .filter(([dx, dy]) => { const e = document.elementFromPoint(cx + dx, cy + dy); return !(e && b.contains(e)); }).map(([dx, dy]) => `${dx},${dy}`);
      return { name: b.getAttribute('aria-label') || b.textContent.trim(), w: r.width, h: r.height, sq, box: { l: box.left, t: box.top, r: box.right, b: box.bottom }, miss };
    }); });
}"""


def check_viewer_controls(page, tag):
    """The first picture opened after a page load (the viewer's chunk not fetched yet) is fitted to the screen from its
    first drawn frame, and so is the next one a swipe brings, neither with a loading spinner (#11511). With either (a file's four icon buttons below it,
    then a long name and two worded buttons), every control in the viewer's top row and bottom bar answers a 44 px
    square around its middle, inside its row and clear of its neighbours, and the top row's buttons stay round."""
    page.goto(S["base"] + "/overview?token=mock")
    page.wait_for_selector("aside", state="attached", timeout=15000)
    go(page, REVIEW)
    page.evaluate(VIEWER_FRAMES)
    strip = page.locator("main [data-turn-images] button").first
    strip.scroll_into_view_if_needed()
    strip.tap()
    dlg = page.locator('[role=dialog][aria-modal="true"]')
    img = dlg.locator("img").first
    expect(img).to_be_visible()
    page.wait_for_timeout(500)
    w, h = page.viewport_size["width"], page.viewport_size["height"]

    def fitted(what):
        frames = page.evaluate("() => window.__viewerFrames")
        if not frames["seen"]:
            fail(f"{tag}: {what} never showed")
        if frames["wrong"]:
            fail(f"{tag}: {what} was drawn at the wrong size in {len(frames['wrong'])} of {frames['seen']} frames ({frames['wrong'][0]})")
        if not img.evaluate("e => !!e.style.scale"):
            fail(f"{tag}: {what} is not fitted, so it cannot zoom")
        # the chat's pictures are in the page already: no spinner for them, not even for a frame
        if frames["spinning"]:
            fail(f"{tag}: the loading spinner showed for {what}, already loaded, in {frames['spinning']} frames")
        page.evaluate("() => { window.__viewerFrames.seen = 0; }")
        return frames["seen"]

    def controls(what):
        for row, buttons in zip(("top row", "bottom bar"), dlg.evaluate(VIEWER_HITS)):
            if not buttons:
                fail(f"{tag}: the viewer's {row} has no buttons with {what}")
            for i, c in enumerate(buttons):
                sq, box = c["sq"], c["box"]
                if row == "top row" and abs(c["w"] - c["h"]) > 0.5:
                    fail(f"{tag}: {c['name']} in the top row is squeezed to {c['w']:.0f}x{c['h']:.0f} with {what}")
                if sq["r"] - sq["l"] < 43.5 or sq["b"] - sq["t"] < 43.5:
                    fail(f"{tag}: {c['name']} in the {row} answers only {sq['r'] - sq['l']:.0f}x{sq['b'] - sq['t']:.0f} with {what}")
                if c["miss"]:
                    fail(f"{tag}: a tap at {c['miss']} from {c['name']}'s middle does not reach it with {what}")
                if sq["l"] < max(box["l"], 0) - 0.5 or sq["r"] > min(box["r"], w) + 0.5 or sq["t"] < box["t"] - 0.5 or sq["b"] > min(box["b"], h) + 0.5:
                    fail(f"{tag}: {c['name']}'s square leaves the {row} with {what} ({sq} in {box})")
                for o in buttons[i + 1:]:
                    if min(sq["r"], o["sq"]["r"]) - max(sq["l"], o["sq"]["l"]) > 0.5 and min(sq["b"], o["sq"]["b"]) - max(sq["t"], o["sq"]["t"]) > 0.5:
                        fail(f"{tag}: {c['name']}'s square overlaps {o['name']}'s in the {row} with {what}")
            ok(f"{tag}: with {what}, the viewer's {row} ({', '.join(c['name'] for c in buttons)}) answers 44 px squares, apart and inside it")

    ok(f"{tag}: the first picture after a load is fitted in each of its {fitted('the first picture')} frames")
    controls("the first picture")
    first = img.get_attribute("src")
    x, y = center(img)
    send = touches(page)
    send("touchStart", [(x + 90, y)])
    for k in range(1, 10):
        send("touchMove", [(x + 90 - k * 20, y)])
        page.wait_for_timeout(16)
    send("touchEnd", [])
    expect(dlg.locator('[aria-live="polite"]')).to_contain_text("2 / ")
    page.wait_for_timeout(500)
    if img.get_attribute("src") == first:
        fail(f"{tag}: a swipe did not bring the next picture")
    ok(f"{tag}: a swipe brings the next picture, fitted in each of its {fitted('the next picture')} frames")
    controls("the next picture")
    if S["shots"]:
        page.add_style_tag(content="[role=dialog] .touch-hit::before { outline: 1px solid rgb(255 64 64) }")
        shot(page, f"{tag}-viewer-hits")
    page.go_back()
    expect(dlg).to_have_count(0)
    page.wait_for_timeout(500)


def check_table(page, tag):
    """Every table's last column holds a few characters a line (it was one a line): the narrow table and the wide one
    that scrolls sideways alike, measured as its cell's text width over the font size, and its height in lines."""
    go(page, RICH)
    expect(page.locator("main .prose-j table").first).to_be_visible()
    cols = page.evaluate("""() => [...document.querySelectorAll('main .prose-j table')].map(t => {
      const td = t.querySelector('tbody tr:last-child td:last-child'); const cs = getComputedStyle(td);
      const inner = td.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
      const high = td.clientHeight - parseFloat(cs.paddingTop) - parseFloat(cs.paddingBottom);
      return { chars: inner / parseFloat(cs.fontSize), lines: high / parseFloat(cs.lineHeight) }; })""")
    if len(cols) < 2:
        fail(f"{tag}: expected the two tables, found {len(cols)}")
    for n, c in enumerate(cols):
        if c["chars"] < 3:
            fail(f"{tag}: table {n + 1}'s last column holds {c['chars']:.1f} characters a line ({c['lines']:.0f} lines)")
    ok(f"{tag}: the tables' last columns hold " + ", ".join(f"{c['chars']:.0f} characters a line ({c['lines']:.0f} lines)" for c in cols))
    shot(page, f"{tag}-table")


def check_long_press(page, tag):
    go(page, READ)
    a = page.locator("main [data-answer]").first
    a.scroll_into_view_if_needed()
    b = a.bounding_box()
    x, y = b["x"] + 40, b["y"] + 10
    cdp_touch = touches(page)
    cdp_touch("touchStart", [(x, y)])
    page.wait_for_timeout(650)
    cdp_touch("touchEnd", [])
    page.wait_for_timeout(400)
    menu = page.get_by_role("menu")
    if not menu.count() or not menu.first.is_visible():
        fail(f"{tag}: the long press menu closed when the finger lifted")
    ok(f"{tag}: a long press opens the menu and it stays after the finger lifts")
    shot(page, f"{tag}-menu")
    page.keyboard.press("Escape")
    expect(menu).to_have_count(0)
    page.wait_for_function("!history.state?.personaOverlay")  # its entry goes a tick later; the next go() would be the one popped


def check_revisit(page, tag):
    """A work group left open shows its steps when the chat comes back (it stayed at "Fetching N")."""
    go(page, READ)
    open_group(page)
    go(page, GROUPS)
    go(page, READ)
    expect(page.locator('main [data-work-detail] [data-action-row="Edit"]')).to_be_visible(timeout=5000)
    ok(f"{tag}: a work group left open has its steps when the chat comes back")


def check_diff(page, tag):
    """An edit's diff, in the Changes side panel since #9362 (the full-screen diff went with it): on a phone the panel
    covers the screen, the page never scrolls sideways (a long line scrolls inside the diff), and Back closes it on the
    chat."""
    go(page, READ)
    open_group(page)
    page.locator('main [data-action-row="Edit"] button').first.tap()
    panel = page.locator("aside[aria-label=变更]")
    expect(panel.locator("[data-changes-panel] [data-change-diff] [data-change-id]").first).to_be_visible()  # the edit drawn
    page.wait_for_timeout(350)  # its slide-in
    b = panel.bounding_box()
    cw = page.evaluate("document.documentElement.clientWidth")
    if abs(b["width"] - cw) > 1 or b["x"] > 1:
        fail(f"{tag}: the changes panel is {b['width']:.0f} wide at {b['x']:.0f}, not the screen ({cw})")
    if page.evaluate("document.documentElement.scrollWidth") > cw:
        fail(f"{tag}: the page scrolls sideways under the diff")
    shot(page, f"{tag}-diff")
    ok(f"{tag}: an edit's diff opens in the changes panel over the whole screen")
    page.go_back()
    expect(panel).to_have_count(0)
    if not same_chat(READ, page.url):
        fail(f"{tag}: Back left the chat")
    ok(f"{tag}: Back closes the diff on the chat")


# The chat's pills on a touch screen (#11511): "Reload" in the failed-refresh pill, "Jump to bottom" and "Back to where
# you were" answer a finger 20 px from their middle (touch-hit's 44 px), and their squares do not overlap.
PILLS = "/s/studio/a1f3c2d4-0000-4000-8000-000000000003"  # an Agent no other check opens: its items come from the route below
PILL_HITS = """(sels) => sels.map((sel) => {
  const b = document.querySelector(sel);
  if (!b) return { sel, missing: true };
  const r = b.getBoundingClientRect(), x = r.left + r.width / 2, y = r.top + r.height / 2;
  const on = (px, py) => document.elementFromPoint(px, py)?.closest('button') === b;
  const half = (n) => Math.max(n / 2, 22);
  return { sel, h: Math.round(r.height), above: on(x, y - 20), below: on(x, y + 20),
           sq: [x - half(r.width), y - half(r.height), x + half(r.width), y + half(r.height)] };
})"""


def check_chat_pills(page, tag):
    # twelve exchanges with more before them; the older page fails (a hub that lost its computer), so the pill shows
    n, last = 12, 900000
    items = []
    for k in range(n):
        seq = last - 2 * (n - k) + 1
        items += [{"id": f"pill-u{k}", "seq": seq, "ts": "2026-11-19T08:00:00Z", "role": "user", "kind": "text", "text": f"第 {k + 1} 个问题：这一块的写法有没有问题？"},
                  {"id": f"pill-a{k}", "seq": seq + 1, "ts": "2026-11-19T08:01:00Z", "role": "assistant", "kind": "text",
                   "text": "\n\n".join([f"第 {k + 1} 块看过了，命名可以再统一一点，测试也覆盖到了主要的分支。"] * 4)}]
    pattern = "**/api/v1/sessions/studio/a1f3c2d4-0000-4000-8000-000000000003/items?*"

    def serve(route):
        url = route.request.url
        if "before_seq=" in url:
            route.fulfill(status=502, json={"error": "node unreachable"})
        elif "turns=" in url:
            route.fulfill(json={"items": items, "has_more": True, "to_seq": last})
        else:
            route.fallback()
    page.route(pattern, serve)
    try:
        go(page, PILLS)
        page.wait_for_timeout(600)
        # leave the end with a finger, then up to the top: the older page is asked for, and fails
        send = touches(page)
        x, y = center(page.locator("[data-chat-scroll]"))
        send("touchStart", [(x, y - 100)])
        for i in range(1, 9):
            page.wait_for_timeout(16)
            send("touchMove", [(x, y - 100 + 30 * i)])
        send("touchEnd", [])
        page.wait_for_timeout(400)
        page.evaluate("document.querySelector('[data-chat-scroll]').scrollTop = 0")
        expect(page.locator("[data-testid=chat-not-live] button")).to_be_visible(timeout=10000)
        expect(page.locator("[data-jump-pill]")).to_be_visible()
        page.wait_for_timeout(300)  # the pills' fade-up
        shot(page, f"{tag}-pills")
        reload, jump = "[data-testid=chat-not-live] button", "[data-jump-pill]"
        for d in page.evaluate(PILL_HITS, [reload, jump]):
            if d.get("missing") or not (d["above"] and d["below"]):
                fail(f"{tag}: {d['sel']} ({d.get('h')} tall) does not answer 20 px from its middle ({d})")
        # a search hit gone to: "Back to where you were" over the chat's top edge, the failed-refresh pill under it
        page.locator("header").get_by_role("button", name="更多操作", exact=True).tap()
        page.get_by_role("menuitem", name="会话内搜索").tap()
        page.locator("[data-chat-search] input").fill("第 1 个问题")
        page.locator("[data-search-result]").first.tap()
        expect(page.locator("[data-search-return]")).to_be_visible()
        # The hit is the first exchange, at the top. Opening the search re-read the newest page, which took the error
        # away and dropped the older page's waiting retry (#11522); the failed page is asked for again only on a scroll
        # once its wait (3 s) is over: the reader moves a pixel at a time until it has failed again and the pill is back.
        for i in range(30):
            if page.locator(reload).is_visible():
                break
            page.evaluate(f"document.querySelector('[data-chat-scroll]').scrollTop += {1 if i % 2 else -1}")
            page.wait_for_timeout(500)
        expect(page.locator(reload)).to_be_visible()
        page.wait_for_timeout(300)
        hits = page.evaluate(PILL_HITS, [reload, "[data-search-return]", jump])
        for d in hits:
            if d.get("missing") or not (d["above"] and d["below"]):
                fail(f"{tag}: with the return pill up, {d['sel']} does not answer 20 px from its middle ({d})")
        for i, a in enumerate(hits):
            for b in hits[i + 1:]:
                ox = min(a["sq"][2], b["sq"][2]) - max(a["sq"][0], b["sq"][0])
                oy = min(a["sq"][3], b["sq"][3]) - max(a["sq"][1], b["sq"][1])
                if ox > 0.5 and oy > 0.5:
                    fail(f"{tag}: the touch squares of {a['sel']} and {b['sel']} overlap ({a['sq']}, {b['sq']})")
        shot(page, f"{tag}-pills-return")
        ok(f"{tag}: Reload ({hits[0]['h']} tall), Back to where you were and Jump to bottom answer a finger 20 px from "
           "their middle, their squares apart")
    finally:
        page.unroute(pattern)


def phone(browser, name, theme, full):
    w, h = PHONES[name]
    tag = f"{name}-{theme}"
    ctx, page = open_page(browser, w, h, theme)
    body = "16px" if w < 640 else "15px"
    check_sizes(page, tag, body, "15px" if w < 640 else "14px", group_min=40)
    shot(page, f"{tag}-read")
    check_bubble(page, tag)
    go(page, GROUPS)
    shot(page, f"{tag}-groups")
    check_table(page, tag)
    if full:
        check_copy(page, tag)
        check_math(page, tag)
        check_mermaid(page, tag)
        check_viewer(page, tag)
        check_long_press(page, tag)
        check_revisit(page, tag)
        check_diff(page, tag)
        check_pending(page, tag)
    check_viewer_controls(page, tag)
    if name != "land": check_chat_pills(page, tag)
    ctx.close()


def desktop(browser, theme):
    """A mouse at 1280: the sizes and the header as before."""
    tag = f"desktop-{theme}"
    ctx, page = open_page(browser, 1280, 820, theme, phone=False)
    check_sizes(page, tag, "15px", "14px")
    h = main_box(page, "[data-work-group] > button")
    if h and h["height"] >= 32:
        fail(f"{tag}: the work group header grew on a desktop ({h['height']:.0f})")
    # a code block's Copy sits in its language bar, always shown (#3210: the corner button that waited for hover is gone)
    btn = page.locator('main [data-answer] [data-code-bar] button[aria-label="复制代码"]').first
    if float(btn.evaluate("e => getComputedStyle(e).opacity")) < 0.99:
        fail(f"{tag}: copy code is not shown in the code bar on a desktop")
    ok(f"{tag}: header {h['height']:.0f} tall, copy code in the code bar")
    shot(page, f"{tag}-read")
    ctx.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--shots")
    a = ap.parse_args()
    S["base"] = a.base.rstrip("/")
    S["shots"] = a.shots
    if a.shots:
        os.makedirs(a.shots, exist_ok=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(channel="chrome")
        try:
            for theme in ("light", "dark"):
                phone(browser, "390", theme, full=True)
                phone(browser, "360", theme, full=False)
                phone(browser, "land", theme, full=theme == "dark")
                desktop(browser, theme)
        finally:
            browser.close()
    print("PASS", flush=True)


if __name__ == "__main__":
    main()
