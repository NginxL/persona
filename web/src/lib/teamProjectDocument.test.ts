import { createHash } from "node:crypto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { loadApprovedProjectDocument } from "./teamProjectDocument";

const digest = (content: string) => createHash("sha256").update(content, "utf8").digest("hex");
const content = "设计稿 📚\r\nCafe\u0301\n";
const doc = { project: "studio", document: "design_2", revision: 3, content, sha256: digest(content), author: "alice", approved_by: "bob" };
const read = (signal = new AbortController().signal) => loadApprovedProjectDocument("studio", "design_2", "fixture-reader", signal);
let fetcher: ReturnType<typeof vi.fn<typeof fetch>>;

beforeEach(() => {
  vi.stubGlobal("location", new URL("https://teams.example/hub/"));
  fetcher = vi.fn<typeof fetch>().mockImplementation(async () => Response.json(doc));
  vi.stubGlobal("fetch", fetcher);
});
afterEach(() => vi.unstubAllGlobals());

describe("reading an approved project revision", () => {
  it("sends only the member credential to the fixed same-origin endpoint and verifies exact UTF-8 bytes", async () => {
    const signal = new AbortController().signal;
    await expect(read(signal)).resolves.toEqual(doc);
    expect(fetcher).toHaveBeenCalledWith(new URL("https://teams.example/v1/projects/studio/documents/design_2/approved"), {
      method: "GET", headers: { Authorization: "Bearer fixture-reader", Accept: "application/json" },
      credentials: "omit", mode: "same-origin", cache: "no-store", redirect: "error", referrerPolicy: "no-referrer", signal,
    });
  });
  it.each(["http://localhost:1812", "http://127.0.0.2", "http://[::1]"])("allows loopback development at %s", async url => {
    vi.stubGlobal("location", new URL(url));
    await expect(read()).resolves.toEqual(doc);
  });
  it.each(["http://projects.example", "http://127.example.test", "file:///test"])("declines %s before contacting it", async url => {
    vi.stubGlobal("location", new URL(url));
    await expect(read()).rejects.toMatchObject({ code: "unavailable" });
    expect(fetcher).not.toHaveBeenCalled();
  });
  it.each([["bad/path", "design_2"], ["studio", "?"], ["a".repeat(101), "design_2"]])("rejects invalid identifiers", async (project, id) => {
    await expect(loadApprovedProjectDocument(project, id, "fixture-reader", new AbortController().signal)).rejects.toMatchObject({ code: "invalid" });
    expect(fetcher).not.toHaveBeenCalled();
  });
  it.each([[401,"access"],[403,"access"],[404,"missing"],[500,"unavailable"]] as const)("maps %i without surfacing the body", async (status, code) => {
    fetcher.mockResolvedValue(new Response("fixture-reader", { status }));
    await expect(read()).rejects.toMatchObject({ code, message: code });
  });
  it.each([
    { project: "other" }, { document: "other" }, { approved_by: "" }, { approved_by: undefined },
    { approved_by: "bad/id" }, { author: "" }, { revision: 0 }, { revision: 2.5 }, { revision: Number.MAX_SAFE_INTEGER + 1 },
    { content: "" }, { content: "a\0b" }, { content: "\ud800" }, { sha256: digest(content.normalize("NFC")) },
  ])("rejects a mismatched or malformed revision", async patch => {
    fetcher.mockResolvedValue(Response.json({ ...doc, ...patch }));
    await expect(read()).rejects.toMatchObject({ code: "invalid" });
  });
  it("checks the exact byte limit after UTF-8 encoding", async () => {
    const exact = "é".repeat(128 * 1024);
    for (const text of [exact, exact + "a"]) {
      fetcher.mockResolvedValue(Response.json({ ...doc, content: text, sha256: digest(text) }));
      if (text === exact) expect((await read()).content).toEqual(exact);
      else await expect(read()).rejects.toMatchObject({ code: "invalid" });
    }
  });
  it("bounds each streamed response without requiring Content-Length", async () => {
    const cancel = vi.fn();
    const body = new ReadableStream({ pull(controller) { controller.enqueue(new Uint8Array(256 * 1024)); }, cancel });
    fetcher.mockResolvedValue(new Response(body));
    await expect(read()).rejects.toMatchObject({ code: "invalid" });
    expect(cancel).toHaveBeenCalledOnce();
  });
  it("skips an already cancelled request", async () => {
    await expect(read(AbortSignal.abort("fixture-reader"))).rejects.toMatchObject({ name: "AbortError", message: "Aborted" });
    expect(fetcher).not.toHaveBeenCalled();
  });
  it("returns a safe cancellation when a request is aborted", async () => {
    const controller = new AbortController();
    fetcher.mockImplementation(async () => { controller.abort("fixture-reader"); throw Error("fixture-reader"); });
    await expect(read(controller.signal)).rejects.toMatchObject({ name: "AbortError", message: "Aborted" });
  });
  it("does not surface upstream errors or parse their text as a document", async () => {
    fetcher.mockRejectedValueOnce(Error("fixture-reader")).mockResolvedValueOnce(new Response("fixture-reader"));
    await expect(read()).rejects.toMatchObject({ code: "unavailable", message: "unavailable" });
    await expect(read()).rejects.toMatchObject({ code: "invalid", message: "invalid" });
  });
});
