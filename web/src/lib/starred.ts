import { useEffect, useSyncExternalStore } from "react";
import { t } from "../i18n";
import { toast } from "../ui/atoms";
import { api, ApiError, credentialOwner, errorText, fullItem, getToken, subscribeCredentialOwner } from "./api";
import { chosenManager, defaultKnown, onDefaultManager, onManagerChoice, shownManager, stampedHeaders } from "./manager-choice";
import { setStarredRows, starKey, type Star, type StarInput, type StarSource } from "./starred-rows";
import { setStarredSummary } from "./starred-summary";
import { onHubEvent } from "./store";

const PATH = "/assistant/starred";
const MAX_TEXT = 4000;
type Ref = StarSource & { id: string };
type List = { stars: readonly Star[]; status: "loading" | "ready" | "unsupported" | "failed"; error: string; canWrite: boolean };

const blank = (): List => ({ stars: [], status: "loading", error: "", canWrite: false });
let list = blank();
let index = new Map<string, Ref>();
let indexState: "pending" | "ready" | "unavailable" = "pending";
let indexWrite = false;
const pending = new Map<string, "add" | "remove">();
const listeners = new Set<() => void>();
let stop: (() => void)[] | undefined;
let detailUsers = 0;
let generation = 0;
let indexRead = 0;
let detailRead = 0;
let manager = "";

function request() {
  const credential = credentialOwner();
  const at = generation;
  const id = shownManager();
  const current = () => at === generation && credential === credentialOwner() && id === shownManager();
  return {
    current,
    options: { token: getToken(), tokenOwner: credential, headers: stampedHeaders(id), isAuthContextCurrent: current },
  };
}

function publishRows() {
  const rows = new Map<string, { id?: string; pending?: "add" | "remove" }>();
  for (const [key, ref] of index) rows.set(key, { id: ref.id });
  for (const [key, operation] of pending) rows.set(key, { id: index.get(key)?.id, pending: operation });
  setStarredRows(rows);
  setStarredSummary(indexState === "ready" ? index.size : indexState === "unavailable" ? null : undefined, indexWrite);
}

function clearIndex(state: "pending" | "unavailable") {
  index = new Map();
  indexState = state;
  indexWrite = false;
  publishRows();
}

function publishList(next: List) {
  list = next;
  listeners.forEach((l) => l());
}

const knowsManager = () => defaultKnown() || chosenManager() !== null;

async function readIndex() {
  if (!knowsManager() || !getToken()) return;
  const owner = request();
  const n = ++indexRead;
  try {
    const result = await api<{ refs: Ref[]; can_write: boolean }>(PATH + "?index=1", owner.options);
    if (n !== indexRead || !owner.current()) return;
    index = new Map(result.refs.map((ref) => [starKey(ref), ref]));
    indexState = "ready";
    indexWrite = result.can_write;
    publishRows();
  } catch (error) {
    if (n !== indexRead || !owner.current()) return;
    if (error instanceof ApiError && (error.status === 404 || error.status === 403)) clearIndex("unavailable");
  }
}

export async function reloadStarred() {
  if (!knowsManager() || !getToken()) return;
  const owner = request();
  const n = ++detailRead;
  try {
    const result = await api<{ stars: Star[]; can_write: boolean }>(PATH, owner.options);
    if (n !== detailRead || !owner.current() || !detailUsers) return;
    indexRead++;
    index = new Map(result.stars.map((star) => [starKey(star), star]));
    indexState = "ready";
    indexWrite = result.can_write;
    publishRows();
    publishList({ stars: result.stars, canWrite: result.can_write, status: "ready", error: "" });
  } catch (error) {
    if (n !== detailRead || !owner.current() || !detailUsers) return;
    if (error instanceof ApiError && (error.status === 404 || error.status === 403)) {
      indexRead++;
      clearIndex("unavailable");
    }
    if (error instanceof ApiError && error.status === 404) {
      publishList({ ...blank(), status: "unsupported" });
    } else {
      publishList({ ...list, status: list.status === "ready" ? "ready" : "failed", error: errorText(error) });
    }
  }
}

function refresh() {
  if (detailUsers) void reloadStarred();
  else void readIndex();
}

