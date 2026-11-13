// Switching a running Claude Code Agent to another launcher of its engine (#5035, docs/web/web.md "Model pill and model
// panel"): on a hub with `launcher_switch` the other launchers' models are picked like the session's own, a pick there is
// one POST …/modes {launcher, model?, effort?} that the next message carries out, the pending switch is the panel's
// `pending` and its warning note, picking the session's own launcher drops it first; another engine, a Codex thread and
// an older hub stay as before. Also what the pill says of the pending switch, and which send refusals are the switch's.
import { describe, expect, it } from "vitest";
import { t } from "../i18n";
import { ApiError } from "./api";
import type { LauncherModels } from "./launcher-models";
import { pendingView, switchRefused, type ModelList } from "./session-models";
import { pickPlan, sessionPanel, type PanelOptions } from "./session-panel";
import type { Launcher, Session } from "./types";

const ALL = ["low", "medium", "high", "xhigh", "max"];
const claudeList: LauncherModels = {
  agent: "claude",
  models: [
    { id: "default", name: "Default (recommended)", default: true, resolved: "claude-opus-5-5", efforts: ALL },
    { id: "opus", name: "Opus 5.5", resolved: "claude-opus-5-5", efforts: ALL },
    { id: "haiku", name: "Haiku 5.5", resolved: "claude-haiku-5-5", efforts: ["low", "high"] },
  ],
};
const workList: LauncherModels = {
  agent: "claude",
  models: [
    { id: "default", name: "Default (recommended)", default: true, resolved: "glm-5.3", efforts: ALL },
    { id: "glm-5.3", name: "GLM 5.3", efforts: ["low", "high"] },
  ],
};
const lockedList: LauncherModels = { ...claudeList, locked: "--model" };
const codexList: LauncherModels = { agent: "codex", models: [{ id: "gpt-5.5", name: "GPT-5.5", default: true }] };
const launchers: Launcher[] = [{ name: "claude" }, { name: "work" }, { name: "pinned" }, { name: "cx", agent: "codex" }];
const read: Record<string, ModelList> = { claude: { list: claudeList }, work: { list: workList }, pinned: { list: lockedList }, cx: { list: codexList } };
const listOf = (name: string) => read[name] ?? {};
const raw = (name: string) => read[name]?.list;

const session = (x: Partial<Session> = {}): Session =>
  ({ machine_id: "mac", session_id: "s1", title: "t", cwd: "/code", status: "idle", state: "idle", agent: "claude", launcher: "claude", model: "claude-opus-5-5", effort: "high", ...x }) as Session;
const on: PanelOptions = { switching: true };
const provider = (s: Session, id: string, o: PanelOptions = on) => sessionPanel(s, launchers, listOf, o).providers.find((p) => p.id === id)!;

describe("a Codex session that names no launcher (#6501)", () => {
  // the computer runs the Codex App: the node offers its launcher, whose list is empty and locked (the App picks the model)
  const appList: LauncherModels = { agent: "codex", models: [], locked: "the App picks" };
  const withApp: Launcher[] = [...launchers, { name: "codex-app", agent: "codex", source: "codex_app" }];
  const readApp = (name: string): ModelList => (name === "codex-app" ? { list: appList } : listOf(name));
  const chat = session({ agent: "codex", source_kind: "codex_history", space_kind: "codex_app", agent_surface: "desktop", launcher: undefined,
    capabilities: { send: true }, model: "gpt-6-astra", effort: "ultra", efforts: ["low", "ultra"] });

  it("is the Codex App's for one of its chats: its own provider, never Claude Code's models", () => {
    const panel = sessionPanel(chat, withApp, readApp, on);
    const own = panel.providers.find((p) => p.engine === "codex" && p.id === "codex-app")!;
    expect(own).toBeDefined();
    expect(own.models?.some((m) => m.id === "opus" || m.id === "default")).toBeFalsy();
    // Claude Code's launcher is another engine's, not this chat's own
    expect(panel.providers.find((p) => p.id === "claude")!.unavailable).toBe(t("chat-modelpanel.other_engine", { from: "Codex", to: "Claude Code" }));
  });
});

