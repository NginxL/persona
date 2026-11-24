#!/usr/bin/env python3
"""End-to-end check that a web chat opens at its newest message and stays there (#1340): first open, reload, switching
from the sidebar, content that keeps landing after the first paint (images, cards), and the composer growing under the
list. A reader who scrolled up is not pulled back.

The race behind #1340: the chat pins itself to the end, and when more content lands before that scroll's event fires,
the event sees the end far away and used to take it as the reader leaving. The check makes content land in a burst
(one piece per task, faster than a frame), like images decoding one after another.

Needs a running web page against the fake hub with the long conversations, and Playwright for Python (it drives the
installed Chrome, or the browser PERSONA_E2E_CHROMIUM points at):

    MOCK_TASKS=1 MOCK_LONG_ASSISTANT=1 MOCK_LONG_HISTORY=1 PORT=18792 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18792 npx vite --port 5292 --strictPort &)
    python3 scripts/test/web_chat_scroll_e2e.py http://127.0.0.1:5292 [--hub http://127.0.0.1:18792] [--shots <dir>]

    # The same checks with deferred bodies already active, including the upward pages:
    MOCK_TASKS=1 MOCK_LONG_ASSISTANT=1 MOCK_LONG_HISTORY=90 PORT=18792 python3 scripts/mock/mock_hub.py &
    python3 scripts/test/web_chat_scroll_e2e.py http://127.0.0.1:5292 --hub http://127.0.0.1:18792 --windowed

Finished exchanges before the last two skip drawing while off screen (#2485): in the agent chat, what is on screen
holds still while they are drawn on the way up, the browser's find reaches their words, and a rail jump and a search
jump land on their row and stay there.

Prints one line per check and a verdict; exits 1 on the first failure. --shots saves light / dark / narrow screenshots.
--hub lets the fake hub push a new exchange while the reader is up inside the last one (the exchange being read stays
in place), then turns on its paging and a slow network: the earlier pages come in two exchanges at a time while the
reader scrolls up, and the rows on screen hold still, the loading row drawn over the chat's top padding, with nothing
moving the view while a page is on its way. The same on a touch phone, left from the end with a finger (then it
resets the fake hub).
"""
import argparse
import json
import os
import re
import sys
import urllib.parse
import urllib.request

from playwright.sync_api import expect, sync_playwright

SCROLL = "[data-chat-scroll]"
# The agent chat with fifteen earlier exchanges (MOCK_LONG_HISTORY).
AGENT = ("macbook", "a1f3c2d4-0000-4000-8000-000000000001")

METRICS = """() => {
  const s = document.querySelector('[data-chat-scroll]');
  if (!s || !s.firstElementChild || s.firstElementChild.childElementCount < 2) return null;
  return { gap: s.scrollHeight - s.scrollTop - s.clientHeight, top: s.scrollTop, height: s.scrollHeight, view: s.clientHeight };
}"""

# Content lands in a burst below the newest message: one piece per task, each before the next frame.
BURST = """async (n) => {
  const s = document.querySelector('[data-chat-scroll]');
  for (let i = 0; i < n; i++) {
    await new Promise((r) => setTimeout(r, 7));
    const d = document.createElement('div');
    d.style.height = '120px';
    d.setAttribute('data-e2e-burst', '');
    s.firstElementChild.appendChild(d);
  }
  await new Promise((r) => setTimeout(r, 600));
}"""

# The composer's text box: the textarea outside the list (cards in the list have their own).
COMPOSER = """() => [...document.querySelectorAll('main textarea')].filter((t) => !t.closest('[data-chat-scroll]')).pop()"""


def ok(msg):
    print("ok  " + msg, flush=True)


def fail(msg):
    print("FAIL " + msg, flush=True)
    sys.exit(1)


def settled(page, what, timeout=8000):
    """Waits until the list has content and its size holds still for half a second; returns its metrics."""
    last, still, waited = None, 0, 0
    while waited < timeout:
        m = page.evaluate(METRICS)
        if m and last and abs(m["height"] - last["height"]) < 1 and abs(m["top"] - last["top"]) < 1:
            still += 100
            if still >= 500:
                return m
        else:
            still = 0
        last = m
        page.wait_for_timeout(100)
        waited += 100
    fail(f"{what}: the list did not settle ({last})")


