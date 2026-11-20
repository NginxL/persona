#!/usr/bin/env python3
"""End-to-end check of the chat's composer area on a phone (#1996): with three cards waiting the conversation still
shows and the composer stays on screen (the first card open, its "1 / 3" opening the others (#8928), one line while
typing); a short viewport (a keyboard on Android) and a twenty-line draft keep the send button on screen; the iOS
keyboard moves the side panel with it (--kb-top / --kb-h); Back closes a full-screen side panel and stays on the
conversation; the header leaves the title room at 360; on a phone on its side the run's status and the queue share a
line. Since #11442 also: a waiting card's own field typed into with the keyboard up stays in sight (it
needs --hub: the card comes from the fake hub's /mock routes, which Vite does not proxy); a Feishu chat keeps its newest
message in view as the keyboard and a long draft shorten it; a tap on an attached picture's middle opens it and its ✕
answers only at its corner; toasts stay in the visible frame, clear of the composer and a sheet's footer, and a sheet's
focused field stays in view.

Needs a running web page against the fake hub with the crowded conversation, and Playwright for Python (it drives the
installed Chrome):

    MOCK_SPLIT_TABS=1 MOCK_CHAT_CARDS=1 MOCK_FILES=1 PORT=18961 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18961 npx vite --port 5961 --strictPort &)
    uv run --no-project --with playwright==1.58.0 python3 scripts/test/web_mobile_input_e2e.py http://127.0.0.1:5961 \
        --hub http://127.0.0.1:18961 [--shots <dir>]

Prints one line per check and a verdict; exits 1 on the first failure. --shots saves 390, 360 and 844×390 screenshots,
light and dark.
"""
import argparse
import json
import os
import sys
from urllib.parse import quote

from playwright.sync_api import expect, sync_playwright

CLAUDE = "5b1170b5-0000-4000-8000-000000000001"  # working, three cards, two queued (MOCK_CHAT_CARDS)
PANE = "/t/server/w1/" + quote("w1:t1", safe="") + "/" + CLAUDE
UA = ("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 "
      "(KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1")

# iOS: the keyboard shrinks the visual viewport only; a stand-in that the page's listeners follow.
FAKE_VV = """(() => {
  const et = new EventTarget();
  const vv = { height: innerHeight, width: innerWidth, offsetTop: 0, offsetLeft: 0, scale: 1, pageTop: 0,
    addEventListener: et.addEventListener.bind(et), removeEventListener: et.removeEventListener.bind(et) };
  Object.defineProperty(window, 'visualViewport', { get: () => vv });
  window.__kb = (h, top = 0) => { vv.height = h; vv.offsetTop = top; et.dispatchEvent(new Event('resize')); };
})();"""

BOX = """(sel) => { const e = document.querySelector(sel); if (!e) return null; const r = e.getBoundingClientRect();
  return { top: r.top, bottom: r.bottom, left: r.left, right: r.right, width: r.width, height: r.height, inner: innerHeight }; }"""

ERRORS = []  # uncaught errors on any page: the run fails on them
COMPOSER = "main textarea[aria-label=消息]"
SEND = "main [aria-label=发送], main [aria-label=中断]"


def ok(msg):
    print("ok  " + msg, flush=True)


def fail(msg):
    print("FAIL " + msg, flush=True)
    sys.exit(1)


def box(page, sel):
    b = page.evaluate(BOX, sel)
    if not b:
        fail(f"no {sel}")
    return b


def on_screen(page, sel, what):
    b = box(page, sel)
    if b["top"] < 0 or b["bottom"] > b["inner"] + 0.5:
        fail(f"{what}: {sel} is off screen ({b})")
    return b


def phone(browser, w, h, theme="light", ios=False):
    ctx = browser.new_context(viewport={"width": w, "height": h}, device_scale_factor=3, is_mobile=True, has_touch=True,
                              user_agent=UA, color_scheme=theme)
    if ios:
        ctx.add_init_script(FAKE_VV)
    page = ctx.new_page()
    page.on("pageerror", lambda e: ERRORS.append(str(e)))
    return ctx, page


def login(page, base):
    """Signs in; the theme follows the context's color scheme (the page's own setting is left at "Match system")."""
    page.goto(base + "/overview?token=mock")
    page.wait_for_selector("aside", state="attached", timeout=15000)


def go(page, path):
    """In-page navigation (a pushState and its popstate), as a link in the app would."""
    page.evaluate(f"history.pushState(null, '', {json.dumps(path)}); dispatchEvent(new PopStateEvent('popstate'))")


def open_pane(page, base, cards=True):
    """The crowded conversation, settled (its cards' box only exists on a narrow screen)."""
    login(page, base)
    go(page, PANE)
    page.wait_for_selector(COMPOSER)
    if cards:
        page.wait_for_selector("[data-chat-cards]", state="attached")
    page.wait_for_timeout(700)


def chat_height(page):
    return page.evaluate("document.querySelector('[data-chat-scroll]').clientHeight")


POSITION = "[data-chat-cards-position][aria-expanded=false]"  # the first card's "1 / 3" (#8928), which opens the others
FOLD = "[data-chat-cards-fold]"  # the one line in their place (typing, or a phone on its side)


def crowded(page, what, min_chat=0.25, fold=None):
    """Three cards: the first open with its "1 / 3" in sight (or, with `fold`, all of them in one line); the
    conversation still shows, the composer is on screen, the dock within about half."""
    sel, label = (FOLD, fold) if fold else (POSITION, "1 / 3")
    expect(page.locator(sel)).to_contain_text(label)
    # not scrolled out of a box or covered: a finger on its middle lands on it
    if not page.evaluate("""(sel) => { const e = document.querySelector(sel); const r = e.getBoundingClientRect();
        return e.contains(document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)); }""", sel):
        fail(f"{what}: {label} is out of sight")
    inner = page.evaluate("innerHeight")
    dock = box(page, "[data-chat-dock]")
    if dock["height"] > inner * 0.55 + 1:
        fail(f"{what}: the dock is {dock['height']:.0f} of {inner} px")
    chat = chat_height(page)
    if chat < inner * min_chat:
        fail(f"{what}: the conversation has {chat} of {inner} px")
    on_screen(page, COMPOSER, what)
    on_screen(page, SEND, what)
    ok(f"{what}: three cards, {label}; the chat keeps {chat} px, the composer on screen")


