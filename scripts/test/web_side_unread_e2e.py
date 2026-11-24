#!/usr/bin/env python3
"""End-to-end check of the sidebar line that names the side session keeping a parent Unread (#13496, after #13299). On
`MOCK_SIDE_SESSIONS=1 MOCK_SIDE_UNREAD=1` the PR review was seen while two of its side sessions (the /btw question and the
side chat) each have a reply nobody has seen: the review's row is Unread for them alone. The row names the newer side
under its title with "+1" for the other, in place of the review's own words; a click on that line opens the review's page
with the side in its Side sessions panel (not the review alone), and once both sides are seen the row's dot and line go
without coming back. Run against a complete-list hub and a paged one (`MOCK_SESSIONS_LIST=1`), light / dark, a wide sidebar
(the line's lead shown) and the narrowest (the side's title alone), English and Chinese.

    MOCK_SIDE_SESSIONS=1 MOCK_SIDE_UNREAD=1 MOCK_MANAGERS=0 PORT=18791 python3 scripts/mock/mock_hub.py &
    MOCK_SESSIONS_LIST=1 MOCK_SIDE_SESSIONS=1 MOCK_SIDE_UNREAD=1 MOCK_MANAGERS=0 PORT=18792 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18791 npx vite --port 5211 --strictPort &)
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18792 npx vite --port 5212 --strictPort &)
    python3 scripts/test/web_side_unread_e2e.py http://127.0.0.1:5211 http://127.0.0.1:5212 [--shots <dir>]

A fresh pair of mock hubs per run: the check marks the sides seen. Prints one line per check and a verdict (also when a
wait fails or times out); exits 1 on the first failure.
"""
import argparse
import os
import sys
import time

from playwright.sync_api import Error as PlaywrightError, expect, sync_playwright

MID = "macbook"
REVIEW = "a1f3c2d4-0000-4000-8000-000000000006"
BTW = "a1f3c2d4-0000-4000-8000-0000000059a1"  # the newer unread side
CHAT = "a1f3c2d4-0000-4000-8000-0000000059a2"  # the older unread side
# (label, theme, locale, sidebar width): each list is checked once per run, since the check marks the sides seen
RUNS = {"full": ("light", "en-US", 420), "paged": ("dark", "zh-CN", 240)}
UNREAD = {"en-US": "Unread", "zh-CN": "完成未看"}
WORDS = {"en-US": "New side reply:", "zh-CN": "分支新回复："}


def ok(msg):
    print("ok  " + msg, flush=True)


def fail(msg):
    print("FAIL " + msg, flush=True)
    sys.exit(1)


