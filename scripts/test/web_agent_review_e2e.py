#!/usr/bin/env python3
"""Browser acceptance for the explicit independent session review (#1808).

Run against the mock hub and a web dev/preview server, with Playwright for Python and Chromium installed:

    PORT=18873 MOCK_FILES=1 MOCK_AGENT_REVIEW=1 MOCK_REVIEW=1 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18873 npx vite --port 5193 --strictPort &)
    python3 scripts/test/web_agent_review_e2e.py http://127.0.0.1:5193 --hub http://127.0.0.1:18873 [--shots <dir>]

The four desktop/phone × light/dark runs cover the menu, disabled reasons, a hub refusal, the findings card and both
answers. A final 200% page-zoom run keeps the card's actions visible. Prints one verdict per check and exits on failure.
"""
import argparse
import json
import os
import sys
import urllib.request

from playwright.sync_api import TimeoutError as PlaywrightTimeoutError, expect, sync_playwright

SIZES = {"wide": {"width": 1440, "height": 900}, "narrow": {"width": 390, "height": 844}}
MID, SID = "macbook", "a1f3c2d4-0000-4000-8000-000000000006"
BUSY = "a1f3c2d4-0000-4000-8000-000000000001"


def ok(msg):
    print("ok  " + msg, flush=True)


def fail(msg):
    print("FAIL " + msg, flush=True)
    sys.exit(1)


def mock(hub, token, query="", route="agent-review"):
    req = urllib.request.Request(f"{hub}/mock/{route}{query}", headers={"Authorization": "Bearer " + token})
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.loads(r.read())


def run(browser, base, hub, token, size, theme, shots):
    tag = f"{size}/{theme}"
    narrow = size == "narrow"
    ctx = browser.new_context(viewport=SIZES[size], device_scale_factor=2, has_touch=narrow, locale="zh-CN")
    page = ctx.new_page()
    errors = []
    page.on("pageerror", lambda error: errors.append(str(error)))
    page.set_default_timeout(20000)
    page.goto(f"{base}/overview?token=" + token)
    page.wait_for_function("localStorage.getItem('persona.token')")
    page.evaluate(f"localStorage.setItem('persona.theme', '{theme}')")

    def shot(name):
        if shots:
            page.wait_for_timeout(400)
            page.screenshot(path=os.path.join(shots, f"session-review-{size}-{theme}-{name}.png"))

    def open_chat(card=True):
        mock(hub, token, "?state=" + ("card" if card else "none") + "&count=1&refuse=&running=0&changes=1")
        page.goto(f"{base}/s/{MID}/{SID}", wait_until="commit")
        expect(page).to_have_url(f"{base}/s/{MID}/{SID}")
        try:
            page.wait_for_selector("[data-composer], textarea")
        except PlaywrightTimeoutError:
            print(f"{tag}: composer missing at {page.url}; page errors: {errors}", flush=True)
            print(page.locator("body").inner_text(timeout=5000)[:2000], flush=True)
            raise
        page.wait_for_function("dark => document.documentElement.classList.contains('dark') === dark", arg=theme == "dark")

    def menu_item():
        page.get_by_role("button", name="更多").first.click()
        return page.get_by_role("menuitem", name="独立审查这项工作")

    def close_menu():
        # The next chat navigation closes the menu. An explicit Escape or corner click can win a race with its
        # overlay history entry on phone layouts, so leave closing to that navigation.
        pass

    mock(hub, token, "?mode=1", route="review")
    open_chat(card=False)
    mock(hub, token, "?reset=1")
    item = menu_item()
    expect(item).to_be_visible()
    expect(item).to_be_enabled()
    shot("menu")
    with page.expect_response(lambda response: response.url.endswith("/review") and response.request.method == "POST") as pending_start:
        item.click()
    response = pending_start.value
    if response.status != 200:
        fail(f"{tag}: review start returned {response.status}: {response.text()}")
    starts = mock(hub, token)["starts"]
    if len(starts) != 1 or starts[0]["machine"] != MID or starts[0]["session"] != SID:
        print(f"{tag}: start diagnostics url={page.url} errors={errors} body={page.locator('body').inner_text()[:2000]}", flush=True)
        fail(f"{tag}: the hub was not asked to review {SID}: {starts}")
    expect(page.get_by_text("已请求审查")).to_be_visible()
    ok(f"{tag}: the menu entry starts a review of this session")
    close_menu()

    mock(hub, token, "?state=none&refuse=")
    page.evaluate(f"localStorage.setItem('persona.sessionView', JSON.stringify({{'{MID}/{BUSY}': 'chat'}}))")
    page.goto(f"{base}/s/{MID}/{BUSY}")
    page.wait_for_selector("[data-composer], textarea")
    page.get_by_role("button", name="更多").first.click()
    busy = page.get_by_role("menuitem", name="独立审查这项工作")
    expect(busy).to_be_visible()
    expect(busy).to_be_disabled()
    expect(busy).to_contain_text("正忙")
    ok(f"{tag}: a busy chat keeps the entry, disabled with its reason")
    close_menu()

    mock(hub, token, "?mode=1", route="review")
    open_chat(card=False)
    mock(hub, token, "?changes=0", route="agent-review")
    clean = menu_item()
    expect(clean).to_be_disabled()
    expect(clean).to_contain_text("没有可审查的改动")
    shot("clean")
    ok(f"{tag}: a clean repository disables the entry with its reason")
    close_menu()

    open_chat(card=False)
    mock(hub, token, "?refuse=session_busy")
    item = menu_item()
    expect(item).to_be_enabled()
    item.click()
    expect(page.get_by_text("这个 Agent 正在工作，等它停下来再审查")).to_be_visible()
    ok(f"{tag}: the hub's refusal is shown in its own words")
    close_menu()

    open_chat(card=True)
    card = page.locator('[data-card="review_a1b2c3"]')
    expect(card).to_be_visible()
    expect(card).to_contain_text("3 条发现")
    expect(card).to_contain_text("Forecast.swift:42")
    expect(card.get_by_role("button", name="发给 Agent")).to_be_visible()
    expect(card.get_by_role("button", name="不发了")).to_be_visible()
    shot("card")
    ok(f"{tag}: the findings card shows the count, report and both buttons")

    mock(hub, token, "?reset=1")
    card.get_by_role("button", name="发给 Agent").click()
    expect(card).to_have_count(0)
    answers = mock(hub, token)["answers"]
    if len(answers) != 1 or answers[0].get("answers", {}).get("独立审查") != "发给 Agent":
        fail(f"{tag}: Send did not answer the source card: {answers}")
    ok(f"{tag}: Send resolves the source card and it goes")

    open_chat(card=True)
    card = page.locator('[data-card="review_a1b2c3"]')
    mock(hub, token, "?reset=1")
    card.get_by_role("button", name="不发了").click()
    expect(card).to_have_count(0)
    answers = mock(hub, token)["answers"]
    if len(answers) != 1 or answers[0].get("answers", {}).get("独立审查") != "不发了":
        fail(f"{tag}: Dismiss did not resolve the source card: {answers}")
    ok(f"{tag}: Dismiss closes the card without an Agent message")

    mock(hub, token, "?count=0")
    page.reload()
    page.wait_for_selector("[data-composer], textarea")
    card = page.locator('[data-card="review_a1b2c3"]')
    expect(card).to_be_visible()
    expect(card).to_contain_text("审查完成")
    expect(card).not_to_contain_text("条发现")
    shot("no-count")
    ok(f"{tag}: a review without a count says the review completed")
    if errors:
        fail(f"{tag}: uncaught page errors: {errors}")
    ctx.close()