def title_room(browser, base, shots):
    """At 360 the header leaves the title some ten characters (#2029), with no state words in it (#5421). It caught
    #9541's Conversation tasks button, shown in the header at every width: the title kept about eight characters at
    360 until #11511 took that button off the phone's header."""
    for theme in ("light", "dark"):
        ctx, page = phone(browser, 360, 740, theme)
        open_pane(page, base)
        h1 = box(page, "main header h1")
        size = page.evaluate("parseFloat(getComputedStyle(document.querySelector('main header h1')).fontSize)")
        if h1["width"] < size * 9.5:
            fail(f"360: the title gets {h1['width']:.0f} px ({h1['width'] / size:.1f} characters), under 9.5 (see title_room)")
        if page.locator("main header").get_by_text("需要你").count() or page.locator("main header").get_by_text("工作中").count():
            fail("360: the header says no state (#5421, cindy's: the running bar and the sidebar say it)")
        if page.locator("main header").get_by_text("同空间").count():
            fail("360: 同空间 is a row of ⋯, not a header button (#5421)")
        crowded(page, f"360×740 {theme}")
        if theme == "light":
            ok(f"360: the title gets {h1['width']:.0f} px ({h1['width'] / size:.1f} characters)")
        if shots:
            page.screenshot(path=os.path.join(shots, f"360-{theme}.png"))
            page.locator("main header").screenshot(path=os.path.join(shots, f"360-header-{theme}.png"))
        ctx.close()


def card_field_keyboard(browser, base, hub, shots):
    """The Assistant's question card (#11442): its own field, typed into with the keyboard up, stays in sight and takes a
    finger over the composer at 390 and 360, with iOS's keyboard (the visual viewport shrinks) and Android's (Chrome
    resizes the page); with the keyboard down or the focus gone the dock keeps its cap."""
    import urllib.request
    if not hub:
        fail("card field: pass --hub <the fake hub's URL> (the card comes from its /mock/assistant_ask)")
    hit = """() => { const f = document.activeElement, c = document.querySelector('main textarea[aria-label=消息]');
      const at = (e) => { const r = e.getBoundingClientRect(); return document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2); };
      const fr = f.getBoundingClientRect(), cr = c.getBoundingClientRect();
      return { field: Boolean(f.closest('[data-pending-card]')) && at(f) === f, top: fr.top, bottom: fr.bottom, composer: at(c) === c,
        composerBottom: cr.bottom, dock: document.querySelector('[data-chat-dock]').getBoundingClientRect().toJSON() }; }"""
    def keyboard(page, ios, w, vh):
        if ios:
            page.evaluate(f"window.__kb({vh})")
        else:
            page.set_viewport_size({"width": w, "height": vh})

    for ios, w, h, kb in ((True, 390, 844, 508), (True, 360, 740, 440), (False, 390, 844, 508), (False, 360, 740, 440)):
        what = f"{'iOS' if ios else 'Android'} {w}"
        # Persona's main conversation asks; the card is answered at the end whatever happens (/mock/reset keeps it)
        ix = json.load(urllib.request.urlopen(hub + "/mock/assistant_ask?session=b2e4d6f8-0000-4000-8000-000000000001"))["id"]
        ctx, page = phone(browser, w, h, ios=ios)
        try:
            login(page, base)
            go(page, "/")
            field = page.locator("[data-pending-card]").get_by_role("textbox", name="自定义回答").first
            field.tap()
            for vh in (h - 100, h - 200, kb):  # the keyboard slides up
                keyboard(page, ios, w, vh)
                page.wait_for_timeout(120)
            page.keyboard.type("Next Monday, after the review")
            page.wait_for_timeout(500)
            at = page.evaluate(hit)
            if not at["field"] or at["top"] < 0 or at["bottom"] > kb + 0.5:
                fail(f"{what}: the card's field is not in sight over the keyboard ({at})")
            if not at["composer"] or at["composerBottom"] > kb + 0.5 or at["dock"]["bottom"] > kb + 0.5:
                fail(f"{what}: the composer left the screen while typing in the card ({at})")
            if shots:
                page.screenshot(path=os.path.join(shots, f"card-field-{'ios' if ios else 'android'}-{w}.png"),
                                clip={"x": 0, "y": 0, "width": w, "height": kb})
            keyboard(page, ios, w, h)  # the keyboard down, the field still focused
            page.wait_for_timeout(400)
            if page.evaluate(hit)["dock"]["height"] > h * 0.55 + 1:
                fail(f"{what}: with the keyboard down the dock is over its cap")
            keyboard(page, ios, w, kb)
            page.evaluate("document.activeElement.blur()")
            page.wait_for_timeout(400)
            if box(page, "[data-chat-dock]")["height"] > kb * 0.55 + 1:
                fail(f"{what}: the focus gone, the dock is over its cap")
            # answered from the card with the keyboard up: its arrow takes the focus on the tap, and the card (on a page
            # the keyboard made short, Android at 360) must still be there for the click
            keyboard(page, ios, w, h)
            page.wait_for_timeout(400)
            field.tap()
            for vh in (h - 100, h - 200, kb):
                keyboard(page, ios, w, vh)
                page.wait_for_timeout(120)
            page.wait_for_timeout(400)
            page.locator("[data-pending-card]").get_by_role("button", name="发送回答").first.tap()
            for _ in range(25):
                if any(r["id"] == ix for r in json.load(urllib.request.urlopen(hub + "/mock/resolved"))["resolved"]):
                    break
                page.wait_for_timeout(200)
            else:
                fail(f"{what}: tapping the card's arrow with the keyboard up sent no answer")
            ok(f"{what}: the card's own field stays in sight and takes a tap over the keyboard, the composer on screen; "
               "the cap again with the keyboard down or the focus gone; its arrow answers it")
        finally:
            ctx.close()
            urllib.request.urlopen(urllib.request.Request(
                f"{hub}/api/v1/interactions/{ix}/resolve", data=b"{}", method="POST",
                headers={"Authorization": "Bearer mock", "Content-Type": "application/json"})).read()


