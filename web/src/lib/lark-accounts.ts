// Several Feishu accounts on the hub's Lark gateway (docs/extensions/feishu.md "Multiple accounts", iOS LarkAccounts.swift). A request about
// another account than the hub's own (lark.account) names it with `ol_account`; the media URLs the hub hands out for
// such an account already carry it. The "Messages" page shows one account's chats at a time: the choice is kept per browser.
import { t } from "../i18n";
import { useSyncExternalStore } from "react";
import { api, ApiError } from "./api";
import { withManager } from "./manager-choice";

/** GET /api/v1/lark/accounts: one account signed in on the hub's Lark gateway. */
export interface LarkAccount {
  id: number;
  name?: string;
  avatar?: string;
  avatar_url?: string;
  tenant?: string;
  user_id?: string;
  signed_in: boolean;
  primary?: boolean;
  unread: number;
  quarantined?: boolean;
}

export interface LarkAccountList {
  accounts: LarkAccount[];
  /** lark.account: the one a request without `ol_account` is about. */
  default: number;
}

/** `path` (`/lark/…`, with or without a query) naming account `ol`, and `manager` when the chat is another manager's
 * (an inbox item's stamp, docs/assistant/managers.md "Clients": the hub reads `?manager=`); unchanged for the hub's own account
 * (undefined) and the default manager, or for what it names already. */
export function larkPath(path: string, ol?: number, manager?: string): string {
  const named = ol === undefined || /[?&]ol_account=/.test(path) ? path : `${path}${path.includes("?") ? "&" : "?"}ol_account=${ol}`;
  return withManager(named, manager);
}

export const accountTitle = (a: Pick<LarkAccount, "id" | "name">) => a.name?.trim() || t("lib-lark-accounts.account_fallback", { id: a.id });

/** What an item's or a chat's `ol_account` is: a positive account number, else the hub's own (undefined). */
export function olAccount(v: unknown): number | undefined {
  return typeof v === "number" && Number.isInteger(v) && v > 0 ? v : undefined;
}

// ── the choice ───────────────────────────────────────────

const CHOICE_KEY = "lark.account";

export interface LarkAccountsState {
  /** Signed-in accounts in the Lark gateway's order; empty from a hub that has no account list (one account). */
  accounts: LarkAccount[];
  default?: number;
  /** The account the "Messages" page shows: the chosen one while it is signed in, else the hub's own. */
  selected?: number;
  /** What the "Messages" page names as `ol_account`: undefined for the hub's own account. */
  request?: number;
  /** Whose chats they are, shown beside Lark while more than one account is signed in. */
  selectedName?: string;
}

export interface LarkAccountsDeps {
  list: () => Promise<LarkAccountList>;
  load: () => number | undefined;
  save: (id: number | undefined) => void;
}

export const liveAccountsDeps: LarkAccountsDeps = {
  list: () => api<LarkAccountList>("/lark/accounts"),
  load: () => {
    try {
      return olAccount(Number(localStorage.getItem(CHOICE_KEY)));
    } catch {
      return undefined;
    }
  },
  save: (id) => {
    try {
      if (id === undefined) localStorage.removeItem(CHOICE_KEY);
      else localStorage.setItem(CHOICE_KEY, String(id));
    } catch {
      // a browser that keeps nothing: the choice lasts this tab
    }
  },
};

export class LarkAccountsStore {
  private list: LarkAccount[] = [];
  private def?: number;
  private chosen?: number;
  private s: LarkAccountsState = { accounts: [] };
  private listeners = new Set<() => void>();
  /** Reads of the list, numbered: the latest sent and the latest applied. */
  private asked = 0;
  private applied = 0;
  constructor(private deps: LarkAccountsDeps = liveAccountsDeps) {
    this.chosen = deps.load();
  }

  get state(): LarkAccountsState {
    return this.s;
  }
  subscribe = (l: () => void) => {
    this.listeners.add(l);
    return () => {
      this.listeners.delete(l);
    };
  };

  private publish() {
    const selected = this.chosen !== undefined && this.list.some((a) => a.id === this.chosen) ? this.chosen : this.def;
    const request = selected !== undefined && selected !== this.def ? selected : undefined;
    const current = this.list.find((a) => a.id === selected);
    const next: LarkAccountsState = {
      accounts: this.list, default: this.def, selected, request,
      selectedName: this.list.length > 1 && current ? accountTitle(current) : undefined,
    };
    const same = next.selected === this.s.selected && next.request === this.s.request && next.default === this.s.default
      && next.selectedName === this.s.selectedName && JSON.stringify(next.accounts) === JSON.stringify(this.s.accounts);
    if (same) return;
    this.s = next;
    this.listeners.forEach((l) => l());
  }

  select(id: number) {
    if (id === this.s.selected) return;
    this.chosen = id;
    this.deps.save(id);
    this.publish();
  }

  /** Reads the list again (unread counts change with every message). A hub from before several accounts answers 404:
   * no chips, the hub's own account. Other failures keep the last list. */
  async refresh(): Promise<void> {
    // an answer to a read sent before the one already applied is older (bursts of pushes overlap): it keeps out
    const mine = ++this.asked;
    try {
      const r = await this.deps.list();
      if (mine < this.applied) return;
      this.applied = mine;
      this.list = (r.accounts ?? []).filter((a) => a.signed_in && !a.quarantined);
      this.def = r.default;
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 404) || mine < this.applied) return;
      this.applied = mine;
      this.list = [];
      this.def = undefined;
    }
    this.publish();
  }
}

export const larkAccounts = new LarkAccountsStore();

export function useLarkAccounts(): LarkAccountsState {
  return useSyncExternalStore(larkAccounts.subscribe, () => larkAccounts.state);
}
