import { expect, test } from "bun:test";
import { settingsCheckFetch, type SettingsCheckParams } from "./index.ts";

// The source adapter consumes the request-bound proxy option on every broker attempt, including redirects.
test("settingsCheckFetch keeps each check proxy on its own requests", async () => {
  const ordinaryFetch = globalThis.fetch;
  const calls: { url: string; init?: RequestInit & { proxy?: string } }[] = [];
  let release!: () => void;
  const pending = new Promise<void>(resolve => { release = resolve; });
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit & { proxy?: string }) => {
    calls.push({ url: String(input), init });
    await pending;
    return new Response("ok");
  }) as typeof fetch;
  try {
    const params = (proxy?: string): SettingsCheckParams => ({ config: {}, secrets: {}, ...(proxy ? { proxy } : {}) });
    const checkB = settingsCheckFetch(params("http://ext:candidate-b@127.0.0.1:3128"));
    const checkC = settingsCheckFetch(params("http://ext:candidate-c@127.0.0.1:3128"));
    const init = { method: "POST", headers: { "x-check": "unchanged" }, body: "candidate" };
    const originalInit = { ...init, headers: { ...init.headers } };
    const responses = [
      checkB("http://b.test/start", init),
      globalThis.fetch("http://a.test/saved"),
      checkC("http://c.test/start"),
      settingsCheckFetch(params())("http://a.test/legacy", init),
    ];
    release();
    await Promise.all(responses);

    expect(init).toEqual(originalInit);
    expect(calls.map(call => [call.url, call.init?.proxy])).toEqual([
      ["http://b.test/start", "http://ext:candidate-b@127.0.0.1:3128"],
      ["http://a.test/saved", undefined],
      ["http://c.test/start", "http://ext:candidate-c@127.0.0.1:3128"],
      ["http://a.test/legacy", undefined],
    ]);
    expect(calls[0].init).toMatchObject(originalInit);
    expect(calls[3].init).toEqual(originalInit);
  } finally {
    globalThis.fetch = ordinaryFetch;
  }
});
