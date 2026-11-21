// The hub's connectors (docs/extensions/connectors.md, docs/protocol/protocol.md "Connector API"): GitHub, Telegram, an extension's, as
// GET /api/v1/connectors lists them, their sign-in a step at a time, their switches, and the threads their inbox items
// open as (GET /api/v1/connectors/{id}/thread). Mail and Feishu keep their own pages (settings/Connectors.tsx).
// iOS: ConnectorsViews.swift, ConnectorSignInViews.swift, GitHubAuthViews.swift, GitHubInboxSettings.swift,
// ConnectorThread/*.
//
// The pure parts (state words, rows, sign-in checks, footers, thread pages) are plain functions for the tests.

import { formatList, formatNumber, t } from "../i18n";
import { api, enc } from "./api";
import { connectorProfile } from "./connector-profiles";
import { itemHeaders } from "./manager-choice";

// ── wire types ───────────────────────────────────────────

/** One of GET /api/v1/connectors (connector.Info). */
export interface HubConnector {
  id: string;
  /** The manager whose connector it is, in the hub's merged list (each manager has its own, its built-ins too); none:
   *  the default's. */
  manager?: string;
  name: string;
  status: string; // ok | not_configured | not_signed_in | no_permission | rate_limited | error | disabled
  account?: string;
  via?: string;
  error?: string;
  /** Permissions the connector still needs, when the hub can name them. */
  missing_scopes?: string[];
  /** The open-platform page where the connector's missing permissions can be enabled (the hub sends it as guide_url). */
  guide_url?: string;
  /** Alias of guide_url, accepted as well. */
  permission_url?: string;
  /** How to fix it, a step a sentence. */
  guide?: string[];
  last_sync?: string;
  last_event?: string;
  capabilities?: string[];
  hosts?: string[];
  /** Switched on (absent: a hub from before the switch, always on). */
  enabled?: boolean;
  /** "Add to knowledge base" / "Add to Inbox": only there when it has an archive, an inbox. */
  archive?: boolean;
  inbox?: boolean;
  /** Its own settings (GitHub's: GitHubInboxOptions). */
  options?: unknown;
}

export interface PromptField {
  name: string;
  label: string;
  secret?: boolean;
  placeholder?: string;
}

/** A QR code's modules as the hub draws them (connector.PromptQR): one string of "0" / "1" per row, no quiet zone. */
export interface PromptQR {
  size: number;
  rows: string[];
}

/** Where a sign-in stands (connector.Prompt): the step to fill in next, a device code, a QR code, or done. */
export interface ConnectorPrompt {
  step?: string;
  fields?: PromptField[];
  kind?: string; // "" | device_code | qr
  url?: string;
  /** url's QR code for a qr step, filled in by the hub on its way to the page. */
  qr?: PromptQR;
  user_code?: string;
  interval?: number;
  hint?: string;
  /** Where this step's hint points at (the provider's console, to create the app); the page opens it in a new tab. */
  link?: string;
  /** link's button label (the hub localizes it). */
  link_text?: string;
  error?: string;
  done?: boolean;
  account?: string;
}

/** What deleting a connector's data removed (DELETE …/data, …/archive). */
export interface ConnectorDeleted {
  chats?: number;
  people?: number;
  inbox?: number;
}

export interface GitHubMissing {
  scope: string;
  affects: string[];
}

export interface GitHubMachine {
  id: string;
  name: string;
  state?: string; // signed_in | not_signed_in | no_gh | too_old | offline | error
  account?: string;
  scopes?: string[];
  missing?: GitHubMissing[];
  error?: string;
}

/** GET /api/v1/connectors/github/auth. */
export interface GitHubAuth {
  source: string; // machine | device
  machine?: string;
  machines: GitHubMachine[];
  scopes?: string[];
  scopes_known?: boolean;
  missing?: GitHubMissing[];
}

