// @vitest-environment jsdom
// The recording module (#13407, web half; docs/web/web.md "Modules"): a recording's parts of its knowledge page come
// through the `kb.detail` slot. On (the default), a recording's page draws its facts and transcript as before, and the
// module's chunk loads only when a recording opens, never for another entry. Off, recordings are absent: their
// addresses are unknown ones, the Meetings list reads only the imported meetings, an open recording says it is not
// there, and the chunk and the strings are never requested.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { forgetModuleSwitches, setModuleSwitchedOff } from "../../core/module-switch";
import { connectModuleHub, moduleKBKindAt, registerModule, resetModulesForTest, type ModuleDescriptor } from "../../core/module-host";
import { loadKBDetailParts, resetKBDetailPartsForTest } from "../../core/ModuleKBDetail";
import { setToken } from "../../lib/api";
import { setRosterDefault } from "../../lib/manager-choice";
import { parse } from "../../lib/router";
import { getState, reset, subscribe } from "../../lib/store";
import { lists, searches } from "../../views/knowledge/cache";
import { EntryDetail } from "../../views/knowledge/KBDetail";
import { ListPane } from "../../views/knowledge/KBList";
import { recording } from "./module";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const id = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa";
const detail = (kind: string, entry: string) => ({
  detail: {
    kind, id: entry, path: `${kind}/${entry}`,
    meta: kind === "recordings" ? { title: "Design review", started_at: "2026-11-10T06:00:00Z", complete: true, kind: "meeting" } : {},
    segments: kind === "recordings" ? [{ start: 0, end: 4, text: "Keep the export cap." }] : undefined,
  },
});
const chunk = vi.fn();
const catalog = vi.fn();
function counted(over: Partial<ModuleDescriptor> = {}): ModuleDescriptor {
  const locales = Object.fromEntries(Object.entries(recording.locales!).map(([lang, load]) => [lang, () => (catalog(lang), load())]));
  return { ...recording, locales, load: () => (chunk(), recording.load()), ...over };
}

let host: HTMLDivElement;
let root: Root;
let urls: string[];
const flush = async () => {
  for (let i = 0; i < 12; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const text = () => host.textContent ?? "";
const open = async (kind: string, entry: string) => {
  act(() => root.render(<EntryDetail kind={kind} id={entry} reloadKey={0} onToggleSidebar={() => {}} onBack={() => {}} onOpen={() => {}} onSaved={() => {}} />));
  await flush();
};

beforeEach(() => {
  connectModuleHub(getState, subscribe);
  localStorage.clear();
  forgetModuleSwitches();
  resetModulesForTest();
  resetKBDetailPartsForTest();
  lists.clear();
  searches.clear();
  chunk.mockClear();
  catalog.mockClear();
  setToken("token");
  setRosterDefault("assistant");
  urls = [];
  vi.stubGlobal("fetch", vi.fn((url: string) => {
    urls.push(url.replace(/^.*\/api\/v1/, ""));
    const m = /\/kb\/([a-z]+)\/([^/?]+)$/.exec(url);
    const body = m ? detail(m[1]!, decodeURIComponent(m[2]!)) : { items: [], tags: [] };
    return Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  resetModulesForTest();
  resetKBDetailPartsForTest();
  localStorage.clear();
  forgetModuleSwitches();
  vi.unstubAllGlobals();
});

describe("the recording module", () => {
  it("is on by default: a recording's page draws its facts and transcript, its chunk loaded when a recording opens", async () => {
    registerModule(counted());
    expect(moduleKBKindAt("recordings")).toMatchObject({ id: "recording" });
    expect(parse(`/kb/recordings/${id}`)).toEqual({ name: "kb", kind: "recordings", id });
    // another entry's page needs none of it
    await open("projects", "persona");
    expect(chunk).not.toHaveBeenCalled();
    expect(catalog).not.toHaveBeenCalled();
    await open("recordings", id);
    // its code takes a moment to come in the first time
    await vi.waitFor(async () => {
      await flush();
      expect(text()).toContain("转写");
    }, { timeout: 10_000 });
    expect(chunk).toHaveBeenCalledTimes(1);
    // its strings came before its code
    expect(catalog).toHaveBeenCalledTimes(1);
    expect(text()).toContain("Keep the export cap.");
    expect(text()).toContain("设备");
    expect(text()).toContain("改名");
  });

  it("reads the recordings into the Meetings list while it is on", async () => {
    registerModule(counted());
    await act(async () => root.render(<ListPane kind="meetings" detailOpen={false} reloadKey={0} onToggleSidebar={() => {}} onOpen={() => {}} />));
    await flush();
    expect(urls).toEqual(expect.arrayContaining(["/kb/meetings", "/kb/recordings"]));
  });

  it("switched off, makes recordings absent and never loads its chunk or strings", async () => {
    setModuleSwitchedOff("recording", true);
    registerModule(counted());
    expect(moduleKBKindAt("recordings")).toBe("absent");
    expect(parse(`/kb/recordings/${id}`).name).toBe("not-found");
    // the Meetings list is the imported meetings alone
    await act(async () => root.render(<ListPane kind="meetings" detailOpen={false} reloadKey={0} onToggleSidebar={() => {}} onOpen={() => {}} />));
    await flush();
    expect(urls).toContain("/kb/meetings");
    expect(urls).not.toContain("/kb/recordings");
    // a recording left open says it is not there, with none of its parts
    await open("recordings", id);
    expect(text()).not.toContain("转写");
    expect(host.querySelector("[role=status]")).not.toBeNull();
    expect(await loadKBDetailParts("recordings")).toBeNull();
    expect(chunk).not.toHaveBeenCalled();
    expect(catalog).not.toHaveBeenCalled();
  });

  it("is off on a hub that publishes module flags without module.recording, and on with it", () => {
    registerModule(counted());
    Object.assign(getState(), { features: ["modules"], loaded: true });
    expect(moduleKBKindAt("recordings")).toBe("absent");
    expect(parse(`/kb/recordings/${id}`).name).toBe("not-found");
    Object.assign(getState(), { features: ["modules", "module.recording"], loaded: true });
    expect(moduleKBKindAt("recordings")).toMatchObject({ id: "recording" });
    // a hub that publishes no module flags (an older one) keeps it on
    Object.assign(getState(), { features: ["managers"], loaded: true });
    expect(moduleKBKindAt("recordings")).toMatchObject({ id: "recording" });
  });

  it("refuses a second module that claims the same knowledge kind", () => {
    registerModule(recording);
    expect(() => registerModule({ ...recording, id: "recording-two" })).toThrow(/knowledge kind "recordings"/);
  });
});
