#!/usr/bin/env python3
"""End-to-end check of the web client's pages on a phone (#1996): Dialog is a bottom sheet at phone width (its
footer in sight, Tab kept inside, Back closes it) and still the centred card on a wide screen; icon buttons take a
44pt touch; Settings is a list of categories with Back between them; the inbox filters and section bar keep to one row each; a todo's circle is
hit without opening the row, and the row opens from the keyboard; a long-pressed message keeps its menu.

Needs a running web page against the fake hub and Playwright for Python (it drives the installed Chrome):

    MOCK_GITHUB_INBOX=1 PORT=18964 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18964 npx vite --port 5964 --strictPort &)
    uv run --no-project --with playwright==1.58.0 python3 scripts/test/web_mobile_pages_e2e.py http://127.0.0.1:5964 [--shots <dir>]

Prints one line per check and a verdict; exits 1 on the first failure. --shots saves light and dark screenshots at
390 and 360 wide, and the wide dialog at 1280.
"""
import argparse
import json
import os
import re
import sys

from playwright.sync_api import expect, sync_playwright

UA = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"
REVIEW = "a1f3c2d4-0000-4000-8000-000000000006"  # the fake hub's session titled "take a look at PR #42"


def ok(msg):
    print("ok  " + msg, flush=True)


def fail(msg):
    print("FAIL " + msg, flush=True)
    sys.exit(1)


def check(cond, msg, detail=None):
    if not cond:
        fail(msg + ("" if detail is None else f": {detail}"))
    ok(msg)


def open_page(browser, base, token, w, h, dark=False, mobile=True):
    ctx = browser.new_context(viewport={"width": w, "height": h}, device_scale_factor=3 if mobile else 2, is_mobile=mobile, has_touch=mobile,
                              user_agent=UA if mobile else None, color_scheme="dark" if dark else "light")
    page = ctx.new_page()
    page.set_default_timeout(10000)
    page.goto(f"{base}/overview?token={token}")
    page.wait_for_selector("aside", state="attached", timeout=20000)
    return ctx, page


def goto(page, path):
    page.evaluate("p => { history.pushState(null, '', p); dispatchEvent(new PopStateEvent('popstate')); }", path)
    page.wait_for_timeout(400)


def shot(page, shots, name):
    if shots:
        page.screenshot(path=os.path.join(shots, name + ".png"))


PANEL = "[role=dialog]"
# what Tab reaches in a dialog (ui/overlay.ts FOCUSABLE, shown ones)
FOCUSABLES = """d => [...d.querySelectorAll('a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"]), [contenteditable="true"]')]
  .filter(e => e.getClientRects().length && !e.closest('[inert]'))"""


def open_new_agent(page, tap=True):
    b = page.get_by_role("button", name="新建 Agent").last
    b.tap() if tap else b.click()
    page.wait_for_selector(PANEL)
    page.wait_for_timeout(500)  # the sheet's slide


