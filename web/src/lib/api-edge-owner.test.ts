// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";

vi.mock("./edge-auth", async (original) => ({ ...(await original<typeof import("./edge-auth")>()), reloadForSignIn: vi.fn() }));
import { api, apiBlob, credentialOwner, getToken, handleUnauthorized, setPairedToken, setToken } from "./api";
import { reloadForSignIn } from "./edge-auth";

let answer: (response: Response) => void;
beforeEach(() => {
  localStorage.clear(); setToken("synthetic-owner-a");
  vi.mocked(reloadForSignIn).mockClear();
  vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>((resolve) => { answer = resolve; })));
});
afterEach(() => { handleUnauthorized(() => {}); setToken(""); vi.unstubAllGlobals(); });

it.each(["html 401", "redirect"])("reloads for current owned %s but not for a retired generation", async (kind) => {
  const response = () => kind === "html 401"
    ? new Response("<html>sign in</html>", { status: 401, headers: { "Content-Type": "text/html" } })
    : new Response(null, { status: 302 });
  const token = getToken(), tokenOwner = credentialOwner();
  const retired = api("/machines", { token, tokenOwner, timeout: 0 }).catch((error: unknown) => error);
  setPairedToken(token);
  answer(response());
  await retired;
  expect(reloadForSignIn).not.toHaveBeenCalled();
  const current = api("/machines", { token, tokenOwner: credentialOwner(), timeout: 0 }).catch((error: unknown) => error);
  answer(response());
  await current;
  expect(reloadForSignIn).toHaveBeenCalledOnce();
});

it("keeps proxy sign-in for an unowned explicit probe", async () => {
  const probe = api("/machines", { token: "synthetic-probe", timeout: 0 }).catch((error: unknown) => error);
  setToken("synthetic-owner-b");
  answer(new Response("<html>sign in</html>", { status: 401, headers: { "Content-Type": "text/html" } }));
  await probe;
  expect(reloadForSignIn).toHaveBeenCalledOnce();
});

it.each(["html 401", "redirect", "JSON 401"])("fences %s for a retired context while preserving current behavior", async (kind) => {
  const response = () => kind === "html 401"
    ? new Response("<html>sign in</html>", { status: 401, headers: { "Content-Type": "text/html" } })
    : kind === "redirect" ? new Response(null, { status: 302 }) : Response.json({ error: "invalid token" }, { status: 401 });
  const unauthorized = vi.fn(); handleUnauthorized(unauthorized);
  let current = true;
  const options = { token: getToken(), tokenOwner: credentialOwner(), isAuthContextCurrent: () => current, timeout: 0 };
  const retired = api("/machines", options).catch((error: unknown) => error);
  expect(vi.mocked(fetch).mock.calls[0][1]).not.toHaveProperty("isAuthContextCurrent");
  current = false; answer(response()); await retired;
  expect(unauthorized).not.toHaveBeenCalled();
  expect(reloadForSignIn).not.toHaveBeenCalled();
  current = true;
  const fresh = api("/machines", options).catch((error: unknown) => error);
  answer(response()); await fresh;
  expect(unauthorized).toHaveBeenCalledTimes(kind === "JSON 401" ? 1 : 0);
  expect(reloadForSignIn).toHaveBeenCalledTimes(kind === "JSON 401" ? 0 : 1);
});

// The visualization fragment route answers 200 text/html on purpose (#10321): its opt-out must neither raise the edge
// interception error nor reload the page, while every other /api route keeps the guard.
it("does not treat the visualization route's own HTML answer as an edge interception", async () => {
  const guarded = apiBlob("/sessions/m/s/items/i1/visualization", { token: getToken(), timeout: 0 })
    .catch((error: unknown) => error);
  answer(new Response("<html>sign in</html>", { status: 200, headers: { "Content-Type": "text/html" } }));
  expect(await guarded).toBeInstanceOf(Error);
  expect(reloadForSignIn).toHaveBeenCalledOnce();

  vi.mocked(reloadForSignIn).mockClear();
  const optedIn = apiBlob("/sessions/m/s/items/i1/visualization", { token: getToken(), timeout: 0, allowHtmlResponse: true });
  answer(new Response("<div>chart</div>", { status: 200, headers: { "Content-Type": "text/html; charset=utf-8" } }));
  const blob = await optedIn;
  expect(blob.type).toBe("text/html;charset=utf-8");
  expect(blob.size).toBe("<div>chart</div>".length);
  expect(reloadForSignIn).not.toHaveBeenCalled();
});