def at_end(page, what, short_ok=False):
    m = settled(page, what)
    if m["height"] <= m["view"] + 40 and not short_ok:
        fail(f"{what}: the conversation is too short to tell ({m})")
    if m["gap"] > 2:
        fail(f"{what}: {round(m['gap'])} px above the newest message ({m})")
    ok(f"{what}: at the newest message")
    return m


def burst_stays(page, what):
    for _ in range(3):
        page.evaluate(BURST, 6)
    at_end(page, what + ", content landing in bursts")


# The dock against the chat: the chat's bottom edge, the dock's top and bottom edges, the window.
DOCK = """() => {
  const s = document.querySelector('[data-chat-scroll]').getBoundingClientRect();
  const d = document.querySelector('[data-chat-dock]').getBoundingClientRect();
  return { chat: s.bottom, top: d.top, bottom: d.bottom, win: innerHeight };
}"""


def composer_grows(page, what):
    box = page.evaluate_handle(COMPOSER).as_element()
    if not box:
        fail(f"{what}: no composer")
    box.fill("一\n二\n三\n四\n五\n六")
    at_end(page, what + ", composer grown to six lines")
    # past the box's cap (#5375): typed key by key, each one measures the box again
    box.fill("")
    box.click()
    for i in range(24):
        page.keyboard.type(f"第 {i + 1} 行，很长的一段输入内容")
        page.keyboard.press("Shift+Enter")
    at_end(page, what + ", composer typed past its cap")
    d = page.evaluate(DOCK)
    if d["top"] < d["chat"] - 1 or d["bottom"] > d["win"] + 1:
        fail(f"{what}: the dock covers the chat or leaves the window ({d})")
    ok(f"{what}: the dock sits under the chat, inside the window")
    box.fill("")
    page.wait_for_timeout(300)


def scrolled_up_stays(page, what):
    sc = page.locator(SCROLL)
    sc.hover()
    page.mouse.wheel(0, -900)
    m = settled(page, what + " scrolled up")
    if m["gap"] < 400:
        fail(f"{what}: the wheel did not move the list up ({m})")
    page.evaluate(BURST, 6)
    after = settled(page, what + " scrolled up, then content")
    if abs(after["top"] - m["top"]) > 2:
        fail(f"{what}: a reader who scrolled up was moved from {m['top']} to {after['top']}")
    ok(f"{what}: scrolled up, content landing does not pull the reader back")
    down = page.locator("[data-jump-pill]")  # "Jump to bottom" ("New messages" once something came in)
    down.wait_for(state="visible")
    down.click()
    at_end(page, what + ", 跳到底部")


OFFSCREEN = """() => {
  const cv = [...document.querySelectorAll('[data-chat-scroll] section.seg-cv')];
  return { cv: cv.length, skipped: cv.filter((s) => !s.firstElementChild?.checkVisibility({ contentVisibilityAuto: true })).length,
           placeholders: document.querySelectorAll('[data-chat-scroll] [data-segment-placeholder]').length };
}"""

# Scrolls up by script, a step a frame; per frame, how far the row at the view's middle moved beyond the step.
DRIFT = """async () => {
  const sc = document.querySelector('[data-chat-scroll]');
  const r = sc.getBoundingClientRect();
  const frame = () => new Promise((f) => requestAnimationFrame(() => f()));
  const probe = () => {
    let el = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    while (el && el !== sc && el.getBoundingClientRect().height < 4) el = el.parentElement;
    // Compensating a row above moves scrollTop, so the list wrapper moves relative to the visible rows by design.
    // Measure the exchange at this point; its section survives both content-visibility and deferred-body mounting.
    const row = el?.closest('section[data-segment]');
    if (row) return row;
    // A gap belongs to the wrapper, not to either exchange. Follow the next row instead of measuring that wrapper.
    const y = r.top + r.height / 2;
    return [...sc.querySelectorAll('section[data-segment]')].find((s) => s.getBoundingClientRect().bottom >= y) ?? el;
  };
  let worst = 0, frames = 0;
  while (sc.scrollTop > 0 && frames < 100) {
    const el = probe(), top = el.getBoundingClientRect().top, was = sc.scrollTop;
    sc.scrollTop -= 120;
    const step = was - sc.scrollTop;
    await frame();
    await frame();
    if (el.isConnected) worst = Math.max(worst, Math.abs(el.getBoundingClientRect().top - top - step));
    frames++;
  }
  return { worst, frames };
}"""

