#!/usr/bin/env python3
"""End-to-end check of engine host sets on the web (#2576, docs/integrations/engines.md "Host sets"): the machine page's "Engine" is a
checklist with a default (check / uncheck, "Make default", none at all, "Revert", "Save" sends {hosts, default}); "New Agent"
offers "Open in" only for a machine with more than one engine that can start sessions, and sends the host when it is not
the default; a session live in another host too shows "Also open in …" above the composer. The page runs in zh-CN, as
the other web e2e checks, so the selectors use the Chinese labels.

Needs a running web page against the fake hub with MOCK_ENGINE_SETS=1, and Playwright for Python:

    MOCK_ENGINE_SETS=1 PORT=18891 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18891 npx vite --port 5891 --strictPort &)
    python3 scripts/test/web_engine_hosts_e2e.py http://127.0.0.1:5891 --hub http://127.0.0.1:18891 [--shots <dir>]

Prints one line per check and a verdict; exits 1 on the first failure. Runs light and dark, at desktop and phone
widths; --shots saves each state there. --channel msedge where there is no Chrome.
"""
import argparse
import json
import os
import sys
import urllib.request

from playwright.sync_api import expect, sync_playwright

SIZES = {"wide": {"width": 1280, "height": 860}, "narrow": {"width": 390, "height": 844}}
WIDGET = "a1f3c2d4-0000-4000-8000-000000000001"  # the mock's widget session, live in Herdr App too


def ok(msg):
    print("ok  " + msg, flush=True)


def machine(hub, token, mid):
    req = urllib.request.Request(f"{hub}/api/v1/machines", headers={"Authorization": f"Bearer {token}"})
    ms = json.load(urllib.request.urlopen(req, timeout=10))["machines"]
    return next(m for m in ms if m["id"] == mid)


def put_hosts(hub, token, mid, hosts, default):
    body = json.dumps({"hosts": hosts, "default": default}).encode()
    req = urllib.request.Request(f"{hub}/api/v1/machines/{mid}/engine", data=body, method="PUT",
                                 headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"})
    urllib.request.urlopen(req, timeout=10).read()


def run(browser, base, hub, token, size, theme, shots):
    tag = f"{size}/{theme}"
    put_hosts(hub, token, "studio", ["herdr", "orca"], "herdr")  # every pass starts from the fixture
    ctx = browser.new_context(viewport=SIZES[size], device_scale_factor=2, has_touch=size == "narrow", locale="zh-CN",
                              color_scheme=theme)
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto(base + "/overview?token=" + token)
    page.wait_for_function("localStorage.getItem('persona.token')")
    page.evaluate(f"localStorage.setItem('persona.theme', '{theme}')")

    def shot(name):
        if shots:
            page.wait_for_timeout(300)
            page.screenshot(path=os.path.join(shots, f"engine-hosts-{size}-{theme}-{name}.png"))

    # The machine page: a checklist, herdr the default.
    page.goto(base + "/machines/studio")
    section = page.locator('[data-testid="engine-section"][data-engine-sets]')
    expect(section).to_be_visible()
    section.scroll_into_view_if_needed()
    box = lambda id: section.locator(f'[data-engine="{id}"] [role="checkbox"]')
    expect(box("herdr")).to_have_attribute("aria-checked", "true")
    expect(box("orca")).to_have_attribute("aria-checked", "true")
    expect(box("paseo")).to_have_attribute("aria-checked", "false")
    expect(box("tern")).to_have_attribute("aria-checked", "false")  # found on the studio, not enabled (#2154)
    expect(section.locator('[data-engine="tern"]')).to_contain_text("Tern 里的终端")
    expect(box("rex")).to_have_attribute("aria-checked", "false")
    expect(section.locator('[data-engine="rex"]')).to_contain_text("Rex 里的终端")
    expect(box("cindy")).to_have_attribute("aria-checked", "false")
    expect(section.locator('[data-engine="cindy"]')).to_contain_text("Cindy 里的 Agent")
    expect(box("t3code")).to_have_attribute("aria-checked", "false")
    expect(section.locator('[data-engine="t3code"]')).to_contain_text("T3 Code 里的 Agent")
    expect(section.locator('[data-engine="herdr"]')).to_contain_text("默认")
    shot("machine")
    ok(f"{tag}: the machine page lists the set, herdr the default")

    # Check Paseo, make Orca the default, save: the hub gets {hosts, default}.
    box("paseo").click()
    section.locator('[data-engine="orca"]').get_by_role("button", name="设为默认").click()
    expect(section).to_contain_text("改为 herdr + Orca + Paseo")
    shot("edited")
    section.get_by_role("button", name="保存").click()
    expect(section.get_by_role("button", name="保存")).to_have_count(0)
    m = machine(hub, token, "studio")
    assert m["engine_hosts"] == ["herdr", "orca", "paseo"] and m["engine_default"] == "orca", m
    expect(section.locator('[data-engine="orca"]')).to_contain_text("默认")
    ok(f"{tag}: Save sends the set and the default; the page follows the hub")

    # None at all: allowed, with its footer; Revert brings the saved set back.
    for id in ("herdr", "orca", "paseo"):
        box(id).click()
    expect(section).to_contain_text("没有启用终端引擎")
    shot("none")
    section.get_by_role("button", name="还原").click()
    expect(box("orca")).to_have_attribute("aria-checked", "true")
    ok(f"{tag}: the empty set is offered, Revert undoes it")

    # New Agent on the studio: "Open in" lists the enabled hosts that can start sessions, Orca the default now.
    page.evaluate("localStorage.setItem('new.machine', 'studio')")
    page.goto(base + "/overview")
    page.get_by_text("Mac Studio").first.wait_for()  # the app is up and listens for openNewAgent
    page.evaluate("import('/src/lib/new-agent.ts').then(m => m.openNewAgent())")
    hosts = page.locator('[data-testid="start-host"]')
    expect(hosts).to_be_visible()
    expect(hosts.get_by_role("radio")).to_have_count(3)
    expect(hosts.get_by_role("radio", name="Orca · 默认")).to_have_attribute("aria-checked", "true")
    shot("new-agent")
    page.keyboard.press("Escape")
    expect(hosts).to_have_count(0)  # closed (and any navigation it made settled) before the next page
    page.wait_for_load_state()
    ok(f"{tag}: New Agent offers Open in with the default picked")

    # A session live in Herdr App too: the note above the composer.
    page.goto(base + f"/s/macbook/{WIDGET}")
    note = page.locator('[data-testid="chat-also-open"]')
    expect(note).to_contain_text("也在 Herdr App 里打开")
    shot("also-open")
    ok(f"{tag}: the conflict note names the other host")
    ctx.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--hub", required=True)
    ap.add_argument("--token", default="mock")
    ap.add_argument("--shots")
    ap.add_argument("--channel", default="chrome")
    a = ap.parse_args()
    if a.shots:
        os.makedirs(a.shots, exist_ok=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(channel=a.channel)
        try:
            for size in SIZES:
                for theme in ("light", "dark"):
                    run(browser, a.base, a.hub, a.token, size, theme, a.shots)
        except Exception as e:  # noqa: BLE001 — one line and a verdict, as the other e2e scripts
            print(f"FAIL {e}", flush=True)
            sys.exit(1)
        finally:
            browser.close()
    print("PASS", flush=True)


if __name__ == "__main__":
    main()