describe("another launcher of the engine", () => {
  it("is picked like the session's own on a hub that switches, and stays off on an older one", () => {
    const work = provider(session(), "work");
    expect(work.unavailable).toBeUndefined();
    expect(work.locked).toBeUndefined();
    expect(work.typable).toBe(true);
    expect(work.models!.map((m) => m.id)).toEqual(["default", "glm-5.3"]);
    expect(provider(session(), "work", {}).unavailable).toBe(t("chat-modelpanel.other_provider"));
    // the models that switch at once are still the session's own launcher's
    expect(sessionPanel(session(), launchers, listOf, on).state.recommended).toEqual(["claude"]);
  });

  it("another engine, a Codex thread and an agent that can't switch its model keep their reasons", () => {
    expect(provider(session(), "cx").unavailable).toBe(t("chat-modelpanel.other_engine", { from: "Claude Code", to: "Codex" }));
    const codex = session({ agent: "codex", source_kind: "codex_app_server", launcher: "cx", capabilities: { send: true }, model: "gpt-5.5", efforts: ["low", "high"] });
    expect(provider(codex, "cx").unavailable).toBe(t("chat-modelpanel.model_fixed", { agent: "Codex" }));
    expect(provider(codex, "work").unavailable).toBe(t("chat-modelpanel.other_engine", { from: "Codex", to: "Claude Code" }));
    expect(provider(session({ status: "offline", state: "offline" }), "work").unavailable).toBe(t("chat-modelpanel.other_provider"));
  });

  it("one that fixes its own model offers what it runs by default, only", () => {
    const pinned = provider(session(), "pinned");
    expect(pinned.models!.map((m) => m.id)).toEqual(["default"]);
    expect(pinned.locked).toBeUndefined();
    expect(pinned.typable).toBeUndefined();
  });
});

describe("a switch that waits for the next message", () => {
  it("is the panel's pending setup, with the warning note; none for the session's own launcher", () => {
    const { state } = sessionPanel(session({ pending_switch: { launcher: "work", model: "glm-5.3", effort: "low", at: "2026-11-12T10:00:00Z" } }), launchers, listOf, on);
    expect(state.pending).toEqual({ provider: "work", model: "glm-5.3", effort: "low" });
    expect(state.note).toBe(t("chat-modelpanel.pending_note", { provider: "work" }));
    // no model picked: the launcher's default entry
    expect(sessionPanel(session({ pending_switch: { launcher: "work" } }), launchers, listOf, on).state.pending).toEqual({ provider: "work", model: "default" });
    const none = sessionPanel(session({ pending_switch: { launcher: "claude" } }), launchers, listOf, on).state;
    expect(none.pending).toBeUndefined();
    expect(none.note).toBeUndefined();
  });

  it("the pill names it: the launcher, its list's name for the model (the default when none was picked), the effort", () => {
    expect(pendingView(session({ pending_switch: { launcher: "work", model: "glm-5.3" } }), listOf)).toEqual({ engine: "claude", provider: "work", model: "GLM 5.3", effort: "high" });
    expect(pendingView(session({ pending_switch: { launcher: "work", effort: "low" } }), listOf)).toEqual({ engine: "claude", provider: "work", model: "Default (recommended)", effort: "low" });
    // its list not read: a short form of the ID, else "Default model"
    expect(pendingView(session({ pending_switch: { launcher: "relay", model: "claude-sonnet-5-5" } }), () => ({}))!.model).toBe("sonnet 5.5");
    expect(pendingView(session({ pending_switch: { launcher: "relay" } }), () => ({}))!.model).toBe(t("chat-modelpill.default_model"));
    expect(pendingView(session(), listOf)).toBeNull();
    expect(pendingView(session({ pending_switch: { launcher: "claude" } }), listOf)).toBeNull();
  });
});

