// What an Agent's model pill and panel read (#5032, docs/web/web.md "Model pill and model panel"): the launchers of the
// computer it runs on (the panel's providers), each with the models its agent lists in its own /model menu
// (lib/launcher-models.ts), and what the pill says. The panel's own data and what a pick sends are built from these in
// lib/session-panel.ts, which loads with the panel.
//
// A list read from the agent takes seconds the first time, so what was read is kept per computer and launcher for the
// page's life: the pill and a later opening show the last list at once, and an opening reads every list again behind it
// (the session's own first, at most three at a time).
import { useSyncExternalStore } from "react";
import { t } from "../i18n";
import { ApiError, credentialOwner, errorText, onCredentialChange } from "./api";
import { agentId, sessionAgent } from "./agent-kind";
import { modelShort } from "./context-usage";
import { sessionCan, stateOf } from "./format";
import { runsAssistant, type AssistantLauncher } from "./assistant-chat";
import { SUPERVISE_PURPOSE } from "./background-supervisor";
import {
  defaultModel, forgetCustomModel, launcherModels, listedModel, machineLaunchers, modelName, withoutCustom, type AgentModel, type LauncherDefault, type LauncherModels, type UnavailableLauncher,
} from "./launcher-models";
import { canSwitchModel, isCurrentModel, sessionLauncher } from "./model-switch";
import { hasEffortOnly, hasModes } from "./session-actions";
import type { Launcher, Session } from "./types";
import { chosenManager, defaultKnown, defaultManager, onDefaultManager, onManagerChoice } from "./manager-choice";

/** One launcher's list as read: `list` undefined until a read answered, null from an older hub (it has no list). */
export interface ModelList {
  list?: LauncherModels | null;
  /** A read is on its way. */
  loading?: boolean;
  /** The last read failed and there is no list to show. */
  error?: string;
}

// ── what was read, kept for the page's life ──────────────────────────────────

const lists = new Map<string, ModelList>();
const machines = new Map<string, { launchers?: Launcher[]; loading?: boolean }>();
const reads = new Map<string, { promise: Promise<void>; hidden: Set<string> }>();
const machineReads = new Map<string, Promise<void>>();
// Typed IDs belong to the launcher in both catalogs. Each read keeps the deletions it overlapped even after they finish.
type ModelForget = { restore: Map<string, (now: LauncherModels) => LauncherModels>; promise: Promise<string> };
const forgetting = new Map<string, Map<string, ModelForget>>();
let version = 0;
const subscribers = new Set<() => void>();
const changed = () => {
  version++;
  for (const f of subscribers) f();
};
const subscribe = (f: () => void) => {
  subscribers.add(f);
  return () => void subscribers.delete(f);
};
type CatalogScope = { owner: string; manager?: string };
const catalogScope = (purpose?: "assistant"): CatalogScope => ({ owner: credentialOwner(),
  manager: (purpose === "assistant" ? chosenManager() : null) ?? (defaultKnown() ? defaultManager() : undefined) });
const listKey = (mid: string, launcher: string, purpose?: "assistant", scope = catalogScope(purpose)) =>
  JSON.stringify([scope.owner, scope.manager ?? "", mid, launcher, purpose ?? ""]);
const machineKey = (mid: string, scope = catalogScope()) => JSON.stringify([scope.owner, scope.manager ?? "", mid]);

/** Changes when the assistant's catalog must be loaded again, including when the default manager becomes known. */
export const assistantCatalogScope = (): string => JSON.stringify(catalogScope("assistant"));
export const agentCatalogScope = (): string => JSON.stringify(catalogScope());
onManagerChoice(changed);
onDefaultManager(changed);
onCredentialChange(changed);

/** Draws again whenever a list or a computer's launchers change. */
export function useModelLists(): number {
  return useSyncExternalStore(subscribe, () => version, () => version);
}

export const modelList = (mid: string, launcher: string, purpose?: "assistant"): ModelList => lists.get(listKey(mid, launcher, purpose)) ?? {};

/** The launchers of `mid` as last read; undefined before the first answer. */
export const machineLauncherList = (mid: string): Launcher[] | undefined => machines.get(machineKey(mid))?.launchers;

/** What a Persona chat's pill and panel read of its computer (`?purpose=assistant`, #5033): the launchers that can run
 *  assistant chats, the main chat's default (Model configuration › Main chat), and what can't run them, with why. */
export interface AssistantLaunchers {
  launchers: AssistantLauncher[];
  def?: LauncherDefault | null;
  unavailable: UnavailableLauncher[];
}

const assistantMachines = new Map<string, { info?: AssistantLaunchers; loading?: boolean; error?: string }>();
const assistantReads = new Map<string, Promise<void>>();

/** The Persona chats' launchers of `mid` as last read (`info` undefined before the first answer), plus the in-flight
 *  read's identity for a caller that must know whether its refresh joined that exact request. */
