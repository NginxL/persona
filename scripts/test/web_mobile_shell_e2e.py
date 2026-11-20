#!/usr/bin/env python3
"""End-to-end check of the phone shell (#1996): the sidebar drawer dragged open and shut with real touches (CDP
Input.dispatchTouchEvent), Esc and Back closing it, its focus and inert, the drawer's first screen reaching the Agents,
"Assistant | Agents" with the buttons in the same place on both pages (the switch clear of them, #5049), an old /s/ link
becoming its /t/, the pages that load on demand, the offline empty state, and a landscape notch clear of the drawer.

Needs a running web page against the fake hub with split tabs (MOCK_SPLIT_TABS=1) and Playwright for Python (it
drives the installed Chrome):

    MOCK_SPLIT_TABS=1 PORT=18963 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18963 npx vite --port 5963 --strictPort &)
    uv run --no-project --with playwright==1.58.0 python3 scripts/test/web_mobile_shell_e2e.py http://127.0.0.1:5963 [--shots <dir>]

Prints one line per check and a verdict; exits 1 on the first failure. --shots saves the drawer, both pages and the
offline page at 390 × 844, 360 × 740 and 844 × 390, light and dark.
"""
import argparse
import os
import sys
from urllib.parse import quote

from playwright.sync_api import expect, sync_playwright

UA = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"
CODEX = "5b1170b5-0000-4000-8000-000000000002"
TAB1 = "/t/server/w1/" + quote("w1:t1", safe="")
SIZES = [(390, 844), (360, 740), (844, 390)]


def ok(msg):
    print("ok  " + msg, flush=True)


def phone(browser, w=390, h=844, theme="light"):
    ctx = browser.new_context(viewport={"width": w, "height": h}, device_scale_factor=3, is_mobile=True, has_touch=True, user_agent=UA, color_scheme=theme)
    page = ctx.new_page()
    page.set_default_timeout(15000)
    return ctx, page


def sign_in(page, base, token, theme="light", grouping="machine"):
    page.goto(base + "/overview?token=" + token)
    # signed in (the link's token traded for this browser's) before going anywhere else
    expect(page.locator("aside")).to_be_attached(timeout=30000)
    page.evaluate(f"Object.keys(localStorage).filter(k => k.startsWith('fold:')).forEach(k => localStorage.removeItem(k)); localStorage.setItem('persona.theme', '{theme}'); localStorage.setItem('persona.agentGrouping', '{grouping}')")


class Touch:
    """Real touch drags through CDP: a start, moves every ~16 ms, an end."""

    def __init__(self, page):
        self.page = page
        self.cdp = page.context.new_cdp_session(page)

    def drag(self, x0, y0, x1, y1, steps=12, ms=16):
        send = self.cdp.send
        send("Input.dispatchTouchEvent", {"type": "touchStart", "touchPoints": [{"x": x0, "y": y0}]})
        for i in range(1, steps + 1):
            self.page.wait_for_timeout(ms)
            send("Input.dispatchTouchEvent", {"type": "touchMove", "touchPoints": [{"x": x0 + (x1 - x0) * i / steps, "y": y0 + (y1 - y0) * i / steps}]})
        send("Input.dispatchTouchEvent", {"type": "touchEnd", "touchPoints": []})
        self.page.wait_for_timeout(450)  # the drawer settles (300 ms)


# What iOS may take the installed app's status bar strip from (#2102): a full-width fixed element at the top of the page
# with a background colour, even at opacity 0. With nothing open there must be none.
TOP_TINTS = """() => [...document.querySelectorAll('body *')].filter((e) => {
  const cs = getComputedStyle(e);
  if (cs.position !== 'fixed' || cs.display === 'none') return false;
  const r = e.getBoundingClientRect();
  if (r.top > 1 || r.width < innerWidth - 1 || r.height < 40) return false;
  const bg = cs.backgroundColor;
  return bg !== 'transparent' && !/rgba\\([^)]*,\\s*0\\)$/.test(bg);
}).map((e) => e.className.slice(0, 60))"""


def drawer_x(page):
    # the drawer's left edge: 0 open, minus its width shut
    return page.evaluate("() => document.querySelector('aside').parentElement.getBoundingClientRect().left")


def is_open(page):
    return drawer_x(page) > -2


def side_button(page):
    return page.locator("main header").get_by_role("button", name="侧边栏").first


