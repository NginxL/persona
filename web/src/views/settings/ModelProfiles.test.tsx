// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia;
});
vi.mock("../../lib/store", () => ({ onHubEvent: () => () => {} }));
import { initI18n, t } from "../../i18n";
import { modeTitle } from "../../lib/session-actions";
import { ModelProfiles } from "./ModelProfiles";
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
afterEach(() => vi.unstubAllGlobals());
it("switches profiles and exposes rollback", async () => {
 await initI18n();
 const writes: string[] = [];
 let active = "performance";
 vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
  const path = String(input);
  if (init?.method === "POST") { writes.push(path); active = path.includes("rollback") ? "performance" : "economy"; }
  return new Response(JSON.stringify({ revision: 1, active, profiles: ["performance", "economy"].map((id) => ({ id, name: id, active: id === active, changes: ["agent.new"], tiers: {} })), rollback: { to: "performance", changes: ["agent.new"] } }), { status: 200 });
 }));
 const host = document.createElement("div"); document.body.append(host); const root = createRoot(host);
 const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
 await act(async () => { root.render(<ModelProfiles onChanged={() => {}} />); }); await flush();
 const btn = (text: string, scope: ParentNode = document) => [...scope.querySelectorAll("button")].find((b) => b.textContent?.includes(text))!;
 act(() => btn("economy").click()); await flush();
 act(() => btn(t("settings-modelprofiles.apply"), document.querySelector('[role="dialog"]')!).click()); await flush();
 expect(writes).toEqual(["/api/v1/model-profiles/economy/apply"]);
 act(() => btn(t("settings-modelprofiles.rollback")).click()); await flush();
 expect(writes[1]).toBe("/api/v1/model-profiles/rollback");
 await act(async () => root.unmount()); host.remove();
});

it("shows apply failures inside the dialog and clears errors on cancel and close", async () => {
 await initI18n();
 const onChanged = vi.fn();
 vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
  if (init?.method === "POST") {
   expect(String(input)).toBe("/api/v1/model-profiles/economy/apply");
   return new Response(JSON.stringify({ error: "Provider is no longer available" }), { status: 400 });
  }
  return new Response(JSON.stringify({ revision: 1, profiles: ["economy", "performance"].map((id) => ({ id, name: id, changes: [], tiers: {} })) }), { status: 200 });
 }));
 const host = document.createElement("div"); document.body.append(host); const root = createRoot(host);
 const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
 const btn = (text: string, scope: ParentNode = document) => [...scope.querySelectorAll("button")].find((b) => b.textContent?.includes(text))!;
 try {
  await act(async () => { root.render(<ModelProfiles onChanged={onChanged} />); }); await flush();
  for (const close of ["cancel", "escape"]) {
   act(() => btn("economy", host).click()); await flush();
   expect(document.querySelector('[role="alert"]')).toBeNull();
   const dialog = document.querySelector('[role="dialog"]')!;
   act(() => btn(t("settings-modelprofiles.apply"), dialog).click()); await flush();
   expect(document.querySelector('[role="dialog"]')).toBe(dialog);
   expect(dialog.querySelector('[role="alert"]')?.textContent).toContain("Provider is no longer available");
   expect(document.querySelectorAll('[role="alert"]')).toHaveLength(1);
   expect(host.querySelector('[role="alert"]')).toBeNull();
   expect(onChanged).not.toHaveBeenCalled();
   act(() => {
    if (close === "cancel") btn(t("common.cancel"), dialog).click();
    else document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
   }); await flush();
   expect(document.querySelector('[role="dialog"]')).toBeNull();
   expect(document.querySelector('[role="alert"]')).toBeNull();
  }
  act(() => btn("performance", host).click()); await flush();
  expect(document.querySelector('[role="dialog"]')).not.toBeNull();
  expect(document.querySelector('[role="alert"]')).toBeNull();
 } finally {
  await act(async () => root.unmount()); host.remove();
 }
});

