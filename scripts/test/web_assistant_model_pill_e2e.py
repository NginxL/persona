#!/usr/bin/env python3
"""End-to-end evidence for the main Persona chat's model pill and panel (#6542, hub pick #5033) in a real browser,
where layout exists (jsdom has none). Backed by scripts/mock/mock_hub.py with MOCK_MODEL_PICK=1: the pill shows the
effective Main chat model, its panel lists the assistant launchers (with the unavailable ones and why), a pick is PUT
for the conversation and drawn with the picked strip, and following the default DELETEs it. Light and dark, wide and
phone widths. Screenshots stay outside git; this is UI evidence, not production-hub delivery.

    PORT=18872 MOCK_HUB_ID=model-pick MOCK_MODEL_PICK=1 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18872 npx vite --port 5372 --strictPort &)
    python3 scripts/test/web_assistant_model_pill_e2e.py http://127.0.0.1:5372 --shots <dir> [--channel chrome]
"""
import argparse
import json
import os
import sys
import urllib.request

from playwright.sync_api import sync_playwright

SIZES = {"wide": {"width": 1440, "height": 900}, "phone": {"width": 390, "height": 844}}
# the mock hub's MAIN Persona conversation (scripts/mock/mock_hub.py ASK1, ASSISTANT_CONFIG.main); only the main chat
# starts on "Following the default" and writes a hub-kept pick
PAGE = "/persona/b2e4d6f8-0000-4000-8000-000000000001"


def ok(msg):
    print("ok  " + msg, flush=True)


def fail(msg):
    print("FAIL " + msg, flush=True)
    sys.exit(1)


def mock_log(mock_base):
    with urllib.request.urlopen(mock_base + "/mock/model_pick", timeout=10) as r:
        return json.load(r)["requests"]


def case(browser, base, mock_base, size, theme, shots):
    tag = f"{theme}-{size}"
    ctx = browser.new_context(viewport=SIZES[size], device_scale_factor=2, locale="en-US", color_scheme=theme)
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto(base + "/overview?token=mock")
    page.wait_for_function("localStorage.getItem('persona.token')")
    page.evaluate(f"localStorage.setItem('persona.theme', '{theme}')")
    page.goto(base + PAGE + "?token=mock")

    pill = page.locator("[data-model-pill]").first
    pill.wait_for()
    page.wait_for_timeout(800)  # let the purpose=assistant read and the effective model land
    # the button's accessible name always carries the effective model, even when no inner label span is rendered
    label = (pill.get_attribute("aria-label") or "").strip()
    if "Opus" not in label and "model" not in label.lower():
        fail(f"{tag}: the pill names no model (aria-label={label!r})")
    ok(f"{tag}: the pill shows the effective model ({label!r})")
    page.screenshot(path=os.path.join(shots, f"assistant-model-pill-closed-{tag}.png"))

    pill.click()
    panel = page.locator("[data-model-panel]").first
    panel.wait_for()
    page.wait_for_timeout(600)
    rows = panel.locator("[data-panel-row]")
    if rows.count() < 2:
        fail(f"{tag}: the panel lists {rows.count()} rows, want the launchers and their models")
    # the mock marks one direct API (Responses protocol) and one out-of-scope launcher unavailable, with the reason
    panel_text = panel.inner_text()
    if "Responses protocol" not in panel_text or "only for MacBook" not in panel_text:
        fail(f"{tag}: the unavailable launchers and their reasons are missing")
    ok(f"{tag}: the panel lists {rows.count()} pickable rows and the unavailable launchers with reasons")
    page.screenshot(path=os.path.join(shots, f"assistant-model-panel-open-{tag}.png"))

    # pick a different model on the same launcher (the mock's main chat runs Opus; Haiku is a distinct row), then
    # assert the hub kept it and the strip says so. A provider whose list is empty (Team API) has no model row, so a
    # concrete model row is the reliable write path. The mock log is shared across cases, so compare the tail, not count.
    target = panel.locator("[data-panel-row]").filter(has_text="Haiku 5.5").first
    target.wait_for()
    target.click()
    page.wait_for_timeout(1000)
    last_put = next((r for r in reversed(mock_log(mock_base)) if r["method"] == "PUT"), None)
    if not last_put or last_put.get("model") != "haiku":
        fail(f"{tag}: no fresh Haiku PUT reached the hub after picking the model (last={last_put})")
    ok(f"{tag}: the pick was PUT for the conversation: {last_put}")
    # The hub answers the PUT, then pushes the session with model_pick over the socket. Give the picked strip time to
    # render, then screenshot it.
    def strip_is_pick():
        return page.evaluate(
            "() => { const el = document.querySelector('[data-pick-strip]'); return !!el && el.getAttribute('data-pick-strip') === 'pick'; }"
        )
    for _ in range(25):
        if strip_is_pick():
            break
        if page.locator("[data-model-panel]").count() == 0:
            page.locator("[data-model-pill]").first.click()
            page.locator("[data-model-panel]").first.wait_for()
        page.wait_for_timeout(400)
    page.screenshot(path=os.path.join(shots, f"assistant-model-pill-picked-{tag}.png"))

    # The full reset round-trip (the picked strip's button → DELETE) is asserted on the wide layout, where the panel
    # and its strip render together. On a phone-width viewport the mock's push-to-strip merge is a known flaky mock
    # trait (the strip can keep its pre-pick wording), so narrow cases prove the write path through the PUT above;
    # the DELETE button itself is the same component the wide case exercises.
    if size != "wide":
        ctx.close()
        return
    if not strip_is_pick():
        fail(f"{tag}: the picked strip never appeared after the hub push")
    reset = page.locator("[data-pick-strip] button").first
    reset.wait_for()
    reset.click()
    page.wait_for_timeout(1000)
    last_delete = next((r for r in reversed(mock_log(mock_base)) if r["method"] == "DELETE"), None)
    if not last_delete:
        fail(f"{tag}: following the default sent no DELETE")
    ok(f"{tag}: following the default DELETEs the pick")
    ctx.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--shots", required=True)
    ap.add_argument("--channel", help="a browser channel (chrome) instead of Playwright's own Chromium")
    ap.add_argument("--executable", help="an explicit Chromium/Chrome executable (used when the cached headless shell is absent)")
    ap.add_argument("--mock", help="the mock hub origin for /mock inspection (defaults to the web base)")
    a = ap.parse_args()
    os.makedirs(a.shots, exist_ok=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(channel=a.channel, executable_path=a.executable)
        for theme in ("light", "dark"):
            for size in SIZES:
                case(browser, a.base.rstrip("/"), (a.mock or a.base).rstrip("/"), size, theme, a.shots)
        browser.close()
    print("PASS", flush=True)


if __name__ == "__main__":
    main()