def gestures(browser, base, token):
    ctx, page = phone(browser)
    sign_in(page, base, token)
    page.goto(base + "/overview")
    expect(side_button(page)).to_be_visible()
    t = Touch(page)
    assert not is_open(page)

    # a drag to the right from the middle of the page, 30° off horizontal, opens it
    t.drag(120, 420, 300, 316)
    assert is_open(page), drawer_x(page)
    ok("a 30° drag to the right from the middle of the page opens the drawer")
    # open, focus is in it, the page under it is inert, and it is a modal dialog
    assert page.evaluate("() => document.querySelector('aside').parentElement.contains(document.activeElement)"), "focus not in the drawer"
    assert page.evaluate("() => document.querySelector('main').inert"), "page not inert"
    assert page.evaluate("() => document.querySelector('aside').parentElement.getAttribute('aria-modal')") == "true"
    ok("open: focus moved into it, aria-modal, the page under it inert")

    # a drag to the left closes it
    t.drag(250, 500, 60, 520)
    assert not is_open(page), drawer_x(page)
    ok("a drag to the left closes it")

    # a short slow drag snaps back; a quick flick of the same length opens
    t.drag(100, 400, 200, 400, steps=20, ms=30)
    assert not is_open(page), "a slow drag short of half opened it"
    t.drag(100, 400, 200, 400, steps=4, ms=16)
    assert is_open(page), "a flick did not open it"
    t.drag(250, 400, 60, 400)
    ok("letting go: slow and short of half snaps back, a flick of the same length opens")

    # steep: the page scrolls, the drawer stays shut
    t.drag(200, 600, 240, 300)
    assert not is_open(page)
    ok("a steep drag (> 45°) is the page's, not the drawer's")

    # while dragging, the drawer follows the finger
    cdp = t.cdp
    cdp.send("Input.dispatchTouchEvent", {"type": "touchStart", "touchPoints": [{"x": 40, "y": 400}]})
    for x in (55, 80, 110, 140):
        page.wait_for_timeout(16)
        cdp.send("Input.dispatchTouchEvent", {"type": "touchMove", "touchPoints": [{"x": x, "y": 402}]})
    mid = drawer_x(page)
    cdp.send("Input.dispatchTouchEvent", {"type": "touchEnd", "touchPoints": []})
    page.wait_for_timeout(450)
    assert -284 < mid < 0, mid
    t.drag(250, 400, 60, 400) if is_open(page) else None
    ok(f"the drawer follows the finger (at {mid:.0f}px mid-drag)")

    # shut: inert, Tab never lands in the sidebar
    assert page.evaluate("() => document.querySelector('aside').parentElement.inert")
    page.keyboard.press("Tab")
    for _ in range(12):
        page.keyboard.press("Tab")
        assert not page.evaluate("() => !!document.activeElement?.closest('aside')"), "Tab went into the shut drawer"
    ok("shut: the drawer is inert, Tab doesn't land in it")

    # the button opens it; Esc closes it and gives focus back to the button
    side_button(page).focus()
    page.keyboard.press("Enter")
    page.wait_for_timeout(400)
    assert is_open(page)
    page.keyboard.press("Escape")
    page.wait_for_timeout(400)
    assert not is_open(page)
    assert page.evaluate("() => document.activeElement?.getAttribute('aria-label')") == "侧边栏"
    ok("Esc closes it and focus goes back to the button that opened it")

    # Back closes it first, and stays on the page
    side_button(page).tap()
    page.wait_for_timeout(400)
    assert is_open(page)
    page.go_back()
    page.wait_for_timeout(400)
    assert not is_open(page)
    assert page.evaluate("location.pathname") == "/overview"
    ok("Back closes the drawer first and stays on the page")

    # the scrim fades out (it is still there, going transparent, right after closing)
    side_button(page).tap()
    page.wait_for_timeout(400)
    page.mouse.click(370, 400)
    op = page.evaluate("() => getComputedStyle(document.querySelector('main').previousElementSibling).opacity")
    page.wait_for_timeout(400)
    assert not is_open(page) and float(op) > 0, op
    ok(f"tapping the scrim closes it, fading out (opacity {op} the moment after)")
    # faded out, it leaves the page: nothing tints the top any more (iOS took the dimmed colour for the status bar)
    tints = page.evaluate(TOP_TINTS)
    assert not tints, f"with the drawer closed, fixed layers with a background still cover the top: {tints}"
    ok("closed, nothing with a background covers the top of the page (the status bar keeps the page's colour)")

    # a sideways scroller under the finger scrolls first: a code block, a wide table (a stand-in below)
    page.goto(base + TAB1)
    expect(page.locator("main textarea[aria-label=消息]")).to_be_visible()  # the chat is up (its pane tabs went in #5263)
    page.evaluate("() => { const s = document.createElement('div'); s.id = 'wide'; s.style.cssText = 'position:fixed;left:0;top:300px;width:300px;overflow-x:auto;z-index:5'; s.innerHTML = '<div style=\"width:1200px;height:80px\"></div>'; document.querySelector('main').append(s); s.scrollLeft = 200; }")
    # no move over it is cancelled, not even the first ones while the direction is undecided: on iOS a cancelled first
    # touchmove takes the scroller's own pan away for the whole touch (the drawer's handler runs before window's)
    page.evaluate("() => { window.__cancelled = 0; addEventListener('touchmove', (e) => { if (e.defaultPrevented) window.__cancelled++; }, { passive: true }); }")
    t.drag(100, 340, 250, 340)
    assert not is_open(page), "the drawer opened over a scroller that could scroll"
    cancelled = page.evaluate("window.__cancelled")
    assert cancelled == 0, f"{cancelled} touchmoves over the scroller were cancelled"
    left = page.evaluate("document.getElementById('wide').scrollLeft")
    assert left < 200, left
    page.evaluate("document.getElementById('wide').scrollLeft = 0")
    t.drag(100, 340, 250, 340)
    assert is_open(page), "at its left edge the drag should go to the drawer"
    t.drag(250, 400, 60, 400)
    page.evaluate("document.getElementById('wide').remove()")
    ok("a sideways scroller scrolls first; at its left edge the drag opens the drawer")
    ctx.close()


def reduced(browser, base, token):
    ctx, page = phone(browser)
    ctx.new_cdp_session(page).send("Emulation.setEmulatedMedia", {"features": [{"name": "prefers-reduced-motion", "value": "reduce"}]})
    sign_in(page, base, token)
    page.goto(base + "/overview")
    side_button(page).tap()
    page.wait_for_timeout(60)
    assert is_open(page), drawer_x(page)
    ok("reduced motion: the drawer is there at once")
    ctx.close()