def zoomed(browser, base, hub, token, shots):
    ctx = browser.new_context(viewport=SIZES["narrow"], device_scale_factor=2, has_touch=True, locale="zh-CN")
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto(f"{base}/overview?token=" + token)
    page.wait_for_function("localStorage.getItem('persona.token')")
    page.evaluate("localStorage.setItem('persona.theme', 'light')")
    mock(hub, token, "?state=card&count=1&running=0&changes=1&refuse=")
    page.goto(f"{base}/s/{MID}/{SID}", wait_until="commit")
    page.evaluate("document.documentElement.style.zoom = '200%'")
    card = page.locator('[data-card="review_a1b2c3"]')
    expect(card.get_by_role("button", name="发给 Agent")).to_be_visible()
    expect(card.get_by_role("button", name="不发了")).to_be_visible()
    card.get_by_role("button", name="发给 Agent").scroll_into_view_if_needed()
    if shots:
        page.wait_for_timeout(400)
        page.screenshot(path=os.path.join(shots, "session-review-narrow-light-zoom-200.png"))
    ok("narrow/light at 200% page zoom: both card actions remain visible")
    ctx.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--hub", required=True)
    ap.add_argument("--token", default="mock")
    ap.add_argument("--shots")
    ap.add_argument("--executable", help="a Chromium executable to drive instead of Playwright's installed browser")
    a = ap.parse_args()
    if a.shots:
        os.makedirs(a.shots, exist_ok=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(executable_path=a.executable) if a.executable else p.chromium.launch()
        for size, theme in (("wide", "light"), ("wide", "dark"), ("narrow", "light"), ("narrow", "dark")):
            run(browser, a.base, a.hub, a.token, size, theme, a.shots)
        zoomed(browser, a.base, a.hub, a.token, a.shots)
        browser.close()
    print("PASS web session review", flush=True)


if __name__ == "__main__":
    main()