# Where a row sits under the view's top, frame by frame until it has held still for 30 frames (or 4 s).
HOLDS = """async (sel) => {
  const sc = document.querySelector('[data-chat-scroll]');
  const frame = () => new Promise((f) => requestAnimationFrame(() => f()));
  const tops = [];
  for (let i = 0, still = 0; i < 240 && still < 30; i++) {
    await frame();
    const el = typeof sel === 'string' ? document.querySelector(sel) : null;
    const top = el ? el.getBoundingClientRect().top - sc.getBoundingClientRect().top : NaN;
    still = tops.length && Math.abs(top - tops[tops.length - 1]) < 0.5 ? still + 1 : 0;
    tops.push(top);
  }
  const last = tops.slice(-30);
  return { top: tops[tops.length - 1], spread: Math.max(...last) - Math.min(...last), view: sc.clientHeight };
}"""


def offscreen_exchanges(page, what, windowed=False):
    page.reload()
    at_end(page, what + ", reload")
    s = page.evaluate(OFFSCREEN)
    if s["cv"] < 3 or s["skipped"] < 1:
        fail(f"{what}: earlier exchanges do not skip drawing off screen ({s})")
    ok(f"{what}: {s['skipped']} of {s['cv']} earlier exchanges skip drawing off screen")
    if bool(s["placeholders"]) != windowed:
        fail(f"{what}: deferred bodies do not match --windowed={windowed} ({s})")
    ok(f"{what}: {s['placeholders']} completed bodies wait off screen (windowed={windowed})")

    page.locator(SCROLL).hover()
    page.mouse.wheel(0, -200)  # leave the end as a reader does (a script's scroll is not the reader leaving)
    page.wait_for_timeout(800)
    d = page.evaluate(DRIFT)
    if d["worst"] > 1:
        fail(f"{what}: scrolling up, what is on screen moved {d['worst']:.1f} px in a frame ({d})")
    ok(f"{what}: scrolling up, what is on screen moves at most {d['worst']:.1f} px a frame beyond the scroll")

    page.locator("[data-jump-pill]").click()
    at_end(page, what + ", back to the end")
    if not page.evaluate("() => !document.querySelector('[data-chat-scroll] section.seg-cv')?.firstElementChild?.checkVisibility({ contentVisibilityAuto: true })"):
        fail(f"{what}: the first exchange is drawn while off screen")
    # wrapping around as Ctrl+F does: the search starts where the pill was clicked, below the words
    if not page.evaluate("() => window.find('第 1 个问题', false, false, true) && String(getSelection()) === '第 1 个问题'"):
        fail(f"{what}: the browser's find does not reach an exchange that skipped drawing")
    page.evaluate("() => getSelection().removeAllRanges()")
    ok(f"{what}: the browser's find reaches an exchange that skipped drawing")

    tick = page.locator("[data-nav-rail] button").nth(1)
    # A long history windows the rail too: its second visible tick is not necessarily the second question in the fixture.
    label = tick.get_attribute("aria-label") or ""
    match = re.search(r"第 \d+ 个问题", label)
    if not match:
        fail(f"{what}: the rail tick has no synthetic question in its label ({label})")
    question = match.group(0)
    tick.click()
    page.wait_for_function("""(question) => [...document.querySelectorAll('[data-chat-scroll] [data-user-bubble]')].some(
      (el) => el.textContent.includes(question))""", arg=question)
    page.evaluate("""(question) => {
      const b = [...document.querySelectorAll('[data-chat-scroll] [data-user-bubble]')].find((el) => el.textContent.includes(question));
      document.querySelectorAll('[data-e2e-row]').forEach((el) => el.removeAttribute('data-e2e-row'));
      b.closest('[data-line] > *').setAttribute('data-e2e-row', '');
    }""", question)
    h = page.evaluate(HOLDS, "[data-e2e-row]")
    if abs(h["top"] - 12) > 1 or h["spread"] > 1:
        fail(f"{what}: a rail jump left its question {h['top']:.1f} px under the top (12 wanted) or still moving ({h})")
    ok(f"{what}: a rail jump lands its question {h['top']:.1f} px under the top and it stays")

    page.locator("[data-jump-pill]").click()
    at_end(page, what + ", back to the end again")
    page.locator("header").get_by_role("button", name=re.compile(r"^(?:More actions|更多操作|その他の操作)$")).click()  # the chat's menu, not a message's
    page.get_by_role("menuitem", name=re.compile(r"^(?:Search in chat|会话内搜索|会話内を検索)$")).click()
    page.locator("[data-chat-search] input").fill("第 3 个问题")
    page.locator("[data-search-result]").first.click()
    h = page.evaluate(HOLDS, "[data-search-hit]")
    if not (0 <= h["top"] <= h["view"] - 20) or h["spread"] > 1:
        fail(f"{what}: a search jump left its hit out of view or moving ({h})")
    ok(f"{what}: a search jump brings its hit into view and it stays")
    page.locator("[data-jump-pill]").click()
    at_end(page, what + ", back to the end after the search")