def sheet_checks(page, w, h, tag, shots):
    open_new_agent(page)
    box = page.evaluate("""() => { const d = document.querySelector('[role=dialog]'); const r = d.getBoundingClientRect();
      const go = [...d.querySelectorAll('button')].find(b => b.textContent.trim() === '启动').getBoundingClientRect();
      const s = getComputedStyle(d);
      return { top: r.top, bottom: r.bottom, left: r.left, right: r.right, go: go.bottom, goTop: go.top, vh: innerHeight,
               rtl: s.borderTopLeftRadius, rbl: s.borderBottomLeftRadius, labelled: d.getAttribute('aria-labelledby') &&
               document.getElementById(d.getAttribute('aria-labelledby'))?.textContent }; }""")
    check(abs(box["bottom"] - h) <= 1 and box["left"] == 0 and box["right"] == w, f"{tag}: 新建 Agent is a bottom sheet, full width", box)
    check(box["top"] >= h * 0.1 - 1, f"{tag}: the sheet is at most 90% of the screen", box)
    check(box["rtl"] != "0px" and box["rbl"] == "0px", f"{tag}: rounded on top only", box)
    check(box["goTop"] >= box["top"] and box["go"] <= box["vh"], f"{tag}: 启动 is in sight without scrolling", box)
    check(box["labelled"] == "新建 Agent", f"{tag}: the sheet is labelled by its title", box)
    shot(page, shots, f"sheet_{tag}")

    # Tab stays inside: 40 presses forward and 10 back never leave the panel.
    inside = page.evaluate("() => document.querySelector('[role=dialog]').contains(document.activeElement)")
    check(inside, f"{tag}: the focus moved into the sheet")
    out = []
    for i in range(50):
        page.keyboard.press("Shift+Tab" if i >= 40 else "Tab")
        if not page.evaluate("() => document.querySelector('[role=dialog]').contains(document.activeElement)"):
            out.append(i)
    check(not out, f"{tag}: Tab and Shift+Tab stay in the sheet", out)
    # Shift+Tab from the first control goes to the last one
    wrapped = page.evaluate(f"() => {{ const f = ({FOCUSABLES})(document.querySelector('[role=dialog]')); f[0].focus(); return f.length; }}")
    page.keyboard.press("Shift+Tab")
    last = page.evaluate(f"() => ({FOCUSABLES})(document.querySelector('[role=dialog]')).indexOf(document.activeElement)")
    check(last == wrapped - 1, f"{tag}: Shift+Tab on the first control wraps to the last", (last, wrapped))

    path = page.evaluate("location.pathname")
    page.go_back()
    page.wait_for_selector(PANEL, state="detached")
    check(page.evaluate("location.pathname") == path, f"{tag}: Back closes the sheet and stays on the page")
    focus_back = page.evaluate("() => document.activeElement && document.activeElement.textContent.includes('新建 Agent')")
    check(focus_back, f"{tag}: the focus went back to 新建 Agent")
    # Closing it from the page ("Cancel", Esc) takes its history entry back off: no dead entry for Back to land on.
    for close in (lambda: page.get_by_role("button", name="取消").tap(), lambda: page.keyboard.press("Escape")):
        open_new_agent(page)
        close()
        page.wait_for_selector(PANEL, state="detached")
        try:  # its entry goes a tick after it closes (lib/back-close.ts)
            page.wait_for_function("!history.state?.personaOverlay", timeout=3000)
        except Exception:
            pass
        check(not page.evaluate("history.state && history.state.personaOverlay"), f"{tag}: closing it from the page leaves no overlay entry")


def touch_hit(page, tag):
    # The inbox header's refresh button (28px): 8px outside its edge still lands on it.
    goto(page, "/inbox")
    page.wait_for_selector('[aria-label="收件箱列表"]')
    res = page.evaluate("""() => { const b = document.querySelector('header button[aria-label^="刷新"]'); const r = b.getBoundingClientRect();
      const at = (x, y) => document.elementFromPoint(x, y)?.closest('button') === b;
      return { w: r.width, h: r.height, left: at(r.left - 8, r.top + r.height / 2), up: at(r.left + r.width / 2, r.top - 7), down: at(r.left + r.width / 2, r.bottom + 7), far: at(r.left - 20, r.top + r.height / 2) }; }""")
    check(res["w"] < 44 and res["left"] and res["up"] and res["down"], f"{tag}: a {res['w']:.0f}px icon button is hit 8px outside its edge", res)
    check(not res["far"], f"{tag}: but not 20px outside", res)
    # A settings switch too.
    goto(page, "/settings?tab=general")
    sw = page.locator("[role=switch]").first
    sw.wait_for()
    sw.scroll_into_view_if_needed()  # below a phone's first screen since #10656 (the font pickers above it)
    res = page.evaluate("""() => { const b = document.querySelector('[role=switch]'); const r = b.getBoundingClientRect();
      return { h: r.height, hit: document.elementFromPoint(r.left + r.width / 2, r.top - 8)?.closest('[role=switch]') === b }; }""")
    check(res["h"] < 44 and res["hit"], f"{tag}: a {res['h']:.0f}px switch is hit 8px above it", res)


