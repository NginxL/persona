#!/usr/bin/env python3
"""Browser evidence for a to-do's **Repositories and work** section (Web).

Input for Browser Use, from the repository root:

    MOCK_TODO_LIST=1 PORT=18961 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:18961 npx vite --port 5961 --strictPort &)
    REPOSITORY_WEB=http://127.0.0.1:5961 REPOSITORY_HUB=http://127.0.0.1:18961 browser-use < web/e2e/todo-repositories-browser.py

Only the association store's replies are synthetic: the page, the to-do list, its rows and the assistant's repository
catalog come from the mock hub, and `GET/PUT /todos/{id}/repositories` is answered in the browser by an invented store,
so no Agent, repository or Git checkout is involved. Checks, in English, light and dark at 1280 and 390 px, plus Large
text at 390: the section draws saved repositories and their PRs as links without an editor; each repository carries its
provider's own mark and its project address on one line, and its links and unlisted note are indented beneath it; a
saved link's complete title is its one hyperlink (and an untitled link falls back to its kind and number); the legacy
`pr_url` is not repeated beside it; **Manage links**
opens the dialog and keeps the same hierarchy; another repository's URL is refused inline and a valid one is added with
Enter; a revision conflict keeps the draft and **Discard edits and reload** takes the store's newest value; Save sends
the revisions it was shown; Escape closes the dialog and returns the focus; a hub without the route shows only the
legacy link. Screenshots stay outside Git (`REPOSITORY_EVIDENCE_DIR`, default `/tmp/persona-todo-repositories-evidence`).
"""

import json
import os
import sys
import time
import urllib.request
from pathlib import Path

WEB = os.environ.get("REPOSITORY_WEB", "http://127.0.0.1:5961").rstrip("/")
HUB = os.environ.get("REPOSITORY_HUB", "http://127.0.0.1:18961").rstrip("/")
EVIDENCE = Path(os.environ.get("REPOSITORY_EVIDENCE_DIR", "/tmp/persona-todo-repositories-evidence"))
EVIDENCE.mkdir(parents=True, exist_ok=True)

IDENTITY = {"provider": "github", "instance_url": "https://github.com", "path": "example/web", "canonical_url": "https://github.com/example/web"}
GITLAB_IDENTITY = {"provider": "gitlab", "instance_url": "https://gitlab.example.com", "path": "team/api", "canonical_url": "https://gitlab.example.com/team/api"}
CATALOG = {"manager": "assistant", "catalog_revision": 4, "repositories": [
    {**IDENTITY, "id": "web", "name": "Web app", "verified_at": "2026-01-01T00:00:00Z"},
    {"provider": "github", "instance_url": "https://github.com", "path": "example/docs",
     "canonical_url": "https://github.com/example/docs", "id": "docs", "name": "Docs", "verified_at": "2026-01-01T00:00:00Z"},
]}


def hub_todos():
    # A loopback hub is reached directly: a proxy from the shell's environment refuses it.
    request = urllib.request.Request(f"{HUB}/api/v1/todos")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(request, timeout=10) as response:
        return json.load(response)["todos"]


TODOS = hub_todos()
TODO = next((item for item in TODOS if item.get("pr_url")), TODOS[0] if TODOS else None)
assert TODO, "the mock hub lists no to-do (start it with MOCK_TODO_LIST=1)"
TODO_ID, LEGACY_PR = TODO["id"], TODO.get("pr_url", "")


