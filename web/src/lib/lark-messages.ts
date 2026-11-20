// The Feishu chat's messages as the hub sends them, and their order on a page. lib/inbox.ts re-exports them until its
// importers import them from here (#11538).

// ── Feishu messages (docs/protocol/protocol.md "Lark API") ──────

export interface LarkPart {
  kind: string;
  text?: string;
  url?: string;
  refId?: string;
  metadata?: Record<string, unknown>;
}

export interface LarkMessage {
  id: string;
  position: string;
  sender_id?: string;
  sender_name?: string;
  sender_avatar?: string;
  sender_type?: string;
  is_mine?: boolean;
  type?: string;
  text?: string;
  created_at?: string;
  recalled?: boolean;
  /** The Lark gateway kept a recalled message's content (lark-display.ts): shown only with "Keep recalled messages" on, still marked "Recalled". */
  retained?: boolean;
  edited?: boolean;
  /** An edited message's earlier versions, oldest first (lark-display.ts, shown with "Edit history" on). */
  edit_history?: { edited_at: string; text: string; parts?: LarkPart[]; olk?: unknown }[];
  reply_count?: number;
  thread_reply_count?: number;
  at_me?: boolean;
  /** Read state (others' messages) and Feishu's unread badge at this message: what the read report carries. */
  is_read?: boolean;
  badge_count?: number;
  parts?: LarkPart[];
  reply_to?: { id?: string; sender_name?: string; text?: string; parts?: LarkPart[]; recalled?: boolean; retained?: boolean };
  sending?: boolean;
  failed?: boolean;
  client_id?: string;
  /** The Lark gateway's capabilities as the hub passes them on: whether a Pin may be added / removed (lark-pins.ts). */
  can_pin?: boolean;
  can_unpin?: boolean;
  deleted?: boolean;
  local_state?: string;
  /** The thread a message is in; "root", "reply" or "" (a plain message). */
  thread_id?: string;
  thread_kind?: string;
  /** An encrypted message's public face is in text / parts already; its presence marks it encrypted. */
  olk?: unknown;
  /** Reactions under it (lib/lark-chat.ts): the key, how many, whether one is mine, who. */
  reactions?: { key: string; count: number; mine?: boolean; users?: string[] }[];
}

export interface LarkPage {
  messages: LarkMessage[];
  has_older?: boolean;
  has_newer?: boolean;
  older_cursor?: string;
  newer_cursor?: string;
  /** A topic group: the messages are its topics' first messages (the hub finds out without being told). */
  topic?: boolean;
}

/** Oldest first by position (numeric when both are), one copy per id: a later page wins. */
export function mergeMessages(have: LarkMessage[], batch: LarkMessage[]): LarkMessage[] {
  // the hub's copy of a message this page sent replaces its local one (same client_id), which may still be waiting
  const echoed = new Set(batch.flatMap((m) => (m.client_id && !m.id.startsWith("temp-") ? [m.client_id] : [])));
  const byId = new Map(have.filter((m) => !(m.id.startsWith("temp-") && m.client_id && echoed.has(m.client_id))).map((m) => [m.id, m]));
  for (const m of batch) byId.set(m.id, m);
  return [...byId.values()].sort((a, b) => comparePosition(a.position, b.position));
}

export function comparePosition(a: string, b: string): number {
  const x = Number(a), y = Number(b);
  if (Number.isFinite(x) && Number.isFinite(y)) return x - y;
  return a.localeCompare(b);
}

/** Someone a sent message @s: Feishu notifies them; the text holds their `@name` ("Everyone" is `user_id` "all"). */
export interface LarkMention {
  user_id: string;
  name: string;
}
