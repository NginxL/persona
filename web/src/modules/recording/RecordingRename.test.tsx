// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { recording } from "./module";
import { setToken } from "../../lib/api";
import { namedBy, RecordingRenameDialog } from "./RecordingRename";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
type Request = { url: string; init: RequestInit; resolve: (value: Response) => void };
let requests: Request[];
let host: HTMLDivElement;
let root: Root;
const closed = vi.fn();
const done = vi.fn();
const ID = "0f8fad5b-d9cb-469f-a165-70867728950e";
const render = (name: string, source: string) => act(() => root.render(
  <RecordingRenameDialog id={ID} name={name} source={source} auth={{ headers: {} }} onClose={closed} onDone={done} />,
));
const field = () => document.querySelector<HTMLInputElement>("input")!;
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label)!;
const type = (value: string) => act(() => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field(), value);
  field().dispatchEvent(new Event("input", { bubbles: true }));
});
const reply = async (value: unknown, status = 200) => act(async () => {
  requests.at(-1)!.resolve(new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } }));
});

beforeEach(async () => {
  // this module's strings come with its code, not the client catalog (module.ts)
  await initI18n("en", recording.locales!);
  setToken("synthetic-owner-a");
  closed.mockClear();
  done.mockClear();
  requests = [];
  vi.stubGlobal("fetch", vi.fn((url: string, init: RequestInit) => new Promise<Response>((resolve) => requests.push({ url, init, resolve }))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("renaming a recording on the web (#8877)", () => {
  it("starts from the name shown and saves a new one", async () => {
    render("Export cap", "model");
    expect(field().value).toBe("Export cap");
    expect(button("Save").disabled).toBe(true);
    await type("  Release date  ");
    expect(button("Save").disabled).toBe(false);
    await act(async () => button("Save").click());
    expect(requests).toHaveLength(1);
    expect(requests[0].url).toContain(`/recordings/${ID}/title`);
    expect(requests[0].init.method).toBe("PUT");
    expect(JSON.parse(String(requests[0].init.body))).toEqual({ title: "Release date" });
    await reply({ name: "Release date", name_source: "renamed" });
    expect(done).toHaveBeenCalledTimes(1);
    expect(closed).toHaveBeenCalledTimes(1);
  });

  it("starts empty for a recording named only by when it started, and an empty name clears a web name", async () => {
    render("Recording 2026-11-10 14:00", "");
    expect(field().value).toBe("");
    expect(button("Save").disabled).toBe(true);
    await act(async () => root.unmount());
    root = createRoot(host);
    render("Export cap", "renamed");
    await type("");
    await act(async () => button("Save").click());
    expect(JSON.parse(String(requests[0].init.body))).toEqual({ title: "" });
  });

  it("keeps the dialog open and says why when the hub refuses", async () => {
    render("Export cap", "model");
    await type("Release date");
    await act(async () => button("Save").click());
    await reply({ error: "A title must be one line of at most 200 characters." }, 400);
    expect(done).not.toHaveBeenCalled();
    expect(closed).not.toHaveBeenCalled();
    expect(button("Save").disabled).toBe(false);
  });

  it("reads where the name comes from", () => {
    expect(namedBy({ name_source: "model" })).toBe("model");
    expect(namedBy({})).toBe("");
    expect(namedBy(null)).toBe("");
  });
});