def fixture_source(legacy=False, empty=False, rich=False):
    """The whole page's fetch shim, installed before any page script so the section's first read finds it."""
    return """(() => {
  const state = window.repoFixture = {calls: [], creation: null, creations: [], revision: 2, numbers: [3211, 3212], catalogRevision: 4, failNextSave: null, legacy: %(legacy)s, empty: %(empty)s, rich: %(rich)s};
  const identity = %(identity)s, gitlabIdentity = %(gitlab)s, catalog = %(catalog)s, todoId = %(todo)s;
  const row = (id, source, links) => ({repo_id: id, requested_ref: {kind: 'default'},
    source_snapshot: {identity: source, clone_url: source.canonical_url, catalog_revision: 4, transport_origin: 'canonical_default'},
    links, catalog_state: 'current'});
  // Provider marks, one-line project addresses and the links' own full titles.
  const richSet = () => ({schema_version: 1, manager: "assistant", todo_id: todoId, revision: state.revision, repositories: [
    row('web', identity, [
      {kind: 'pull_request', number: '7', canonical_url: identity.canonical_url + '/pull/7', title: 'Fix the login redirect for every provider'},
      {kind: 'issue', number: '8', canonical_url: identity.canonical_url + '/issues/8'},
    ]),
    {...row('api', gitlabIdentity, [
      {kind: 'pull_request', number: '9', canonical_url: gitlabIdentity.canonical_url + '/-/merge_requests/9', title: 'Rotate access tokens without logging out'},
      {kind: 'issue', number: '10', canonical_url: gitlabIdentity.canonical_url + '/-/issues/10', title: 'Handle expired credentials gracefully across the whole flow'},
    ]), catalog_state: 'unlisted'},
  ]});
  const set = () => state.rich ? richSet() : state.empty ? {schema_version: 1, manager: "assistant", todo_id: todoId, revision: state.revision, repositories: []}
    : ({schema_version: 1, manager: "assistant", todo_id: todoId, revision: state.revision, repositories: [{
    repo_id: 'web', requested_ref: {kind: 'default'},
    source_snapshot: {identity, clone_url: identity.canonical_url, catalog_revision: 4, transport_origin: 'canonical_default'},
    links: state.numbers.map((n) => ({kind: 'pull_request', number: String(n), canonical_url: identity.canonical_url + '/pull/' + n})),
    catalog_state: 'current'}]});
  const reply = (body, status=200) => new Response(JSON.stringify(body), {status, headers: {'Content-Type': 'application/json'}});
  const original = window.fetch.bind(window);
  window.fetch = async (url, init={}) => {
    const path = String(url).split('/api/v1')[1], method = (init.method || 'GET').toUpperCase();
    if (path === '/todos' && method === 'POST') {
      const body = init.body ? JSON.parse(init.body) : {};
      state.creation = body;
      state.creations.push(body);
      return reply({todo: {id: 'created_todo', title: body.title || 'Created', created_at: new Date().toISOString(), updated_at: new Date().toISOString(), agents: []}});
    }
    if (!path || !path.includes('/repositories')) return original(url, init);
    if (state.legacy) return reply({code: 'not_found', error: 'Not Found'}, 404);
    const manager = new Headers(init.headers).get('X-Persona-Manager');
    if (path.endsWith('/assistant/repositories')) return reply({...catalog, manager});
    if (method === 'GET') return reply(set());
    const body = init.body ? JSON.parse(init.body) : {};
    state.calls.push({method, body, shownRevision: state.revision});
    if (state.failNextSave) { const failure = state.failNextSave; state.failNextSave = null; return reply(failure, 409); }
    if (body.expected_revision !== state.revision) return reply({code: 'task_repositories_revision_conflict', error: 'changed'}, 409);
    if (body.expected_catalog_revision !== state.catalogRevision) return reply({code: 'task_repositories_catalog_conflict', error: 'catalog changed'}, 409);
    state.revision += 1;
    state.numbers = body.repositories.flatMap((row) => (row.links || []).map((link) => Number(String(link.number || link.url).split('/').pop())));
    return reply(set());
  };
  return true;
})()""" % {"legacy": "true" if legacy else "false", "empty": "true" if empty else "false", "rich": "true" if rich else "false",
          "identity": json.dumps(IDENTITY), "gitlab": json.dumps(GITLAB_IDENTITY), "catalog": json.dumps(CATALOG), "todo": json.dumps(TODO_ID)}


def js(expression):
    result = cdp("Runtime.evaluate", expression=expression, returnByValue=True, awaitPromise=True)
    if result.get("exceptionDetails"):
        raise AssertionError(f"page script failed: {result['exceptionDetails'].get('text')}: {expression[:120]}")
    return result["result"].get("value")


def eventually(expression, seconds=10):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if js(expression):
            return
        time.sleep(0.05)
    raise AssertionError(f"never true: {expression}")


def settle():
    js("new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)))")
    eventually("document.getAnimations().every(a=>a.playState!=='running'||a.effect?.getTiming().iterations===Infinity)")


def shot(name):
    settle()
    capture_screenshot(str(EVIDENCE / f"{name}.png"))


def press(name, role="button"):
    """A real pointer press on the control with this accessible name, at its own box (so it also takes the focus, as a
    press does for a person). The box is checked to be the one the pointer reaches before it is clicked."""
    nodes = cdp("Accessibility.getFullAXTree")["nodes"]
    node = next((n for n in nodes if n.get("role", {}).get("value") == role and n.get("name", {}).get("value") == name), None)
    assert node, f"no {role} named {name}"
    backend = node["backendDOMNodeId"]
    cdp("DOM.scrollIntoViewIfNeeded", backendNodeId=backend)
    deadline, previous = time.monotonic() + 8, None
    while time.monotonic() < deadline:
        box = cdp("DOM.getBoxModel", backendNodeId=backend)["model"]["content"]
        x, y = sum(box[0::2]) / 4, sum(box[1::2]) / 4
        clear = js(f"document.elementFromPoint({x},{y})?.closest('[aria-label={json.dumps(name)}], button, a') !== null")
        if box == previous and clear:
            click_at_xy(x, y)
            return
        previous = box
        time.sleep(0.05)
    raise AssertionError(f"control stayed covered or moving: {name}")


