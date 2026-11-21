// Pure helpers behind the usage sources page (docs/server/usage-windows.md "Account-pool gateways"), apart from the page so that they
// can be tested without a browser.
import { t } from "../i18n";
import { ago } from "../lib/format";
import { AdminError } from "./api";
import type { UsageSource, UsageSourceType, UsageTestResult } from "./types";

/** The intervals the page offers; the hub accepts 1 to 1440 minutes, so one set by hand in hub.json is shown too. */
export const POLL_CHOICES = [1, 5, 15, 30, 60];

export const typeLabel = (type: UsageSourceType | string): string =>
  type === "generic" ? t("admin-pages-usage.type_generic") : type === "relay_key" ? t("admin-pages-usage.type_relay_key") : t("admin-pages-usage.type_relay");

/** Both relay types are read with a key (a management key, or an ordinary API key); a generic gateway may need none. */
export const keyRequired = (type: UsageSourceType): boolean => type !== "generic";

/** The words for a failed read, by the code the hub gives (usagesource.Code*); a missing route depends on what the type reads. */
export function reasonText(code: string, type?: UsageSourceType | string): string {
  switch (code) {
    case "unauthorized":
      return t("admin-pages-usage.err_unauthorized");
    case "forbidden":
      return t("admin-pages-usage.err_forbidden");
    case "not_found":
      return type === "relay_key" ? t("admin-pages-usage.err_not_found_key") : t("admin-pages-usage.err_not_found");
    case "timeout":
      return t("admin-pages-usage.err_timeout");
    case "bad_response":
      return t("admin-pages-usage.err_bad_response");
    case "too_large":
      return t("admin-pages-usage.err_too_large");
    case "redirect":
      return t("admin-pages-usage.err_redirect");
    case "server_error":
      return t("admin-pages-usage.err_server_error");
    case "bad_config":
      return t("admin-pages-usage.err_bad_config");
    default:
      return t("admin-pages-usage.err_unreachable");
  }
}

/** The errors of this page's writes that the hub words itself, in the language of the request. */
const HUB_WORDS = new Set(["invalid_type", "invalid_url", "invalid_name", "invalid_key", "key_required", "invalid_poll", "too_many", "not_found", "busy"]);
export const hubWords = (e: unknown): string | null => (e instanceof AdminError && HUB_WORDS.has(e.code) && e.message ? e.message : null);

/** What a test of the connection found, as one sentence. */
export function testText(r: UsageTestResult, type?: UsageSourceType): { ok: boolean; text: string } {
  if (!r.ok) return { ok: false, text: t("admin-pages-usage.test_failed", { reason: reasonText(r.code, type) }) };
  if (r.quota_hidden) return { ok: true, text: t("admin-pages-usage.test_hidden") };
  if (r.windows === 0) return { ok: true, text: t("admin-pages-usage.test_none", { listed: r.listed }) };
  const providers = Object.entries(r.providers)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([name, n]) => `${name} ${n}`)
    .join(" · ");
  const head = t("admin-pages-usage.test_ok", { accounts: t("admin-pages-usage.n_accounts", { count: r.accounts }), windows: t("admin-pages-usage.n_windows", { count: r.windows }) });
  return { ok: true, text: providers ? `${head} (${providers})` : head };
}

export type SourceTone = "green" | "red" | "orange" | "gray";

/** How a source is doing: off, not read yet, working (with what it saw) or failing (with why). */
export function sourceStatus(s: UsageSource, globalOn: boolean, now = Date.now()): { tone: SourceTone; label: string; detail?: string } {
  if (!s.enabled) return { tone: "gray", label: t("admin-pages-usage.status_off") };
  if (!globalOn) return { tone: "gray", label: t("admin-pages-usage.status_paused") };
  const st = s.status;
  const seen = st.seen?.quota_hidden
    ? t("admin-pages-usage.quota_hidden")
    : st.seen
      ? `${t("admin-pages-usage.n_accounts", { count: st.seen.accounts })} · ${t("admin-pages-usage.n_windows", { count: st.seen.windows })}`
      : "";
  if (st.error)
    return { tone: "red", label: t("admin-pages-usage.status_failed"), detail: st.ok_at ? `${reasonText(st.error, s.type)} · ${t("admin-pages-usage.last_ok", { ago: ago(st.ok_at, now) })}` : reasonText(st.error, s.type) };
  if (!st.ok_at) return { tone: "gray", label: t("admin-pages-usage.status_never") };
  return { tone: "green", label: t("admin-pages-usage.status_ok"), detail: `${seen} · ${t("admin-pages-usage.read_ago", { ago: ago(st.ok_at, now) })}` };
}

/** A usable address: http(s) with a host, and no credentials in it (the key has its own field). */
export function validUrl(raw: string): boolean {
  try {
    const u = new URL(raw.trim());
    return (u.protocol === "http:" || u.protocol === "https:") && u.hostname !== "" && !u.username && !u.password;
  } catch {
    return false;
  }
}

/** The address as the hub keeps it (no query, fragment or trailing slash), to tell whether an edit moved it. */
export function normalUrl(raw: string): string {
  try {
    const u = new URL(raw.trim());
    u.search = "";
    u.hash = "";
    return u.href.replace(/\/+$/, "");
  } catch {
    return raw.trim();
  }
}

/** Whether saving needs the key typed again: the source now points elsewhere (or is another type) than the saved one. */
export function needsKeyAgain(saved: Pick<UsageSource, "url" | "type">, form: { url: string; type: UsageSourceType }): boolean {
  return normalUrl(form.url) !== normalUrl(saved.url) || form.type !== saved.type;
}