it("lists a profile's Agent presets and says when none is in use", async () => {
 await initI18n();
 vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ revision: 1, profiles: [{ id: "economy", name: "economy", changes: [],
  tiers: { review: { launcher: "fast", model: "m-review", effort: "high", description: "Code review" }, quick: { launcher: "claude" } } }] }), { status: 200 })));
 const host = document.createElement("div"); document.body.append(host); const root = createRoot(host);
 const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
 try {
  await act(async () => { root.render(<ModelProfiles onChanged={() => {}} />); }); await flush();
  expect(host.textContent).toContain(t("settings-modelprofiles.none_active"));
  act(() => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("economy"))!.click()); await flush();
  const rows = [...document.querySelectorAll('[data-testid="model-profile-tiers"] li')].map((li) => li.textContent);
  expect(rows).toEqual(["quickclaude", "reviewfast · m-review · highCode review"]);
 } finally {
  await act(async () => root.unmount()); host.remove();
 }
});

it("names a preset's launcher by the provider's display name, its ID beside it; an rc alias by its ID (#5029)", async () => {
 await initI18n();
 vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ revision: 1, profiles: [{ id: "economy", name: "economy", changes: [],
  tiers: { quick: { launcher: "cc_glm" }, review: { launcher: "relay-example-com", model: "glm-5", effort: "high" } } }] }), { status: 200 })));
 const providers = [{ id: "launcher.relay-example-com", name: "我的中转 站", kind: "launcher" as const }, { id: "launcher.cc_glm", name: "cc_glm", kind: "launcher" as const }];
 const host = document.createElement("div"); document.body.append(host); const root = createRoot(host);
 const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
 try {
  await act(async () => { root.render(<ModelProfiles providers={providers} onChanged={() => {}} />); }); await flush();
  act(() => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("economy"))!.click()); await flush();
  const rows = [...document.querySelectorAll('[data-testid="model-profile-tiers"] li')];
  expect(rows.map((li) => li.textContent)).toEqual(["quickcc_glm", "review我的中转 站 relay-example-com · glm-5 · high"]);
  expect([...rows[1].querySelectorAll("span span.font-mono")].map((s) => s.textContent)).toEqual(["relay-example-com"]);
  expect(rows[0].querySelectorAll("span span.font-mono")).toHaveLength(0);
 } finally {
  await act(async () => root.unmount()); host.remove();
 }
});