export const assistantLaunchers = (mid: string): { info?: AssistantLaunchers; loading?: boolean; error?: string; read?: Promise<void> } => ({
  ...(assistantMachines.get(machineKey(mid, catalogScope("assistant"))) ?? {}),
  read: assistantReads.get(machineKey(mid, catalogScope("assistant"))),
});

const superviseDefaults = new Map<string, LauncherDefault | null>();
const superviseReads = new Map<string, Promise<void>>();

/** What the background supervisor's own model purpose resolves to on `mid` (`?purpose=assistant.supervise`: Main chat's
 *  default until it is set apart); undefined before the first answer, null when the hub gave none. */
export const superviseDefault = (mid: string): LauncherDefault | null | undefined => superviseDefaults.get(machineKey(mid, catalogScope("assistant")));

/** Reads it: once, or `again` (a push changed the conversation, an opening of the panel). */
export function readSuperviseDefault(mid: string, o: { again?: boolean } = {}): Promise<void> {
  const scope = catalogScope("assistant");
  const key = machineKey(mid, scope);
  const reading = superviseReads.get(key);
  if (reading) return reading;
  if (superviseDefaults.has(key) && !o.again) return Promise.resolve();
  const read = machineLaunchers(mid, { purpose: SUPERVISE_PURPOSE, manager: scope.manager }).then(
    (r) => void superviseDefaults.set(key, r.default ?? null),
    () => undefined, // the one shown stays; none shown: Main chat's default is the closest thing known
  ).finally(() => {
    superviseReads.delete(key);
    changed();
  });
  superviseReads.set(key, read);
  return read;
}

/** Reads them: once (a pill that needs the default), or `again` behind the ones shown (an opening of the panel). */
export function readAssistantLaunchers(mid: string, o: { again?: boolean } = {}): Promise<void> {
  const scope = catalogScope("assistant");
  const key = machineKey(mid, scope);
  const reading = assistantReads.get(key);
  if (reading) return reading;
  const had = assistantMachines.get(key);
  if (had?.info && !o.again) return Promise.resolve();
  assistantMachines.set(key, { info: had?.info, loading: true });
  changed();
  const read = machineLaunchers(mid, { purpose: "assistant", manager: scope.manager }).then(
    (r) => void assistantMachines.set(key, { info: { launchers: (r.launchers ?? []).filter(runsAssistant), def: r.default, unavailable: r.unavailable ?? [] } }),
    // the ones shown stay; with none, say why
    (e) => void assistantMachines.set(key, had?.info ? { info: had.info } : { error: errorText(e) }),
  ).finally(() => {
    assistantReads.delete(key);
    changed();
  });
  assistantReads.set(key, read);
  return read;
}

// at most three lists are read at a time (each may start the agent's CLI on the computer); `first` jumps the queue
const MAX_READS = 3;
const queue: (() => void)[] = [];
let running = 0;
function slot(first: boolean): Promise<() => void> {
  return new Promise((go) => {
    const start = () => {
      running++;
      go(() => {
        running--;
        queue.shift()?.();
      });
    };
    if (running < MAX_READS) start();
    else if (first) queue.unshift(start);
    else queue.push(start);
  });
}

/** Reads a launcher's list: once (the pill), or `again` behind the one shown (an opening, Retry). */
export function readModels(mid: string, launcher: string, o: { again?: boolean; first?: boolean; purpose?: "assistant" } = {}): Promise<void> {
  const scope = catalogScope(o.purpose);
  const key = listKey(mid, launcher, o.purpose, scope);
  const reading = reads.get(key);
  if (reading) return reading.promise;
  const had = lists.get(key);
  if (had?.list !== undefined && !o.again) return Promise.resolve();
  lists.set(key, { list: had?.list, loading: true });
  changed();
  const sharedKey = listKey(mid, launcher, undefined, scope);
  const hidden = new Set(forgetting.get(sharedKey)?.keys());
  const read = slot(Boolean(o.first)).then(async (done) => {
    try {
      if (scope.owner !== credentialOwner()) return;
      const r = await launcherModels(mid, launcher, { purpose: o.purpose, manager: scope.manager });
      for (const id of hidden) {
        if (r?.models.some((m) => m.id === id && m.custom)) {
          forgetting.get(sharedKey)?.get(id)?.restore.set(key, withoutCustom(r, id).restore);
        }
      }
      lists.set(key, { list: r && hidden.size ? { ...r, models: r.models.filter((m) => !(m.custom && hidden.has(m.id))) } : r });
    } catch (e) {
      // a list already shown stays (and says nothing): a newer one just didn't come
      const now = lists.get(key);
      lists.set(key, now?.list !== undefined ? { list: now.list } : { error: errorText(e) });
    } finally {
      done();
      reads.delete(key);
      changed();
    }
  });
  reads.set(key, { promise: read, hidden });
  return read;
}

