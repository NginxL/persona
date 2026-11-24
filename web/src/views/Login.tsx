import { lazy, Suspense, useEffect, useState } from "react";
import { ApiError, errorText } from "../lib/api";
import { pair } from "../lib/auth";
import { here, rememberHub } from "../lib/hubs";
import { Button, cx, Spinner } from "../ui/atoms";
import { AppMark } from "../ui/AppMark";
import { Icon } from "../ui/icons";
import { OtherHubs } from "./Hubs";
import { t } from "../i18n";
import { tRich } from "../i18n/rich";
import type { PasswordState } from "../lib/password";

import { BrowserLoginQR } from "./BrowserLoginQR";

const PasswordLogin = lazy(() => import("./PasswordLogin").then((m) => ({ default: m.PasswordLogin })));

const code = "whitespace-nowrap rounded-[5px] bg-field px-1 py-0.5 font-mono text-[11.5px] text-ink-2 shadow-hairline";
// what went wrong, under the button of the form it came from (the link's, or the master key's)
const formError = "text-center text-[12.5px] text-red";

// Sign in without a terminal (docs/assistant/onboarding.md 4.2, 4.5): first a login code from a device that is already signed
// in (Settings › Devices › Connect a new device › Browser makes a `#token=jp_…` link: opened here it signs in by itself, App.tsx), then a
// pasted pairing / login link. The hub's own token and the commands that print it are folded under "Advanced". `error` is
// what went wrong with a token that came in the link. Each form says what went wrong under its own button, where the eye
// is, and editing its field takes that away (web usability audit item 41).
export function Login({ onDone, error: linkError = "" }: { onDone: () => void; error?: string }) {
  const [passwordMode, setPasswordMode] = useState(false);
  useEffect(() => {
    let live = true;
    import("../lib/password").then((m) => m.passwordApi<PasswordState>("/state")).then((s) => { if (live) setPasswordMode(s.enabled); }).catch(() => {});
    return () => { live = false; };
  }, []);
  const [link, setLink] = useState("");
  const [master, setMaster] = useState("");
  const [busy, setBusy] = useState<"" | "link" | "master">("");
  // the link form's error (a link's token that didn't work starts there) and the master key's
  const [error, setError] = useState(linkError);
  const [masterError, setMasterError] = useState("");
  const [panel, setPanel] = useState<"qr" | "help" | "advanced" | null>(null);
  useEffect(() => void rememberHub(here()), []);

  const submit = (from: "link" | "master") => async (e: React.FormEvent) => {
    e.preventDefault();
    const tok = (from === "link" ? link : master).trim();
    if (!tok || busy) return;
    setBusy(from);
    const fail = from === "link" ? setError : setMasterError;
    fail("");
    try {
      await pair(tok);
      onDone();
    } catch (err) {
      const msg = errorText(err);
      const invalid = (err instanceof ApiError && err.status === 401) || msg === "invalid token";
      fail(invalid ? t(from === "link" ? "login.invalid_link" : "login.invalid_token") : msg);
    } finally {
      setBusy("");
    }
  };

  const field = "flex min-h-11 items-center gap-2 rounded-control bg-inset px-3 shadow-hairline focus-within:shadow-[0_0_0_1px_var(--line-strong)]";
  const input = "min-w-0 flex-1 bg-transparent font-mono text-meta text-ink outline-none placeholder:font-sans placeholder:text-ink-3";

  if (passwordMode) return <div><Suspense fallback={<Spinner />}><PasswordLogin onDone={onDone} error={linkError} /></Suspense><button type="button" onClick={() => setPasswordMode(false)} className="mx-auto mb-6 block text-ink-2">{t("login.password_pairing")}</button></div>;

  return (
    <div className="flex min-h-full items-center justify-center bg-page px-5 py-16">
      <div className="w-full max-w-[380px]" style={{ animation: "fade-up 450ms cubic-bezier(0.23,1,0.32,1) both" }}>
        <header className="mb-7 flex flex-col items-center gap-4 text-center">
          <AppMark size={80} radius={20} className="shadow-raised" />
          <h1 id="login-title" className="text-2xl font-semibold tracking-tight text-ink">{t("login.title")}</h1>
        </header>

        <div className="rounded-card bg-surface shadow-card">
          <form onSubmit={submit("link")} aria-labelledby="login-title" className="p-2">
            <div className="flex items-center gap-2">
              <label className={cx(field, "min-w-0 flex-1")}>
                <Icon name="link" size={14} className="text-ink-3" />
                <input
                  autoFocus
                  type="text"
                  value={link}
                  onChange={(e) => {
                    setLink(e.target.value);
                    setError("");
                  }}
                  placeholder={t("login.link_placeholder")}
                  aria-label={t("login.link_placeholder")}
                  aria-describedby="login-link-error"
                  autoComplete="off"
                  autoCapitalize="off"
                  spellCheck={false}
                  className={input}
                />
              </label>
              <Button type="submit" tone="primary" disabled={!link.trim() || busy !== ""} aria-label={t("login.sign_in")} title={t("login.sign_in")} className="min-h-11 w-11 shrink-0 !p-0">
                {busy === "link" ? <Spinner /> : <Icon name="arrowRight" size={20} />}
              </Button>
            </div>
            <p id="login-link-error" className={`mt-2 empty:hidden ${formError}`} role="alert">{error}</p>
          </form>
        </div>

        <div className="mt-5 flex justify-center gap-3">
          {([
            ["qr", "phone", "login.qr_short", "login.qr_open"],
            ["help", "info", "login.help_short", "login.get_link"],
            ["advanced", "key", "login.key_short", "login.advanced"],
          ] as const).map(([id, icon, caption, name]) => (
            <button key={id} type="button" title={t(name)}
              aria-expanded={panel === id} aria-controls={`login-${id}`}
              onClick={() => setPanel(panel === id ? null : id)}
              className="group flex min-w-16 flex-col items-center gap-1.5 rounded-control text-meta text-ink-2">
              <span className={cx("grid size-11 place-items-center rounded-full bg-surface shadow-hairline group-hover:bg-hover", panel === id && "bg-field ring-1 ring-current")}>
                <Icon name={icon} size={20} />
              </span>
              {t(caption)}
            </button>
          ))}
        </div>
        <div id="login-qr" className="mt-4" hidden={panel !== "qr"}>
          {panel === "qr" && <BrowserLoginQR onDone={onDone} />}
        </div>
        <div id="login-help" hidden={panel !== "help"} className="mt-4 rounded-card bg-surface p-4 text-meta text-ink-2 shadow-card">
          <h2 className="mb-2 font-semibold text-ink">{t("login.get_link")}</h2>
          <ol className="list-decimal space-y-2 pb-2 pl-5 text-meta leading-relaxed text-ink-2">
            <li>{tRich("login.phone_step_open", { path: <strong className="font-medium text-ink">{t("login.phone_path")}</strong> })}</li>
            <li>{t("login.phone_step_copy")}</li>
          </ol>
          <p className="pb-3 text-meta leading-relaxed text-ink-2">{t("login.phone_step_scan")}</p>
          <p className="mb-3">{t("login.credential_note")}</p>
          <details>
            <summary className="cursor-pointer py-2">{t("login.no_hub")}</summary>
            <div className="space-y-1.5 px-3 pb-2 text-[12px] leading-relaxed text-ink-2">
              <p>{t("login.no_hub_what")}</p>
              <p>{t("login.no_hub_yours")}</p>
              <p>{t("login.no_hub_others")}</p>
            </div>
          </details>
        </div>
        <div id="login-advanced" hidden={panel !== "advanced"} className="mt-4 rounded-card bg-surface p-3 shadow-card">
          <form onSubmit={submit("master")} aria-label={t("login.connect")} className="px-3 pb-2">
            <p className="flex items-start gap-1.5 text-[12px] leading-relaxed text-ink-2">
              <Icon name="warning" size={13} className="mt-0.5 shrink-0 text-orange" />
              <span>{t("login.master_warning")}</span>
            </p>
            <p className="mt-1.5 text-[11.5px] leading-[1.9] text-ink-3">
              {tRich("login.master_where", {
                pair: <code className={code}>personad hub pair --legacy</code>,
                jq: <code className={code}>jq -r .token ~/.persona/hub.json</code>,
              })}
            </p>
            <div className="mt-2 rounded-card bg-surface p-1.5 shadow-card">
              <label className={field}>
                <Icon name="key" size={14} className="text-ink-3" />
                <input
                  type="password"
                  value={master}
                  onChange={(e) => {
                    setMaster(e.target.value);
                    setMasterError("");
                  }}
                  placeholder={t("login.master_placeholder")}
                  aria-label={t("login.master_placeholder")}
                  autoComplete="off"
                  className={input}
                />
              </label>
              <Button type="submit" tone="secondary" disabled={!master.trim() || busy !== ""} className="mt-1.5 min-h-11 w-full">
                {busy === "master" ? <Spinner /> : t("login.connect")}
              </Button>
            </div>
            <p className={`mt-2 ${formError}`} role="alert">
              {masterError}
            </p>
          </form>
        </div>
        <OtherHubs />
      </div>
    </div>
  );
}