def check(condition, message):
    assert condition, message
    print(f"ok  {message}")


def type_url(value):
    js("""(() => { const el = document.querySelector('[role=dialog] input[type=url]');
      if (!el) throw new Error('no link field');
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(el, %s);
      el.dispatchEvent(new Event('input', {bubbles: true})); })()""" % json.dumps(value))


OWNED_TABS: set[str] = set()


def close_owned_tabs():
    """Close only tabs this fixture created; a browser may carry the user's own tabs, which it never touches."""
    for target in tuple(OWNED_TABS):
        try:
            close_tab(target)
        except Exception:
            pass
        OWNED_TABS.discard(target)


def open_fixture_tab(url):
    # Supplying a URL may reuse a blank tab; create one explicitly before taking ownership.
    target = new_tab()
    OWNED_TABS.add(target)
    goto_url(url)


def fresh_tab():
    """Close only tabs this fixture opened, retaining the current one until the next document is ready."""
    ensure_real_tab()
    current = current_tab()
    keep = current.get("target_id") or current.get("targetId")
    for target in tuple(OWNED_TABS):
        if target != keep:
            try:
                close_tab(target)
                OWNED_TABS.discard(target)
            except Exception:
                pass


def open_todo(theme, width, legacy=False, large=False, empty=False, rich=False):
    fresh_tab()
    # The page is opened first and reloaded: Page.addScriptToEvaluateOnNewDocument only reaches documents created after
    # it, not the tab's first one.
    open_fixture_tab(f"{WEB}/?token=mock")
    wait_for_load()
    js("localStorage.setItem('persona.token','mock'); localStorage.setItem('persona.language','en'); "
       "localStorage.setItem('persona.todos.view','list'); localStorage.setItem('persona.theme',%s); true" % json.dumps(theme))
    js("localStorage.setItem('persona.text-size', %s); true" % json.dumps("large" if large else "standard"))
    open_fixture_tab(f"{WEB}/todos?id={TODO_ID}")
    wait_for_load()
    cdp("Page.enable")
    # A production build registers its service worker: its cached shell carries the previous build's asset names, so the
    # reloaded page would run old code. Unregister and clear its caches (a dev server has none).
    js("navigator.serviceWorker?.getRegistrations().then((rs) => Promise.all(rs.map((r) => r.unregister())))")
    js("caches?.keys().then((ks) => Promise.all(ks.map((k) => caches.delete(k))))")
    cdp("Page.addScriptToEvaluateOnNewDocument", source=fixture_source(legacy=legacy, empty=empty, rich=rich))
    cdp("Page.reload")
    wait_for_load()
    eventually("!!window.repoFixture")
    cdp("Emulation.setDeviceMetricsOverride", width=width, height=1100 if width > 600 else 1700, deviceScaleFactor=1, mobile=width <= 600)
    eventually("!!document.querySelector('[data-todo-repositories]')", seconds=30)
    eventually("!document.querySelector('[data-todo-repositories]').innerText.includes('Loading')", seconds=30)
    if large:
        check(js("document.documentElement.dataset.textSize") == "large", "the page is at the Large text size")
    settle()