# The last exchange (the one being read) and the one before it, marked to be found again.
MARK = """() => {
  const sc = document.querySelector('[data-chat-scroll]');
  const r = sc.getBoundingClientRect();
  const segs = sc.querySelectorAll('section[data-segment]');
  const [prev, last] = [segs[segs.length - 2], segs[segs.length - 1]];
  prev.setAttribute('data-e2e-prev', '');
  last.setAttribute('data-e2e-read', '');
  return { top: last.getBoundingClientRect().top - r.top, segs: segs.length, prevBottom: prev.getBoundingClientRect().bottom - r.top };
}"""

# Once one more exchange is in, over 30 frames: how far the marked exchange being read moved, and whether the one
# before it skips drawing now (it was the last but one, and is off screen).
MOVED = """async ({ top, segs }) => {
  const sc = document.querySelector('[data-chat-scroll]');
  const frame = () => new Promise((f) => requestAnimationFrame(() => f()));
  for (let i = 0; i < 600 && sc.querySelectorAll('section[data-segment]').length <= segs; i++) await frame();
  const tops = [];
  for (let i = 0; i < 30; i++) {
    await frame();
    tops.push(document.querySelector('[data-e2e-read]').getBoundingClientRect().top - sc.getBoundingClientRect().top - top);
  }
  const prev = document.querySelector('[data-e2e-prev]');
  return { arrived: sc.querySelectorAll('section[data-segment]').length > segs, worst: Math.max(...tops.map(Math.abs)),
           skipped: !prev.firstElementChild?.checkVisibility({ contentVisibilityAuto: true }) };
}"""


def new_exchange_holds(page, hub, what):
    """The reader is up inside a long last exchange when a new one comes in from elsewhere (another device, a channel):
    the exchange before it starts skipping drawing off screen and keeps the height it was drawn at, so the exchange
    being read stays where it was. (Inside it, the live row at its top goes to the new exchange, as before #2485.)"""
    long = "\n\n".join(["The cache layer reads each session once and keeps the decoded rows, so a push only touches the changed entries. " * 6] * 40)
    hub_get(hub, "/mock/say?" + urllib.parse.urlencode({"user": "How does the cache work, in detail?", "text": long}))
    page.wait_for_function("() => [...document.querySelectorAll('[data-chat-scroll] section[data-segment]')].pop()?.textContent.includes('cache layer')")
    at_end(page, what + ", a long reply came in")
    page.locator(SCROLL).hover()
    for _ in range(10):
        page.mouse.wheel(0, -240)
        page.wait_for_timeout(60)
    m = settled(page, what + " scrolled up inside the long reply")
    before = page.evaluate(MARK)
    if m["gap"] < 1500 or before["top"] > 0 or before["prevBottom"] > -m["view"]:
        fail(f"{what}: the view is not inside the long reply, or the exchange before it is not off screen ({m}, {before})")
    hub_get(hub, "/mock/say?" + urllib.parse.urlencode({"user": "A new question from another device", "text": "Done."}))
    after = page.evaluate(MOVED, before)
    if not after["arrived"] or not after["skipped"]:
        fail(f"{what}: the new exchange did not come in, or the one before the long reply did not start skipping ({after})")
    if after["worst"] > 1:
        fail(f"{what}: a new exchange coming in moved the exchange being read {after['worst']:.1f} px ({after})")
    ok(f"{what}: a new exchange coming in moves the exchange being read {after['worst']:.1f} px")


