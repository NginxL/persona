// Settings › Extensions › Channels (docs/extensions/channels.md): every way of talking to Persona — the apps, the Feishu bot, WeChat, /agent in
// Feishu, voice, and the chat app bots extensions contribute — as GET /api/v1/channels lists them; one channel's page
// (its switch, binding a step at a time, where its messages go, what it does, a test message), and the source mark on
// a message that came from one (its client_msg_id is `<channel>-<message id>`).
//
// The pure parts (state words, sections, the source of a message, the bind steps) are plain functions for the tests.

import { useEffect, useState, useSyncExternalStore } from "react";
import { formatList, t } from "../i18n";
import { api, enc } from "./api";
import { itemHeaders } from "./manager-choice";
import type { ConnectorPrompt } from "./connectors";
import { useStore } from "./store";

// ── wire types (server/internal/channel) ─────────────────

export type ChannelState = "connected" | "not_connected" | "error" | "disabled" | "not_installed";

export interface ChannelCapabilities {
  images_in?: boolean;
  files_in?: boolean;
  voice_in?: boolean;
  format?: "markdown" | "plain";
  max_text?: number;
  asks?: "none" | "text" | "buttons";
  push?: boolean;
  reply_only?: boolean;
  typing?: boolean;
  progress?: boolean;
  threads?: boolean;
}

export interface ChannelTarget {
  kind: "main" | "new" | "conversation" | "agent";
  machine_id?: string;
  session_id?: string;
  title?: string;
}

export interface ChannelFeature {
  id: string;
  title: string;
  description?: string;
  on: boolean;
}

/** One of GET /api/v1/channels (channel.View). */
export interface Channel {
  id: string;
  name: string;
  kind: "app" | "im" | "mention" | "voice";
  source: "builtin" | "plugin" | "market";
  plugin?: string;
  market?: string;
  installed: boolean;
  enabled: boolean;
  state: ChannelState;
  error?: string;
  account?: { id?: string; name: string };
  description?: string;
  capabilities: ChannelCapabilities;
  target?: ChannelTarget;
  features?: ChannelFeature[];
  bind: "none" | "qr" | "code" | "fields" | "config";
  surfaces?: string[];
  last_event_at?: string;
  read_only?: boolean;
}

/** GET /api/v1/channels/{id} (channel.Detail). */
export interface ChannelDetail extends Channel {
  waiting: { answers: number; asks: number };
  conversations?: { machine_id: string; session_id: string; title?: string; at: string }[];
  /** The channel's own full page in the app ("settings/connectors/feishu-bot", "settings/plugins/<id>"). */
  settings_url?: string;
}

/** A bind step (connector.Prompt); a channel adds pair_code, a code to send to the bot, to the kinds a connector has. */
export type ChannelPrompt = ConnectorPrompt;

// ── the hub's interface ──────────────────────────────────

/** `manager`: whose channels (another manager's, docs/assistant/managers.md "Clients"); none: the default's. */
export async function listChannels(manager?: string): Promise<Channel[]> {
  const r = await api<{ channels?: Channel[] }>("/channels", { headers: itemHeaders(manager) });
  return r.channels ?? [];
}

export function channelDetail(id: string): Promise<ChannelDetail> {
  return api<ChannelDetail>(`/channels/${enc(id)}`);
}

export function updateChannel(id: string, body: { enabled?: boolean; target?: ChannelTarget; features?: Record<string, boolean> }): Promise<ChannelDetail> {
  return api<ChannelDetail>(`/channels/${enc(id)}`, { method: "PUT", json: body });
}

export function bindChannel(id: string, step: string, values: Record<string, string>): Promise<ChannelPrompt> {
  return api<ChannelPrompt>(`/channels/${enc(id)}/bind`, { method: "POST", json: { step, values } });
}

export function unbindChannel(id: string): Promise<ChannelDetail> {
  return api<ChannelDetail>(`/channels/${enc(id)}/binding`, { method: "DELETE" });
}

export interface ChannelTestResult {
  ok: boolean;
  reply_only?: boolean;
  note?: string;
}

export function testChannel(id: string): Promise<ChannelTestResult> {
  return api<ChannelTestResult>(`/channels/${enc(id)}/test`, { method: "POST" });
}