def settings_list(page, tag, shots):
    goto(page, "/settings")
    page.locator('nav[aria-label="设置分类"]').wait_for()
    # the categories' rows; the first group's 切换 (#3667, its assistants) is a text button in its title line
    rows = page.evaluate("() => [...document.querySelectorAll('nav[aria-label=\"设置分类\"] button:not([data-settings-switch])')].map(b => [b.textContent.trim(), Math.round(b.getBoundingClientRect().height)])")
    check(len(rows) >= 10 and all(h >= 44 for _, h in rows), f"{tag}: 设置 opens on the list of categories, rows of 44pt+", rows)
    sw = page.evaluate("""() => { const b = document.querySelector('[data-settings-switch]'); if (!b) return null; const r = b.getBoundingClientRect();
      const at = (y) => document.elementFromPoint(r.left + r.width / 2, y)?.closest('[data-settings-switch]') === b;
      return { h: r.height, up: at(r.top - 10), down: at(r.bottom + 10) }; }""")
    check(sw is None or (sw["up"] and sw["down"]), f"{tag}: 切换 is hit 10px above and below it", sw)
    shot(page, shots, f"settings_list_{tag}")
    # Codex is a section of Agent since #2317 (Claude Code, Codex, Antigravity, omp, one category)
    page.locator('nav[aria-label="设置分类"]').get_by_role("button", name=re.compile(r"^Agent")).tap()
    page.wait_for_function("location.search === '?tab=agents'")
    expect(page.locator("header h1")).to_have_text("Agent")
    check(page.locator('nav[aria-label="设置分类"]').count() == 0, f"{tag}: Agent opens as its own page with a back button")
    shot(page, shots, f"settings_agents_{tag}")
    page.go_back()
    page.wait_for_function("location.pathname === '/settings' && location.search === ''")
    page.locator('nav[aria-label="设置分类"]').wait_for()
    ok(f"{tag}: Back returns to the list")
    # the header's back button does the same, and a deep link's back lands on the list too
    page.get_by_role("button", name="通用").first.tap()
    page.wait_for_function("location.search === '?tab=general'")
    page.get_by_role("button", name="返回设置").tap()
    page.locator('nav[aria-label="设置分类"]').wait_for()
    goto(page, "/settings?tab=hub")
    page.get_by_role("button", name="返回设置").tap()
    page.locator('nav[aria-label="设置分类"]').wait_for()
    check(page.evaluate("location.pathname + location.search") == "/settings", f"{tag}: 返回 goes to the list, from the list or a link")


def inbox_filters(page, tag, shots):
    goto(page, "/inbox")
    bar = page.locator('[role=toolbar][aria-label="筛选"]')
    bar.wait_for()
    page.wait_for_timeout(600)
    res = page.evaluate("""() => { const t = document.querySelector('[role=toolbar][aria-label="筛选"]');
      const tops = new Set([...t.children].filter(c => c.getClientRects().length).map(c => { const r = c.getBoundingClientRect(); return Math.round((r.top + r.bottom) / 2); }));
      return { rows: tops.size, docW: document.documentElement.scrollWidth, W: innerWidth }; }""")
    check(res["rows"] == 1 and res["docW"] <= res["W"], f"{tag}: the inbox filters keep to one row that scrolls sideways", res)
    # the section bar above them (#9053): one row of choices too, its words never wrapping, with All (the reset) in full
    res = page.evaluate("""() => { const t = document.querySelector('[role=radiogroup][aria-label="分段"]');
      const pills = [...t.querySelectorAll('[data-bar-choice]')];
      const tops = new Set(pills.map(c => { const r = c.getBoundingClientRect(); return Math.round((r.top + r.bottom) / 2); }));
      const all = t.querySelector('[data-bar-choice="all"]').getBoundingClientRect(), box = t.getBoundingClientRect();
      return { rows: tops.size, tall: Math.max(...pills.map(c => c.getBoundingClientRect().height)), allIn: all.left >= box.left - 0.5 && all.right <= box.right + 0.5,
        docW: document.documentElement.scrollWidth, W: innerWidth }; }""")
    check(res["rows"] == 1 and res["tall"] <= 24 and res["allIn"] and res["docW"] <= res["W"], f"{tag}: the inbox section bar keeps to one row", res)
    shot(page, shots, f"inbox_{tag}")
    page.get_by_role("button", name="更多操作").tap()
    item = page.get_by_role("menuitem", name="全部清理")
    item.wait_for()
    page.wait_for_timeout(300)  # the menu's pop-in scale
    check(item.bounding_box()["height"] >= 44, f"{tag}: 全部清理 is in the ⋯ menu, a 44pt row")
    shot(page, shots, f"inbox_menu_{tag}")
    page.keyboard.press("Escape")
    page.get_by_role("menu").wait_for(state="detached")
    # the menu's history entry goes a tick after it closes (lib/back-close.ts): until then the next page's address is
    # the one its Back pops, which left the to-dos step on /inbox about one run in two
    page.wait_for_function("!history.state?.personaOverlay")


