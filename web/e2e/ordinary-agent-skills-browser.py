"""Browser Use contract against the synthetic skill fixture and the real Vite app.

Run through browser-use stdin, with PERSONA_SKILL_SHOTS set to an evidence directory.
The fixture uses loopback ports 25357 (mock) and 25358 (Vite); no real runtime is contacted.
"""
import os
import time
import json
import urllib.request
from pathlib import Path

shots = Path(os.environ["PERSONA_SKILL_SHOTS"])
shots.mkdir(parents=True, exist_ok=True)
control = urllib.request.build_opener(urllib.request.ProxyHandler({}))
hub_url = os.environ.get("PERSONA_SKILL_HUB", "http://127.0.0.1:25357")
web_url = os.environ.get("PERSONA_SKILL_WEB", "http://127.0.0.1:25358")

def state(value):
    control.open(f"{hub_url}/mock/skills?state={value}").read()

def shot(name):
    time.sleep(.3)
    capture_screenshot(str(shots / f"{name}.png"))

for agent, sid in [("claude", "c1111111-0000-4000-8000-000000000001"), ("codex", "d2222222-0000-4000-8000-000000000002")]:
    state("ready")
    new_tab(f"{web_url}/s/macbook/{sid}")
    wait_for_load()
    js("localStorage.clear(); localStorage.setItem('persona.token','mock'); localStorage.setItem('persona.language','en'); location.reload()")
    wait_for_element("textarea", timeout=20)
    for attempt in range(60):
        if "/t/" in page_info()["url"]: break
        time.sleep(.2)
    time.sleep(.2)  # the session page becomes its terminal layout once its source arrives
    print(page_info())
    assert not js("document.querySelector('textarea').disabled")
    fill_input("textarea", "@review")
    for attempt in range(50):
        if js("document.querySelectorAll('[role=option]').length >= 2"): break
        time.sleep(.2)
    shot(f"{agent}-initial")
    rows = js("Array.from(document.querySelectorAll('[role=option]')).map(e=>e.textContent)")
    print(agent, "rendered rows", rows)
    assert len(rows) == 2
    for width, height, size in [(1365, 900, "wide"), (390, 844, "narrow")]:
        cdp("Emulation.setDeviceMetricsOverride", width=width, height=height, deviceScaleFactor=1, mobile=width < 600)
        js("document.documentElement.setAttribute('data-text-size','large')")
        for dark in [False, True]:
            js("document.documentElement.classList.toggle('dark', " + str(dark).lower() + ")")
            shot(f"{agent}-{size}-{'dark' if dark else 'light'}-menu")
            assert js("document.documentElement.scrollWidth <= innerWidth")
    press_key("ArrowDown")
    press_key("Enter")
    time.sleep(.2)
    fill_input("textarea", "Check the example")
    assert js("document.querySelector('textarea').value") == "Check the example"
    time.sleep(.4)
    assert js("Array.from({length:localStorage.length},(_,i)=>localStorage.key(i)).some(k=>k.startsWith('draft:') && localStorage.getItem(k).includes('fixture-user'))")
    shot(f"{agent}-selected")
    node = next(n for n in cdp("Accessibility.getFullAXTree")["nodes"] if n.get("role", {}).get("value") == "button" and n.get("name", {}).get("value") == "Remove this context")
    box = cdp("DOM.getBoxModel", backendNodeId=node["backendDOMNodeId"])["model"]["content"]
    assert max(box[0::2])-min(box[0::2]) >= 44 and max(box[1::2])-min(box[1::2]) >= 44
    if agent == "claude":
        state("refuse")
        js("Array.from(document.querySelectorAll('button')).find(b=>b.getAttribute('aria-label')==='Send').click()")
        time.sleep(.7)
        assert js("document.body.innerText.includes('Selected skill is unavailable')")
        sent = json.loads(control.open(f"{hub_url}/mock/skill-sends").read())["sends"][-1]
        assert 'ref="fixture-user"' in sent["body"]["text"]
        assert js("Array.from(document.querySelectorAll('[data-user-bubble]')).every(e=>{const r=e.getBoundingClientRect();return r.left>=0 && r.right<=innerWidth})")
        shot("claude-refused-with-identity")
        state("ready")
        js("Array.from(document.querySelectorAll('button')).find(b=>b.textContent==='Retry').click()")
        time.sleep(.7)
        sent_again = json.loads(control.open(f"{hub_url}/mock/skill-sends").read())["sends"][-1]
        assert sent_again["body"]["text"] == sent["body"]["text"]
    else:
        cdp("Emulation.setTouchEmulationEnabled", enabled=True)
        x, y = sum(box[0::2])/4, sum(box[1::2])/4
        cdp("Input.dispatchTouchEvent", type="touchStart", touchPoints=[{"x":x,"y":y}])
        cdp("Input.dispatchTouchEvent", type="touchEnd", touchPoints=[])
        assert not js("document.body.innerText.includes('review · user · ~/example/skills')")
    state("error")
    fill_input("textarea", "@review")
    time.sleep(.5)
    assert js("document.body.innerText.includes('Skills unavailable here')")
    shot(f"{agent}-unavailable")
    press_key("Escape")
    fill_input("textarea", "Plain chat remains usable")
    assert not js("document.querySelector('textarea').disabled")
    state("partial")
    fill_input("textarea", "@security")
    time.sleep(.5)
    assert js("document.body.innerText.includes('Verified local skills only')")
    shot(f"{agent}-partial")
    press_key("Escape")
    fill_input("textarea", "text")
    state("loading")
    fill_input("textarea", "@security")
    time.sleep(.1)
    shot(f"{agent}-loading")
    time.sleep(2)
print("PASS ordinary Claude/Codex rendered picker, keyboard identity, draft, failed retry, 44px touch removal, light/dark, narrow large text, unavailable/partial/loading and plain input")