export interface GitHubInboxOptions {
  ci: string;
  muted_repos: string[];
  ignore_branches?: string[];
}

// ── one connector ────────────────────────────────────────

/** Its state in a word or two. A state this page does not know (a newer hub's) is not "Error". */
export function connectorState(c: HubConnector): string {
  switch (c.status) {
    case "ok":
      return t("lib-connectors.state_ok");
    case "not_configured":
      return t("lib-connectors.state_not_configured");
    case "not_signed_in":
      return t("lib-connectors.state_not_signed_in");
    case "no_permission":
      return t("lib-connectors.state_no_permission");
    case "rate_limited":
      return t("lib-connectors.state_rate_limited");
    case "disabled":
      return t("lib-connectors.state_disabled");
    case "error":
      return t("lib-connectors.state_error");
    default:
      return t("lib-connectors.state_unknown");
  }
}

export type ConnectorTone = "green" | "orange" | "red" | "idle";

/** The dot beside the state: red only for what is broken; off, not set up, unknown or loading are gray. */
export function connectorTone(c?: HubConnector | null): ConnectorTone {
  switch (c?.status) {
    case "ok":
      return "green";
    case "rate_limited":
      return "orange";
    case "error":
    case "no_permission":
      return "red";
    default:
      return "idle";
  }
}

/** The list row's right side: who it reads as when connected, else its state. */
export function connectorSummary(c: HubConnector): string {
  return c.status === "ok" ? c.account || connectorState(c) : connectorState(c);
}

/** The user signs in to it here, a step at a time (Telegram; GitHub's device flow). */
export function signsInHere(c: HubConnector): boolean {
  return !!c.capabilities?.includes("login");
}

/** A program the hub runs that has not said what it has yet (switched off since the hub started, or starting). */
export function undescribed(c: HubConnector): boolean {
  return Array.isArray(c.capabilities) && c.capabilities.length === 0;
}

export function isEnabled(c: HubConnector): boolean {
  return c.enabled ?? true;
}

/** It brings things into the knowledge base or the inbox (Telegram), so its switch syncs and it has data to delete; a
 * chat bot (WeChat: a status and a sign-in, docs/extensions/channels.md) only talks. One not described yet (no capabilities, or a
 * hub from before them) counts as bringing them, as before there were bots (the apps do the same). */
export function storesData(c: HubConnector): boolean {
  const caps = c.capabilities;
  if (!caps?.length) return true;
  return caps.includes("archive") || caps.includes("inbox");
}

/** The connector's switch: "Sync X" for one that brings data in, "Enable X" for one that does not. */
export function switchLabel(c: HubConnector): string {
  return storesData(c) ? t("lib-connectors.switch_sync", { name: c.name }) : t("lib-connectors.switch_enable", { name: c.name });
}

export function isSignedIn(c: HubConnector): boolean {
  return c.status === "ok" || c.status === "rate_limited";
}

/** Switched on and still starting: the page asks again every second for a while. */
export function isStarting(c?: HubConnector | null): boolean {
  return !!c && isEnabled(c) && undescribed(c);
}

/** The connectors that get rows of their own after "Mail" and Lark (those two keep their own groups; Lark's only shows on a
 * hub with feature `lark`, `larkGroup`, so without it a connector named lark is listed like any other). */
export function otherConnectors(list: HubConnector[], larkGroup = true): HubConnector[] {
  return list.filter((c) => c.id !== "mail" && c.id !== "larkcli" && !(larkGroup && c.id === "lark"));
}

// ── brand and wording, by id (connector-profiles.ts) ─────

export { connectorBrand, connectorOpenTitle, type BrandId } from "./connector-profiles";

// ── its page ─────────────────────────────────────────────

export function switchFooter(c: HubConnector): string {
  if (!storesData(c)) return isEnabled(c) ? t("lib-connectors.switch_footer_bot_on", { name: c.name }) : t("lib-connectors.switch_footer_bot_off");
  if (isEnabled(c)) return t("lib-connectors.switch_footer_on", { name: c.name });
  return t("lib-connectors.switch_footer_off", { reads: connectorProfile(c.id).reads ?? t("lib-connectors.reads_default") });
}