function resetOwner() {
  generation++;
  manager = shownManager();
  pending.clear();
  clearIndex("pending");
  publishList(blank());
  refresh();
}

export function startStarred() {
  if (stop) return;
  manager = shownManager();
  stop = [
    onHubEvent("hello", refresh),
    onHubEvent("assistant_starred", refresh),
    onManagerChoice(() => { if (manager !== shownManager()) resetOwner(); }),
    onDefaultManager((previous) => { if (!previous.settled && manager === shownManager()) refresh(); }),
    subscribeCredentialOwner(resetOwner),
  ];
  void readIndex();
}

export function useStarredList(): List {
  useEffect(() => {
    startStarred();
    if (detailUsers++ === 0) void reloadStarred();
    return () => {
      if (--detailUsers === 0) {
        detailRead++;
        publishList(blank()); // keep only the index when no list is being shown
      }
    };
  }, []);
  return useSyncExternalStore((l) => (listeners.add(l), () => listeners.delete(l)), () => list, () => list);
}

function bounded(input: StarInput): StarInput {
  // Code points keep emoji and CJK intact, and match the manager's rune limit.
  let text = "";
  let length = 0;
  for (const point of input.text) {
    if (length++ < MAX_TEXT) text += point;
  }
  return { ...input, text, omitted_chars: (input.omitted_chars ?? 0) + Math.max(0, length - MAX_TEXT) };
}

/** Preflight positional originals before leaving the snapshot page. The destination checks again when landing. */
export async function checkStarSource(star: Star): Promise<"verified" | "unavailable" | "failed" | "retired"> {
  const owner = request();
  try {
    const item = await fullItem(star.machine_id, star.session_id, star.item_id);
    if (!owner.current()) return "retired";
    return star.source_fingerprint && item.id === star.item_id && item.source_fingerprint === star.source_fingerprint
      ? "verified" : "unavailable";
  } catch (error) {
    if (!owner.current()) return "retired";
    return error instanceof ApiError && error.status === 404 ? "unavailable" : "failed";
  }
}

export async function addStar(input: StarInput, loadText?: () => Promise<string>): Promise<void> {
  const key = starKey(input);
  if (pending.has(key) || !knowsManager() || !getToken()) return;
  const owner = request();
  pending.set(key, "add");
  publishRows();
  try {
    const text = loadText ? await loadText() : input.text;
    if (!owner.current()) return;
    const { star } = await api<{ star: Star }>(PATH, { ...owner.options, method: "POST", json: bounded({ ...input, text }) });
    if (!owner.current()) return;
    indexRead++;
    detailRead++;
    index.set(starKey(star), star);
    if (list.status === "ready") publishList({ ...list, stars: list.stars.some((s) => s.id === star.id) ? list.stars : [star, ...list.stars] });
    toast(t("starred.saved"), "ok");
  } catch (error) {
    if (owner.current()) toast(errorText(error), "error");
  } finally {
    if (owner.current()) {
      pending.delete(key);
      publishRows();
      refresh();
    }
  }
}

export async function removeStar(ref: Ref): Promise<boolean> {
  const key = starKey(ref);
  if (pending.has(key) || !knowsManager() || !getToken()) return false;
  const owner = request();
  pending.set(key, "remove");
  publishRows();
  try {
    const { removed } = await api<{ removed: Star | null }>(`${PATH}/${encodeURIComponent(ref.id)}`, { ...owner.options, method: "DELETE" });
    if (!owner.current()) return false;
    indexRead++;
    detailRead++;
    if (index.get(key)?.id === ref.id) index.delete(key);
    if (list.status === "ready") publishList({ ...list, stars: list.stars.filter((s) => s.id !== ref.id) });
    if (removed) toast(t("starred.removed"), "info", {
      label: t("starred.undo"),
      run: () => { if (owner.current()) void addStar(removed); },
    });
    return true;
  } catch (error) {
    if (owner.current()) toast(errorText(error), "error");
    return false;
  } finally {
    if (owner.current()) {
      pending.delete(key);
      publishRows();
      refresh();
    }
  }
}

export function resetStarredForTest() {
  stop?.forEach((off) => off());
  stop = undefined;
  generation++;
  detailUsers = 0;
  pending.clear();
  clearIndex("pending");
  publishList(blank());
}