LARK_END = """() => { const el = [...document.querySelectorAll('.overflow-y-auto')].find((e) => e.querySelector('[data-mid]'));
  const last = [...el.querySelectorAll('[data-mid]')].pop().getBoundingClientRect(); const box = el.getBoundingClientRect();
  return { top: el.scrollTop, fromEnd: el.scrollHeight - el.clientHeight - el.scrollTop, last: last.bottom, bottom: box.bottom, boxTop: box.top }; }"""


def lark_keyboard(browser, base, shots):
    """A Feishu chat at its newest message keeps it in view as the iOS keyboard comes up and a five-line draft grows the
    box; scrolled up, the same changes leave the list where the reader put it."""
    lark = "main textarea[aria-label=飞书消息]"
    draft = "\n".join(f"第 {i} 行" for i in range(1, 6))
    for w, h in ((390, 844), (360, 740)):
        ctx, page = phone(browser, w, h, ios=True)
        login(page, base)
        go(page, "/inbox/lark%3Af1%3A100")
        page.wait_for_selector("[data-mid]")
        page.wait_for_timeout(700)

        def at_end(what):
            s = page.evaluate(LARK_END)
            if s["last"] > s["bottom"] + 1 or s["last"] <= s["boxTop"]:
                fail(f"{w}: {what}: the newest message is out of view ({s})")
            return s

        page.locator(lark).tap()
        page.evaluate("__kb(450, 0)")
        page.wait_for_timeout(400)
        at_end("the keyboard up")
        if shots:
            page.screenshot(path=os.path.join(shots, f"lark-kb-{w}.png"), clip={"x": 0, "y": 0, "width": w, "height": 450})
        page.locator(lark).fill(draft)
        page.wait_for_timeout(400)
        at_end("a five-line draft")
        # scrolled up, a longer draft does not pull the list back to its end
        page.locator(lark).fill("")
        page.wait_for_timeout(300)
        page.evaluate("(() => { const el = [...document.querySelectorAll('.overflow-y-auto')].find((e) => e.querySelector('[data-mid]')); el.scrollTop -= 150; })()")
        page.wait_for_timeout(300)
        up = page.evaluate(LARK_END)
        page.locator(lark).fill(draft)
        page.wait_for_timeout(400)
        moved = page.evaluate(LARK_END)
        if abs(moved["top"] - up["top"]) > 1 or moved["fromEnd"] < 150:
            fail(f"{w}: scrolled up, the draft moved the list ({up} -> {moved})")
        ctx.close()
        ok(f"{w}: the Feishu chat keeps its newest message in view with the keyboard up and a five-line draft; scrolled up, it stays")


# What a finger finds on an attachment tile: at its middle, at its top-right corner and 7 px above it (past the ✕'s reach
# into the 6 px gap over a row of tiles), and how far the ✕ answers along the lines through its middle.
TILE_HITS = """(tile) => {
  const x = tile.querySelector('[aria-label^="移除"], [data-remove]'), edit = tile.querySelector('button:has(img)');
  const at = (px, py) => { const e = document.elementFromPoint(px, py); return !e ? '-' : x.contains(e) ? 'remove' : edit?.contains(e) ? 'edit' : e.tagName.toLowerCase(); };
  const r = tile.getBoundingClientRect(), b = x.getBoundingClientRect(), cx = b.left + b.width / 2, cy = b.top + b.height / 2;
  const run = (dx, dy) => { let n = 0; while (n < 60 && at(cx + dx * (n + 1), cy + dy * (n + 1)) === 'remove') n++; return n; };
  return { centre: at(r.left + r.width / 2, r.top + r.height / 2), corner: at(r.right - 3, r.top + 3), above: at(cx, r.top - 7),
    w: run(-1, 0) + run(1, 0) + 1, h: run(0, -1) + run(0, 1) + 1, mid: [r.left + r.width / 2, r.top + r.height / 2] };
}"""


def tile_hits(browser, base, shots):
    """An attachment's ✕ (#11442): a tap on a picture's middle opens the editor, not remove; the ✕ answers out of its
    corner but not over what sits above the tray (the composer, the New Agent sheet); a file's ✕ answers down its column
    and the file row scrolls no further for it."""
    import struct
    import zlib

    def chunk(kind, data):
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF)

    def png(rgb):
        """A 64 px square of one colour."""
        raw = b"".join(b"\0" + bytes(rgb) * 64 for _ in range(64))
        data = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", 64, 64, 8, 2, 0, 0, 0)) + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b"")
        return {"name": "picture.png", "mimeType": "image/png", "buffer": data}

    def text(name):
        return {"name": name, "mimeType": "text/plain", "buffer": b"hello\n"}

    ctx, page = phone(browser, 390, 844)
    open_pane(page, base, cards=False)
    page.locator("main input[type=file]").first.set_input_files([png((220, 60, 60))])
    pic = page.locator("main [data-chat-dock] .group\\/att:has(img)")
    expect(pic).to_have_count(1)
    page.wait_for_timeout(400)  # its pop-in
    h = pic.evaluate(TILE_HITS)
    if h["centre"] != "edit" or h["corner"] != "remove":
        fail(f"a picture's middle should open it and its corner remove it: {h}")
    if h["above"] == "remove":
        fail(f"the picture's ✕ answers 7 px above the tray, over what sits there: {h}")
    if h["w"] < 36 or h["h"] < 32:
        fail(f"the picture's ✕ answers only {h['w']}×{h['h']} px")
    page.touchscreen.tap(*h["mid"])
    editor = page.get_by_role("dialog", name="编辑图片")
    expect(editor).to_be_visible()
    expect(pic).to_have_count(1)
    ok(f"a tap on a picture's middle opens the editor; its ✕ answers {h['w']}×{h['h']} px out of the corner, not above the tray")
    editor.get_by_role("button", name="取消").click()
    expect(editor).to_have_count(0)
    page.wait_for_timeout(500)

    # files: two overflow the row at 390, which must scroll no further for the ✕
    page.locator("main input[type=file]").first.set_input_files([text("a.txt"), text("b.txt")])
    tiles = page.locator("[data-testid=composer-files] > span")
    expect(tiles).to_have_count(2)
    page.wait_for_timeout(400)
    f = tiles.first.evaluate(TILE_HITS)
    if f["corner"] != "remove" or f["centre"] == "remove":
        fail(f"a file's ✕ should answer at its corner and leave its middle: {f}")
    if f["w"] < 28 or f["h"] < 44:
        fail(f"a file's ✕ answers only {f['w']}×{f['h']} px")
    row = page.locator("[data-testid=composer-files]").evaluate("""(e) => { const t = [...e.children].map((c) => c.getBoundingClientRect());
      return [e.scrollWidth, t[t.length - 1].right - t[0].left]; }""")
    if row[0] > row[1] + 0.5:
        fail(f"the file row scrolls {row[0]} px for {row[1]:.0f} px of tiles")
    if shots:
        card = page.locator(COMPOSER).evaluate("(e) => { const r = e.closest('.rounded-\\\\[14px\\\\]').getBoundingClientRect(); return [r.top, r.height]; }")
        page.screenshot(path=os.path.join(shots, "390-attachments.png"), clip={"x": 0, "y": max(0, card[0] - 20), "width": 390, "height": card[1] + 40})
    ok(f"a file's ✕ answers {f['w']}×{f['h']} px down its column; the row scrolls {row[0]} px, its tiles' width")

    # the New Agent sheet: no editor, the middle still is not remove, nor is the sheet above the tray
    page.evaluate("import('/src/lib/new-agent.ts').then((m) => m.openNewAgent())")
    sheet = page.get_by_role("dialog", name="新建 Agent", exact=True)
    expect(sheet).to_be_visible()
    sheet.locator("input[type=file]").first.set_input_files([png((60, 90, 220))])
    pic = sheet.locator(".group\\/att:has(img)")
    expect(pic).to_have_count(1)
    pic.scroll_into_view_if_needed()
    page.wait_for_timeout(400)
    n = pic.evaluate(TILE_HITS)
    if n["centre"] == "remove" or n["corner"] != "remove" or n["above"] == "remove":
        fail(f"New Agent: the picture's ✕ should answer at its corner only: {n}")
    ok(f"New Agent: the picture's ✕ answers {n['w']}×{n['h']} px at its corner, not its middle or above the tray")
    ctx.close()