def drawer_screen(browser, base, token, shots):
    for w, h in SIZES:
        for theme in ("light", "dark"):
            ctx, page = phone(browser, w, h, theme)
            sign_in(page, base, token, theme)
            page.goto(base + "/overview")
            side_button(page).tap()
            page.wait_for_timeout(500)
            agents = page.locator("aside").get_by_text("Agents", exact=True)
            first_machine = page.locator("aside .group\\/sec > button[aria-expanded]").first
            if h > w:
                expect(agents).to_be_in_viewport()
                box = first_machine.bounding_box()
                assert box and box["y"] + box["height"] <= h, (w, h, box)
            # every page is one tap away, in the grid
            grid = page.locator('aside nav[aria-label="页面"] a')
            assert grid.count() >= 8, grid.count()
            if shots:
                page.screenshot(path=os.path.join(shots, f"drawer-{w}x{h}-{theme}.png"))
            ctx.close()
        if h > w:
            ok(f"{w}×{h}: the drawer's first screen shows AGENTS and the first machine, the pages in a grid")


def landscape_notch(browser, base, token, shots):
    for theme in ("light", "dark"):
        ctx, page = phone(browser, 844, 390, theme)
        sign_in(page, base, token, theme)
        page.goto(base + "/overview")
        # a notch on the left: the safe area the browser would give in landscape
        page.add_style_tag(content=":root{--sal:47px} #root{padding-left:47px!important} .max-lg\\:pl-\\[env\\(safe-area-inset-left\\)\\]{padding-left:47px!important} .max-lg\\:w-\\[calc\\(284px\\+env\\(safe-area-inset-left\\)\\)\\]{width:331px!important}")
        side_button(page).tap()
        page.wait_for_timeout(500)
        x = page.locator("aside").bounding_box()["x"]
        assert x >= 47, x
        assert page.locator("aside").bounding_box()["width"] >= 283
        if shots:
            page.screenshot(path=os.path.join(shots, f"landscape-notch-{theme}.png"))
        ctx.close()
    ok("landscape: the drawer's content starts right of the notch (safe area injected)")


def switch_pages(browser, base, token, shots):
    for w, h in SIZES:
        for theme in ("light", "dark"):
            ctx, page = phone(browser, w, h, theme)
            sign_in(page, base, token, theme)
            page.goto(base + "/")
            sw = page.locator('main header nav[aria-label="切换页面"]')
            expect(sw).to_be_visible()
            page.wait_for_timeout(600)

            def places():
                hdr = page.locator("main header").first
                btns = hdr.locator("button:visible")
                # the switch takes the room left between the slots (#5049): it touches no button, its labels show whole
                clear = page.evaluate("""() => { const hd = document.querySelector('main header'), sw = hd.querySelector('nav[aria-label="切换页面"]');
                  const s = sw.getBoundingClientRect();
                  const hit = [...hd.querySelectorAll('button, a')].filter((e) => !sw.contains(e) && e.getBoundingClientRect().width)
                    .filter((e) => { const r = e.getBoundingClientRect(); return r.right > s.left && r.left < s.right; }).map((e) => e.getAttribute('aria-label'));
                  const cut = [...sw.querySelectorAll('[data-switch-label]')].filter((e) => e.scrollWidth > e.clientWidth).map((e) => e.textContent);
                  return { hit, cut, height: s.height }; }""")
                assert not clear["hit"] and not clear["cut"] and clear["height"] <= 40, (page.url, clear)
                return [tuple(round(v) for v in (b["x"], b["y"], b["width"], b["height"])) for b in (btns.nth(0).bounding_box(), btns.nth(btns.count() - 1).bounding_box())], tuple(round(v) for v in sw.bounding_box().values())

            a_buttons, a_switch = places()
            if shots:
                page.screenshot(path=os.path.join(shots, f"assistant-{w}x{h}-{theme}.png"))
            sw.get_by_role("link", name="Agents").tap()
            page.wait_for_function("location.pathname === '/agents'")
            expect(page.locator("main span", has_text="Agents").first).to_be_visible()
            expect(page.locator("main .group\\/sec > button[aria-expanded]").first).to_be_visible()
            page.wait_for_timeout(400)
            g_buttons, g_switch = places()
            if shots:
                page.screenshot(path=os.path.join(shots, f"agents-{w}x{h}-{theme}.png"))
            assert a_buttons == g_buttons, (a_buttons, g_buttons)
            # the switch itself may sit off by what the slots differ (#5049: the main chat's right slot holds more buttons)
            assert abs(a_switch[1] - g_switch[1]) <= 1, (a_switch, g_switch)
            sw.get_by_role("link", name="助手").tap()
            page.wait_for_function("location.pathname === '/'")
            ctx.close()
        ok(f"{w}×{h}:「助手 | Agents」switches pages, the side buttons stay in place, the switch clear of them and whole")

    # a row on the Agents page opens its conversation
    ctx, page = phone(browser)
    sign_in(page, base, token)
    page.goto(base + "/agents")
    page.locator(f'main a[href="{TAB1}"]').tap()
    page.wait_for_function(f"location.pathname.startsWith({TAB1!r})")
    ok("a row on the Agents page opens its conversation")
    ctx.close()

    # wide: /agents opens the same list as a page titled Agents, no switch
    ctx = browser.new_context(viewport={"width": 1280, "height": 820})
    page = ctx.new_page()
    sign_in(page, base, token)
    page.goto(base + "/agents")
    expect(page.locator("main h1", has_text="Agents")).to_be_visible()
    expect(page.locator('main nav[aria-label="切换页面"]')).to_be_hidden()
    expect(page.locator("main .group\\/sec > button[aria-expanded]").first).to_be_visible()
    # and the wide sidebar is the list it was, rows not a grid
    expect(page.locator('aside nav[aria-label="页面"]')).to_have_count(0)
    expect(page.locator('aside a[href="/settings"]')).to_have_text("设置")
    ok("wide: /agents is a page titled Agents (no switch); the sidebar keeps its rows")
    ctx.close()