export function signInFooter(c: HubConnector): string {
  return connectorProfile(c.id).signInFooter ?? t("lib-connectors.sign_in_footer");
}

/** The sign-out confirmation. Only a connector known to end the remote session says so; every other one (GitHub's device
 * token, an extension's key) only has the hub forget its copy, so it says the authorization stays. */
export function logoutMessage(c: HubConnector): string {
  if (connectorProfile(c.id).logoutEndsRemoteSession) return t("lib-connectors.logout_remote", { name: c.name });
  return t("lib-connectors.logout");
}

export function feedsFooter(c: HubConnector): string {
  const lines: string[] = [];
  if (c.archive !== undefined) {
    lines.push(c.archive ? t("lib-connectors.feeds_archive_on", { name: c.name }) : t("lib-connectors.feeds_archive_off", { name: c.name }));
  }
  if (c.inbox === false) lines.push(t("lib-connectors.feeds_inbox_off"));
  return lines.join("\n");
}

export function deletedLine(r: ConnectorDeleted, withInbox = true): string {
  const vars = { chats: r.chats ?? 0, people: r.people ?? 0, inbox: r.inbox ?? 0 };
  return withInbox ? t("lib-connectors.deleted_with_inbox", vars) : t("lib-connectors.deleted", vars);
}

/** Every field of the step on screen is filled in. */
export function canSendStep(p: ConnectorPrompt | null, values: Record<string, string>): boolean {
  if (!p?.fields?.length) return false;
  return p.fields.every((f) => (values[f.name] ?? "").trim().length > 0);
}

/** How the page draws a sign-in step: fields to fill in, a device code, a QR code (or, without its modules, the
 * link it encodes), or a step this page cannot draw (a kind it does not know, a QR code with nothing to show, a form
 * with no fields) — said so, with what to do instead, rather than an empty form whose "Next" never lights up. */
export type StepView = "fields" | "device_code" | "qr" | "qr_link" | "unsupported";

export function stepView(p: ConnectorPrompt): StepView {
  switch (p.kind ?? "") {
    case "":
      return p.fields?.length ? "fields" : "unsupported";
    case "device_code":
      return p.user_code ? "device_code" : "unsupported";
    case "qr":
      if (p.qr?.rows?.length) return "qr";
      return /^https?:\/\//i.test(p.url ?? "") ? "qr_link" : "unsupported";
    default:
      return "unsupported";
  }
}

/** A step the page asks again by itself until it is done (a code to enter, a QR code to scan). */
export function isPolledStep(p?: ConnectorPrompt | null): boolean {
  if (!p || p.done) return false;
  const v = stepView(p);
  return v === "device_code" || v === "qr" || v === "qr_link";
}

/** What a field's input should be: digits for codes, a phone keypad, a password. */
export function fieldInput(f: PromptField): { type: string; inputMode?: "numeric" | "tel"; autoComplete: string } {
  if (f.secret) return { type: "password", autoComplete: "current-password" };
  if (f.name === "phone") return { type: "tel", inputMode: "tel", autoComplete: "tel" };
  if (f.name === "code" || f.name === "api_id") return { type: "text", inputMode: "numeric", autoComplete: "one-time-code" };
  return { type: "text", autoComplete: "off" };
}

// ── GitHub ───────────────────────────────────────────────

export function scopeNames(missing: GitHubMissing[]): string {
  return formatList(missing.map((m) => m.scope), { type: "conjunction", style: "narrow" });
}