it("edits name, purposes, launchers, and tiers interactively via shared pickers and saves with revision", async () => {
 await initI18n("en");
 const puts: { path: string; body: Record<string, unknown> }[] = [];
 let revision = 3;
 let profileData: Record<string, unknown> = {
  id: "economy",
  name: "Economy",
  description: "Everyday models",
  active: true,
  changes: [],
  purposes: {
   assistant: { provider: "launcher.gw", model: "sonnet-4", effort: "medium" },
   kb: { provider: "launcher.api_fast", model: "fast-1", effort: "low" },
  },
  launchers: {
   gw: { model: "sonnet-4", effort: "medium" },
  },
  tiers: {
   standard: { launcher: "gw", model: "sonnet-4", effort: "medium", description: "Default coding" },
  },
 };
 const providers = [
  { id: "launcher.gw", name: "Team gateway", kind: "launcher" as const, agent: "claude", model: "sonnet-4" },
  { id: "launcher.other", name: "Other launcher", kind: "launcher" as const, agent: "claude", model: "opus-4" },
  { id: "launcher.api_fast", name: "Fast API", kind: "api" as const, api: "messages" as const, model: "fast-1" },
 ];
 const purposes = [
  {
   id: "assistant", category_id: "assistant", name: "Main chat", group: "Default", kind: "launcher" as const,
   override: {}, effective: { provider: "launcher.gw", model: "sonnet-4" }, revision: 1,
   fields: ["provider", "model", "effort", "permission_mode"], provider_ids: ["launcher.gw", "launcher.other"],
  },
  {
   id: "agent.new", category_id: "agent.new", name: "Agents", group: "Default", kind: "launcher" as const,
   override: {}, effective: { provider: "launcher.gw", model: "sonnet-4" }, revision: 1,
   fields: ["provider", "model", "effort", "permission_mode"], provider_ids: ["launcher.gw", "launcher.other"],
  },
  {
   id: "kb", category_id: "kb", name: "Information processing", group: "Default", kind: "api" as const,
   override: {}, effective: { provider: "launcher.api_fast", model: "fast-1" }, revision: 1,
   fields: ["provider", "model", "effort"], provider_ids: ["launcher.api_fast"],
  },
 ];
 vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
  const path = String(input).split("?")[0];
  if (path.endsWith("/model-profiles") && (!init?.method || init.method === "GET")) {
   return Response.json({ revision, active: "economy", profiles: [profileData] });
  }
  if (path.endsWith("/model-profiles/economy") && init?.method === "PUT") {
   const body = JSON.parse(String(init.body)) as Record<string, unknown>;
   puts.push({ path, body });
   revision++;
   profileData = { ...profileData, ...body, changes: ["assistant"] };
   return Response.json({ revision, active: "economy", profiles: [profileData] });
  }
  if (path.endsWith("/launchers") && (!init?.method || init.method === "GET")) {
   return Response.json({
    launchers: [
     { name: "gw", display_name: "Team gateway", source: "persona", kind: "launcher", agent: "claude", model: "sonnet-4" },
     { name: "other", display_name: "Other launcher", source: "persona", kind: "launcher", agent: "claude", model: "opus-4" },
     { name: "api_fast", display_name: "Fast API", source: "persona", kind: "api", api: "messages", model: "fast-1" },
    ],
   });
  }
  if (path.endsWith("/models")) {
   return Response.json({
    agent: "claude",
    models: [
     { id: "sonnet-4", name: "Sonnet 4", default: true, efforts: ["low", "medium", "high"] },
     { id: "opus-4", name: "Opus 4", efforts: ["medium", "high", "max"] },
    ],
   });
  }
  if (path.endsWith("/launchers/discover")) {
   return Response.json({ models: [{ id: "fast-1", efforts: ["off", "low", "high"] }, { id: "fast-2", efforts: ["off", "low", "high"] }] });
  }
  throw new Error(`Unexpected request: ${init?.method ?? "GET"} ${path}`);
 }));
 const onChanged = vi.fn();
 const host = document.createElement("div"); document.body.append(host); const root = createRoot(host);
 const flush = async () => { for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); }); };
 const setVal = (el: HTMLInputElement | HTMLSelectElement, value: string) => {
  const proto = el instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
  act(() => {
   Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
   el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? "change" : "input", { bubbles: true }));
  });
 };
 const btn = (text: string, scope: ParentNode = document) => [...scope.querySelectorAll("button")].find((b) => b.textContent?.trim() === text)!;
 try {
  await act(async () => { root.render(<ModelProfiles providers={providers} purposes={purposes} onChanged={onChanged} />); });
  await flush();
  // Footer no longer tells users they must use the hub API to edit profiles.
  expect(host.textContent).not.toContain("hub API");
  act(() => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("Economy"))!.click());
  await flush();
  const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')!;
  // Structured inspect view replaces raw JSON <pre>.
  expect(dialog().querySelector("pre")).toBeNull();
  expect(dialog().querySelector('[data-testid="model-profile-purposes"]')?.textContent).toContain("Team gateway");
  expect(dialog().querySelector('[data-testid="model-profile-launchers"]')?.textContent).toContain("Team gateway");

  // Open interactive editor (lazy-loaded).
  await act(async () => btn(t("settings-modelprofiles.edit"), dialog()).click());
  await vi.waitFor(() => {
   expect(dialog().querySelector('[data-testid="model-profile-editor"]')).not.toBeNull();
  });
  await flush();
  // Active profile save-vs-apply note is shown.
  expect(dialog().textContent).toContain(t("settings-modelprofiles.save_note_active"));

  // 1) Update profile name.
  setVal(dialog().querySelector<HTMLInputElement>(`input[aria-label="${t("settings-modelprofiles.name")}"]`)!, "Economy Plus");

  // 2) Change assistant model via shared ModelPicker and set permission_mode.
  const assistantCard = dialog().querySelector<HTMLElement>('[data-testid="profile-purpose-assistant"]')!;
  await vi.waitFor(() => {
   const picker = [...assistantCard.querySelectorAll<HTMLButtonElement>("button[aria-expanded]")].find((b) => b.textContent?.includes("Sonnet 4"));
   expect(picker).toBeDefined();
  });
  const pickerBtn = [...assistantCard.querySelectorAll<HTMLButtonElement>("button[aria-expanded]")].find((b) => b.textContent?.includes("Sonnet 4"))!;
  await act(async () => pickerBtn.click());
  await flush();
  await act(async () => [...assistantCard.querySelectorAll<HTMLButtonElement>('[role="option"]')].find((b) => b.textContent?.includes("Opus 4"))!.click());
  await flush();
  setVal(assistantCard.querySelector<HTMLSelectElement>("select[data-permission-mode-select]")!, "acceptEdits");
  await flush();

  // 3) Change kb model via Direct API discovered select (never shows permission_mode).
  const kbCard = dialog().querySelector<HTMLElement>('[data-testid="profile-purpose-kb"]')!;
  expect(kbCard.querySelector("select[data-permission-mode-select]")).toBeNull();
  await vi.waitFor(() => {
   const sel = [...kbCard.querySelectorAll<HTMLSelectElement>("select")].find((s) => [...s.options].some((o) => o.value === "fast-2"));
   expect(sel).toBeDefined();
  });
  const kbModelSelect = [...kbCard.querySelectorAll<HTMLSelectElement>("select")].find((s) => [...s.options].some((o) => o.value === "fast-2"))!;
  setVal(kbModelSelect, "fast-2");
  await flush();

  // 4) Clear small_model on launcher "gw" and add managed launcher "other".
  const gwCard = dialog().querySelector<HTMLElement>('[data-testid="profile-launcher-gw"]')!;
  const clearButtons = [...gwCard.querySelectorAll<HTMLButtonElement>("button")].filter((b) => b.textContent?.trim() === t("settings-modelprofiles.clear_on_switch"));
  await act(async () => clearButtons[0].click());
  await flush();
  const addLauncherSelect = dialog().querySelector<HTMLSelectElement>(`select[aria-label="${t("settings-modelprofiles.add_launcher")}"]`)!;
  setVal(addLauncherSelect, "other");
  await act(async () => btn(t("settings-modelprofiles.add_launcher"), dialog()).click());
  await flush();

  // 5) Add a new Agent preset (tier) with a permission_mode.
  await act(async () => btn(t("settings-modelprofiles.add_tier"), dialog()).click());
  await flush();
  const newTier = [...dialog().querySelectorAll<HTMLElement>('[data-testid^="profile-tier-edit-"]')].at(-1)!;
  setVal(newTier.querySelector<HTMLInputElement>(`input[aria-label="${t("settings-modelprofiles.tier_name")}"]`)!, "fast");
  setVal(newTier.querySelector<HTMLSelectElement>(`select[aria-label="${t("settings-modelprofiles.tier_launcher")}"]`)!, "other");
  await flush();
  setVal(newTier.querySelector<HTMLSelectElement>("select[data-permission-mode-select]")!, "plan");
  await flush();
  setVal(newTier.querySelector<HTMLInputElement>(`input[aria-label="${t("settings-modelprofiles.tier_description")}"]`)!, "Quick mechanical tasks");
  await flush();

  // 6) Save profile -> sends PUT with revision 3 and never exposes/sends secrets.
  await act(async () => btn(t("settings-modelprofiles.save"), dialog()).click());
  await flush();

  expect(puts).toHaveLength(1);
  expect(puts[0].path).toBe("/api/v1/model-profiles/economy");
  expect(puts[0].body).toMatchObject({
   revision: 3,
   name: "Economy Plus",
   description: "Everyday models",
   purposes: {
    assistant: { provider: "launcher.gw", model: "opus-4", effort: "medium", permission_mode: "acceptEdits" },
    kb: { provider: "launcher.api_fast", model: "fast-2", effort: "low" },
   },
   launchers: {
    gw: { model: "sonnet-4", small_model: "", effort: "medium" },
    other: { model: "opus-4" },
   },
   tiers: {
    standard: { launcher: "gw", model: "sonnet-4", effort: "medium", description: "Default coding" },
    fast: { launcher: "other", permission_mode: "plan", description: "Quick mechanical tasks" },
   },
  });
  expect(JSON.stringify(puts[0].body)).not.toMatch(/api_key|token|secret/i);
  expect(onChanged).toHaveBeenCalledTimes(1);
  // Returns to inspect view with active-drift hint so user can Apply profile if desired.
  expect(dialog().querySelector('[data-testid="model-profile-editor"]')).toBeNull();
  expect(dialog().textContent).toContain(t("settings-modelprofiles.active_drift"));
 } finally {
  await act(async () => root.unmount()); host.remove();
 }
});