/** Reads the computer's launchers (the providers), the last ones shown meanwhile. */
function readLaunchers(mid: string): Promise<void> {
  const scope = catalogScope();
  const key = machineKey(mid, scope);
  const reading = machineReads.get(key);
  if (reading) return reading;
  const had = machines.get(key);
  machines.set(key, { launchers: had?.launchers, loading: true });
  changed();
  const read = machineLaunchers(mid, { purpose: "agent.new", manager: scope.manager }).then(
    (r) => void machines.set(key, { launchers: r.launchers ?? [] }),
    () => void machines.set(key, { launchers: had?.launchers }),
  ).finally(() => {
    machineReads.delete(key);
    changed();
  });
  machineReads.set(key, read);
  return read;
}

/** Reuse this computer's catalog for the pill's launcher titles. Opening the panel refreshes it. */
export function readAgentLauncherNames(mid: string): Promise<void> {
  return machineLauncherList(mid) ? Promise.resolve() : readLaunchers(mid);
}

/** An opening of the panel on computer `mid` for a session of launcher `own`: every list read again behind the one shown,
 *  the session's own first. */
export function readForPanel(mid: string, own: string): void {
  void readModels(mid, own, { again: true, first: true });
  void readLaunchers(mid).then(() => {
    for (const l of machineLauncherList(mid) ?? []) if (l.name !== own) void readModels(mid, l.name, { again: true });
  });
}

/** Forgets an ID typed before (#1644): off the list at once, back where it was when the hub says no. "": done; else why not. */
export async function forgetModel(mid: string, launcher: string, id: string, purpose?: "assistant"): Promise<string> {
  const scope = catalogScope(purpose);
  const sharedKey = listKey(mid, launcher, undefined, scope);
  const pending = forgetting.get(sharedKey) ?? new Map<string, ModelForget>();
  const existing = pending.get(id);
  if (existing) return existing.promise;
  if (!lists.get(listKey(mid, launcher, purpose, scope))?.list) return "";
  const keys = [sharedKey, listKey(mid, launcher, "assistant", scope)];
  const mutation: ModelForget = {
    restore: new Map(),
    promise: Promise.resolve().then(async () => {
      try {
        if (scope.owner !== credentialOwner()) return "";
        await forgetCustomModel(mid, launcher, id, { manager: scope.manager });
        return "";
      } catch (e) {
        for (const key of keys) {
          reads.get(key)?.hidden.delete(id);
          const now = lists.get(key);
          const restore = mutation.restore.get(key);
          if (now?.list && restore) lists.set(key, { ...now, list: restore(now.list) });
        }
        changed();
        return t("composer.forget_failed", { id, error: errorText(e) });
      } finally {
        pending.delete(id);
        if (!pending.size) forgetting.delete(sharedKey);
      }
    }),
  };
  pending.set(id, mutation);
  forgetting.set(sharedKey, pending);
  for (const key of keys) {
    const current = lists.get(key);
    if (current?.list) {
      const removed = withoutCustom(current.list, id);
      mutation.restore.set(key, removed.restore);
      lists.set(key, { ...current, list: removed.list });
    }
    reads.get(key)?.hidden.add(id);
  }
  changed();
  return mutation.promise;
}

// ── the session's model, from what was read ──────────────────────────────────

/** The engine a session runs (AgentMark ids: claude, codex, agy…; one of Paseo's providers is that provider, grok…). */
export const sessionEngine = (s: Session): string => agentId(sessionAgent(s).agent);

/** The hub moves a running Agent between Claude Code and Codex with the user's next message (#5054,
 *  docs/protocol/protocol.md). */
export const ENGINE_SWITCH_FEATURE = "engine_switch";

/** Whether `s` may move to the other engine on a hub with ENGINE_SWITCH_FEATURE: a running Claude Code Agent whose model
 *  the app switches in a terminal (as for another launcher, #5035), or a Codex Agent in a terminal (its own row: a Codex
 *  app thread or a runtime's session has a source; a runtime's Claude Code switches its model there, not its engine,
 *  #8142). The node refuses the rest (`switch_unsupported`). */
export function engineSwitchable(s: Session): boolean {
  const engine = sessionEngine(s);
  if (engine === "claude") return !s.source_kind && canSwitchModel(s);
  return engine === "codex" && !s.assistant && !s.source_kind && Boolean(s.pane_id) && sessionCan(s, "send") && stateOf(s) !== "offline";
}

/** What the pill and the panel can change on the session now: its model (Claude Code in a terminal, a runtime that says
 *  so), its effort (the composer's chips used to; a Codex thread on its app-server too). Neither: the pill is a label. */