def run(theme, width, suffix=""):
    where = f"{theme} {width}px{suffix}"
    open_todo(theme, width, large=bool(suffix))
    section = "document.querySelector('[data-todo-repositories]').innerText"
    check("example/web" in js(section), f"{where}: the saved repository is drawn on the page")
    check("#3211" in js(section) and "#3212" in js(section), f"{where}: both saved PRs are links, with no editor open")
    check(js("document.querySelectorAll('[data-todo-repositories] a[href$=\"/pull/3211\"], [data-todo-repositories] a[href$=\"/pull/3212\"]').length") == 2,
          f"{where}: each PR is one link")
    check(js("!document.querySelector('[data-todo-repositories] textarea')"), f"{where}: no multiline URL form")
    if LEGACY_PR:
        # The list still carries the to-do's own PR line; the detail draws the store's links, not that legacy one as well.
        check(js("document.querySelectorAll('[data-testid=todos-detail-column] a[href=%s]').length === 0" % json.dumps(LEGACY_PR)),
              f"{where}: the detail does not draw the legacy PR beside the store's links")
    shot(f"section-{width}-{theme}{suffix}")
    press("Manage links")
    eventually("!!document.querySelector('[role=dialog]')")
    check("Pull request" in js("document.querySelector('[role=dialog]').innerText"), f"{where}: the dialog lists the links as rows")
    settle()
    # The shared editor uses the same hierarchy as the detail.
    dialog = "document.querySelector('[role=dialog]')"
    repo_text = js(f"{dialog}.querySelector('[data-repository-row] a span').getBoundingClientRect().left")
    child_icon = js(f"{dialog}.querySelector('[data-repository-link] [data-link-icon]').getBoundingClientRect().left")
    check(abs(child_icon - repo_text) <= 1, f"{where}: the editor indents links under their repository")
    check(js(f"{dialog}.scrollWidth <= {dialog}.clientWidth"), f"{where}: the editor has no horizontal overflow")
    shot(f"dialog-{width}-{theme}{suffix}")
    press("Add repository")
    eventually("!!document.querySelector('[data-repository-picker]')")
    settle()
    check("Docs" in js("document.querySelector('[data-repository-picker]').innerText"),
          f"{where}: the picker lists a repository not yet linked")
    check(js("document.querySelector('[data-repository-picker]').scrollWidth <= document.querySelector('[role=dialog]').clientWidth"),
          f"{where}: the picker has no horizontal overflow")
    shot(f"picker-{width}-{theme}{suffix}")
    press("Add repository")
    eventually("[...document.querySelectorAll('button')].some((b) => b.innerText.trim() === 'Add repository' && b.getAttribute('aria-expanded') === 'false')")
    press("Add issue or PR")
    eventually("!!document.querySelector('[role=dialog] input[type=url]')")
    type_url("https://github.com/example/docs/pull/9")
    press("Add issue or PR")
    eventually("!!document.querySelector('[role=dialog] [role=alert]')")
    check("Use an issue or pull request link from this repository." in js("document.querySelector('[role=dialog]').innerText"),
          f"{where}: another repository's URL is refused inline")
    shot(f"dialog-invalid-{width}-{theme}{suffix}")
    type_url("https://github.com/example/web/pull/3213")
    js("document.querySelector('[role=dialog] input[type=url]').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true})); true")
    eventually("document.querySelector('[role=dialog]').innerText.includes('#3213')")
    check(True, f"{where}: Enter adds a link of this repository")
    shot(f"dialog-added-{width}-{theme}{suffix}")
    # The server accepts a public HTTP alias and a mixed-case repository path; the editor now normalizes both.
    press("Add issue or PR")
    eventually("!!document.querySelector('[role=dialog] input[type=url]')")
    type_url("HTTPS://github.com/Example/Web/issues/3216")
    press("Add issue or PR")
    eventually("document.querySelector('[role=dialog]').innerText.includes('#3216')")
    check(True, f"{where}: an uppercase scheme, public alias and mixed-case repository path are accepted")
    shot(f"dialog-alias-{width}-{theme}{suffix}")
    js("window.repoFixture.failNextSave = {code: 'task_repositories_revision_conflict', error: 'changed'}; true")
    press("Save")
    eventually("document.querySelector('[role=dialog] [role=alert]') && document.querySelector('[role=dialog]').innerText.includes('#3213')")
    check("Your edits are kept" in js("document.querySelector('[role=dialog]').innerText"), f"{where}: a revision conflict keeps the draft")
    alert_size = js("parseFloat(getComputedStyle(document.querySelector('[role=dialog] [role=alert]')).fontSize)")
    scale = 14 / 13 if suffix else 1
    check(abs(alert_size - 11.5 * scale) <= 0.2, f"{where}: the error text uses the submeta token ({alert_size:.2f}px)")
    shot(f"dialog-conflict-{width}-{theme}{suffix}")
    js("window.repoFixture.revision = 9; window.repoFixture.numbers = [3211, 3214]; true")
    press("Discard edits and reload")
    eventually("document.querySelector('[role=dialog]').innerText.includes('#3214')")
    check("#3213" not in js("document.querySelector('[role=dialog]').innerText"), f"{where}: the reload takes the store's newest links, not the stale draft")
    # Nothing to save yet: the draft is now the store's own value, so Save is off until an edit is made.
    check(js("[...document.querySelectorAll('[role=dialog] button')].some((b) => b.innerText.trim() === 'Save' && b.disabled)"), f"{where}: Save is off while the reloaded draft matches the store")
    press("Add issue or PR")
    eventually("!!document.querySelector('[role=dialog] input[type=url]')")
    type_url("https://github.com/example/web/pull/3215")
    js("document.querySelector('[role=dialog] input[type=url]').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true})); true")
    eventually("document.querySelector('[role=dialog]').innerText.includes('#3215')")
    press("Save")
    eventually("!document.querySelector('[role=dialog]')", seconds=15)
    eventually("document.querySelector('[data-todo-repositories]').innerText.includes('#3215')")
    check(True, f"{where}: the saved set is drawn on the page without reopening the editor")
    saves = js("JSON.stringify(window.repoFixture.calls.filter((c) => c.method === 'PUT'))")
    last = json.loads(saves)[-1]["body"]
    check(last["expected_revision"] == 9 and last["expected_catalog_revision"] == 4, f"{where}: the save sent the revisions it was shown after the reload")
    shot(f"section-saved-{width}-{theme}{suffix}")
    press("Manage links")
    eventually("!!document.querySelector('[role=dialog]')")
    # The dialog's document-level Escape listener is attached in an effect after its first paint: let it run.
    settle()
    # A real Escape on the page: the dialog's own handler closes it and the focus goes back to the button.
    cdp("Input.dispatchKeyEvent", type="keyDown", key="Escape", code="Escape", windowsVirtualKeyCode=27, nativeVirtualKeyCode=27)
    cdp("Input.dispatchKeyEvent", type="keyUp", key="Escape", code="Escape", windowsVirtualKeyCode=27, nativeVirtualKeyCode=27)
    eventually("!document.querySelector('[role=dialog]')")
    check(js("document.activeElement && document.activeElement.innerText.trim() === 'Manage links'"), f"{where}: Escape closes the dialog and hands the focus back")