def todos(page, tag, shots):
    # a to-do's page asks for its repositories and decisions: none is open, so none is asked for (#11442)
    detail_reads = []
    on_request = lambda r: detail_reads.append(r.url) if "/api/v1/todos/" in r.url and ("/repositories" in r.url or "/decisions" in r.url) else None
    page.on("request", on_request)
    goto(page, "/todos")
    circle = page.get_by_role("button", name="完成待办", exact=True).first
    circle.wait_for()
    check(page.locator("[data-todo][aria-current]").count() == 0, f"{tag}: no to-do row is current before one is opened")
    search = page.get_by_placeholder("搜索待办")
    search.press_sequentially("PR", delay=60)
    page.wait_for_timeout(300)
    search.fill("")
    page.wait_for_timeout(500)
    page.remove_listener("request", on_request)
    check(not detail_reads, f"{tag}: no unopened to-do's page loads, also while searching", detail_reads)
    calls = []
    page.route("**/api/v1/todos/*", lambda r: (calls.append((r.request.method, r.request.post_data)), r.fulfill(status=500, body="{}")) if r.request.method == "PATCH" else r.continue_())
    b = circle.bounding_box()
    path = page.evaluate("location.pathname + location.search")
    # 10px left of the 16px circle: the row's padding, but the circle's 44pt target
    page.touchscreen.tap(b["x"] - 10, b["y"] + b["height"] / 2)
    page.wait_for_timeout(500)
    check(len(calls) == 1 and json.loads(calls[0][1]).get("done") is True, f"{tag}: a tap 10px beside the todo's circle completes it", calls)
    check(page.evaluate("location.pathname + location.search") == path, f"{tag}: and does not open the row", path)
    page.unroute("**/api/v1/todos/*")
    # the row opens from the keyboard
    row = page.locator('[role=button][tabindex="0"]').filter(has=page.get_by_role("button", name="完成待办", exact=True)).first
    row.focus()
    page.keyboard.press("Enter")
    page.wait_for_function("location.search.startsWith('?id=')")
    ok(f"{tag}: Enter on a focused todo row opens it")
    shot(page, shots, f"todos_{tag}")