def row_menu(browser, base, token):
    ctx, page = phone(browser)
    sign_in(page, base, token)
    page.goto(base + "/overview")
    side_button(page).tap()
    page.wait_for_timeout(400)
    more = page.locator("aside").get_by_role("button", name="Agent 的操作").first
    box = more.bounding_box()
    assert box and box["width"] >= 44 and box["height"] >= 44, box
    more.tap()
    expect(page.get_by_role("menuitem", name="关联到待办…")).to_be_visible()
    ok("a row's ⋯ is 44 × 44 and has 关联到待办… (no long press needed)")
    ctx.close()


def old_link(browser, base, token):
    """The chat's pane tabs went in #5263 (a tab's workers live in the lead's Team panel, web_team_e2e.py); an old /s/ link
    to a pane in a tab still becomes its /t/."""
    # an old /s/ link of a pane in a tab becomes its /t/ (replaced, Back doesn't come back to it)
    ctx, page = phone(browser)
    sign_in(page, base, token)
    page.goto(base + "/overview")
    page.goto(base + f"/s/server/{CODEX}")
    page.wait_for_function(f"location.pathname === {TAB1 + '/' + CODEX!r}")
    page.go_back()
    page.wait_for_function("location.pathname === '/overview'")
    ok("an old /s/ link opens as its tab's /t/, in place")
    ctx.close()


def lazy_pages(browser, base, token):
    ctx, page = phone(browser)
    sign_in(page, base, token)
    for path, text in (("/settings", "设置"), ("/inbox", "收件箱"), ("/kb", "知识库"), ("/machines", "机器"), ("/workspace-board", "看板"), ("/routines", "定时任务"), ("/todos", "待办"), ("/history", "对话")):
        page.goto(base + path)
        expect(page.locator("main").get_by_text(text).first).to_be_visible()
    # and in-app navigation to one that has not loaded yet
    page.goto(base + "/overview")
    side_button(page).tap()
    page.locator('aside nav[aria-label="页面"] a[href="/settings"]').tap()
    expect(page.locator("main").get_by_text("设置").first).to_be_visible()
    ok("the pages loaded on demand open, by link and from the drawer")
    ctx.close()


# The hub can't be reached: every socket to it fails at once (window.__hubDown, before the page's own scripts), and its
# API requests fail. (Playwright's route_web_socket handlers deadlock the sync API when they close a socket.)
HUB_DOWN = """
window.__hubDown = true;
const Real = window.WebSocket;
window.WebSocket = function (url, protocols) {
  if (window.__hubDown && String(url).includes('/api/v1/ws')) url = 'ws://127.0.0.1:9/closed';
  return new Real(url, protocols);
};
window.WebSocket.prototype = Real.prototype;
Object.assign(window.WebSocket, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 });
"""


def offline(browser, base, token, shots):
    for theme in ("light", "dark"):
        ctx, page = phone(browser, theme=theme)
        sign_in(page, base, token, theme)
        page.add_init_script(HUB_DOWN)
        page.route("**/api/v1/**", lambda r: r.abort())
        page.reload()
        expect(page.locator("main").get_by_text("离线", exact=True)).to_be_visible(timeout=20000)
        retry = page.locator("main").get_by_role("button", name="重试")
        expect(retry).to_be_visible()
        expect(page.locator("main").get_by_text("加载中")).to_have_count(0)
        if shots:
            page.wait_for_timeout(300)
            page.screenshot(path=os.path.join(shots, f"offline-{theme}.png"))
        # back online: a tap on "Retry" brings the page back
        page.unroute("**/api/v1/**")
        page.evaluate("window.__hubDown = false")
        retry.tap()
        expect(page.locator("main").get_by_text("离线", exact=True)).to_have_count(0, timeout=20000)
        expect(page.locator("main header").first).to_be_visible()
        ctx.close()
    ok("unreachable: 离线 with 重试 instead of loading forever; 重试 brings the page back")


# iOS: the keyboard shrinks the visual viewport only; a stand-in the page follows (as web_mobile_input_e2e.py's).
PALETTE_VV = """(() => {
  const et = new EventTarget();
  const vv = { height: innerHeight, width: innerWidth, offsetTop: 0, offsetLeft: 0, scale: 1, pageTop: 0,
    addEventListener: et.addEventListener.bind(et), removeEventListener: et.removeEventListener.bind(et) };
  Object.defineProperty(window, 'visualViewport', { get: () => vv });
  window.__kb = (h, top = 0) => { vv.height = h; vv.offsetTop = top; et.dispatchEvent(new Event('resize')); };
})();"""


