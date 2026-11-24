// @vitest-environment jsdom
// The chat slots, Settings categories and card renderers a module fills (#13846, core/ChatSlots.tsx,
// core/ModuleSettingsPage.tsx): a synthetic module fills each one, and each draws only while the module is on; with no
// module filling a slot it draws nothing.
import { act, useLayoutEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { Interaction } from "../lib/types";
import { getState, reset, subscribe } from "../lib/store";
import { ChatTail, InteractionMarks, ModuleCard, useCardRenderer, useChatBar, useComposerAction } from "./ChatSlots";
import { connectModuleHub, loadModule, MODULES_FEATURE, registerModule, resetModulesForTest, type ChatPageProps, type ChatSlotComponents, type ComposerAction, type ModuleCode, type ModuleDescriptor } from "./module-host";
import { ModuleSettingsPage } from "./ModuleSettingsPage";
import { forgetModuleSwitches, setModuleSwitchedOff } from "./module-switch";
import { isModuleTab, navGroups, settingsAt, tabInfo, TABS } from "../views/settings/nav";
import { searchSettings } from "../views/settings/search";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const page: ChatPageProps = { mid: "m", sid: "s", owner: "o", session: undefined, assistant: false, label: "Claude", persona: "Persona", canSend: true, items: () => [], cards: [] };
const card = { id: "c1", machine_id: "m", session_id: "s", kind: "garden-card" } as unknown as Interaction;

let barTaken = false;
const chat: ChatSlotComponents = {
  "composer.trailingAction": ({ offer, label }) => {
    useLayoutEffect(() => offer({ icon: "heart", name: "Water", label: `Water ${label}`, primary: true, onClick: () => {} }), [offer, label]);
    return <span data-garden-dialog />;
  },
  "chat.tail": ({ sid }) => <span data-garden-tail>{sid}</span>,
  "chat.bar": ({ place, take }) => {
    useLayoutEffect(() => {
      if (place === "composer") take(barTaken);
    }, [place, take]);
    return place === "composer" && barTaken ? <div data-garden-bar /> : null;
  },
  "interaction.asked": ({ interaction }) => <span data-garden-mark>{interaction.id}</span>,
};
const code: ModuleCode = {
  chat,
  settings: { garden: ({ tab }) => <p data-garden-settings>{tab}</p> },
  cards: { "garden-card": ({ interaction }) => <p data-garden-card>{interaction.id}</p> },
};

const garden: ModuleDescriptor = {
  id: "garden",
  feature: "module.garden",
  title: () => "Garden",
  description: () => "Watering reminders",
  chatSlots: ["composer.trailingAction", "chat.tail", "chat.bar", "interaction.asked"],
  settingsTabs: [{ key: "garden", label: () => "Garden", hint: () => "Plots and watering", icon: "heart", group: "device" }],
  cardRenderers: ["garden-card"],
  load: async () => code,
};

let host: HTMLDivElement;
let root: Root;
let seen: { action: ComposerAction | undefined; taken: boolean };

function Chat() {
  const { action, slot } = useComposerAction(page);
  const bar = useChatBar(page);
  const renderer = useCardRenderer(card.kind);
  seen = { action, taken: bar.taken };
  return (
    <div>
      {slot}
      <ChatTail {...page} />
      {bar.bar("dock")}
      {bar.bar("composer")}
      <InteractionMarks interaction={card} />
      {renderer ? <ModuleCard module={renderer} interaction={card} /> : <p data-core-card />}
    </div>
  );
}

const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => await new Promise((r) => setTimeout(r, 0)));
};

beforeEach(() => {
  connectModuleHub(getState, subscribe);
  localStorage.clear();
  forgetModuleSwitches();
  reset();
  resetModulesForTest();
  barTaken = false;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  localStorage.clear();
  forgetModuleSwitches();
  resetModulesForTest();
});