# Scrolls up by script (as DRIFT does) while the earlier pages come in; per page, how far the row at the view's middle
# moved from the frame before the page landed to 20 frames after it, and how often the page itself wrote scrollTop
# while the loading row showed. That row is drawn over the chat's top padding, out of the flow: its showing and going
# move nothing, so nothing has to move the view for it (a script moving the view stops a touch fling).
PAGED = """async () => {
  const sc = document.querySelector('[data-chat-scroll]');
  const r = sc.getBoundingClientRect();
  const frame = () => new Promise((f) => requestAnimationFrame(() => f()));
  const probe = () => {
    let el = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    while (el && el !== sc && el.getBoundingClientRect().height < 4) el = el.parentElement;
    const row = el?.closest('section[data-segment]');
    if (row) return row;
    const y = r.top + r.height / 2;
    return [...sc.querySelectorAll('section[data-segment]')]
      .find((s) => s.getBoundingClientRect().bottom >= y) ?? el;
  };
  // the page's own scrollTop writes, told apart from this script's
  const own = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollTop');
  let writes = 0, mine = false;
  Object.defineProperty(sc, 'scrollTop', { configurable: true, get() { return own.get.call(this); }, set(v) { if (!mine) writes++; own.set.call(this, v); } });
  const up = (dy) => { mine = true; sc.scrollTop -= dy; mine = false; };
  const loading = () => Boolean(sc.querySelector('[data-loading-earlier]'));
  const count = () => sc.querySelectorAll('section[data-segment]').length;
  const moves = [];
  let shown = 0, written = 0;
  try {
    for (let k = 0; k < 20; k++) {
      for (let n = 0; n < 400 && sc.scrollTop >= 300; n++) { up(120); await frame(); await frame(); }
      const el = probe(), n0 = count(), t0 = performance.now();
      let at = el.getBoundingClientRect().top, landed = -1;
      for (let i = 0; performance.now() - t0 < 5000 && (landed < 0 || i - landed < 20); i++) {
        const was = writes, showing = loading();
        await frame();
        if (landed < 0 && count() > n0) landed = i;
        else if (landed < 0) {
          at = el.getBoundingClientRect().top;
          if (showing) { shown++; written += writes - was; }
        }
      }
      if (landed < 0) break;
      moves.push(Math.round((el.getBoundingClientRect().top - at) * 10) / 10);
    }
  } finally {
    delete sc.scrollTop;
  }
  return { pages: moves.length, worst: Math.max(0, ...moves.map(Math.abs)), moves, segs: count(), shown, written,
           placeholders: sc.querySelectorAll('[data-segment-placeholder]').length };
}"""


def paged_up_holds(page, what, touch=False, windowed=False):
    if windowed and page.locator("[data-segment-placeholder]").count() < 1:
        fail(f"{what}: upward paging did not start with deferred bodies")
    if touch:
        # leave the end with a finger (CDP touches, a drag down the chat)
        b = page.locator(SCROLL).bounding_box()
        x, y = b["x"] + b["width"] / 2, b["y"] + b["height"] / 3
        cdp = page.context.new_cdp_session(page)
        cdp.send("Input.dispatchTouchEvent", {"type": "touchStart", "touchPoints": [{"x": x, "y": y}]})
        for i in range(1, 11):
            page.wait_for_timeout(16)
            cdp.send("Input.dispatchTouchEvent", {"type": "touchMove", "touchPoints": [{"x": x, "y": y + 24 * i}]})
        cdp.send("Input.dispatchTouchEvent", {"type": "touchEnd", "touchPoints": []})
    else:
        page.locator(SCROLL).hover()
        page.mouse.wheel(0, -200)  # leave the end as a reader does
    page.wait_for_timeout(800)
    d = page.evaluate(PAGED)
    if d["pages"] < 3:
        fail(f"{what}: scrolling up brought in {d['pages']} earlier pages, 3 or more wanted ({d})")
    if d["shown"] < d["pages"]:
        fail(f"{what}: the loading row did not show while the pages were on their way ({d})")
    if d["worst"] > 1:
        fail(f"{what}: an earlier page landing moved what is on screen {d['worst']:.1f} px ({d})")
    if d["written"]:
        fail(f"{what}: the page moved the view {d['written']} times while an earlier page was on its way ({d})")
    ok(f"{what}: {d['pages']} earlier pages landed, what is on screen moved at most {d['worst']:.1f} px and the view was "
       "not moved while they loaded")