/** A computer's gh login, in a line. */
export function githubMachineLine(m: GitHubMachine): string {
  switch (m.state) {
    case "signed_in": {
      const who = m.account ? t("lib-connectors.gh_signed_in_account", { account: m.account }) : t("lib-connectors.gh_signed_in");
      return m.missing?.length ? t("lib-connectors.gh_missing_scopes", { who, scopes: scopeNames(m.missing) }) : who;
    }
    case "not_signed_in":
      return t("lib-connectors.gh_not_signed_in");
    case "no_gh":
      return t("lib-connectors.gh_not_installed");
    case "too_old":
      return t("lib-connectors.gh_too_old");
    case "offline":
      return t("lib-connectors.gh_offline");
    default:
      return m.error ? t("lib-connectors.gh_unreadable_error", { error: m.error }) : t("lib-connectors.gh_unreadable");
  }
}

export function githubMachineWarns(m: GitHubMachine): boolean {
  return m.state !== "signed_in" || !!m.missing?.length;
}

/** What to do about the scopes the token in use lacks. */
export function githubScopeAdvice(auth: GitHubAuth, missing: GitHubMissing[]): string {
  const names = scopeNames(missing);
  if (auth.source === "device") return t("lib-connectors.scope_advice_device", { scopes: names });
  const machine = auth.machines.find((m) => m.id === auth.machine)?.name ?? t("lib-connectors.this_computer");
  return t("lib-connectors.scope_advice_gh", { machine, scopes: names, command: `gh auth refresh -s ${missing.map((m) => m.scope).join(",")}` });
}

/** GitHub's inbox options as the hub gives them, or null when it gives none (or not that shape). */
export function githubOptions(raw: unknown): GitHubInboxOptions | null {
  if (!raw || typeof raw !== "object") return null;
  const o = raw as Record<string, unknown>;
  if (typeof o.ci !== "string") return null;
  const strings = (v: unknown) => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : undefined);
  return { ci: o.ci, muted_repos: strings(o.muted_repos) ?? [], ignore_branches: strings(o.ignore_branches) };
}

/** lark-cli's Inbox options: group chats taken whole, watched documents and Wiki spaces, and whether every comment on
 * a watched document the user owns comes in (PUT …/larkcli/settings {options}, sent whole). */
export interface LarkCLIInboxOptions {
  inbox_all_chats: string[];
  documents: string[];
  wiki_spaces: string[];
  owned_all_comments: boolean;
  /** What counts as Needs you / FYI / Collapsed for these items, in the user's words (empty: the Inbox's own rules). */
  triage_rules: string;
}

/** lark-cli's options as the hub gives them, or null when it gives none (or not that shape). */
export function larkcliOptions(raw: unknown): LarkCLIInboxOptions | null {
  if (!raw || typeof raw !== "object") return null;
  const o = raw as Record<string, unknown>;
  if (!Array.isArray(o.documents)) return null;
  const strings = (v: unknown) => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : []);
  return { inbox_all_chats: strings(o.inbox_all_chats), documents: strings(o.documents), wiki_spaces: strings(o.wiki_spaces), owned_all_comments: o.owned_all_comments === true, triage_rules: typeof o.triage_rules === "string" ? o.triage_rules : "" };
}

/** The CI choices, most quiet first. */
export const CI_MODES: { value: string; label: string }[] = [
  { value: "off", label: t("lib-connectors.ci_mode_off") },
  { value: "default_branch", label: t("lib-connectors.ci_mode_default_branch") },
  { value: "mine", label: t("lib-connectors.ci_mode_mine") },
  { value: "all", label: t("lib-connectors.ci_mode_all") },
];

export function githubInboxFooter(o: GitHubInboxOptions): string {
  const ci =
    o.ci === "off" ? t("lib-connectors.ci_off")
      : o.ci === "default_branch" ? t("lib-connectors.ci_default_branch")
        : o.ci === "all" ? t("lib-connectors.ci_all")
          : t("lib-connectors.ci_mine");
  const auto = formatList(o.ignore_branches ?? ["train/*"], { type: "conjunction", style: "narrow" });
  return t("lib-connectors.inbox_footer", { ci, auto });
}