describe("what a pick on another launcher sends", () => {
  it("one request recording the switch: the model unless it is that launcher's default, the effort when the setup has one", () => {
    expect(pickPlan(session(), { provider: "work", model: "glm-5.3", effort: "low" }, raw, on)).toEqual([{ launcher: "work", from: "claude", model: "glm-5.3", effort: "low" }]);
    expect(pickPlan(session(), { provider: "work", model: "default" }, raw, on)).toEqual([{ launcher: "work", from: "claude" }]);
    // a model ID typed by hand goes as it is; one the hub would refuse doesn't go
    expect(pickPlan(session(), { provider: "work", model: "~vendor/next" }, raw, on)).toEqual([{ launcher: "work", from: "claude", model: "~vendor/next" }]);
    expect(pickPlan(session(), { provider: "work", model: "a;b" }, raw, on)).toBeNull();
    // the launcher that fixes its model takes its default only
    expect(pickPlan(session(), { provider: "pinned", model: "default" }, raw, on)).toEqual([{ launcher: "pinned", from: "claude" }]);
    expect(pickPlan(session(), { provider: "pinned", model: "haiku" }, raw, on)).toBeNull();
  });

  it("nothing on an older hub, to another engine, from a Codex thread, or when that very switch waits already", () => {
    expect(pickPlan(session(), { provider: "work", model: "glm-5.3" }, raw, {})).toBeNull();
    expect(pickPlan(session(), { provider: "cx", model: "gpt-5.5" }, raw, on)).toBeNull();
    const codex = session({ agent: "codex", source_kind: "codex_app_server", launcher: "cx", capabilities: { send: true }, model: "gpt-5.5" });
    expect(pickPlan(codex, { provider: "work", model: "glm-5.3" }, raw, on)).toBeNull();
    const waiting = session({ pending_switch: { launcher: "work", model: "glm-5.3", effort: "low" } });
    expect(pickPlan(waiting, { provider: "work", model: "glm-5.3", effort: "low" }, raw, on)).toEqual([]);
    expect(pickPlan(waiting, { provider: "work", model: "glm-5.3", effort: "high" }, raw, on)).toEqual([{ launcher: "work", from: "claude", model: "glm-5.3", effort: "high" }]);
  });

  it("the session's own launcher while a switch waits: drops it first, then the usual steps", () => {
    const waiting = session({ pending_switch: { launcher: "work" } });
    expect(pickPlan(waiting, { provider: "claude", model: "opus", effort: "high" }, raw, on)).toEqual([{ launcher: "claude", from: "claude" }]);
    expect(pickPlan(waiting, { provider: "claude", model: "haiku", effort: "low" }, raw, on)).toEqual([{ launcher: "claude", from: "claude" }, { model: "haiku" }, { effort: "low" }]);
    expect(pickPlan(session(), { provider: "claude", model: "haiku" }, raw, on)).toEqual([{ model: "haiku" }]);
  });
});

describe("a message the switch refused", () => {
  it("is a 409 with a switch_ code; others are not", () => {
    expect(switchRefused(new ApiError(409, "gone", { code: "switch_launcher_missing", delivered: "none" }))).toBe(true);
    expect(switchRefused(new ApiError(409, "failed", { code: "switch_failed_stopped", delivered: "none" }))).toBe(true);
    expect(switchRefused(new ApiError(409, "draft", { code: "input_draft", delivered: "none" }))).toBe(false);
    expect(switchRefused(new Error("x"))).toBe(false);
  });
});