def palette_back(browser, base, token):
    """The search palette on a phone (#11442): Back closes it first, from /agents and from the drawer, and a pick leaves no
    copy of the page under the new one; it fits above the keyboard; a gone tab and an ambiguous /s/ link have the header."""
    ctx, page = phone(browser)
    ctx.add_init_script(PALETTE_VV)
    sign_in(page, base, token)
    field = page.locator("[role=dialog] input[role=combobox]")

    def back_to(path, what):
        page.go_back()
        page.wait_for_function(f"location.pathname === {path!r}")
        expect(field).to_have_count(0)
        page.wait_for_timeout(300)
        assert not is_open(page), f"{what}: the drawer is open"

    def agents():
        page.goto(base + "/persona")
        page.locator('main header nav[aria-label="切换页面"]').get_by_role("link", name="Agents").tap()
        page.wait_for_function("location.pathname === '/agents'")
        expect(page.locator("main header").get_by_role("button", name="搜索会话")).to_be_visible()

    agents()
    page.locator("main header").get_by_role("button", name="搜索会话").tap()
    expect(field).to_be_visible()
    back_to("/agents", "Back from the palette")
    back_to("/persona", "the next Back")
    ok("/agents › search › Back: the palette closes on /agents; the next Back goes to /persona")

    # from the drawer: Search closes it and the palette takes its place, so one Back closes the palette
    agents()
    side_button(page).tap()
    page.wait_for_timeout(400)
    page.locator("aside").get_by_role("button", name="搜索会话").tap()
    expect(field).to_be_visible()
    page.wait_for_timeout(400)
    assert not is_open(page), "the drawer stayed open under the palette"
    back_to("/agents", "Back from the drawer's palette")
    # and a pick goes to its page; one Back comes back here, the next to /persona (no dead copy of /agents between)
    side_button(page).tap()
    page.wait_for_timeout(400)
    page.locator("aside").get_by_role("button", name="搜索会话").tap()
    field.fill("设置")
    page.get_by_role("option", name="设置", exact=True).tap()
    page.wait_for_function("location.pathname === '/settings'")
    page.wait_for_timeout(500)
    back_to("/agents", "Back from the picked page")
    back_to("/persona", "the next Back")
    ok("drawer › search: Back closes the palette and stays; a pick, then one Back lands on /agents and the next on /persona")

    # Saved searches hands over to its sheet: Back returns to the search, the next Back closes it
    agents()
    page.locator("main header").get_by_role("button", name="搜索会话").tap()
    page.locator("[role=dialog]").get_by_role("button", name="已保存的搜索").tap()
    expect(page.get_by_role("button", name="返回搜索")).to_be_visible()
    expect(field).to_have_count(0)
    page.go_back()
    expect(field).to_be_visible()
    expect(page.get_by_role("button", name="返回搜索")).to_have_count(0)
    assert page.evaluate("location.pathname") == "/agents"
    page.wait_for_timeout(300)
    back_to("/agents", "Back from the search after Saved searches")
    back_to("/persona", "the next Back")
    ok("Saved searches: Back goes back to the search, then closes it; no entry left behind")

    # a command that opens a sheet (New agent) reuses the palette's entry: Back closes the sheet and stays
    agents()
    page.locator("main header").get_by_role("button", name="搜索会话").tap()
    field.fill(">新建 Agent")
    page.get_by_role("option", name="新建 Agent").first.tap()
    sheet = page.locator("[role=dialog][aria-modal=true]").filter(has_not=page.locator("input[role=combobox]"))
    expect(sheet).to_be_visible()
    page.wait_for_timeout(300)
    page.go_back()
    expect(sheet).to_have_count(0)
    page.wait_for_timeout(500)
    assert page.evaluate("location.pathname") == "/agents"
    back_to("/persona", "Back after the command's sheet")
    ok("> New agent: its sheet takes the palette's entry; Back closes it, the next Back goes to /persona")

    # reloaded with it open, the first Back leaves the page (its entry is settled at start, lib/back-close.ts)
    agents()
    page.locator("main header").get_by_role("button", name="搜索会话").tap()
    expect(field).to_be_visible()
    page.reload()
    expect(page.locator("main header").get_by_role("button", name="搜索会话")).to_be_visible()
    page.wait_for_timeout(800)
    page.go_back()
    page.wait_for_function("location.pathname === '/persona'")
    ok("reloaded with the palette open, the first Back leaves /agents")

    # the keyboard up: the card stays above it and its last row scrolls into view; down, nothing moved
    page.goto(base + "/agents")
    page.locator("main header").get_by_role("button", name="搜索会话").tap()
    expect(field).to_be_visible()
    page.wait_for_timeout(500)
    card = "() => { const r = document.querySelector('[role=dialog][aria-modal=true]').getBoundingClientRect(); return [r.top, r.bottom, r.height]; }"
    before = page.evaluate(card)
    page.evaluate("__kb(500)")
    page.wait_for_timeout(300)
    page.evaluate("() => { const l = document.querySelector('[role=dialog][aria-modal=true] .overflow-y-auto'); l.scrollTop = l.scrollHeight; }")
    page.wait_for_timeout(200)
    last = page.evaluate("() => { const o = [...document.querySelectorAll('[role=dialog] [role=option]')]; return o.length ? o[o.length - 1].getBoundingClientRect().bottom : null; }")
    top, bottom, _ = page.evaluate(card)
    assert last is not None and last <= 500, f"the last row ends at {last}, under the keyboard at 500"
    assert bottom <= 500, f"the card ends at {bottom}, under the keyboard at 500"
    page.evaluate("__kb(844)")
    page.wait_for_timeout(300)
    after = page.evaluate(card)
    assert after == before, (before, after)
    ok(f"keyboard up: the card ends at {bottom:.0f} and its last row at {last:.0f}, above the keyboard (500); down, it is where it was")
    ctx.close()

    # a gone tab and an ambiguous /s/ prefix: the header with the sidebar button, a 44 px target
    ctx, page = phone(browser)
    sign_in(page, base, token)
    for path, text in (("/t/server/w9/" + quote("w9:t9", safe=""), "这个标签页已经不在了"), (f"/s/server/{CODEX[:8]}", "选一个打开")):
        page.goto(base + path)
        expect(page.locator("main").get_by_text(text)).to_be_visible()
        btn = page.locator("main header").get_by_role("button", name="侧边栏")
        expect(btn).to_be_visible()
        b = btn.bounding_box()
        cx, cy = b["x"] + b["width"] / 2, b["y"] + b["height"] / 2
        for dx, dy in ((21, 0), (-21, 0), (0, 21), (0, -21)):
            assert page.evaluate(f"() => document.elementFromPoint({cx + dx}, {cy + dy})?.closest('button')?.getAttribute('aria-label')") == "侧边栏", (path, dx, dy)
        btn.tap()
        page.wait_for_timeout(400)
        assert is_open(page), path
        page.go_back()
        page.wait_for_timeout(500)
        assert not is_open(page), path
    ok("a gone tab and an ambiguous /s/ link have the header's sidebar button (44 px to a finger), which opens the drawer")
    ctx.close()