# Overview on a phone (#11442): a task card opens from a tap anywhere on it, its PR link keeps its own; See all and
# Monitor take a 44pt touch; a Needs you card's kicker never takes more room than its title.
def overview_cards(page, ctx, w, tag, shots):
    def back_to_overview():
        goto(page, "/overview")
        page.wait_for_selector("[data-task]")
        page.wait_for_timeout(300)

    # in the middle of the screen, clear of a toast left at its bottom by an earlier step
    def center(loc):
        loc.evaluate("e => e.scrollIntoView({ block: 'center' })")
        page.wait_for_timeout(200)

    def tap(box, dy=0.0):
        page.touchscreen.tap(box["x"] + min(12, box["width"] / 2), box["y"] + box["height"] / 2 + dy)
        page.wait_for_timeout(500)
        return page.evaluate("location.pathname + location.search")

    back_to_overview()
    card = page.locator("[data-task]:has([data-task-agents])").first
    center(card)
    tid = card.get_attribute("data-task")
    check(tap(card.locator("[data-task-agents]").bounding_box()) == f"/todos?id={tid}", f"{tag}: a tap on a task card's agent line opens the task")
    back_to_overview()
    card = page.locator("[data-task]:has([data-stage]):has(a[target=_blank])").first
    center(card)
    shot(page, shots, f"overview_cards_{tag}")
    tid = card.get_attribute("data-task")
    pr = card.locator("a[target=_blank]")
    with ctx.expect_page() as popup:
        tap(pr.bounding_box())
    check(popup.value.url == pr.get_attribute("href") and page.evaluate("location.pathname") == "/overview", f"{tag}: its PR link opens the PR, not the task", popup.value.url)
    popup.value.close()
    check(tap(card.locator("[data-stage] .rounded-full").first.bounding_box()) == f"/todos?id={tid}", f"{tag}: a tap on its stage bar opens the task")
    for name, path in [("查看全部", "/todos"), ("监控", "/machines")]:
        for dy in (-15, 15):
            back_to_overview()
            link = page.locator("section h2 ~ a", has_text=name)
            center(link)
            box = link.bounding_box()
            check(box["height"] < 30 and tap(box, dy) == path, f"{tag}: {name} answers a touch {abs(dy)}px {'above' if dy < 0 else 'below'} its words")
    if w == 360 and not tag.endswith("d"):
        back_to_overview()
        page.evaluate("localStorage.setItem('persona.language', 'en')")
        page.reload()
        page.wait_for_selector("[data-task]")
        page.wait_for_timeout(800)
        rows = page.evaluate("""() => [...document.querySelectorAll('a')].filter(a => a.querySelector('span[title] > span.truncate')).map(a => {
          const words = a.querySelector('span[title] > span.truncate'), title = a.querySelector('.block.truncate');
          return { kicker: words.textContent, words: words.getBoundingClientRect().width, title: title.getBoundingClientRect().width, cut: words.scrollWidth > words.clientWidth + 1 }; })""")
        page.evaluate("localStorage.removeItem('persona.language')")
        page.reload()
        page.wait_for_selector("aside", state="attached")
        check(rows and all(r["title"] >= r["words"] - 1 for r in rows), "360 en: every Needs you title is at least as wide as its kicker's words", rows)
        check(any(r["cut"] for r in rows) and all(abs(r["title"] - r["words"]) <= 1 for r in rows if r["cut"]), "360 en: a long kicker is cut only to the title's width", rows)


def long_press(page, ctx, tag):
    goto(page, f"/s/macbook/{REVIEW}")
    page.get_by_text("看一下 PR #42").last.wait_for()
    page.wait_for_timeout(800)
    bx = page.evaluate("""() => { const e = [...document.querySelectorAll('div,p,span')].reverse().find(p => p.textContent.trim() === '看一下 PR #42');
      const r = e.getBoundingClientRect(); return [r.left, r.top, r.right, r.bottom]; }""")
    cdp = ctx.new_cdp_session(page)
    cdp.send("Input.dispatchTouchEvent", {"type": "touchStart", "touchPoints": [{"x": bx[2] - 4, "y": bx[1] + 4, "id": 1}]})
    page.wait_for_timeout(700)
    cdp.send("Input.dispatchTouchEvent", {"type": "touchEnd", "touchPoints": []})
    page.wait_for_timeout(600)
    check(page.get_by_role("menu").count() == 1, f"{tag}: a long-pressed message keeps its menu after the finger lifts")
    # a tap outside still closes it
    page.touchscreen.tap(20, 200)
    page.get_by_role("menu").wait_for(state="detached")
    ok(f"{tag}: a tap outside closes it")


SHOW_UNDO = """async () => { const m = await import('/src/ui/atoms.tsx'); const { t } = await import('/src/i18n/index.ts');
  window.__undone = 0; m.toast('已归档', 'ok', { label: t('lib-agent-archives.undo'), run() { window.__undone++; } });
  await new Promise((r) => setTimeout(r, 400));
  const row = [...document.querySelectorAll('[aria-live=polite] > div:not([data-closing])')].pop(); const [undo, close] = row.querySelectorAll('button');
  // the word itself (the button may reach past it on a touch screen)
  const range = document.createRange(); range.selectNodeContents(undo); const u = range.getBoundingClientRect(), c = close.getBoundingClientRect();
  return { label: undo.textContent, right: u.right, cy: u.top + u.height / 2, close: [c.left + c.width / 2, c.top + c.height / 2], height: row.offsetHeight }; }"""