it("keeps edits on 409 conflict and allows reloading latest revision", async () => {
 await initI18n("en");
 let revision = 1;
 let failConflict = true;
 const puts: Record<string, unknown>[] = [];
 vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
  const path = String(input).split("?")[0];
  if (path.endsWith("/model-profiles") && (!init?.method || init.method === "GET")) {
   return Response.json({
    revision,
    active: "economy",
    profiles: [{ id: "economy", name: revision === 1 ? "Economy" : "Economy v2", description: "", active: true, changes: [], tiers: {} }],
   });
  }
  if (path.endsWith("/model-profiles/economy") && init?.method === "PUT") {
   const body = JSON.parse(String(init.body)) as Record<string, unknown>;
   puts.push(body);
   if (failConflict) return Response.json({ error: "conflict" }, { status: 409 });
   revision++;
   return Response.json({
    revision,
    active: "economy",
    profiles: [{ id: "economy", name: String(body.name), description: "", active: true, changes: [], tiers: {} }],
   });
  }
  if (path.endsWith("/launchers")) return Response.json({ launchers: [] });
  if (path.endsWith("/model-config")) return Response.json({ purposes: [], providers: [] });
  return Response.json({ error: "not found" }, { status: 404 });
 }));
 const host = document.createElement("div"); document.body.append(host); const root = createRoot(host);
 const flush = async () => { for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); }); };
 const setVal = (el: HTMLInputElement, value: string) => {
  act(() => {
   Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
   el.dispatchEvent(new Event("input", { bubbles: true }));
  });
 };
 const btn = (text: string, scope: ParentNode = document) => [...scope.querySelectorAll("button")].find((b) => b.textContent?.trim() === text)!;
 try {
  await act(async () => { root.render(<ModelProfiles providers={[]} purposes={[]} onChanged={() => {}} />); });
  await flush();
  act(() => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("Economy"))!.click());
  await flush();
  const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')!;
  await act(async () => btn(t("settings-modelprofiles.edit"), dialog()).click());
  await vi.waitFor(() => {
   expect(dialog().querySelector('[data-testid="model-profile-editor"]')).not.toBeNull();
  });
  await flush();

  const nameInput = () => dialog().querySelector<HTMLInputElement>(`input[aria-label="${t("settings-modelprofiles.name")}"]`)!;
  setVal(nameInput(), "My Draft");
  await act(async () => btn(t("settings-modelprofiles.save"), dialog()).click());
  await flush();

  expect(dialog().textContent).toContain(t("settings-modelprofiles.conflict"));
  expect(nameInput().value).toBe("My Draft");
  expect(btn(t("settings-modelprofiles.save"), dialog()).disabled).toBe(true);

  revision = 2;
  failConflict = false;
  await act(async () => btn(t("settings-modelprofiles.reload_discard"), dialog()).click());
  await flush();
  expect(nameInput().value).toBe("Economy v2");

  setVal(nameInput(), "Economy Final");
  await act(async () => btn(t("settings-modelprofiles.save"), dialog()).click());
  await flush();
  expect(puts.map((b) => b.revision)).toEqual([1, 2]);
  expect(dialog().querySelector('[data-testid="model-profile-editor"]')).toBeNull();
 } finally {
  await act(async () => root.unmount()); host.remove();
 }
});

it("displays a preset's and purpose's permission_mode using the shared mode label", async () => {
 await initI18n("en");
 vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ revision: 1, profiles: [{ id: "safe", name: "Safe", changes: [],
  purposes: { assistant: { provider: "launcher.claude", permission_mode: "acceptEdits" } },
  tiers: { review: { launcher: "claude", model: "sonnet", effort: "high", permission_mode: "plan", description: "Read only review" } } }] }), { status: 200 })));
 const host = document.createElement("div"); document.body.append(host); const root = createRoot(host);
 const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
 try {
  await act(async () => { root.render(<ModelProfiles onChanged={() => {}} />); }); await flush();
  act(() => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("Safe"))!.click()); await flush();
  const tierRows = [...document.querySelectorAll('[data-testid="model-profile-tiers"] li')].map((li) => li.textContent);
  expect(tierRows[0]).toContain(modeTitle("plan"));
  const purposeRows = [...document.querySelectorAll('[data-testid="model-profile-purposes"] li')].map((li) => li.textContent);
  expect(purposeRows[0]).toContain(modeTitle("acceptEdits"));
 } finally {
  await act(async () => root.unmount()); host.remove();
 }
});
