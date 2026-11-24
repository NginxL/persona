// @vitest-environment jsdom
// Settings › Persona › Direction notes (#10086): each current direction is listed with its notes; an edit enables Save,
// which PUTs that direction's notes and shows what the hub kept; Revert goes back; a failed save keeps the edit and says
// so; ended directions are left out; no directions says how to make one; the settings search finds the group.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { searchSettings } from "./search";

const toast = vi.fn();
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: (...a: unknown[]) => toast(...a) }));

const { DirectionNotes, directionNotesDeps } = await import("./DirectionNotes");
import type { DirectionNotesDeps, DirectionNotesEntry } from "./DirectionNotes";

let root: Root;
let host: HTMLDivElement;
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  toast.mockReset();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const LEADERS: DirectionNotesEntry[] = [
  { id: "conv_a", name: "Weather", project: "weather-app", notes: "Readings: build number", notes_at: new Date().toISOString() },
  { id: "conv_b", title: "Infra" },
  { id: "conv_c", name: "Old", ended: true, notes: "gone" },
];

function setValue(el: HTMLTextAreaElement, value: string) {
  const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
  set.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

async function mount(deps: DirectionNotesDeps) {
  await act(async () => root.render(<DirectionNotes deps={deps} />));
}

const area = (id: string) => host.querySelector<HTMLTextAreaElement>(`[data-direction="${id}"] textarea`)!;
const button = (id: string, label: string) =>
  [...host.querySelectorAll<HTMLButtonElement>(`[data-direction="${id}"] button`)].find((b) => b.textContent === label)!;

describe("DirectionNotes", () => {
  it("lists current directions and saves one direction's notes", async () => {
    const save = vi.fn(async (_id: string, notes: string) => notes.trim());
    await mount({ list: async () => LEADERS, save });
    expect(host.querySelectorAll("[data-direction]")).toHaveLength(2);
    expect(host.textContent).toContain("Weather");
    expect(host.textContent).toContain("Infra");
    expect(host.textContent).not.toContain("Old");
    expect(area("conv_a").value).toBe("Readings: build number");
    expect(button("conv_a", t("common.save")).disabled).toBe(true);

    await act(async () => setValue(area("conv_b"), "  Back up first  "));
    expect(button("conv_b", t("common.save")).disabled).toBe(false);
    await act(async () => button("conv_b", t("common.save")).click());
    expect(save).toHaveBeenCalledWith("conv_b", "  Back up first  ");
    expect(area("conv_b").value).toBe("Back up first");
    expect(toast).toHaveBeenCalledWith(t("settings-directionnotes.saved", { name: "Infra" }), "ok");
    expect(button("conv_b", t("common.save")).disabled).toBe(true);
  });

  it("reverts an edit, and keeps it when the save fails", async () => {
    await mount({ list: async () => LEADERS, save: async () => { throw new Error("hub down"); } });
    await act(async () => setValue(area("conv_a"), "changed"));
    await act(async () => button("conv_a", t("settings-assistantsettings.revert")).click());
    expect(area("conv_a").value).toBe("Readings: build number");
    await act(async () => setValue(area("conv_a"), "changed"));
    await act(async () => button("conv_a", t("common.save")).click());
    expect(area("conv_a").value).toBe("changed");
    expect(toast.mock.calls.at(-1)?.[1]).toBe("error");
  });

  it("says how to make a direction when there is none, and why when the list fails", async () => {
    await mount({ list: async () => [], save: vi.fn() });
    expect(host.textContent).toContain(t("settings-directionnotes.empty"));
    act(() => root.unmount());
    root = createRoot(host);
    await mount({ list: async () => { throw new Error("hub down"); }, save: vi.fn() });
    expect(host.textContent).toContain("hub down");
  });

  it("calls the hub's routes", async () => {
    const calls: { path: string; init?: { method?: string; json?: unknown } }[] = [];
    const fake = (async (path: string, init?: { method?: string; json?: unknown }) => {
      calls.push({ path, init });
      return path.endsWith("/notes") ? { notes: "kept" } : { leaders: LEADERS };
    }) as unknown as Parameters<typeof directionNotesDeps>[0];
    const deps = directionNotesDeps(fake);
    expect(await deps.list()).toHaveLength(3);
    expect(await deps.save("conv a", "x")).toBe("kept");
    expect(calls[1]).toMatchObject({ path: "/assistant/directions/conv%20a/notes", init: { method: "PUT", json: { notes: "x" } } });
  });

  it("opens the shared new-conversation permission setting from a direction", async () => {
    const openPermissions = vi.fn();
    const deps: DirectionNotesDeps = { list: async () => LEADERS, save: vi.fn() };
    const props = { deps, onOpenPermissions: openPermissions } as unknown as { deps: DirectionNotesDeps };
    await act(async () => root.render(<DirectionNotes {...props} />));
    const link = host.querySelector<HTMLButtonElement>('[data-settings-row="direction-permissions"]');
    expect(link).not.toBeNull();
    expect(link!.textContent).toContain(t("settings-directionnotes.permissions"));
    await act(async () => link!.click());
    expect(openPermissions).toHaveBeenCalledOnce();
  });

  it("is found by the settings search", () => {
    expect(searchSettings(t("settings-directionnotes.title")).groups.some((r) => r.anchor === "direction-notes")).toBe(true);
    expect(searchSettings(t("settings-directionnotes.title")).groups.some((r) => r.anchor === "default-permissions")).toBe(true);
  });
});