def toast_undo_edges(browser, base, token):
    # A toast's Undo on a phone (#11442): real taps 1, 3 and 5 px past its word run it, not Close, for a short word and
    # a wide one (ja); Close still closes from its own face; a toast with Undo is no taller than one without.
    for lang, label in ((None, "撤销"), ("ja", "元に戻す")):
        ctx, page = open_page(browser, base, token, 390, 844)
        if lang:
            page.evaluate(f"localStorage.setItem('persona.language', '{lang}')")
            page.reload()
            page.wait_for_selector("aside", state="attached", timeout=20000)
        for d in (1, 3, 5):
            u = page.evaluate(SHOW_UNDO)
            if u["label"] != label:
                fail(f"the toast's button reads {u['label']!r}, not {label!r}")
            at = page.evaluate("([x, y]) => document.elementFromPoint(x, y)?.closest('button')?.textContent", [u["right"] + d, u["cy"]])
            if at != label:
                fail(f"{label}: {d} px right of it is {at!r}, not the button")
            page.touchscreen.tap(u["right"] + d, u["cy"])
            try:
                page.wait_for_function("window.__undone === 1 && !document.querySelector('[aria-live=polite] > div:not([data-closing])')", timeout=3000)
            except Exception:
                fail(f"{label}: a tap {d} px right of it did not run it (runs: {page.evaluate('window.__undone')})")
        ok(f"{label}: taps 1, 3 and 5 px right of it run it")
        u = page.evaluate(SHOW_UNDO)
        page.touchscreen.tap(*u["close"])
        page.wait_for_function("!document.querySelector('[aria-live=polite] > div:not([data-closing])')", timeout=3000)
        check(page.evaluate("window.__undone") == 0, f"{label}: a tap on Close closes it without running it")
        page.wait_for_timeout(400)
        plain = page.evaluate("""async () => { const m = await import('/src/ui/atoms.tsx'); m.toast('已复制', 'ok');
          await new Promise((r) => setTimeout(r, 400)); return [...document.querySelectorAll('[aria-live=polite] > div:not([data-closing])')].pop().offsetHeight; }""")
        check(u["height"] == plain, f"{label}: a toast with it is as tall as one without", (u["height"], plain))
        ctx.close()


def wide_dialog(browser, base, token, shots):
    ctx, page = open_page(browser, base, token, 1280, 820, mobile=False)
    open_new_agent(page, tap=False)
    box = page.evaluate("""() => { const d = document.querySelector('[role=dialog]'); const r = d.getBoundingClientRect(); const s = getComputedStyle(d);
      return { left: r.left, right: r.right, top: r.top, w: r.width, r: [s.borderTopLeftRadius, s.borderBottomLeftRadius], mw: s.maxWidth, anim: s.animationName }; }""")
    check(abs(box["w"] - 520) < 1 and abs((box["left"] + box["right"]) / 2 - 640) < 2, "1280: the dialog is the centred 520px card", box)
    check(abs(box["top"] - 0.12 * 820) < 2 and box["r"] == ["14px", "14px"] and box["anim"] == "slide-up", "1280: 12vh from the top, all corners round, sliding up as before", box)
    hist = page.evaluate("history.length")
    page.keyboard.press("Escape")
    page.wait_for_selector(PANEL, state="detached")
    check(page.evaluate("history.length") == hist, "1280: no Back entry for the wide dialog")
    if shots:
        open_new_agent(page, tap=False)
        shot(page, shots, "dialog_1280")
    # Settings keeps its categories beside the page (a column since #2342, not the phone's list): /settings is 通用
    page.goto(f"{base}/settings")
    page.locator('nav[aria-label="设置分类"] button[aria-current="page"]', has_text="通用").wait_for()
    check(page.locator("aside nav[aria-label='设置分类']").count() == 1 and page.get_by_role("button", name="返回设置").count() == 0,
          "1280: 设置 keeps its category column, /settings is 通用")
    ctx.close()