def run_empty(theme="light", width=1280):
    """The canonical store's own empty answer (the last repository removed) is authoritative: no legacy PR comes back."""
    open_todo(theme, width, empty=True)
    section = "document.querySelector('[data-todo-repositories]').innerText"
    eventually("document.querySelector('[data-todo-repositories]').innerText.includes('No repositories linked')")
    check(js("document.querySelector('[data-todo-repositories] a[href=%s]') === null" % json.dumps(LEGACY_PR)),
          "a successful empty store shows no legacy PR")
    check("Add repository" in js(section), "and still offers the editor")
    shot(f"section-empty-{width}-{theme}")


def run_legacy(theme="light", width=1280):
    open_todo(theme, width, legacy=True)
    eventually("document.querySelector('[data-todo-repositories]').innerText.includes('Linked before repositories were stored here.')")
    check(js("!!document.querySelector('[data-todo-repositories] a[href=%s]')" % json.dumps(LEGACY_PR)),
          "a hub without the route shows the legacy PR link")
    check(js("!document.querySelector('[data-todo-repositories]').innerText.includes('Manage links')"), "and offers no editor there")
    shot(f"section-legacy-{width}-{theme}")


def run_rich(theme, width, suffix=""):
    """Provider marks, the one-line address, and a link's complete title as its hyperlink."""
    where = f"{theme} {width}px{suffix} marks and titles"
    open_todo(theme, width, large=bool(suffix), rich=True)
    section = "document.querySelector('[data-todo-repositories]')"
    check(js(f"{section}.querySelectorAll('[data-repository-mark=\"github\"]').length") == 1, f"{where}: the GitHub repository carries GitHub's mark")
    check(js(f"{section}.querySelectorAll('[data-repository-mark=\"gitlab\"]').length") == 1, f"{where}: the GitLab repository carries GitLab's mark")
    check(js(f"{section}.innerText.includes('github.com/example/web')"), f"{where}: the GitHub project address is drawn inline")
    check(js(f"{section}.innerText.includes('gitlab.example.com/team/api')"), f"{where}: the GitLab project address is drawn inline")
    check(js(f"{section}.innerText.includes('Fix the login redirect for every provider')"), f"{where}: a saved PR shows its full title")
    check(js(f"{section}.innerText.includes('Rotate access tokens without logging out')"), f"{where}: a saved MR shows its full title")
    check(js(f"{section}.innerText.includes('Handle expired credentials gracefully across the whole flow')"), f"{where}: a saved issue shows its full title")
    check(js(f"{section}.querySelector('a[href$=\"/-/merge_requests/9\"]').getAttribute('aria-label').includes('!9')"),
          f"{where}: a GitLab MR keeps GitLab's !number")
    check(js(f"{section}.querySelectorAll('[data-link-icon=\"merge\"]').length") == 1, f"{where}: the MR row uses the merge mark")
    check(js(f"{section}.querySelectorAll('[data-link-icon=\"pullRequest\"]').length") == 1, f"{where}: the GitHub PR row uses the pull-request mark")
    check(js(f"{section}.querySelectorAll('[data-link-icon=\"target\"]').length") == 2, f"{where}: both issue rows use the issue mark")
    check(js(f"{section}.querySelector('a[href$=\"/-/merge_requests/9\"]') !== null"), f"{where}: the MR title is one hyperlink")
    check(js(f"{section}.querySelector('a[href$=\"/pull/7\"]') !== null"), f"{where}: the PR title is one hyperlink")
    check(js(f"{section}.querySelector('a[href$=\"/issues/8\"]') !== null"), f"{where}: an untitled issue falls back to its number")
    check(js("document.documentElement.scrollWidth <= document.documentElement.clientWidth"), f"{where}: no horizontal overflow")
    # Named type tokens: the link title is text-meta (13 px) and the address text-submeta (11.5 px), both scaled by
    # the same Small/Standard/Large ratio as the rest of the page.
    scale = 14 / 13 if suffix else 1
    meta_size = js(f"parseFloat(getComputedStyle({section}.querySelector('[data-repository-link] a > span')).fontSize)")
    submeta_size = js(f"parseFloat(getComputedStyle({section}.querySelector('[data-repository-row] a span')).fontSize)")
    check(abs(meta_size - 13 * scale) <= 0.2, f"{where}: the title uses the meta token ({meta_size:.2f}px)")
    check(abs(submeta_size - 11.5 * scale) <= 0.2, f"{where}: the address uses the submeta token ({submeta_size:.2f}px)")
    # Hierarchy: a child row's mark sits under the repository's name/address, and its title starts beyond that mark.
    repo_text = js(f"{section}.querySelector('[data-repository-row] a span').getBoundingClientRect().left")
    child_icon = js(f"{section}.querySelector('[data-repository-link] [data-link-icon]').getBoundingClientRect().left")
    child_text = js(f"{section}.querySelector('[data-repository-link] a > span').getBoundingClientRect().left")
    note_left = js("""(() => {
      const note = document.querySelector('[data-todo-repositories] [data-repository-note]');
      const text = document.createRange(); text.selectNodeContents(note);
      return text.getBoundingClientRect().left;
    })()""")
    check(abs(child_icon - repo_text) <= 1, f"{where}: child rows indent under the repository ({child_icon:.1f} vs {repo_text:.1f})")
    check(child_text > child_icon + 10, f"{where}: child titles start beyond their mark ({child_text:.1f} vs {child_icon:.1f})")
    check(abs(note_left - repo_text) <= 1, f"{where}: the unlisted note text belongs to the repository ({note_left:.1f})")
    # Small geometry, not classes: the mark is inline at icon scale, and rows use the compact density (not a tall
    # min-height). The kind icon, number and external mark sit on the title's first line, including when it wraps.
    check(js(f"{section}.querySelector('[data-repository-mark=\"github\"]').getBoundingClientRect().width") <= 16,
          f"{where}: the provider mark is drawn at icon scale")
    header_h = js(f"{section}.querySelector('[data-repository-row]').getBoundingClientRect().height")
    # The shortest link row is a single line even where a long title wraps its neighbour.
    link_h = js(f"Math.min(...[...{section}.querySelectorAll('[data-repository-link]')].map((r) => r.getBoundingClientRect().height))")
    check(header_h <= 40, f"{where}: the repository row keeps the compact density ({header_h:.1f}px)")
    check(link_h <= 40, f"{where}: a single-line link row keeps the compact density ({link_h:.1f}px)")
    offsets = js("""(() => {
      const row = document.querySelector('[data-todo-repositories] [data-repository-link]');
      const title = row.querySelector('a > span');
      const spans = row.querySelectorAll('a > span');
      const line = title.getBoundingClientRect(), lh = parseFloat(getComputedStyle(title).lineHeight);
      const center = (el) => { const r = el.getBoundingClientRect(); return r.top + r.height / 2; };
      return [center(row.querySelector('[data-link-icon]')) - (line.top + lh / 2),
              center(spans[1]) - (line.top + lh / 2),
              center(row.querySelector('a > svg')) - (line.top + lh / 2)];
    })()""")
    check(all(abs(v) <= 3 for v in offsets), f"{where}: mark, number and external mark align with the title's first line ({offsets})")
    if width <= 600:
        lines = js("""(() => {
          const title = document.querySelector('[data-todo-repositories] [data-repository-link] a > span');
          return Math.round(title.getBoundingClientRect().height / parseFloat(getComputedStyle(title).lineHeight));
        })()""")
        check(lines >= 2, f"{where}: a long title still wraps in full ({lines} lines)")
    shot(f"marks-titles-{width}-{theme}{suffix}")