/** A repository to mute: owner/name, not one already muted. */
export function mutableRepo(input: string, muted: string[]): string | null {
  const repo = input.trim();
  return /^[^/\s]+\/[^/\s]+$/.test(repo) && !muted.includes(repo) ? repo : null;
}

// ── requests ─────────────────────────────────────────────

/** The hub's connectors; `all` takes Feishu and mail too. */
export async function listConnectors(all = false): Promise<HubConnector[]> {
  const r = await api<{ connectors?: HubConnector[] }>(`/connectors${all ? "?all=1" : ""}`);
  return r.connectors ?? [];
}

export function connectorLogin(id: string, step: string, values: Record<string, string>): Promise<ConnectorPrompt> {
  return api<ConnectorPrompt>(`/connectors/${enc(id)}/login`, { method: "POST", json: { step, values } });
}

export function connectorLogout(id: string): Promise<unknown> {
  return api(`/connectors/${enc(id)}/logout`, { method: "POST" });
}

/** The switch (`enabled`), "Add to knowledge base" (`archive`), "Add to Inbox" (`inbox`) or its own settings (`options`).
 *  `manager`: whose connector (another manager's extension page names it); none: the default's, or the page's pin. */
export function setConnector(id: string, body: { enabled?: boolean; archive?: boolean; inbox?: boolean; options?: unknown }, manager?: string): Promise<HubConnector> {
  return api<HubConnector>(`/connectors/${enc(id)}/settings`, { method: "PUT", json: body, headers: itemHeaders(manager) });
}

export function deleteConnectorData(id: string): Promise<ConnectorDeleted> {
  return api<ConnectorDeleted>(`/connectors/${enc(id)}/data`, { method: "DELETE" });
}

export function deleteConnectorArchive(id: string): Promise<ConnectorDeleted> {
  return api<ConnectorDeleted>(`/connectors/${enc(id)}/archive`, { method: "DELETE" });
}

// ── Messages (iMessage): "Hand to Persona" and deleting what the hub kept (iOS MessagesConnectorView) ──

/** GET /api/v1/imessage: the Mac's status; `ai` is "Hand to Persona" (absent from a hub before it). */
export interface IMessageStatus {
  machine: string;
  status: string;
  error?: string;
  guide?: string[];
  via?: string;
  ai?: boolean;
}

export function imessageStatus(): Promise<IMessageStatus> {
  return api<IMessageStatus>("/imessage");
}

/** "Hand to Persona" on or off: off stops at once, what the hub kept stays until deleteIMessageData. */
export function setIMessageAI(on: boolean): Promise<IMessageStatus> {
  return api<IMessageStatus>("/imessage/settings", { method: "PUT", json: { ai: on } });
}

/** What the hub kept of iMessage (knowledge base chats and people, inbox items); the hub refuses while "Hand to Persona" is on. */
export function deleteIMessageData(): Promise<ConnectorDeleted> {
  return api<ConnectorDeleted>("/imessage/data", { method: "DELETE" });
}

/** The words under the "Hand to Persona" switch, as iOS says them. */
export function imessageAIFooter(on: boolean): string {
  return on ? t("lib-connectors.imessage_ai_on") : t("lib-connectors.imessage_ai_off");
}

export function githubAuth(): Promise<GitHubAuth> {
  return api<GitHubAuth>("/connectors/github/auth");
}

/** Where GitHub's token comes from: a machine's gh login, or the device flow (machine null). */
export function setGitHubAuth(machine: string | null): Promise<GitHubAuth> {
  return api<GitHubAuth>("/connectors/github/auth", { method: "PUT", json: machine ? { source: "machine", machine } : { source: "device" } });
}

// ── threads (an inbox item opened) ───────────────────────

export interface ThreadPerson {
  id?: string;
  name: string;
  avatar?: string;
  avatar_url?: string;
}