// ── words ────────────────────────────────────────────────

export type ChannelTone = "green" | "red" | "idle";

export function channelStateText(c: Channel): string {
  switch (c.state) {
    case "connected":
      return t("lib-channels.state_connected");
    case "error":
      return t("lib-channels.state_error");
    case "disabled":
      return t("lib-channels.state_disabled");
    case "not_installed":
      return t("lib-channels.state_not_installed");
  }
  return t("lib-channels.state_not_connected");
}

export function channelTone(c: Channel): ChannelTone {
  if (c.state === "connected") return "green";
  if (c.state === "error") return "red";
  return "idle";
}

/** The row's second line: the error, the account, else what it is. */
export function channelSubtitle(c: Channel): string {
  if (c.state === "error" && c.error) return c.error;
  if (c.kind === "app" && c.surfaces?.length) return c.surfaces.join(" · ");
  if (c.account?.name && c.state !== "not_installed") return c.account.name;
  return c.description ?? "";
}

/** The list's sections: the apps, the installed ones, those a market has. Empty sections are left out. */
export function channelSections(list: Channel[]): { title: string; footer?: string; channels: Channel[] }[] {
  const apps = list.filter((c) => c.kind === "app");
  const installed = list.filter((c) => c.kind !== "app" && c.source !== "market");
  const market = list.filter((c) => c.source === "market");
  return [
    { title: "Persona", footer: t("lib-channels.section_apps_footer"), channels: apps },
    { title: t("lib-channels.section_installed"), footer: t("lib-channels.section_installed_footer"), channels: installed },
    { title: t("lib-channels.section_market"), footer: t("lib-channels.section_market_footer"), channels: market },
  ].filter((s) => s.channels.length > 0);
}

/** "3 connected", with the ones that went wrong. */
export function channelsSummary(list: Channel[]): string {
  const on = list.filter((c) => c.kind !== "app" && c.state === "connected").length;
  const bad = list.filter((c) => c.state === "error").length;
  return bad > 0 ? t("settings-channels.summary_with_errors", { on, bad }) : t("settings-channels.summary", { count: on });
}

export function targetText(target?: ChannelTarget): string {
  switch (target?.kind) {
    case "new":
      return t("lib-channels.target_new");
    case "conversation":
      return target.title || t("lib-channels.target_conversation");
    case "agent":
      return target.title ? t("lib-channels.target_agent_titled", { title: target.title }) : t("lib-channels.target_agent");
  }
  return t("lib-channels.target_main");
}

/** What the channel can do, in a few words for its page. */
export function capabilityText(c: ChannelCapabilities): string[] {
  const out: string[] = [];
  const takes = [t("lib-channels.takes_text"), c.images_in && t("lib-channels.takes_images"), c.files_in && t("lib-channels.takes_files"), c.voice_in && t("lib-channels.takes_voice")].filter((x): x is string => !!x);
  out.push(t("lib-channels.takes", { kinds: formatList(takes, { type: "conjunction", style: "narrow" }) }));
  out.push(c.format === "plain" ? t("lib-channels.format_plain") : t("lib-channels.format_markdown"));
  if (c.max_text) out.push(t("lib-channels.max_text", { count: c.max_text }));
  if (c.asks === "text") out.push(t("lib-channels.asks_text"));
  else if (c.asks === "buttons") out.push(t("lib-channels.asks_buttons"));
  if (c.reply_only) out.push(t("lib-channels.reply_only"));
  if (c.typing) out.push(t("lib-channels.typing"));
  if (c.progress) out.push(t("lib-channels.progress"));
  return out;
}

export function bindTitle(c: Pick<Channel, "bind">): string {
  switch (c.bind) {
    case "qr":
      return t("lib-channels.bind_qr");
    case "code":
      return t("lib-channels.bind_code");
  }
  return t("lib-channels.bind");
}

export function isBound(c: Channel): boolean {
  return c.state === "connected" || (c.state === "error" && !!c.account);
}

/** Whether the page can bind it: it binds here, is on, and is not bound. */
export function canBind(c: Channel): boolean {
  return !c.read_only && c.source !== "market" && c.bind !== "none" && c.bind !== "config" && c.enabled && !isBound(c);
}