def open_creation(theme, width, suffix=""):
    """The New to-do dialog on the To-dos list, with the isolated repository catalog and a captured create POST."""
    fresh_tab()
    open_fixture_tab(f"{WEB}/?token=mock")
    wait_for_load()
    js("localStorage.setItem('persona.token','mock'); localStorage.setItem('persona.language','en'); "
       "localStorage.setItem('persona.todos.view','list'); localStorage.setItem('persona.theme',%s); true" % json.dumps(theme))
    js("localStorage.setItem('persona.text-size', %s); true" % json.dumps("large" if suffix else "standard"))
    open_fixture_tab(f"{WEB}/todos")
    wait_for_load()
    cdp("Page.enable")
    js("navigator.serviceWorker?.getRegistrations().then((rs) => Promise.all(rs.map((r) => r.unregister())))")
    js("caches?.keys().then((ks) => Promise.all(ks.map((k) => caches.delete(k))))")
    cdp("Page.addScriptToEvaluateOnNewDocument", source=fixture_source())
    cdp("Page.reload")
    wait_for_load()
    eventually("!!window.repoFixture")
    cdp("Emulation.setDeviceMetricsOverride", width=width, height=1100 if width > 600 else 1700, deviceScaleFactor=1, mobile=width <= 600)
    eventually("[...document.querySelectorAll('button')].some((b) => b.textContent.trim().includes('New to-do') || b.getAttribute('aria-label') === 'New to-do')", seconds=30)
    settle()
    if suffix:
        check(js("document.documentElement.dataset.textSize") == "large", "the creation page is at the Large text size")