export interface ThreadMedia {
  kind: string; // image | sticker | video | voice | file
  ref?: string;
  thumb?: string;
  mime?: string;
  name?: string;
  size?: number;
  duration?: number;
  width?: number;
  height?: number;
  emoji?: string;
}

export interface ThreadMessage {
  id: string;
  at: string;
  sender: ThreadPerson;
  mine?: boolean;
  event?: boolean;
  text?: string;
  format?: string; // "" | markdown
  media?: ThreadMedia[];
  reply_to?: { id: string; from?: string; text?: string };
  edited?: boolean;
  url?: string;
  kind?: string; // "" | review | code | check
  review?: string; // approved | changes_requested | commented | dismissed
  code?: { path: string; line?: number; hunk?: string };
  check?: { name: string; state: string; url?: string; log?: string };
}

export interface ThreadChat {
  id: string;
  name: string;
  kind: string; // dm | group | channel | pr | issue
  url?: string;
  avatar?: string;
  avatar_url?: string;
  members?: number;
  /** The action that answers here; absent: the user cannot answer in this thread. */
  reply?: string;
  state?: string;
  author?: ThreadPerson;
  labels?: { name: string; color?: string }[];
  branch?: { head: string; base: string };
  body?: string;
  body_format?: string;
}

export interface ThreadPage {
  chat: ThreadChat;
  messages: ThreadMessage[];
  before?: string;
}

export function isChatThread(c?: ThreadChat): boolean {
  return c?.kind === "dm" || c?.kind === "group" || c?.kind === "channel";
}

export function isGitHubThread(c?: ThreadChat): boolean {
  return c?.kind === "pr" || c?.kind === "issue";
}

export const THREAD_PAGE = 30;

/** `manager`: whose connector it is (an inbox item's, docs/assistant/managers.md "Clients"; none: the default's). */
export function loadThread(id: string, thread: string, before = "", manager?: string): Promise<ThreadPage> {
  const q = new URLSearchParams({ thread, limit: String(THREAD_PAGE) });
  if (before) q.set("before", before);
  return api<ThreadPage>(`/connectors/${enc(id)}/thread?${q}`, { headers: itemHeaders(manager) });
}

export function replyInThread(id: string, body: { thread: string; action: string; text: string; reply_to?: string }, manager?: string): Promise<unknown> {
  return api(`/connectors/${enc(id)}/thread/reply`, { method: "POST", json: body, headers: itemHeaders(manager) });
}

/** A media ref's URL under /api/ (an <img> adds the token with withToken). */
export function mediaURL(id: string, ref: string): string {
  return `/api/v1/connectors/${enc(id)}/media?ref=${encodeURIComponent(ref)}`;
}

/** An older page put before what is on screen, one copy per id, the page's chat the newest. */
export function mergeOlder(have: ThreadPage, older: ThreadPage): ThreadPage {
  const ids = new Set(have.messages.map((m) => m.id));
  return { chat: have.chat, messages: [...older.messages.filter((m) => !ids.has(m.id)), ...have.messages], before: older.before };
}

/** The newest page again (after a reply): its messages replace the newest ones, the older ones stay. */
export function mergeNewest(have: ThreadPage | null, newest: ThreadPage): ThreadPage {
  if (!have || !newest.messages.length) return newest;
  const first = newest.messages[0].id;
  const cut = have.messages.findIndex((m) => m.id === first);
  if (cut < 0) return newest;
  return { chat: newest.chat, messages: [...have.messages.slice(0, cut), ...newest.messages], before: have.before };
}

/** Whether a message starts a new group (another sender, or more than five minutes after the last). */
export function startsGroup(m: ThreadMessage, prev?: ThreadMessage): boolean {
  if (!prev || prev.event || m.event || m.reply_to) return true;
  if ((prev.sender.id || prev.sender.name) !== (m.sender.id || m.sender.name) || !!prev.mine !== !!m.mine) return true;
  return Date.parse(m.at) - Date.parse(prev.at) > 5 * 60_000;
}