# toast() from the page's own module (a fresh dev server: one edited since serves the app another copy of it)
TOAST = "async (tone) => { const m = await import('/src/ui/atoms.tsx'); m.toast(tone === 'error' ? '没复制上' : '已复制', tone); }"
TOAST_ROWS = """() => [...document.querySelectorAll('[aria-live=polite] > div:not([data-closing])')].map((e) => {
  const r = e.getBoundingClientRect(); return { top: r.top, bottom: r.bottom, left: r.left }; })"""
# whether a finger on the middle of each control lands on it (not on a toast drawn over it)
HITS_SELF = """(sels) => sels.filter((s) => { const b = document.querySelector(s); if (!b) return true; const r = b.getBoundingClientRect();
  const e = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2); return !(e && (b === e || b.contains(e))); })"""
CONTROLS = [SEND, "main [aria-label=添加文件、图片等]", "main [aria-label^=权限模式]", "main [aria-label^=选择模型]"]


def toasts_phone(browser, base):
    """Toasts on a phone (#11442): in the visible frame, clear of the composer's controls and of a sheet's Cancel and
    Start; one a finger tapped still goes; the New Agent sheet's message box stays in sight with the keyboard up;
    a wide screen keeps them at the bottom as before."""
    ctx, page = phone(browser, 390, 844, ios=True)
    open_pane(page, base)
    page.evaluate(TOAST, "error")
    page.wait_for_timeout(400)
    dock = box(page, "[data-chat-dock]")
    rows = page.evaluate(TOAST_ROWS)
    if not rows or rows[-1]["bottom"] > dock["top"] + 0.5:
        fail(f"390: the toast {rows} is not above the dock (top {dock['top']:.0f})")
    covered = page.evaluate(HITS_SELF, CONTROLS)
    if covered:
        fail(f"390: a toast covers {covered}")
    ok(f"390: the toast sits at {rows[-1]['top']:.0f}-{rows[-1]['bottom']:.0f}, over the dock; send, +, permissions and the model hit themselves")
    page.locator(COMPOSER).tap()
    page.evaluate("__kb(470)")
    page.wait_for_timeout(300)
    page.evaluate(TOAST, "info")
    page.wait_for_timeout(400)
    rows, top = page.evaluate(TOAST_ROWS), box(page, COMPOSER)["top"]
    if any(r["top"] < 0 or r["bottom"] > 470.5 or r["bottom"] > top for r in rows):
        fail(f"keyboard at 470: the toasts {rows} are not in [0, 470] above the composer (top {top:.0f})")
    covered = page.evaluate(HITS_SELF, CONTROLS)
    if covered:
        fail(f"keyboard at 470: a toast covers {covered}")
    ok(f"keyboard at 470: {len(rows)} toasts in the visible frame above the composer, the controls hit themselves")
    page.evaluate("__kb(844)")
    page.locator(COMPOSER).blur()
    page.wait_for_function("!document.querySelector('[aria-live=polite] > div')", timeout=9000)
    # a finger's tap on a toast does not hold it: the tap's compatibility mouseover gets no mouseout. On its icon, far
    # from the close button (the browser's tap adjustment would snap a tap near it onto it, closing the toast).
    go(page, "/overview")
    page.wait_for_timeout(500)
    page.evaluate(TOAST, "error")
    page.wait_for_timeout(400)
    r = page.evaluate(TOAST_ROWS)[-1]
    page.touchscreen.tap(r["left"] + 16, (r["top"] + r["bottom"]) / 2)
    page.wait_for_timeout(1000)
    if not page.evaluate(TOAST_ROWS):
        fail("tapping a toast's icon closed it")
    try:
        page.wait_for_function("!document.querySelector('[aria-live=polite] > div')", timeout=6000)
    except Exception:
        fail("a toast a finger tapped is still there 7 s later")
    ok("a toast a finger tapped still goes after its time")

    # the New Agent sheet: Cancel and Start stay reachable under a toast; its message box in sight with the keyboard up
    page.get_by_role("button", name="新建 Agent").last.tap()
    page.wait_for_selector("[role=dialog]")
    page.wait_for_timeout(600)  # the sheet's slide
    page.evaluate(TOAST, "error")
    page.wait_for_timeout(400)
    foot = box(page, "[role=dialog] [data-dialog-footer]")
    rows = page.evaluate(TOAST_ROWS)
    if rows[-1]["bottom"] > foot["top"] + 0.5:
        fail(f"New Agent: the toast {rows} is not above the sheet's footer (top {foot['top']:.0f})")
    covered = page.evaluate("""() => [...document.querySelectorAll('[role=dialog] [data-dialog-footer] button')].filter((b) => {
      const r = b.getBoundingClientRect(); const e = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !(e && (b === e || b.contains(e))); }).map((b) => b.textContent.trim())""")
    if covered:
        fail(f"New Agent: a toast covers {covered}")
    ok("New Agent: the toast sits over the footer; 取消 and 启动 hit themselves")
    inside = """() => { const d = document.querySelector('[role=dialog]').getBoundingClientRect(); const f = document.activeElement;
      const r = f.getBoundingClientRect(); return { tag: f.tagName, field: [r.top, r.bottom], sheet: [d.top, d.bottom],
      inside: r.top >= d.top - 0.5 && r.bottom <= d.bottom + 0.5 }; }"""
    page.locator("[role=dialog] textarea").first.tap()
    page.wait_for_timeout(300)
    for kb in ("500", "500, 300"):
        page.evaluate("document.querySelector('[role=dialog] .overflow-y-auto').scrollTop = 0")
        page.evaluate(f"__kb({kb})")
        page.wait_for_timeout(400)
        res = page.evaluate(inside)
        if res["tag"] != "TEXTAREA" or not res["inside"]:
            fail(f"New Agent with __kb({kb}): the message box is out of the sheet ({res})")
        page.evaluate("__kb(844)")
        page.wait_for_timeout(300)
    ok("New Agent: with the keyboard up (__kb(500), __kb(500, 300)) the focused message box stays inside the sheet")
    page.keyboard.press("Escape")
    page.wait_for_selector("[role=dialog]", state="detached")
    page.wait_for_timeout(500)
    ctx.close()

    ctx = browser.new_context(viewport={"width": 1280, "height": 900})
    page = ctx.new_page()
    page.on("pageerror", lambda e: ERRORS.append(str(e)))
    open_pane(page, base, cards=False)
    page.evaluate(TOAST, "error")
    page.wait_for_timeout(400)
    r = page.evaluate(TOAST_ROWS)[-1]
    if abs(r["bottom"] - (900 - 20)) > 0.5:
        fail(f"1280: the toast should stay 20 px over the bottom ({r})")
    ok("1280: the toast stays at the bottom, as before")
    ctx.close()