def set_input(selector, value):
    js("""(() => {
      const el = %s;
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(el, %s);
      el.dispatchEvent(new Event('input', {bubbles: true}));
    })()""" % (selector, json.dumps(value)))


def run_creation(theme, width, suffix=""):
    where = f"{theme} {width}px{suffix} creation"
    def mod_enter(selector):
        js(f"{selector}.focus(); true")
        modifiers = 2 if theme == "light" else 4  # Ctrl / Meta, on the actual focused field.
        for event_type in ("keyDown", "keyUp"):
            cdp("Input.dispatchKeyEvent", type=event_type, key="Enter", code="Enter",
                windowsVirtualKeyCode=13, nativeVirtualKeyCode=13, modifiers=modifiers)
        settle()

    open_creation(theme, width, suffix)
    js("""(() => { const b = [...document.querySelectorAll('button')].find((x) => x.textContent.trim().includes('New to-do') || x.getAttribute('aria-label') === 'New to-do'); b.click(); })()""")
    eventually("!!document.querySelector('[role=dialog]')")
    set_input("document.querySelector('[role=dialog] input[type=text]')", "Pack")
    js("""(() => { const b = [...document.querySelectorAll('[role=dialog] button')].find((x) => x.textContent.trim().startsWith('Repositories and links')); b.click(); })()""")
    eventually("!!document.querySelector('[data-task-repository-editor]')")
    js("""(() => { const b = [...document.querySelectorAll('[role=dialog] button')].find((x) => x.textContent.trim() === 'Add repository'); b.click(); })()""")
    eventually("!!document.querySelector('[data-repository-picker]')")
    mod_enter("document.querySelector('[data-repository-picker] input')")
    check(js("window.repoFixture.creations.length === 0 && document.querySelectorAll('[role=dialog] [data-repository-row]').length === 1"),
          f"{where}: picker Mod+Enter selects the repository without creating a to-do")
    js("document.querySelector('[data-task-repository-editor] details summary').click(); true")
    js("""(() => { const s = document.querySelector('[role=dialog] select'); s.value = 'branch'; s.dispatchEvent(new Event('change', {bubbles: true})); })()""")
    eventually("document.querySelector('[role=dialog]').innerText.includes('Use a valid branch, tag, commit or Git ref.')")
    check(js("[...document.querySelectorAll('[role=dialog] button')].find((b) => b.textContent.trim() === 'Create').disabled"),
          f"{where}: an empty branch disables Create")
    js("""(() => { const el = document.querySelector('[role=dialog] textarea'); el.dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter', metaKey: true, bubbles: true, cancelable: true})); })()""")
    settle()
    check(js("!window.repoFixture.creation"), f"{where}: the invalid draft sends no create POST")
    shot(f"creation-invalid-{width}-{theme}{suffix}")
    set_input("document.querySelector('[role=dialog] details').querySelectorAll('input')[1]", "main")
    eventually("!document.querySelector('[role=dialog]').innerText.includes('Use a valid branch, tag, commit or Git ref.')")
    check(not js("[...document.querySelectorAll('[role=dialog] button')].find((b) => b.textContent.trim() === 'Create').disabled"),
          f"{where}: correcting the ref enables Create")
    check(js("document.querySelector('[role=dialog]').scrollWidth <= document.querySelector('[role=dialog]').clientWidth"),
          f"{where}: the creation dialog has no horizontal overflow")
    shot(f"creation-corrected-{width}-{theme}{suffix}")
    # The real Add-link field: a canonical link, the same issue through a public alias (silently deduplicated by the
    # existing AddLink behavior), then a genuinely different issue. One creation POST follows.
    def add_link(url):
        press("Add issue or PR")
        eventually("!!document.querySelector('[role=dialog] input[type=url]')")
        type_url(url)
        mod_enter("document.querySelector('[role=dialog] input[type=url]')")
        eventually("!document.querySelector('[role=dialog] input[type=url]')")
        check(js("window.repoFixture.creations.length === 0"), f"{where}: link Mod+Enter stays inside the editor")

    press("Add issue or PR")
    eventually("!!document.querySelector('[role=dialog] input[type=url]')")
    type_url("https://github.com/example/docs/issues/7")
    mod_enter("document.querySelector('[role=dialog] input[type=url]')")
    check(js("window.repoFixture.creations.length === 0 && !!document.querySelector('[role=dialog] [role=alert]')"),
          f"{where}: invalid link Mod+Enter keeps the error and sends no creation")
    shot(f"creation-keyboard-invalid-{width}-{theme}{suffix}")
    type_url("HTTPS://github.com/Example/Web/issues/7")
    mod_enter("document.querySelector('[role=dialog] input[type=url]')")
    eventually("!document.querySelector('[role=dialog] input[type=url]')")
    check(js("window.repoFixture.creations.length === 0"), f"{where}: correcting the link adds it without submitting")
    add_link("HTTP://github.com/Example/Web/issues/7")
    check(js("document.querySelectorAll('[role=dialog] [data-repository-link]').length") == 1,
          f"{where}: the alias of an existing link is deduplicated")
    shot(f"creation-duplicate-{width}-{theme}{suffix}")
    add_link("https://github.com/example/web/issues/8")
    check(js("document.querySelectorAll('[role=dialog] [data-repository-link]').length") == 2,
          f"{where}: a different issue is added")
    shot(f"creation-links-{width}-{theme}{suffix}")
    if width > 600:
        mod_enter("document.querySelector('[role=dialog] textarea')")
    else:
        js("[...document.querySelectorAll('[role=dialog] button')].find((b) => b.textContent.trim() === 'Create' && b.type === 'submit').click(); true")
    eventually("!!window.repoFixture.creation")
    check(js("window.repoFixture.creations.length") == 1, f"{where}: exactly one creation POST is sent")
    body = json.loads(js("JSON.stringify(window.repoFixture.creation)"))
    check(body["title"] == "Pack", f"{where}: the corrected title is sent")
    bindings = body["repository_bindings"]
    check(bindings["catalog_revision"] == 4, f"{where}: the creation sends the catalog revision")
    row0 = bindings["repositories"][0]
    check(row0["repo_id"] == "web" and row0["requested_ref"] == {"kind": "branch", "value": "main"},
          f"{where}: the corrected branch ref is sent with its repository")
    links = row0.get("links", [])
    check(len(links) == 2 and [link.get("url") for link in links] == [
        "https://github.com/example/web/issues/7", "https://github.com/example/web/issues/8",
    ], f"{where}: the deduplicated and added issues send one create POST")


try:
    for theme in ("light", "dark"):
        for width in (1280, 390):
            run(theme, width)
            run_rich(theme, width)
    run("light", 390, "-large")
    run_rich("light", 390, "-large")
    run_empty()
    run_legacy()
    for theme in ("light", "dark"):
        for width in (1280, 390):
            run_creation(theme, width)
    run_creation("light", 390, "-large")
    print("VERDICT: pass")
except AssertionError as failure:
    print(f"VERDICT: FAIL {failure}")
    sys.exit(1)
finally:
    close_owned_tabs()