def run(browser, label, base, shots):
    theme, locale, width = RUNS[label]
    tag = f"{label}/{theme}/{locale}/{width}px"
    ctx = browser.new_context(viewport={"width": 1280, "height": 860}, device_scale_factor=2, color_scheme=theme, locale=locale)
    page = ctx.new_page()
    page.set_default_timeout(15000)
    errors = []
    page.on("pageerror", lambda e: errors.append(str(e)))
    page.goto(base + "/overview?token=mock")
    page.wait_for_load_state("load")
    # the mock token is exchanged for a device token first: reloading before that signs the page out
    page.locator("aside").first.wait_for()
    page.wait_for_timeout(1000)
    page.evaluate(f"localStorage.setItem('persona.theme', '{theme}'); localStorage.setItem('persona.sidebarWidth', '{width}')")
    page.goto(base + "/overview")
    page.wait_for_load_state("load")
    sidebar = page.locator("aside").first
    # the Unread chip (完成未看): the review is listed there for its sides alone
    chip = sidebar.get_by_role("radio", name=UNREAD[locale]).first
    # the usage bars at the sidebar's foot can cover the chips' row in a short window: a click on the element itself
    chip.dispatch_event("click")
    expect(chip).to_have_attribute("aria-checked", "true")
    unread_before = chip.get_attribute("aria-label")
    row = page.locator(f'aside a[href$="/{REVIEW}"]').first
    row.scroll_into_view_if_needed()
    expect(row).to_be_visible()
    line = row.locator("[data-side-unread]")
    expect(line).to_have_attribute("data-side-unread", BTW)
    text = line.inner_text()
    if "+1" not in text or "btw" not in text:
        fail(f"{tag}: the line reads {text!r}")
    # the lead ("New side reply:") shows where the line has room beside the title, never in the narrowest sidebar
    lead = line.locator("[data-side-lead]")
    if lead.is_visible() != (width > 300) or (width > 300 and WORDS[locale] not in text):
        fail(f"{tag}: the lead is {'shown' if lead.is_visible() else 'hidden'} in a {width} px sidebar ({text!r})")
    # nothing in the line runs past the row's column into its end (the hint, the age)
    spill = line.evaluate("el => el.getBoundingClientRect().right - el.closest('a').querySelector('[data-row-age]')?.getBoundingClientRect().left")
    if spill is not None and spill > 0:
        fail(f"{tag}: the line runs {spill} px under the row's age")
    if row.locator("[data-row-line]").count():
        fail(f"{tag}: the review's own seen words still stand under the side's dot")
    if row.locator(".status-mark").first.get_attribute("data-state") != "unread":
        fail(f"{tag}: the review's row is not Unread")
    # the title keeps its room: the line wraps out of sight instead of growing the row past two lines
    box = row.bounding_box()
    if not box or box["height"] > 48:
        fail(f"{tag}: the row is {box and box['height']} px high")
    ok(f"{tag}: the review's row names the newer unread side, +1, in place of its own words ({text!r})")
    if shots:
        sidebar.screenshot(path=os.path.join(shots, f"{label}-{theme}-before.png"))

    line.click()
    # the review's own page: `/s/…`, or its pane's `/t/…` where it runs in a terminal tab
    page.wait_for_url(f"**/{MID}/**{REVIEW}*")
    panel = page.get_by_role("complementary", name="分支会话" if locale == "zh-CN" else "Side sessions")
    expect(panel).to_be_visible()
    expect(panel.get_by_role("tab", selected=True)).to_contain_text("btw")
    ok(f"{tag}: one click opens the review's page with the side in its Side sessions panel")
    # the side shown is seen: the line names the other one now, alone
    expect(line).to_have_attribute("data-side-unread", CHAT)
    if "+1" in line.inner_text():
        fail(f"{tag}: the line still counts the seen side: {line.inner_text()!r}")
    ok(f"{tag}: the seen side leaves the line; it names the other one")
    if shots:
        page.screenshot(path=os.path.join(shots, f"{label}-{theme}-opened.png"))

    line.click()
    expect(panel.get_by_role("tab", selected=True)).to_contain_text("Side chat")
    expect(row.locator("[data-side-unread]")).to_have_count(0)
    # no frame of the hub's older rollup: sample the row for three seconds while the hub records the looks
    end = time.time() + 3
    while time.time() < end:
        if row.locator("[data-side-unread]").count() or row.locator(".status-mark").first.get_attribute("data-state") == "unread":
            fail(f"{tag}: the row turned Unread again after both sides were seen")
        page.wait_for_timeout(100)
    ok(f"{tag}: with both sides seen the row's dot and line go and stay gone")
    # the Unread chip's count follows the row: one fewer (the review's), at once or with the hub's next read
    expect(chip).not_to_have_attribute("aria-label", unread_before)
    ok(f"{tag}: the Unread chip went from {unread_before!r} to {chip.get_attribute('aria-label')!r}")
    if shots:
        sidebar.screenshot(path=os.path.join(shots, f"{label}-{theme}-after.png"))
    if errors:
        fail(f"{tag}: page errors: {errors[:3]}")
    ctx.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("full")
    ap.add_argument("paged")
    ap.add_argument("--shots")
    a = ap.parse_args()
    if a.shots:
        os.makedirs(a.shots, exist_ok=True)
    try:
        with sync_playwright() as p:
            browser = p.chromium.launch(channel=os.environ.get("PW_CHANNEL", "chrome"))
            run(browser, "full", a.full.rstrip("/"), a.shots)
            run(browser, "paged", a.paged.rstrip("/"), a.shots)
            browser.close()
    except (PlaywrightError, AssertionError) as e:
        where = e.__traceback__
        while where and where.tb_next and where.tb_next.tb_frame.f_code.co_filename == __file__:
            where = where.tb_next
        line = where.tb_lineno if where else "?"
        fail(f"{type(e).__name__} at line {line}: {str(e).splitlines()[0] if str(e) else e}")
    print("PASS side session unread line", flush=True)


if __name__ == "__main__":
    main()