HANDOVER = """() => { const d = document.querySelector('[role=dialog]'); const r = (e) => e.getBoundingClientRect();
  const foot = d.querySelector('[data-dialog-footer]'); const go = foot && [...foot.querySelectorAll('button')].pop();
  const hit = (e) => { const b = r(e); return e.contains(document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2)); };
  const tabs = [...d.querySelectorAll('.bg-field.p-0\\\\.5 > button')]; const x = d.querySelector('button[aria-label="关闭"]');
  return { vv: visualViewport.height, go: go && { text: go.textContent.trim(), top: r(go).top, bottom: r(go).bottom, hit: hit(go) },
    tabs: tabs.map((t) => Math.round(r(t).height)), x: x && hit(x),
    radios: [...d.querySelectorAll('[role=radiogroup] [role=radio]')].map((e) => [e.tagName, e.getAttribute('aria-checked'), Math.round(r(e).height)]) }; }"""


def handover_keyboard(browser, base, shots):
    """The to-do hand-over dialog with the keyboard up (#11511): its Cancel and primary are the sheet's footer, so the
    primary stays in what the keyboard leaves (iOS at 390 with __kb(508), Android's resize to 440 at 360); its tabs take
    a finger and the title's ✕ still answers; the Existing agent rows are radios a finger can take."""
    for w, h, ios in ((390, 844, True), (360, 740, False)):
        ctx, page = phone(browser, w, h, ios=ios)
        login(page, base)
        go(page, "/todos?id=todo_weather")
        page.get_by_role("button", name="交给 Agent", exact=True).first.tap()
        page.wait_for_selector("[role=dialog] textarea")
        page.wait_for_timeout(500)  # the sheet's slide
        page.locator("[role=dialog] textarea").tap()
        if ios:
            page.evaluate("__kb(508)")
        else:
            page.set_viewport_size({"width": w, "height": 440})
        page.wait_for_timeout(700)
        res = page.evaluate(HANDOVER)
        go_ = res["go"]
        if not go_ or go_["text"] != "启动并交给 Agent" or go_["top"] < 0 or go_["bottom"] > res["vv"] + 0.5 or not go_["hit"]:
            fail(f"{w}: with the keyboard up the primary is not in sight above it ({res})")
        if min(res["tabs"]) < 44 or not res["x"]:
            fail(f"{w}: the tabs should take a finger (44 px) and leave the title's ✕ its taps ({res})")
        ok(f"{w}: with the keyboard up ({res['vv']:.0f} px) 启动并交给 Agent ends at {go_['bottom']:.0f}, in the footer; tabs {res['tabs']} px, ✕ answers")
        if shots:
            page.screenshot(path=os.path.join(shots, f"handover-keyboard-{w}.png"))
        if ios:
            page.evaluate("__kb(844)")
        else:
            page.set_viewport_size({"width": w, "height": h})
        page.wait_for_timeout(400)
        page.locator("[role=dialog]").get_by_role("button", name="交给已有的 Agent").tap()
        page.wait_for_timeout(500)
        res = page.evaluate(HANDOVER)
        if not res["radios"] or any(tag != "BUTTON" or hgt < 44 for tag, _, hgt in res["radios"]) or [c for _, c, _ in res["radios"]].count("true") != 1:
            fail(f"{w}: the Existing agent rows should be radio buttons, one picked, 44 px tall ({res['radios']})")
        if not res["go"] or res["go"]["text"] != "发送并交给 Agent":
            fail(f"{w}: the Existing agent tab's primary should be in the footer ({res})")
        ok(f"{w}: {len(res['radios'])} Existing agent rows are radios of 44 px; 发送并交给 Agent is in the footer")
        ctx.close()
    ctx = browser.new_context(viewport={"width": 1280, "height": 900})
    page = ctx.new_page()
    page.on("pageerror", lambda e: ERRORS.append(str(e)))
    login(page, base)
    go(page, "/todos?id=todo_weather")
    page.get_by_role("button", name="交给 Agent", exact=True).first.click()
    page.wait_for_selector("[role=dialog] textarea")
    if page.locator("[role=dialog] [data-dialog-footer]").count() or page.locator("[role=dialog] button", has_text="启动并交给 Agent").count() != 1:
        fail("1280: the hand-over dialog's buttons should end its form, as before")
    ok("1280: the hand-over dialog's buttons end its form, as before")
    ctx.close()