def composer_back(browser, base, token, shots):
    """Back on a phone closes the composer's overlays first (#11442): the full editor, the + panel and the model panel
    each come off with one Back and the chat stays; one closed by its own control leaves no Back that does nothing.
    /model with the Send button opens the model panel instead of sending. In New Agent's sheet a pick closes only the
    panel and the next Back closes the sheet; Configure models leaves no dead entry under Settings."""
    claude = TAB1 + "/5b1170b5-0000-4000-8000-000000000001"
    other = "/t/server/w2/" + quote("w2:t1", safe="") + "/5b1170b5-0000-4000-8000-000000000004"
    ctx, page = phone(browser)
    sign_in(page, base, token)

    def visit(path):  # an in-app navigation (a link's), one history entry
        page.evaluate("p => { history.pushState(null, '', p); dispatchEvent(new PopStateEvent('popstate')); }", path)

    def marked():
        return page.evaluate("() => Boolean(history.state && history.state.personaOverlay)")

    box = page.locator("main textarea[data-composer-input]")
    visit(other)
    expect(box).to_be_visible(timeout=20000)
    visit(claude)
    expect(page.locator("main button[data-model-pill]").first).to_be_visible(timeout=20000)
    chat = page.url

    def back_closes(what, overlay):
        expect(overlay).to_be_visible()
        page.go_back()
        expect(overlay).to_have_count(0)
        assert page.url == chat, f"{what}: Back left the chat for {page.url}"
        assert not marked(), f"{what}: an overlay entry is left"
        ok(f"Back closes {what} and stays in the chat")

    box.fill("\n".join(f"line {i}" for i in range(9)))
    page.locator("[data-testid=composer-expand]").tap()
    back_closes("the full editor", page.locator("[data-testid=composer-expanded]"))
    assert box.input_value().startswith("line 0"), "the draft went with the full editor"
    box.fill("")
    page.locator("main [data-composer-plus]").first.tap()
    back_closes("the + panel", page.locator("[data-composer-panel=plus]"))
    page.locator("main button[data-model-pill]").first.tap()
    back_closes("the model panel", page.locator("[data-model-panel]"))

    box.fill("/model")
    page.locator("main [data-testid=composer-send]").tap()
    expect(page.locator("[data-model-panel]")).to_be_visible()
    assert box.input_value() == "", "/model stayed in the box"
    ok("/model with the Send button opens the model panel instead of sending")
    if shots:
        page.wait_for_timeout(300)
        page.screenshot(path=os.path.join(shots, "composer-model-command.png"))
    back_closes("the model panel opened by /model", page.locator("[data-model-panel]"))

    # closed from the page (a tap outside it), its entry comes off: the next Back leaves the chat
    page.locator("main [data-composer-plus]").first.tap()
    expect(page.locator("[data-composer-panel=plus]")).to_be_visible()
    page.touchscreen.tap(195, 200)
    expect(page.locator("[data-composer-panel=plus]")).to_have_count(0)
    page.wait_for_timeout(500)
    assert page.url == chat, page.url
    assert not marked(), "the + panel's entry stayed after it closed"
    page.go_back()
    expect(page).to_have_url(base + other)
    ok("the + panel closed by a tap outside leaves no entry: the next Back goes to the chat before")

    # New Agent's sheet: the panel's entry is on the sheet's
    visit("/agents")
    agents = page.url

    def sheet():
        page.locator("main").get_by_role("button", name="新建 Agent").first.tap()
        page.get_by_role("menuitem", name="新建 Agent").first.tap()
        d = page.get_by_role("dialog", name="新建 Agent", exact=True)
        expect(d).to_be_visible()
        pill = d.locator('[data-testid="new-agent-model"] [data-model-pill]')
        expect(pill).to_have_attribute("data-model-pill", "ready")
        pill.tap()
        expect(page.locator("[data-model-panel]")).to_be_visible()
        return d

    d = sheet()
    page.locator('[data-model-panel] [data-panel-row="model:claude\\a sonnet"]').tap(position={"x": 48, "y": 12})
    expect(page.locator("[data-model-panel]")).to_have_count(0)
    page.wait_for_timeout(500)
    expect(d).to_be_visible()
    depth = page.evaluate("() => history.state && history.state.personaOverlayDepth")
    assert depth == 1, f"after a pick the history has {depth} overlay entries, not the sheet's alone"
    page.go_back()
    expect(d).to_have_count(0)
    assert page.url == agents, page.url
    page.wait_for_timeout(500)
    assert not marked(), "the sheet's entry stayed"
    ok("in New Agent's sheet a pick closes only the model panel; the next Back closes the sheet")

    sheet()
    page.locator("[data-model-panel]").get_by_role("button", name="配置模型").tap()
    page.wait_for_url(lambda u: "/settings" in u)
    page.go_back()
    expect(page).to_have_url(agents)
    page.wait_for_timeout(500)
    assert not marked(), "a dead overlay entry under Settings"
    expect(page.get_by_role("dialog", name="新建 Agent", exact=True)).to_have_count(0)
    ok("Configure models from New Agent's panel: Back from Settings lands on the page, no dead entry")
    ctx.close()


# what of the panel a finger reaches: its buttons and its header's icon and title, as [leftmost left, rightmost right]
PANEL_EDGES = """(sel) => { const a = document.querySelector(sel); if (!a) return null;
  const els = [...a.querySelectorAll('button, header > *')].filter((e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0; });
  const r = a.getBoundingClientRect();
  return { left: Math.min(...els.map((e) => e.getBoundingClientRect().left)), right: Math.max(...els.map((e) => e.getBoundingClientRect().right)),
    aside: [r.left, r.right], pad: getComputedStyle(a).paddingLeft, inner: innerWidth }; }"""