INBOX_TITLE = """() => { const h = document.querySelector('[data-item-title]'); const s = h.querySelector('span');
  const done = [...h.closest('.items-start').querySelectorAll(':scope > button')].pop();
  const text = [...done.childNodes].find((n) => n.nodeType === 3 && n.textContent.trim()); const range = document.createRange(); range.selectNodeContents(text);
  const lh = parseFloat(getComputedStyle(s).lineHeight);
  return { text: s.textContent, cw: s.clientWidth, sw: s.scrollWidth, ch: s.clientHeight, sh: s.scrollHeight, lines: Math.round(s.getBoundingClientRect().height / lh),
    wrap: getComputedStyle(s).whiteSpace, done: done.textContent.trim(), doneLines: new Set([...range.getClientRects()].map((r) => Math.round(r.top))).size,
    doneH: done.getBoundingClientRect().height }; }"""


def inbox_title_phone(browser, base, token, shots):
    """An Inbox item's title wraps on a phone for every source (#11511), not only a Feishu one's: a GitHub review's
    "Add …" was cut to a few letters beside Done. It wraps in full, all of it shown; Done stays one line; wide, the
    title is still one line, cut at its end."""
    item = "/inbox/github%3Areview12"  # the fake hub's GitHub review (MOCK_GITHUB_INBOX=1), "Add retries to the example uploader"
    for lang, locale in (("en", "en-US"), ("ja", "ja-JP")):
        ctx = browser.new_context(viewport={"width": 360, "height": 740}, device_scale_factor=3, is_mobile=True, has_touch=True,
                                  user_agent=UA, locale=locale)
        ctx.add_init_script(f"localStorage.setItem('persona.language', '{lang}')")
        page = ctx.new_page()
        page.set_default_timeout(10000)
        page.goto(f"{base}/overview?token={token}")
        page.wait_for_selector("aside", state="attached", timeout=20000)
        goto(page, item)
        page.wait_for_selector("[data-item-title]")
        page.wait_for_timeout(500)
        res = page.evaluate(INBOX_TITLE)
        check(res["sw"] <= res["cw"] + 1 and res["sh"] <= res["ch"] + 1 and res["lines"] >= 2,
              f"360 {lang}: a GitHub item's title wraps to {res['lines']} lines, all of it shown", res)
        check(res["doneLines"] == 1 and res["doneH"] <= 33, f"360 {lang}: {res['done']} stays on one line", res)
        shot(page, shots, f"inbox_title_360_{lang}")
        ctx.close()
    ctx, page = open_page(browser, base, token, 1280, 820, mobile=False)
    goto(page, item)
    page.wait_for_selector("[data-item-title]")
    page.wait_for_timeout(500)
    res = page.evaluate(INBOX_TITLE)
    check(res["wrap"] == "nowrap" and res["lines"] == 1, "1280: the title stays one line, cut at its end", res)
    ctx.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--shots")
    ap.add_argument("--token", default="mock")
    a = ap.parse_args()
    base = a.base.rstrip("/")
    if a.shots:
        os.makedirs(a.shots, exist_ok=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(channel="chrome")
        try:
            for w, h, dark in [(390, 844, False), (360, 740, False), (390, 844, True), (360, 740, True)]:
                tag = f"{w}{'d' if dark else ''}"
                ctx, page = open_page(browser, base, a.token, w, h, dark)
                sheet_checks(page, w, h, tag, a.shots)
                touch_hit(page, tag)
                settings_list(page, tag, a.shots)
                inbox_filters(page, tag, a.shots)
                todos(page, tag, a.shots)
                overview_cards(page, ctx, w, tag, a.shots)
                if not dark:
                    long_press(page, ctx, tag)
                ctx.close()
            # landscape phone: wider than a sheet, the card stays (and still opens)
            ctx, page = open_page(browser, base, a.token, 844, 390)
            open_new_agent(page)
            box = page.evaluate("() => { const r = document.querySelector('[role=dialog]').getBoundingClientRect(); return [r.left, r.right, r.width]; }")
            check(box[0] > 0 and abs(box[2] - 520) < 1, "844×390: the dialog is the card, not a sheet", box)
            ctx.close()
            toast_undo_edges(browser, base, a.token)
            wide_dialog(browser, base, a.token, a.shots)
            inbox_title_phone(browser, base, a.token, a.shots)
        finally:
            browser.close()
    print("PASS web mobile pages e2e")


if __name__ == "__main__":
    main()