describe("chat slots", () => {
  it("draw nothing with no module filling them", async () => {
    act(() => root.render(<Chat />));
    await flush();
    expect(host.innerHTML).toBe('<div><p data-core-card="true"></p></div>');
    expect(seen).toEqual({ action: undefined, taken: false });
  });

  it("draw what a module fills, once its code is in", async () => {
    registerModule(garden);
    act(() => root.render(<Chat />));
    await flush();
    expect(host.querySelector("[data-garden-dialog]")).not.toBeNull();
    expect(seen.action).toMatchObject({ name: "Water", label: "Water Claude", primary: true });
    expect(host.querySelector("[data-garden-tail]")?.textContent).toBe("s");
    expect(host.querySelector("[data-garden-mark]")?.textContent).toBe("c1");
    expect(host.querySelector("[data-garden-card]")?.textContent).toBe("c1");
    expect(host.querySelector("[data-core-card]")).toBeNull();
    expect(seen.taken).toBe(false);
  });

  it("let a bar take the composer's place, and give it back when its module goes", async () => {
    barTaken = true;
    registerModule(garden);
    act(() => root.render(<Chat />));
    await flush();
    expect(host.querySelector("[data-garden-bar]")).not.toBeNull();
    expect(seen.taken).toBe(true);
    act(() => void setModuleSwitchedOff("garden", true));
    await flush();
    expect(seen).toEqual({ action: undefined, taken: false });
    expect(host.innerHTML).toBe('<div><p data-core-card="true"></p></div>');
  });

  it("leave out a module a hub that publishes module flags does not serve", async () => {
    registerModule(garden);
    Object.assign(getState(), { loaded: true, features: [MODULES_FEATURE] });
    act(() => root.render(<Chat />));
    await flush();
    expect(host.innerHTML).toBe('<div><p data-core-card="true"></p></div>');
    await expect(loadModule("garden")).rejects.toThrow(/not enabled/);
  });

  it("leave out a module's part that throws, not the page", async () => {
    const broken: ModuleDescriptor = { ...garden, id: "yard", feature: undefined, chatSlots: ["chat.tail"], settingsTabs: [], cardRenderers: [], load: async () => ({ chat: { "chat.tail": () => { throw new Error("broken"); } } }) };
    registerModule(broken);
    const error = console.error;
    console.error = () => {};
    try {
      act(() => root.render(<ChatTail {...page} />));
      await flush();
    } finally {
      console.error = error;
    }
    expect(host.innerHTML).toBe("");
  });
});

describe("Settings categories", () => {
  it("never lets a module's category take a core one's key", () => {
    registerModule({ ...garden, settingsTabs: [{ ...garden.settingsTabs![0]!, key: "garden" }] });
    for (const k of TABS) expect(isModuleTab(k)).toBe(false);
  });

  it("list an enabled module's category in its group, open it by URL, find it, and draw its page", async () => {
    expect(isModuleTab("garden")).toBe(false);
    expect(settingsAt("?tab=garden").tab).toBeNull();
    registerModule(garden);
    expect(isModuleTab("garden")).toBe(true);
    expect(settingsAt("?tab=garden")).toEqual({ tab: "garden", sub: null, section: null });
    expect(navGroups().find((g) => g.key === "device")!.tabs.at(-1)).toBe("garden");
    expect(tabInfo("garden")).toEqual({ label: "Garden", icon: "heart", hint: "Plots and watering" });
    expect(searchSettings("plots").tabs).toEqual(["garden"]);
    act(() => root.render(<ModuleSettingsPage tab="garden" />));
    await flush();
    expect(host.querySelector("[data-garden-settings]")?.textContent).toBe("garden");
    act(() => void setModuleSwitchedOff("garden", true));
    expect(host.innerHTML).toBe("");
    expect(settingsAt("?tab=garden").tab).toBeNull();
  });

  it("find each module's switch in General › Optional features", () => {
    registerModule(garden);
    expect(searchSettings("watering").groups).toEqual([expect.objectContaining({ tab: "general", anchor: "configuration-features-garden" })]);
  });
});