/** A step the app sends again by itself (a QR code to scan, a code to enter or send), not one with fields. */
export function isPolled(p?: ChannelPrompt | null): boolean {
  return !!p && !p.done && (p.kind === "qr" || p.kind === "pair_code" || p.kind === "device_code");
}

/** Where a channel's settings_url ("settings/connectors/feishu-bot") opens in the settings page: its tab and sub-page. */
export function settingsTarget(url: string): { tab: string; sub: string | null } | null {
  const m = /^settings\/([a-z-]+)(?:\/(.+))?$/.exec(url);
  return m ? { tab: settingsTab(m[1]), sub: m[2] ?? null } : null;
}

/** A settings tab by its name now: `plugins` (a hub or a link from before #1844 / #1845) is `extensions`. */
export function settingsTab(tab: string): string;
export function settingsTab(tab: string | null): string | null;
export function settingsTab(tab: string | null): string | null {
  return tab === "plugins" ? "extensions" : tab;
}

// ── where a message came from ────────────────────────────

/** The channel a message came from, by its client_msg_id's prefix (`wechat-…`), when it is one there is. */
export function channelOf(clientMsgId: string | undefined, list: Pick<Channel, "id" | "name" | "kind">[]): Pick<Channel, "id" | "name" | "kind"> | undefined {
  if (!clientMsgId) return undefined;
  const dash = clientMsgId.indexOf("-");
  if (dash <= 0) return undefined;
  const head = clientMsgId.slice(0, dash);
  return list.find((c) => c.id === head && c.kind !== "app");
}

/** The name to show for a conversation's latest channel (Session.channels), "" when it has none known. */
export function conversationChannel(ids: string[] | undefined, list: Pick<Channel, "id" | "name">[]): Pick<Channel, "id" | "name"> | undefined {
  const id = ids?.[0];
  return id ? list.find((c) => c.id === id) ?? { id, name: id } : undefined;
}

// The list, fetched once per hub (it changes when an extension comes or goes: the settings page reloads it), for the
// source marks in conversations. forgetChannels() starts a new generation: a read already out when it was called is
// not cached, and the hooks already mounted read again. A read is shared by every hook asking under the same
// generation and features key, and cached under the key it started with.
let cached: { key: string; list: Channel[] } | null = null;
let inflight: { gen: number; key: string; promise: Promise<Channel[]> } | null = null;
let generation = 0;
const listeners = new Set<() => void>();

function subscribeGeneration(on: () => void) {
  listeners.add(on);
  return () => void listeners.delete(on);
}

const currentGeneration = () => generation;

/** The shared read for this generation and features key, started when there is none. */
function readChannels(key: string) {
  if (inflight && inflight.gen === generation && inflight.key === key) return inflight;
  const run = { gen: generation, key, promise: listChannels() };
  inflight = run;
  run.promise.then((l) => {
    if (run.gen === generation) cached = { key: run.key, list: l };
  }, () => {}).finally(() => {
    if (inflight === run) inflight = null;
  });
  return run;
}

/** The channels as the conversations need them (names for the source marks); [] on a hub without them. */
export function useChannels(): Channel[] {
  const has = useStore((s) => s.features.includes("channels"));
  const key = useStore((s) => s.features.join(","));
  // The third argument is the server snapshot: renderToString (used by the bubble tests) needs one.
  const gen = useSyncExternalStore(subscribeGeneration, currentGeneration, currentGeneration);
  const [list, setList] = useState<Channel[]>(() => (cached && cached.key === key ? cached.list : []));
  useEffect(() => {
    if (!has) return;
    if (cached?.key === key) {
      setList(cached.list);
      return;
    }
    let live = true;
    readChannels(key).promise.then((l) => {
      if (live) setList(l);
    }, () => {});
    return () => void (live = false);
  }, [has, key, gen]);
  return has ? list : [];
}

/** Forget the list (the settings page changed something): drop the read in flight and have mounted hooks read again. */
export function forgetChannels() {
  cached = null;
  inflight = null;
  generation++;
  for (const on of listeners) on();
}