export function sessionSwitches(s: Session): { model: boolean; effort: boolean } {
  const live = stateOf(s) !== "offline";
  return { model: canSwitchModel(s), effort: live && (hasModes(s) || hasEffortOnly(s)) };
}

/** The row of `models` the session runs: the one its model names (an alias or what an alias stands for; the default entry
 *  only when no other row names it); with no model reported yet, the default entry, which is what runs then. */
export function currentModelRow(models: AgentModel[], model: string | undefined): AgentModel | undefined {
  if (!model) return defaultModel(models);
  const hits = models.filter((m) => isCurrentModel(m, model));
  return hits.find((m) => !m.default) ?? hits[0];
}

/** A model ID said briefly where no list names it: Claude's as "opus 5.5", GPT's as their list would, a gateway's path
 *  without its vendor. */
export function modelLabel(id: string): string {
  const short = id.split("/").pop() || id;
  const claude = modelShort(short);
  return claude !== short ? claude : modelName({ id: short });
}

/** One setup as the pill draws it. */
export interface PillSetup {
  /** The engine (AgentMark ids: claude, codex…). */
  engine: string;
  /** What the engine is called when the app has no name of its own for it: a runtime provider's label ("Grok", the
   *  row's agent_label, #8142); its mark and its name in the tooltip. */
  engineLabel?: string;
  /** The provider (launcher) name: its tile, and the tooltip. */
  provider: string;
  /** Explicit Agent launcher name, matching its panel row; absent when only an inferred engine is known. */
  launcherName?: string;
  /** The model's name; absent while not known (no reply yet and no list to say what the launcher runs by default). */
  model?: string;
  effort?: string;
  fast?: boolean;
}

/** What the pill says: the engine, the launcher and the model the session runs (its list's name for it once read), the
 *  effort and speed; `loading` while the list is read the first time, `failed` when that failed. */
export interface PillView extends PillSetup {
  loading: boolean;
  failed: boolean;
}

/** A message refused because the switch to another launcher it was to carry out first could not be (#5035,
 *  docs/protocol/protocol.md "Switching a running Agent to another provider": `switch_launcher_missing`,
 *  `switch_failed`…): nothing was typed and the switch was dropped. */
export const switchRefused = (e: unknown): boolean => e instanceof ApiError && e.status === 409 && Boolean(e.code?.startsWith("switch_"));

/** The engine the waiting switch moves the conversation to (#5054): `pending_switch.agent`, "" when none waits or it
 *  stays on its engine (another launcher of it, #5035). */
export function pendingEngine(s: Session): string {
  const ps = s.pending_switch;
  if (!ps?.launcher || ps.launcher === sessionLauncher(s) || !ps.agent) return "";
  const to = agentId(ps.agent);
  return to === sessionEngine(s) ? "" : to;
}

/** The switch to another launcher waiting for the next message (#5035), as the pill draws it: that launcher, the model
 *  picked (its list's name for it once read; none picked: the launcher's default), the effort picked or the one it runs
 *  with now. A switch to the other engine (#5054) draws that engine, and no effort of the engine it leaves: the one
 *  picked, else the model's own default. null: none waits. */
export function pendingView(s: Session, listOf: (launcher: string) => ModelList): PillSetup | null {
  const ps = s.pending_switch;
  if (!ps?.launcher || ps.launcher === sessionLauncher(s)) return null;
  const models = listOf(ps.launcher).list?.models ?? [];
  const row = ps.model ? listedModel(models, ps.model) : defaultModel(models);
  const to = pendingEngine(s);
  const out: PillSetup = { engine: to || sessionEngine(s), provider: ps.launcher, model: row ? modelName(row) : ps.model ? modelLabel(ps.model) : t("chat-modelpill.default_model") };
  const effort = ps.effort || (to ? row?.default_effort : s.effort);
  if (effort) out.effort = effort;
  return out;
}

export function pillView(s: Session, e: ModelList): PillView {
  const row = currentModelRow(e.list?.models ?? [], s.model);
  const model = row ? modelName(row) : s.model ? modelLabel(s.model) : undefined;
  const out: PillView = { engine: sessionEngine(s), provider: sessionLauncher(s), loading: e.list === undefined && Boolean(e.loading), failed: e.list === undefined && Boolean(e.error) };
  if (s.agent_label) out.engineLabel = s.agent_label;
  if (model) out.model = model;
  if (s.effort) out.effort = s.effort;
  if (s.service_tier === "priority") out.fast = true;
  return out;
}

/** Forgets every list and read kept (tests: each starts with nothing read). */
export function resetModelListsForTests(): void {
  lists.clear();
  machines.clear();
  reads.clear();
  machineReads.clear();
  forgetting.clear();
  queue.length = 0;
  running = 0;
  changed();
}

/** Subscribes to every change of what was read (views/NewAgentModel.tsx useModelList follows one list with it). */
export const subscribeModelLists = subscribe;