WEATHER = "/home/me/code/weather"  # the made-up checkout on the server machine (MOCK_FILES)
FILES_PANEL = "aside[aria-label=文件]"
GIT_PANEL = "aside[aria-label=Git]"
OVERLAY_DEPTH = "(history.state && history.state.personaOverlayDepth) || 0"
# openPanel() from the page's own module (a panel restored on a file goes through it, lib/panel-memory.ts)
OPEN_PANEL = "async (p) => { const m = await import('/src/lib/panel.ts'); p ? m.openPanel(p) : m.closePanel(); }"


def files_from_menu(page):
    """⋯ › 文件 (the menu's row hands its entry to the panel), then the weather checkout from the path bar."""
    page.locator("main header").get_by_role("button", name="更多操作", exact=True).click()
    page.get_by_role("menuitem", name="文件").or_(page.get_by_role("menuitemcheckbox", name="文件")).click()
    aside = page.locator(FILES_PANEL)
    expect(aside).to_be_visible()
    aside.locator("button[title='/home/me/code']").click()
    aside.locator(f"button[title='{WEATHER}']").click()
    expect(aside.locator(f"button[title='{WEATHER}/README.md']")).to_be_visible()
    return aside


def depth_is(page, n, what):
    try:
        page.wait_for_function(f"({OVERLAY_DEPTH}) === {n}", timeout=3000)
    except Exception:
        fail(f"{what}: {page.evaluate(OVERLAY_DEPTH)} overlay entries, not {n}")


ROW_MORE = """(sel) => { const b = document.querySelector(sel); if (!b) return null; const r = b.getBoundingClientRect();
  let o = 1; for (let e = b; e; e = e.parentElement) o *= +getComputedStyle(e).opacity;
  const e = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
  return { w: r.width, h: r.height, x: r.left + r.width / 2, y: r.top + r.height / 2, opacity: o, hit: Boolean(e && (b === e || b.contains(e))) }; }"""


def file_row_menu(browser, base, shots):
    """A file row on a touch screen (#11511): its ⋯ shows (no hover to reveal it, no right click on iOS) and a finger
    on its middle opens the row's menu; the row's Copy path button does not take room (the menu has it); a tap on the
    name still opens the file. In the list and in the tree view, 390 and 360, light and dark."""
    for w, h in ((390, 844), (360, 740)):
        for theme in ("light", "dark"):
            ctx, page = phone(browser, w, h, theme)
            open_pane(page, base, cards=False)
            aside = files_from_menu(page)
            for view in ("list", "tree"):
                if view == "tree":
                    aside.get_by_role("button", name="更多", exact=True).click()
                    page.get_by_role("menuitem", name="树状视图").click()
                    expect(aside.locator("[role=treeitem]").first).to_be_visible()
                more = f"{FILES_PANEL} button[aria-label='「README.md」的更多操作']"
                try:  # once the list's fade-in is over
                    page.wait_for_function(f"(({ROW_MORE})({json.dumps(more)}) || {{}}).opacity > 0.99", timeout=2000)
                except Exception:
                    pass
                m = page.evaluate(ROW_MORE, more)
                if not m or m["w"] < 20 or m["opacity"] < 0.99 or not m["hit"]:
                    fail(f"{w} {theme} {view}: the row's ⋯ is not shown and hit at its middle ({m})")
                if aside.get_by_role("button", name="复制路径").count() and aside.get_by_role("button", name="复制路径").first.is_visible():
                    fail(f"{w} {theme} {view}: a touch row shows Copy path as well as its ⋯")
                if shots:
                    page.screenshot(path=os.path.join(shots, f"files-{view}-{w}-{theme}.png"))
                page.touchscreen.tap(m["x"], m["y"])
                menu = page.locator("[role=menu]")
                expect(menu).to_be_visible()
                for item in ("打开", "复制路径", "简介"):
                    expect(menu.get_by_role("menuitem", name=item, exact=True)).to_be_visible()
                # Back closes the row's menu first; the panel stays
                page.go_back()
                expect(menu).to_have_count(0)
                expect(aside).to_be_visible()
                page.wait_for_timeout(300)
                aside.locator(f"[title='{WEATHER}/README.md']").first.tap()
                expect(aside.get_by_role("button", name="返回", exact=True)).to_be_visible()
                aside.get_by_role("button", name="返回", exact=True).tap()
                expect(aside.locator(f"[title='{WEATHER}/README.md']").first).to_be_visible()
                page.wait_for_timeout(300)
                if theme == "light":
                    ok(f"{w} {view}: the row's ⋯ shows and a tap on it opens the row's menu; a tap on the name opens the file")
            # the tree view is remembered: back to the list for the next context
            page.evaluate("localStorage.removeItem('persona.files.view')")
            ctx.close()
    ok("dark at 390 and 360: the same ⋯ and menu on every row")