// Moving a running Agent to the other engine (#5054): on a hub with `engine_switch` a Claude Code or Codex Agent in a
// terminal picks the other engine's launchers like another launcher of its own; without it, or for a session that can't
// (a Codex app thread, an assistant chat, a stopped one), the reason stays.
describe("a launcher of the other engine", () => {
  const engine: PanelOptions = { switching: true, engineSwitch: true };
  const terminalCodex = (x: Partial<Session> = {}) => session({ agent: "codex", launcher: "cx", model: "gpt-5.5", effort: "medium", pane_id: "w1:p2", ...x });

  it("is picked on a hub that moves the conversation there, and needs a new conversation without it", () => {
    const cx = provider(session(), "cx", engine);
    expect(cx.unavailable).toBeUndefined();
    expect(cx.models!.map((m) => m.id)).toEqual(["gpt-5.5"]);
    expect(provider(session(), "cx", on).unavailable).toBe(t("chat-modelpanel.other_engine", { from: "Claude Code", to: "Codex" }));
    // the other engine's launchers never join Recommended: those switch at once
    expect(sessionPanel(session(), launchers, listOf, engine).state.recommended).toEqual(["claude"]);
  });

  it("a Codex Agent in a terminal picks Claude Code's launchers; its own rows keep their reason", () => {
    const s = terminalCodex();
    expect(provider(s, "claude", engine).unavailable).toBeUndefined();
    expect(provider(s, "work", engine).unavailable).toBeUndefined();
    expect(provider(s, "cx", engine).unavailable).toBe(t("chat-modelpanel.model_fixed", { agent: "Codex" }));
    // without the feature: as before
    expect(provider(s, "claude", on).unavailable).toBe(t("chat-modelpanel.other_engine", { from: "Codex", to: "Claude Code" }));
  });

  it("a Codex app thread, a session not in a terminal, an assistant chat and a stopped Agent keep the reason", () => {
    const reason = t("chat-modelpanel.other_engine", { from: "Codex", to: "Claude Code" });
    expect(provider(terminalCodex({ source_kind: "codex_app_server", capabilities: { send: true } }), "claude", engine).unavailable).toBe(reason);
    expect(provider(terminalCodex({ pane_id: undefined }), "claude", engine).unavailable).toBe(reason);
    expect(provider(terminalCodex({ status: "offline", state: "offline" }), "claude", engine).unavailable).toBe(reason);
    expect(provider(session({ assistant: true }), "cx", engine).unavailable).toBe(t("chat-modelpanel.other_engine", { from: "Claude Code", to: "Codex" }));
  });

  it("a pick sends one request recording the switch, as for another launcher", () => {
    expect(pickPlan(session(), { provider: "cx", model: "gpt-5.5" }, raw, engine)).toEqual([{ launcher: "cx", from: "claude" }]);
    expect(pickPlan(session(), { provider: "cx", model: "gpt-5.5-mini", effort: "low" }, raw, engine)).toEqual([{ launcher: "cx", from: "claude", model: "gpt-5.5-mini", effort: "low" }]);
    expect(pickPlan(terminalCodex(), { provider: "claude", model: "opus", effort: "high" }, raw, engine)).toEqual([{ launcher: "claude", from: "cx", model: "opus", effort: "high" }]);
    // without the feature: nothing
    expect(pickPlan(session(), { provider: "cx", model: "gpt-5.5" }, raw, on)).toBeNull();
    // the very same switch waiting already: nothing to send
    const waiting = session({ pending_switch: { launcher: "cx", agent: "codex" } });
    expect(pickPlan(waiting, { provider: "cx", model: "gpt-5.5" }, raw, engine)).toEqual([]);
  });

  it("knows the engine of a launcher whose list isn't read from the panel's providers", () => {
    const unread = (name: string) => (name === "relay" ? undefined : raw(name));
    const engineOf = (name: string) => (name === "relay" ? "codex" : undefined);
    expect(pickPlan(session(), { provider: "relay", model: "gpt-5.5" }, unread, { ...engine, engineOf })).toEqual([{ launcher: "relay", from: "claude", model: "gpt-5.5" }]);
    expect(pickPlan(session(), { provider: "relay", model: "gpt-5.5" }, unread, { engineSwitch: true, engineOf })).toEqual([{ launcher: "relay", from: "claude", model: "gpt-5.5" }]);
    // nobody knows: taken as a launcher of its own engine (none to switch to without `launcher_switch`)
    expect(pickPlan(session(), { provider: "relay", model: "gpt-5.5" }, unread, { engineSwitch: true })).toBeNull();
  });

  it("waiting: Cindy's warning on top, the pill names the engine with that model's own effort", () => {
    const waiting = session({ pending_switch: { launcher: "cx", agent: "codex", at: "2026-11-12T10:00:00Z" } });
    const { state } = sessionPanel(waiting, launchers, listOf, engine);
    expect(state.note).toBe(t("chat-modelpanel.engine_note"));
    expect(state.pending).toEqual({ provider: "cx", model: "gpt-5.5" });
    // reopened, the panel stands on what runs now
    expect(state.current).toEqual({ provider: "claude", model: "opus", effort: "high" });
    const withEffort: Record<string, ModelList> = { ...read, cx: { list: { agent: "codex", models: [{ id: "gpt-5.5", name: "GPT-5.5", default: true, default_effort: "medium" }] } } };
    expect(pendingView(waiting, (n) => withEffort[n] ?? {})).toEqual({ engine: "codex", provider: "cx", model: "5.5", effort: "medium" });
    // no effort of Claude Code's carried over when the model has none to say
    expect(pendingView(waiting, listOf)).toEqual({ engine: "codex", provider: "cx", model: "5.5" });
    expect(pendingView(session({ pending_switch: { launcher: "cx", agent: "codex", effort: "low" } }), listOf)!.effort).toBe("low");
  });

  it("is dropped by a pick of what runs now: Claude Code's own model, or a Codex Agent's, which has no other pick", () => {
    const waiting = session({ pending_switch: { launcher: "cx", agent: "codex" } });
    expect(pickPlan(waiting, { provider: "claude", model: "opus", effort: "high" }, raw, engine)).toEqual([{ launcher: "claude", from: "claude" }]);
    const codexWaiting = terminalCodex({ pending_switch: { launcher: "claude", model: "opus", agent: "claude" } });
    expect(pickPlan(codexWaiting, { provider: "cx", model: "gpt-5.5", effort: "medium" }, raw, engine)).toEqual([{ launcher: "cx", from: "cx" }]);
    expect(sessionPanel(codexWaiting, launchers, listOf, engine).state.note).toBe(t("chat-modelpanel.engine_note"));
  });
});
