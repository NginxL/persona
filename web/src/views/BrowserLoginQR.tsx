import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { deviceName, forgetUnrevoked, unrevokedTokenId } from "../lib/auth";
import { errorText, setPairedToken } from "../lib/api";
import { BASE } from "../lib/base";
import { storedCredentials } from "../lib/compatibility";
import { t } from "../i18n";
import { Button, Spinner } from "../ui/atoms";

const QRCode = lazy(() => import("./settings/LoginSteps").then((m) => ({ default: m.QRCode })));
type Challenge = { id: string; secret: string; expires_at: string; qr: { rows: string[] } };
type Credential = { state: string; token?: string; id?: string };

class BrowserLoginRequestError extends Error {
  constructor(readonly status: number, readonly code = "", readonly state = "") {
    super(status === 410 ? t("login.qr_expired") : t("login.qr_failed"));
  }
}

// Sign in by scanning a code with Persona on a phone (docs/protocol/protocol.md "Browser QR sign-in"): the phone allows
// it and shows a two-digit number, which is typed here. One try: a wrong number cancels the request.
export function BrowserLoginQR({ onDone }: { onDone: () => void }) {
  const [generation, refresh] = useState(0);
  const [challenge, setChallenge] = useState<Challenge>();
  const [state, setState] = useState("loading");
  const [error, setError] = useState("");
  const [number, setNumber] = useState("");
  const [sending, setSending] = useState(false);
  const confirm = useRef<(n: string) => Promise<void>>(async () => {});
  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    let done = false;
    // The credential this browser had before, as pair() sends it: the hub revokes it once the new one is issued.
    const replaces = storedCredentials().tokenId || unrevokedTokenId();
    let replacing = false;
    async function request<T>(action: string, body: unknown): Promise<T> {
      const response = await fetch(`${BASE}/api/v1/auth/browser-login/${action}`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body), signal: controller.signal, cache: "no-store",
      });
      if (!response.ok) {
        const body = await response.json().catch(() => ({})) as { code?: string; state?: string };
        throw new BrowserLoginRequestError(response.status, body.code, body.state);
      }
      return response.json();
    }
    function failed(err: unknown) {
      if (controller.signal.aborted) return;
      done = true; setState("failed"); setError(errorText(err));
    }
    function finish(result: Credential): boolean {
      if (result.state === "approved" && result.token && result.id) {
        done = true; setPairedToken(result.token, result.id);
        if (replacing) forgetUnrevoked(); // the hub has had its one chance at it
        onDone(); return true;
      }
      if (result.state === "denied" || result.state === "voided") { done = true; setState(result.state); return true; }
      return false;
    }
    async function poll(c: Challenge) {
      if (controller.signal.aborted || done) return;
      if (document.visibilityState !== "hidden") {
        try {
          const result = await request<Credential>("poll", { id: c.id, secret: c.secret });
          if (controller.signal.aborted || done) return;
          if (finish(result)) return;
          setState(result.state);
        } catch (err) {
          if (controller.signal.aborted || done) return;
          if (err instanceof TypeError || err instanceof BrowserLoginRequestError && (err.status >= 500 || err.status === 429)) {
            setState((s) => (s === "confirm" ? s : "retrying"));
            timer = setTimeout(() => void poll(c), 3000);
          } else failed(err);
          return;
        }
      }
      timer = setTimeout(() => void poll(c), 1500);
    }
    setChallenge(undefined); setState("loading"); setError(""); setNumber("");
    const name = deviceName();
    (replaces
      ? request<Challenge>("start", { name, replaces }).then((c) => { replacing = true; return c; }, (err) => {
        // A hub from before #10291 refuses the unknown field: sign in without replacing, as before.
        if (err instanceof BrowserLoginRequestError && err.status === 400) return request<Challenge>("start", { name });
        throw err;
      })
      : request<Challenge>("start", { name })).then((c) => {
      if (controller.signal.aborted) return;
      confirm.current = async (n) => {
        try {
          finish(await request<Credential>("confirm", { id: c.id, secret: c.secret, number: n }));
        } catch (err) {
          if (controller.signal.aborted || done) return;
          if (err instanceof BrowserLoginRequestError && err.status === 409) finish({ state: err.state || "voided" });
          else if (err instanceof BrowserLoginRequestError && err.status === 410) { done = true; setState("expired"); }
          // A lost reply: the poll picks up the credential if the number went through.
          else if (!(err instanceof TypeError || err instanceof BrowserLoginRequestError && err.status >= 500)) failed(err);
        }
      };
      setChallenge(c); setState("pending"); void poll(c);
    }).catch(failed);
    return () => { controller.abort(); clearTimeout(timer); };
  }, [generation, onDone]);
  const ended = ["expired", "denied", "voided", "failed"].includes(state);
  const endedText = error || t(state === "denied" ? "login.qr_denied" : state === "voided" ? "login.qr_voided" : "login.qr_expired");
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (number.length !== 2 || sending) return;
    setSending(true);
    try { await confirm.current(number); } finally { setSending(false); }
  };
  return <section className="rounded-card bg-surface p-4 text-center shadow-card" aria-labelledby="login-qr-title">
    <h2 id="login-qr-title" className="text-card-title font-semibold text-ink">{t("login.qr_title")}</h2>
    <div className="my-4 flex items-center justify-center" style={{ minHeight: 184 }}>
      {state === "loading" ? <Spinner />
        : ended ? <p className="text-meta text-ink-2" role="status">{endedText}</p>
        : state === "confirm" ? <form onSubmit={submit} className="flex flex-col items-center gap-3">
          <label htmlFor="login-qr-number" className="text-meta text-ink-2">{t("login.qr_number_label")}</label>
          <span className="flex h-10 w-24 items-center gap-2 rounded-control bg-inset px-3 shadow-hairline focus-within:shadow-[0_0_0_1px_var(--line-strong)]">
            <input id="login-qr-number" value={number} onChange={(e) => setNumber(e.target.value.replace(/\D/g, "").slice(0, 2))}
              inputMode="numeric" autoComplete="one-time-code" autoFocus maxLength={2} aria-describedby="login-qr-hint"
              className="min-w-0 flex-1 bg-transparent text-center font-mono text-card-title text-ink outline-none" />
          </span>
          <Button type="submit" tone="primary" disabled={number.length !== 2 || sending}>{sending ? <Spinner /> : t("login.sign_in")}</Button>
        </form>
        : challenge ? <Suspense fallback={<Spinner />}><QRCode rows={challenge.qr.rows} label={t("login.qr_title")} /></Suspense> : null}
    </div>
    {!ended && <p id="login-qr-hint" className="text-meta text-ink-2" role="status">
      {t(state === "scanned" ? "login.qr_scanned" : state === "confirm" ? "login.qr_number_hint" : state === "retrying" ? "login.qr_retry" : "login.qr_hint")}
    </p>}
    <Button tone="secondary" type="button" className="mt-3" onClick={() => refresh((n) => n + 1)}>{t("login.qr_refresh")}</Button>
  </section>;
}