def shot(page, shots, name):
    if shots:
        page.wait_for_timeout(300)
        page.screenshot(path=os.path.join(shots, name))


def set_theme(page, theme):
    page.evaluate(f"localStorage.setItem('persona.theme', '{theme}')")


def hub_get(hub, path):
    return urllib.request.urlopen(urllib.request.Request(hub + path, headers={"Authorization": "Bearer mock"}), timeout=10).read()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--hub")
    ap.add_argument("--shots")
    ap.add_argument("--token", default="mock")
    ap.add_argument("--windowed", action="store_true", help="require deferred bodies; run the hub with MOCK_LONG_HISTORY=90")
    a = ap.parse_args()
    base = a.base.rstrip("/")
    req = urllib.request.Request(base + "/api/v1/assistant", headers={"Authorization": "Bearer " + a.token})
    assistant = json.load(urllib.request.urlopen(req, timeout=10))
    main_id = assistant["main"]
    other = next(c for c in assistant["conversations"] if c["session_id"] != main_id)
    if a.shots:
        os.makedirs(a.shots, exist_ok=True)

    with sync_playwright() as p:
        exe = os.environ.get("PERSONA_E2E_CHROMIUM")
        browser = p.chromium.launch(executable_path=exe) if exe else p.chromium.launch(channel="chrome")
        try:
            wide(browser, base, a, other)
            narrow(browser, base, a)
        finally:
            browser.close()
            if a.hub:
                hub_get(a.hub, "/mock/reset")
    print("PASS web chat scroll e2e")


def wide(browser, base, a, other):
    ctx = browser.new_context(viewport={"width": 1280, "height": 820}, device_scale_factor=2)
    page = ctx.new_page()
    page.set_default_timeout(10000)
    page.goto(base + "/?token=" + a.token)
    set_theme(page, "light")

    # The main conversation: long, with a task dock and a question card under it.
    at_end(page, "wide / first open")
    page.reload()
    at_end(page, "wide / reload")
    burst_stays(page, "wide /")
    composer_grows(page, "wide /")
    scrolled_up_stays(page, "wide /")
    shot(page, a.shots, "wide-main-light.png")
    set_theme(page, "dark")
    page.reload()
    at_end(page, "wide / reload, dark")
    shot(page, a.shots, "wide-main-dark.png")
    set_theme(page, "light")

    # From the sidebar: another Persona conversation, then back to the main one.
    # the sidebar's link carries the machine as a query since the conversation route took one (?machine=…)
    page.locator(f'aside a[href^="/persona/{other["session_id"]}"]').first.click()
    page.wait_for_function(f"location.pathname === '/persona/{other['session_id']}'")
    settled(page, "wide another Persona conversation")
    ok("wide: another Persona conversation opens")
    # The main-chat route is stable across UI languages and optional keyboard-shortcut labels.
    page.locator('aside a[href="/"]').click()
    page.wait_for_function("location.pathname === '/'")
    at_end(page, "wide sidebar → Persona")

    # An agent's chat.
    path = f"/s/{AGENT[0]}/{AGENT[1]}"
    link = page.locator(f'aside a[href="{path}"]')
    if link.count():
        link.first.click()
        # an agent's chat opens in its terminal tab (/t/<machine>/<workspace>/<tab>/<session>)
        page.wait_for_function(f"location.pathname.endsWith('/{AGENT[1]}')")
    else:
        page.goto(base + path)
    at_end(page, "wide agent chat, from the sidebar")
    page.reload()
    at_end(page, "wide agent chat, reload")
    burst_stays(page, "wide agent chat")
    composer_grows(page, "wide agent chat")
    scrolled_up_stays(page, "wide agent chat")
    offscreen_exchanges(page, "wide agent chat", a.windowed)
    if a.shots:
        # without the empty blocks the bursts added
        page.reload()
        at_end(page, "wide agent chat, reload before the screenshot")
    shot(page, a.shots, "wide-agent-light.png")
    if a.shots:
        for theme, size in (("dark", "default"), ("light", "large"), ("dark", "large")):
            set_theme(page, theme)
            page.evaluate("(size) => size === 'default' ? localStorage.removeItem('persona.text-size') : localStorage.setItem('persona.text-size', size)", size)
            page.reload()
            at_end(page, f"wide agent chat, {theme}, {size} text")
            shot(page, a.shots, f"wide-agent-{theme}{'-large' if size == 'large' else ''}.png")
        page.evaluate("() => localStorage.removeItem('persona.text-size')")
        set_theme(page, "light")
        page.reload()
        at_end(page, "wide agent chat, restored default appearance")

    if a.hub:
        new_exchange_holds(page, a.hub, "wide agent chat")
        # The newest page first, more over a slow network. The long reply stays, so the agent chat's newest page fills
        # the view and scrolling up asks for the earlier ones. Windowed mode starts with 60 turns already loaded.
        hub_get(a.hub, "/mock/paging?on=1" + ("&first=60" if a.windowed else ""))
        hub_get(a.hub, "/mock/delay?s=0.4")
        page.goto(base + "/")
        at_end(page, "wide / over a slow network, paged")
        page.goto(base + path)
        at_end(page, "wide agent chat over a slow network, paged")
        paged_up_holds(page, "wide agent chat over a slow network, paged", windowed=a.windowed)
        hub_get(a.hub, "/mock/reset")
    ctx.close()