/** A file / voice message described: example.pdf · 278 KB, voice 0:12. */
export function mediaLine(m: ThreadMedia): string {
  const dur = m.duration ? `${Math.floor(m.duration / 60)}:${String(m.duration % 60).padStart(2, "0")}` : "";
  if (m.kind === "voice") return [t("lib-connectors.media_voice"), dur].filter(Boolean).join(" ");
  if (m.kind === "video") return [t("lib-connectors.media_video"), dur].filter(Boolean).join(" ");
  // MB from 1023.5 KB, where the KB figure would round to 1024.
  const size = m.size ? (m.size >= 1023.5 * 1024 ? `${(m.size / (1 << 20)).toFixed(1)} MB` : `${Math.max(1, Math.round(m.size / 1024))} KB`) : "";
  return [m.name || t("lib-connectors.media_file"), size].filter(Boolean).join(" · ");
}

/** GitHub's state of a pull request / issue in a word. */
export function githubStateLabel(state?: string, kind?: string): string {
  switch (state) {
    case "open":
      return t("lib-connectors.gh_state_open");
    case "merged":
      return t("lib-connectors.gh_state_merged");
    case "closed":
      return kind === "issue" ? t("lib-connectors.gh_state_closed") : t("lib-connectors.gh_state_closed_unmerged");
    case "draft":
      return t("lib-connectors.gh_state_draft");
    default:
      return state ?? "";
  }
}

export function reviewLabel(review?: string): string {
  switch (review) {
    case "approved":
      return t("lib-connectors.review_approved");
    case "changes_requested":
      return t("lib-connectors.review_changes_requested");
    case "dismissed":
      return t("lib-connectors.review_dismissed");
    default:
      return t("lib-connectors.review_commented");
  }
}

export function checkLabel(state: string): string {
  switch (state) {
    case "success":
      return t("lib-connectors.check_success");
    case "failure":
      return t("lib-connectors.check_failure");
    case "pending":
      return t("lib-connectors.check_pending");
    case "skipped":
      return t("lib-connectors.check_skipped");
    case "cancelled":
      return t("lib-connectors.check_cancelled");
    default:
      return state;
  }
}

/** A label's own color (six hex digits) or none. */
export function labelColor(color?: string): string | undefined {
  return color && /^[0-9a-f]{6}$/i.test(color) ? `#${color}` : undefined;
}

/** Pages a jump to a quote's message reads back at most before giving up (iOS ConnectorThreadModel.jumpPages). */
export const QUOTE_JUMP_PAGES = 3;

/**
 * The line over a group's or channel's conversation, as iOS ThreadLayout.subtitle: the kind of chat and, when the hub
 * knows (`chat.members`, first page only), how many are in it — "Group chat · 128 members". Nothing for a direct chat.
 */
export function chatHeadline(chat?: ThreadChat): string | undefined {
  const n = chat?.members && chat.members > 0 ? chat.members : 0;
  switch (chat?.kind) {
    case "group":
      return n ? t("lib-connectors.chat_group_members", { count: n, n: formatNumber(n) }) : t("lib-connectors.chat_group");
    case "channel":
      return n ? t("lib-connectors.chat_channel_members", { count: n, n: formatNumber(n) }) : t("lib-connectors.chat_channel");
    default:
      return undefined;
  }
}

/** The page with older pages put before it until message [id] is on it (at most [pages] more), and whether it is. */
export async function reachMessage(have: ThreadPage, id: string, older: (before: string) => Promise<ThreadPage>, pages = QUOTE_JUMP_PAGES): Promise<{ page: ThreadPage; found: boolean }> {
  let page = have;
  for (let i = 0; i < pages && !page.messages.some((m) => m.id === id) && page.before; i++) page = mergeOlder(page, await older(page.before));
  return { page, found: page.messages.some((m) => m.id === id) };
}