def panel_safe_area(browser, base, token, shots):
    """A full-screen side panel on a phone on its side with a notch (#11511): Files and Git keep their buttons clear of
    the left and right safe areas (the browser's insets, set through CDP), their background still under the notch; a
    phone held upright has no inset and no padding."""
    chat = TAB1 + "/" + CODEX
    weather = "/home/me/code/weather"
    open_panel = "async (p) => { const m = await import('/src/lib/panel.ts'); m.openPanel(p); }"
    for theme in ("light", "dark"):
        ctx, page = phone(browser, 844, 390, theme)
        ctx.new_cdp_session(page).send("Emulation.setSafeAreaInsetsOverride", {"insets": {"left": 47, "right": 47}})
        sign_in(page, base, token, theme)
        page.goto(base + chat)
        expect(page.locator("main header")).to_be_visible()
        for kind, label in (("files", "文件"), ("git", "Git")):
            page.evaluate(open_panel, {"kind": kind, "mid": "server", "sid": CODEX, "cwd": weather})
            sel = f"aside[aria-label={label}]"
            expect(page.locator(sel)).to_be_visible()
            page.wait_for_timeout(500)  # its slide in
            e = page.evaluate(PANEL_EDGES, sel)
            assert e and e["left"] >= 47 and e["right"] <= e["inner"] - 47, f"{kind} {theme}: under the notch ({e})"
            assert e["aside"][0] <= 0.5 and e["aside"][1] >= e["inner"] - 0.5, f"{kind} {theme}: the panel no longer spans the screen ({e})"
            if shots:
                page.screenshot(path=os.path.join(shots, f"panel-notch-{kind}-{theme}.png"))
        ctx.close()
    ctx, page = phone(browser, 390, 844)
    sign_in(page, base, token)
    page.goto(base + chat)
    expect(page.locator("main header")).to_be_visible()
    page.evaluate(open_panel, {"kind": "files", "mid": "server", "sid": CODEX, "cwd": weather})
    expect(page.locator("aside[aria-label=文件]")).to_be_visible()
    pad = page.evaluate(PANEL_EDGES, "aside[aria-label=文件]")["pad"]
    assert pad == "0px", f"upright, the panel is padded ({pad})"
    ctx.close()
    ok("844×390 with a 47 px notch each side: Files' and Git's buttons start at 47 and end 47 short of the right edge; upright, no padding")