def narrow(browser, base, a):
    for theme in ("light", "dark"):
        ctx = browser.new_context(viewport={"width": 390, "height": 844}, device_scale_factor=2, has_touch=True)
        page = ctx.new_page()
        page.set_default_timeout(10000)
        page.goto(base + "/overview?token=" + a.token)
        # signed in (the link's token traded for this browser's) before leaving, or the next page is the login form
        expect(page.locator("aside")).to_be_attached()
        set_theme(page, theme)
        page.goto(base + "/")
        at_end(page, f"narrow / first open, {theme}")
        if theme == "light":
            page.reload()
            at_end(page, "narrow / reload")
            burst_stays(page, "narrow /")
            page.goto(base + f"/s/{AGENT[0]}/{AGENT[1]}")
            at_end(page, "narrow agent chat")
            burst_stays(page, "narrow agent chat")
            page.goto(base + "/")
            at_end(page, "narrow / again")
        shot(page, a.shots, f"narrow-main-{theme}.png")
        ctx.close()
    if a.hub:
        narrow_paged(browser, base, a)


def narrow_paged(browser, base, a):
    """A touch phone (every exchange drawn, #2485) over a slow network, paged: what is on screen holds as the earlier
    pages land, and nothing moves the view while one is on its way (that would stop a fling)."""
    ctx = browser.new_context(viewport={"width": 390, "height": 844}, device_scale_factor=2, is_mobile=True, has_touch=True)
    page = ctx.new_page()
    page.set_default_timeout(10000)
    page.goto(base + "/overview?token=" + a.token)
    expect(page.locator("aside")).to_be_attached()
    set_theme(page, "light")
    try:
        page.goto(base + f"/s/{AGENT[0]}/{AGENT[1]}")
        at_end(page, "narrow agent chat, before paging")
        # a long reply in it, so its newest page fills the view and scrolling up asks for the earlier ones
        long = "\n\n".join(["Each earlier page comes in two exchanges at a time while the reader scrolls up. " * 6] * 30)
        hub_get(a.hub, "/mock/say?" + urllib.parse.urlencode({"user": "How do the earlier pages come in?", "text": long}))
        page.wait_for_function("() => [...document.querySelectorAll('[data-chat-scroll] section[data-segment]')].pop()?.textContent.includes('Each earlier page')")
        hub_get(a.hub, "/mock/paging?on=1")
        hub_get(a.hub, "/mock/delay?s=0.4")
        page.reload()
        at_end(page, "narrow agent chat over a slow network, paged")
        paged_up_holds(page, "narrow agent chat over a slow network, paged, by touch", touch=True)
    finally:
        hub_get(a.hub, "/mock/reset")
    ctx.close()


if __name__ == "__main__":
    main()
