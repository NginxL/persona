import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import assert from "node:assert/strict";
import test from "node:test";
import { externalBrowserURL, hubRoute, inHub, parseDeepLink, parseHubAddress } from "../src/urls.js";

test("hub root and reverse-proxy subpath are preserved", () => {
  assert.equal(parseHubAddress("https://hub.example/persona").url.href, "https://hub.example/persona/");
  assert.equal(parseHubAddress("hub.example/persona/").prefix, "/persona/");
  assert.equal(hubRoute(parseHubAddress("https://hub.example/persona"), "/s/computer/session"), "https://hub.example/persona/s/computer/session");
  assert.equal(inHub(parseHubAddress("https://hub.example/persona"), "https://hub.example/persona/api/v1/ws"), true);
  assert.equal(inHub(parseHubAddress("https://hub.example/persona"), "https://hub.example/other"), false);
  assert.equal(inHub(parseHubAddress("https://hub.example/persona"), "https://hub.example.evil.test/persona/"), false);
});

test("hub addresses reject credentials, fragments and arbitrary schemes", () => {
  for (const input of ["file:///tmp/a", "javascript:alert(1)", "https://u:p@hub.example/", "https://hub.example/#a", "https://hub.example/a b", ""]) {
    assert.throws(() => parseHubAddress(input), input);
  }
});

test("external links are restricted to HTTP(S) without credentials", () => {
  assert.equal(externalBrowserURL("https://example.com/a"), "https://example.com/a");
  assert.equal(externalBrowserURL("file:///tmp/a"), undefined);
  assert.equal(externalBrowserURL("javascript:alert(1)"), undefined);
  assert.equal(externalBrowserURL("https://u:p@example.com/"), undefined);
});

test("deep links never auto-pair or navigate to an arbitrary origin", () => {
  assert.deepEqual(parseDeepLink("persona://open?route=%2Fs%2Fcomputer%2Fsession"), { kind: "open", url: "persona://open?route=%2Fs%2Fcomputer%2Fsession", route: "/s/computer/session" });
  assert.equal(parseDeepLink("persona://link?url=https%3A%2F%2Fhub.example")?.kind, "pair-link");
  assert.equal(parseDeepLink("persona://pair?token=temporary")?.kind, "pair-app");
  assert.equal(parseDeepLink("persona://open?route=//evil.test"), undefined);
  assert.equal(parseDeepLink("persona://open?route=https://evil.test"), undefined);
  assert.equal(parseDeepLink("file:///tmp/a"), undefined);
});

test("the route table's app links (spec/fixtures/deep-links): the route handed to the web app, malformed ones refused", () => {
  // docs/protocol/deep-links.md: persona://open<path> is the hub's web path after persona://open; only the keys a page
  // reads go with it (no `hub`, no token), and the web app opens it or says it is no page.
  const fixture = JSON.parse(readFileSync(resolve("../spec/fixtures/deep-links/cases.json"), "utf8")) as {
    parse: { name: string; url: string; route?: string; reason?: string }[];
  };
  let checked = 0;
  for (const c of fixture.parse) {
    if (!c.url.startsWith("persona://open")) continue;
    if (c.route !== undefined) {
      assert.equal(parseDeepLink(c.url)?.route, c.route, c.name);
      checked++;
    } else if (c.reason === "unsafe") {
      assert.equal(parseDeepLink(c.url), undefined, c.name);
      checked++;
    }
  }
  assert.ok(checked >= 8, `only ${checked} cases`);
  assert.deepEqual(parseDeepLink("persona://open/inbox/i-1?manager=m2&token=x#y"), undefined); // a fragment: not a page link
  assert.equal(parseDeepLink("persona://open/settings?tab=extensions&extension=gh&add=1")?.route, "/settings?tab=extensions&extension=gh");
  assert.equal(parseDeepLink("persona://open/s/m/%2F%2Fevil")?.route, "/s/m/%2F%2Fevil"); // escaped: still a path of the hub
});