def agents_list_phone(browser, base, token, shots):
    """The Agents list on a phone (#11511): Back from an Agent the page opened lands where the list was, a fresh visit at
    the top; the title row's Filter, ⋯ and + (4 px apart) each take their own taps, on the page and in the drawer; By
    to-do's rows are 44 px tall and fold from their count or just off their title; a landscape drawer shows no ⌘K."""
    scroller = "document.querySelector('main [data-row-scroller]')"
    # a row a finger reaches: inside the scroller's view and not under a sticky heading
    reachable = f"""() => {{ const s = {scroller}, b = s.getBoundingClientRect();
      for (const a of s.querySelectorAll('a[href^="/t/"], a[href^="/s/"]')) {{
        const r = a.getBoundingClientRect(), x = r.left + r.width / 2, y = r.top + r.height / 2;
        if (y > b.top && y < b.bottom && a.contains(document.elementFromPoint(x, y))) return [x, y];
      }} return null; }}"""
    # 375 × 548: an iPhone SE's page under Safari's bars, where the fixture's list is longer than the screen
    for w, h in ((844, 390), (375, 548)):
        ctx, page = phone(browser, w, h)
        sign_in(page, base, token)
        page.goto(base + "/")
        sw = page.locator('main header nav[aria-label="切换页面"]')
        sw.get_by_role("link", name="Agents").tap()
        page.wait_for_function("location.pathname === '/agents'")
        expect(page.locator('main [data-row-scroller] a[href^="/t/"]').first).to_be_visible()
        page.wait_for_timeout(600)
        room = page.evaluate(f"() => {scroller}.scrollHeight - {scroller}.clientHeight")
        assert room >= 80, f"{w}×{h}: the Agents list is too short to scroll ({room} px)"
        target = min(600, room)
        while True:
            page.evaluate(f"() => {{ {scroller}.scrollTop = {target}; }}")
            page.wait_for_timeout(300)
            at = page.evaluate(f"() => {scroller}.scrollTop")
            spot = page.evaluate(reachable)
            if spot or target < 60:
                break
            target -= 40
        assert spot and at > 40, f"{w}×{h}: no row to tap below the top ({at})"
        page.touchscreen.tap(*spot)
        page.wait_for_function("location.pathname !== '/agents'")
        page.wait_for_timeout(500)
        page.go_back()
        page.wait_for_function("location.pathname === '/agents'")
        page.wait_for_timeout(1000)
        back = page.evaluate(f"() => {scroller}.scrollTop")
        assert abs(back - at) <= 2, f"{w}×{h}: Back landed at {back}, the list was at {at}"
        sw.get_by_role("link", name="助手").tap()
        page.wait_for_function("location.pathname === '/'")
        sw.get_by_role("link", name="Agents").tap()
        page.wait_for_function("location.pathname === '/agents'")
        page.wait_for_timeout(600)
        fresh = page.evaluate(f"() => {scroller}.scrollTop")
        assert fresh == 0, f"{w}×{h}: a fresh visit opened at {fresh}"
        ok(f"{w}×{h}: Back from an Agent lands where the Agents list was ({at} px); a fresh visit opens at the top")
        ctx.close()

    # each icon's own box answers with its own button; a tap 3 px inside a button's right edge is that button's
    title_row = """(root) => [...document.querySelector(root + ' [data-testid=agent-filter-button]').parentElement.querySelectorAll(':scope > button[aria-label]')]
      .map((b) => { const r = b.getBoundingClientRect(), i = b.querySelector('svg').getBoundingClientRect(), y = i.top + i.height / 2;
        const own = [i.left + 1, i.left + i.width / 2, i.right - 1].every((x) => document.elementFromPoint(x, y)?.closest('button') === b)
          && [i.top + 1, i.bottom - 1].every((yy) => document.elementFromPoint(i.left + i.width / 2, yy)?.closest('button') === b);
        return { label: b.getAttribute('aria-label'), own, menu: b.dataset.testid !== 'agents-fold-all', right: r.right, y: r.top + r.height / 2, w: r.width, h: r.height }; })"""

    def taps_own(page, root, where):
        buttons = page.evaluate(title_row, root)
        assert len(buttons) >= 2, (where, buttons)
        for b in buttons:
            assert b["own"], f"{where}: {b['label']}'s icon answers with another button"
            assert b["w"] >= 44 and b["h"] >= 44, f"{where}: {b['label']} is {b['w']}×{b['h']}"
        for b in (b for b in buttons if b["menu"]):  # Filter, ⋯ and + open menus; fold-all would fold the list
            button = page.locator(f'{root} button[aria-label="{b["label"]}"]').first
            page.touchscreen.tap(b["right"] - 3, b["y"])
            if button.get_attribute("aria-haspopup") == "menu":
                expect(button).to_have_attribute("aria-expanded", "true")
            else:
                expect(page.get_by_role("menuitem", name=b["label"]).first).to_be_visible()
            page.keyboard.press("Escape")
            expect(page.get_by_role("menu")).to_have_count(0)
            page.wait_for_timeout(500)
        return [b["label"] for b in buttons]

    for w, h in ((390, 844), (360, 740)):
        ctx, page = phone(browser, w, h)
        sign_in(page, base, token)
        page.goto(base + "/agents")
        expect(page.locator("main [data-testid=agent-filter-button]")).to_be_visible()
        page.wait_for_timeout(500)
        on_page = taps_own(page, "main", f"/agents {w}")
        if shots:
            page.screenshot(path=os.path.join(shots, f"agents-title-row-{w}.png"))
        page.goto(base + "/overview")
        side_button(page).tap()
        page.wait_for_timeout(500)
        page.evaluate("() => document.querySelector('aside [data-testid=agent-filter-button]').scrollIntoView({ block: 'center' })")
        page.wait_for_timeout(400)
        in_drawer = taps_own(page, "aside", f"drawer {w}")
        title = page.evaluate("() => { const t = document.querySelector('aside [data-sidebar-agents-controls] button:not([aria-label]) span'); return t.scrollWidth <= t.clientWidth; }")
        assert title, f"drawer {w}: the Agents title is cut"
        if shots:
            page.screenshot(path=os.path.join(shots, f"drawer-title-row-{w}.png"))
        ok(f"{w}: the Agents title row's buttons are 44 px and take their own taps ({', '.join(on_page)}; drawer: {', '.join(in_drawer)})")
        ctx.close()

    ctx, page = phone(browser)
    sign_in(page, base, token, grouping="todo")
    page.goto(base + "/agents")
    group = page.locator('main [data-tree-branch="todo:none"] [data-tree-branch-row]')
    expect(group).to_be_visible()
    page.wait_for_timeout(500)
    heights = page.evaluate("() => [...document.querySelectorAll('main [data-tree-branch-row]')].map((r) => r.getBoundingClientRect().height)")
    assert heights and min(heights) >= 44, heights
    chevron = group.locator("button[aria-expanded]").first
    was = chevron.get_attribute("aria-expanded")
    count = group.locator(":scope > span[title]").first.bounding_box()
    page.touchscreen.tap(count["x"] + count["width"] / 2, count["y"] + count["height"] / 2)
    expect(chevron, "a tap on Not on a to-do's count folds it").not_to_have_attribute("aria-expanded", was)
    page.wait_for_timeout(400)
    name = group.locator("button:not([aria-label])").first.bounding_box()
    page.touchscreen.tap(name["x"] + name["width"] / 2, name["y"] + name["height"] / 2 - 15)
    expect(chevron, "a tap 15 px above Not on a to-do's title folds it back").to_have_attribute("aria-expanded", was)
    ok(f"By to-do: its {len(heights)} rows are 44 px tall and fold from their count and from just above their title")
    ctx.close()

    ctx, page = phone(browser, 844, 390)
    sign_in(page, base, token)
    page.goto(base + "/overview")
    side_button(page).tap()
    page.wait_for_timeout(500)
    hints = page.evaluate("() => [...document.querySelectorAll('aside button kbd')].map((k) => k.getBoundingClientRect().width)")
    assert hints and all(width == 0 for width in hints), hints
    ok("844×390: the drawer's search box shows no ⌘K (no keyboard to press it on)")
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
        gestures(browser, base, a.token)
        reduced(browser, base, a.token)
        drawer_screen(browser, base, a.token, a.shots)
        landscape_notch(browser, base, a.token, a.shots)
        switch_pages(browser, base, a.token, a.shots)
        row_menu(browser, base, a.token)
        old_link(browser, base, a.token)
        lazy_pages(browser, base, a.token)
        offline(browser, base, a.token, a.shots)
        palette_back(browser, base, a.token)
        composer_back(browser, base, a.token, a.shots)
        panel_safe_area(browser, base, a.token, a.shots)
        agents_list_phone(browser, base, a.token, a.shots)
        browser.close()
    print("PASS web mobile shell e2e")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:  # one line of verdict on any failure
        print(f"FAIL web mobile shell e2e: {e}", file=sys.stderr)
        sys.exit(1)