def run(base, shots, hub=None):
    with sync_playwright() as p:
        browser = p.chromium.launch(channel="chrome")

        # 1. Three cards at 390×844: one open, the others in a line; tapping it opens them in their own scroll box.
        ctx, page = phone(browser, 390, 844)
        open_pane(page, base)
        if page.evaluate("matchMedia('(any-pointer: fine)').matches"):
            fail("the phone context reports a precise pointer: Enter would send")
        crowded(page, "390×844")
        # the run's time and the live group's "Working..." count from the same start (the turn's message)
        times = page.evaluate(r"""(() => {
          const secs = (t) => { const m = /(?:(\d+)\s*(?:分|m))?\s*(\d+)\s*(?:秒|s)/.exec(t); return m ? (+(m[1] || 0)) * 60 + +m[2] : null; };
          const bar = document.querySelector('[data-running-elapsed]')?.textContent ?? '';
          const live = [...document.querySelectorAll('[data-chat-scroll] *')].filter((e) => !e.children.length && /^\d+m \d+s$|^\d+s$/.test(e.textContent.trim())).pop()?.textContent ?? '';
          return [bar, live, secs(bar), secs(live)]; })()""")
        if times[2] is None or times[3] is None or abs(times[2] - times[3]) > 2:
            fail(f"the run's time {times[0]!r} and 正在工作… {times[1]!r} do not agree")
        ok(f"the run's time ({times[0]}) agrees with 正在工作… ({times[1]})")
        # tapped (a click() is a mouse, which since #8523 makes the page a precise pointer: no keyboard, no fold below)
        page.locator(POSITION).tap()
        expect(page.locator("[data-chat-cards] [data-pending-card][aria-label=刷新失败以后怎么处理？]")).to_be_visible()
        cards = page.evaluate("(() => { const e = document.querySelector('[data-chat-cards]'); return [e.scrollHeight, e.clientHeight]; })()")
        if cards[0] <= cards[1]:
            fail(f"opened, the three cards should scroll in their own box ({cards})")
        dock = box(page, "[data-chat-dock]")
        if dock["height"] > 844 * 0.55 + 1:
            fail(f"opened, the dock is {dock['height']:.0f} px")
        on_screen(page, SEND, "opened")
        ok(f"1 / 3 opens the others in a box of their own (scrolls {cards[0]} in {cards[1]} px), the dock still capped")
        page.locator("[data-chat-cards-position][aria-expanded=true]").tap()  # the same chip, now 收起其余
        expect(page.locator(POSITION)).to_have_text("1 / 3")

        # 2. Typing (the keyboard up): the cards fold into one line; tapping it puts the keyboard away.
        page.locator(COMPOSER).tap()
        expect(page.locator("[data-chat-cards-fold]").get_by_text("3 件待处理")).to_be_visible()
        try:
            page.wait_for_function("[...document.querySelectorAll('[data-chat-cards] .grid')].every((g) => g.inert)", timeout=2000)
        except Exception:
            fail("while typing, a card is still open")
        ok("while typing, the three cards are one line: 3 件待处理")
        page.locator("[data-chat-cards-fold]").tap()
        expect(page.locator(POSITION)).to_have_text("1 / 3")
        if page.evaluate("document.activeElement?.getAttribute('aria-label')") == "消息":
            fail("tapping 3 件待处理 should put the keyboard away")
        ok("tapping it puts the keyboard away and the first card is back")

        # 3. A short viewport (Android's keyboard) and a twenty-line draft: the composer and send stay on screen.
        page.set_viewport_size({"width": 390, "height": 450})
        page.wait_for_timeout(400)
        crowded(page, "390×450 (short: all in one line)", min_chat=0.15, fold="3 件待处理")
        page.locator(COMPOSER).fill("\n".join(f"第 {i} 行" for i in range(1, 21)))
        page.wait_for_timeout(300)
        send = on_screen(page, "main [aria-label=排队发送]", "twenty lines at 450")  # the agent works: send queues (#5400)
        ta = box(page, COMPOSER)
        if ta["height"] > 450 * 0.35 + 1:
            fail(f"twenty lines: the text box is {ta['height']:.0f} px, over 35% of 450")
        if page.evaluate(f"(() => {{ const t = document.querySelector({json.dumps(COMPOSER)}); return t.scrollHeight <= t.clientHeight; }})()"):
            fail("twenty lines: the text box should scroll")
        ok(f"twenty lines at 450: the box stops at {ta['height']:.0f} px and scrolls, send at {send['bottom']:.0f}")
        page.set_viewport_size({"width": 390, "height": 844})
        page.wait_for_timeout(300)
        page.locator(COMPOSER).fill("")
        page.locator(COMPOSER).blur()
        page.wait_for_timeout(400)  # the draft is saved 250 ms after the last change

        # Scrolled up: the plan's Step N / M and "Jump to bottom" side by side, not over each other.
        # a finger dragging the chat down (a programmatic scroll is not the reader leaving the end)
        cdp = page.context.new_cdp_session(page)
        y0 = box(page, "[data-chat-scroll]")["top"] + 20
        for _ in range(3):
            cdp.send("Input.dispatchTouchEvent", {"type": "touchStart", "touchPoints": [{"x": 200, "y": y0}]})
            for i in range(1, 13):
                cdp.send("Input.dispatchTouchEvent", {"type": "touchMove", "touchPoints": [{"x": 200, "y": y0 + 22 * i}]})
            cdp.send("Input.dispatchTouchEvent", {"type": "touchEnd", "touchPoints": []})
        cdp.detach()
        expect(page.locator("[data-plan-pill]")).to_be_visible()
        expect(page.locator("[data-jump-pill]")).to_be_visible()
        pill, jump = box(page, "[data-plan-pill]"), box(page, "[data-jump-pill]")
        if not (pill["right"] <= jump["left"] or jump["right"] <= pill["left"]) and not (pill["bottom"] <= jump["top"] or jump["bottom"] <= pill["top"]):
            fail(f"the plan's pill {pill} and 跳到底部 {jump} overlap")
        if abs(pill["top"] + pill["height"] / 2 - (jump["top"] + jump["height"] / 2)) > 2:
            fail(f"the plan's pill {pill} and 跳到底部 {jump} are not side by side")
        if shots:
            page.screenshot(path=os.path.join(shots, "390-pills.png"))
        page.locator("[data-jump-pill]").click()
        expect(page.locator("[data-jump-pill]")).to_have_count(0)
        ok("scrolled up: Step N / M and 跳到底部 side by side; 跳到底部 still goes to the end")
        if shots:
            for theme in ("dark", "light"):  # light last: the checks below go on in it
                page.evaluate(f"localStorage.setItem('persona.theme', '{theme}')")
                page.reload()
                page.wait_for_selector("[data-chat-cards]", state="attached")
                page.wait_for_timeout(900)
                page.screenshot(path=os.path.join(shots, f"390-cards-{theme}.png"))

        # 4. Back closes the full-screen file panel and stays on the conversation.
        url = page.url
        page.locator("main header").get_by_role("button", name="更多操作").tap()  # #5439: the header's ⋯; a reply's ⋯ is 更多
        page.locator("[role=menu] button", has_text="文件").first.click()
        expect(page.locator("aside[aria-label=文件]")).to_be_visible()
        page.go_back()
        expect(page.locator("aside[aria-label=文件]")).to_have_count(0)
        if page.url != url:
            fail(f"Back with the file panel open left the conversation ({page.url})")
        expect(page.locator(COMPOSER)).to_be_visible()
        ok("Back closes the full-screen file panel and stays on the conversation")
        # closed from the page, its entry goes too: Back then leaves the conversation
        page.locator("main header").get_by_role("button", name="更多操作").tap()  # #5439: the header's ⋯; a reply's ⋯ is 更多
        page.locator("[role=menu] button", has_text="文件").first.click()
        expect(page.locator("aside[aria-label=文件]")).to_be_visible()
        page.get_by_role("button", name="关闭面板").click()
        expect(page.locator("aside[aria-label=文件]")).to_have_count(0)
        page.wait_for_timeout(300)
        if page.url != url:
            fail(f"closing the panel moved the page ({page.url})")
        page.go_back()
        page.wait_for_function("!location.pathname.startsWith('/t/')")
        ok("closed with ×, the panel takes its history entry with it")
        ctx.close()

        # 5. (Switching panes writing no history went with the chat's pane tabs in #5263: web_team_e2e.py checks it on
        # the lead's Team panel.)

        # 6. iOS: the keyboard shrinks the visual viewport only; the side panel (terminal key bar) sits on what shows.
        ctx, page = phone(browser, 390, 844, ios=True)
        open_pane(page, base)
        page.evaluate("__kb(450, 0)")
        page.wait_for_timeout(200)
        kb = page.evaluate("[getComputedStyle(document.documentElement).getPropertyValue('--kb-top'), getComputedStyle(document.documentElement).getPropertyValue('--kb-h')]")
        if kb != ["0px", "450px"]:
            fail(f"--kb-top / --kb-h with the keyboard up: {kb}")
        page.locator("main header").get_by_role("button", name="更多操作").tap()  # #5439: the header's ⋯; a reply's ⋯ is 更多
        page.locator("[role=menu] button", has_text="文件").first.click()
        aside = box(page, "aside[aria-label=文件]")
        if abs(aside["top"]) > 1 or abs(aside["height"] - 450) > 1:
            fail(f"the side panel with the iOS keyboard up: {aside}")
        page.evaluate("__kb(450, 300)")
        page.wait_for_timeout(200)
        aside = box(page, "aside[aria-label=文件]")
        if abs(aside["top"] - 300) > 1:
            fail(f"the side panel follows the scrolled visual viewport: {aside}")
        page.evaluate("__kb(844, 0)")
        page.wait_for_timeout(200)
        aside = box(page, "aside[aria-label=文件]")
        if abs(aside["height"] - 844) > 1 or page.evaluate("document.documentElement.style.getPropertyValue('--kb-h')"):
            fail(f"keyboard down: the panel and the variables should be back ({aside})")
        ok("iOS keyboard: --kb-top / --kb-h follow the visual viewport and the side panel sits in it")
        ctx.close()

        # 7. 360 wide: the header leaves the title some ten characters.
        title_room(browser, base, shots)

        # 8. On its side (844×390): the run's status and the queue share a line; the chat keeps some room.
        for theme in ("light", "dark"):
            ctx, page = phone(browser, 844, 390, theme)
            open_pane(page, base)
            run_ = box(page, "[data-running-status]")
            queue = box(page, "[data-queue-strip]")
            if abs(run_["top"] + run_["height"] / 2 - (queue["top"] + queue["height"] / 2)) > 4:
                fail(f"844×390: the status ({run_}) and the queue ({queue}) are not on one line")
            if queue["height"] > 44:
                fail(f"844×390: the queue takes {queue['height']:.0f} px, should be one line")
            chat = chat_height(page)
            if chat < 390 * 0.22:
                fail(f"844×390: the conversation has {chat} px")
            on_screen(page, COMPOSER, "844×390")
            expect(page.locator("[data-chat-cards-fold]").get_by_text("3 件待处理")).to_be_visible()
            if shots:
                page.screenshot(path=os.path.join(shots, f"land-{theme}.png"))
            if theme == "light":
                ok(f"844×390: status and queue on one line, the cards in one more, the chat keeps {chat} px")
                page.locator("[data-chat-cards-fold]").click()
                expect(page.locator("[data-chat-cards] [data-pending-card][aria-label=刷新失败以后怎么处理？]")).to_be_visible()
                if box(page, "[data-chat-dock]")["height"] > 390 * 0.55 + 1:
                    fail("844×390: opened, the dock is over its cap")
                on_screen(page, SEND, "844×390 opened")
                page.get_by_role("button", name="收起").click()
                expect(page.locator("[data-chat-cards-fold]").get_by_text("3 件待处理")).to_be_visible()
                ok("844×390: tapping the line opens the cards in the capped box, 收起 folds them again")
            ctx.close()

        # 9. Wide (1280): no box, no fold; the first card in full with its 1 / 3 (#8928); the header says no state (#5421).
        ctx = browser.new_context(viewport={"width": 1280, "height": 900})
        page = ctx.new_page()
        page.on("pageerror", lambda e: ERRORS.append(str(e)))
        open_pane(page, base, cards=False)
        if page.locator("[data-chat-cards]").count() or page.locator("[data-chat-cards-fold]").count():
            fail("1280: the cards should have no box and no fold")
        expect(page.locator("[data-pending-card]")).to_have_count(1)
        expect(page.locator(POSITION)).to_have_text("1 / 3")
        if page.locator("main header").get_by_text("工作中").count():
            fail("1280: the header says no state (#5421)")
        ok("1280: the first card in full with its 1 / 3, no box, no fold; no state in the header")
        if shots:
            page.screenshot(path=os.path.join(shots, "1280.png"))
        ctx.close()
        card_field_keyboard(browser, base, hub, shots)
        lark_keyboard(browser, base, shots)
        tile_hits(browser, base, shots)
        toasts_phone(browser, base)
        handover_keyboard(browser, base, shots)
        file_row_menu(browser, base, shots)
        browser.close()
    if ERRORS:
        fail(f"uncaught page errors: {ERRORS[:3]}")
    print("PASS web_mobile_input_e2e", flush=True)


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--shots")
    ap.add_argument("--hub")
    a = ap.parse_args()
    if a.shots:
        os.makedirs(a.shots, exist_ok=True)
    run(a.base.rstrip("/"), a.shots, a.hub)
