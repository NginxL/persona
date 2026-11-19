// A Codex "visualize" fragment (#10321): a self-contained HTML file the node wrote under its visualizations
// directory, fetched through the session's visualization route and run in the same offline QuickJS sandbox as
// assistant HTML cards (ui/HtmlCard.tsx). The fragments get no bridge: every persona.* call is refused. A wide
// fragment may take 1024 px, another 736; the frame follows what it draws up to 640 px tall, and off screen the
// runtime is torn down while the fetched bytes stay cached, so several on one turn stay cheap.
import { useCallback, useEffect, useRef, useState } from "react";
import { currentLanguage, t } from "../../i18n";
import { visualizationHTML } from "../../lib/api";
import { baseName } from "../../lib/format";
import { claimSlot, releaseSlot, runCard, type RunningCard } from "../../lib/html-runtime/host";
import { FRAME_MIN, frameHeight, HEIGHT_MAX } from "../../lib/html-runtime/height";
import { HIDDEN_GRACE_MS } from "../../lib/html-runtime/limits";
import { FRAME_SANDBOX } from "../../lib/html-runtime/sanitize";
import type { Item } from "../../lib/types";
import { Button, cx, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

const REGULAR_MAX = 736;
const WIDE_MAX = 1024;
const SLOT_RETRY_MS = 1000;

type Phase = "idle" | "loading" | "running" | "crashed";

export default function VisualizationCard({ item, mid, sid }: { item: Item; mid: string; sid: string }) {
  const viz = item.visualization;
  const wide = viz?.mode === "wide";
  const label = viz?.title || t("chat-visualization.frame");

  const box = useRef<HTMLDivElement>(null);
  const frame = useRef<HTMLIFrameElement>(null);
  const [visible, setVisible] = useState(false);
  const [html, setHtml] = useState<string | null>(null);
  const [fetchFailed, setFetchFailed] = useState(false);
  const [phase, setPhase] = useState<Phase>("idle");
  // how tall what the running fragment draws is, null until it says
  const [content, setContent] = useState<number | null>(null);
  const slot = useRef<ReturnType<typeof claimSlot>>(null);
  const run = useRef<RunningCard | null>(null);
  // a fetch started by a run that hide/show interrupted is still the one fetch: never start a second
  const fetching = useRef(false);
  const alive = useRef(true);

  const halt = useCallback(() => {
    run.current?.stop();
    run.current = null;
    releaseSlot(slot.current);
    slot.current = null;
    if (!alive.current) return;
    setPhase("idle");
    setContent(null);
  }, []);

  // on-screen state; leaving tears the runtime down, the fetched bytes stay cached
  useEffect(() => {
    alive.current = true;
    const el = box.current;
    let hide: ReturnType<typeof setTimeout> | undefined;
    const io = new IntersectionObserver(([e]) => {
      clearTimeout(hide);
      if (e.isIntersecting) setVisible(true);
      else hide = setTimeout(() => setVisible(false), HIDDEN_GRACE_MS);
    });
    if (el) io.observe(el);
    return () => {
      io.disconnect();
      clearTimeout(hide);
      alive.current = false;
      halt();
    };
  }, [halt]);

  // on screen: take a runtime slot (waiting for one), then fetch the fragment once
  useEffect(() => {
    if (!visible) {
      if (phase === "loading" || phase === "running") halt();
      return;
    }
    if (phase !== "idle" || slot.current || fetching.current || fetchFailed) return;
    let retry: ReturnType<typeof setTimeout> | undefined;
    const begin = async () => {
      const s = claimSlot(() => halt());
      if (!s) {
        retry = setTimeout(begin, SLOT_RETRY_MS);
        return;
      }
      slot.current = s;
      setPhase("loading");
      let text = html;
      if (text === null) {
        fetching.current = true;
        try {
          text = await visualizationHTML(mid, sid, item.id);
        } catch {
          fetching.current = false;
          if (!alive.current) return;
          releaseSlot(slot.current);
          slot.current = null;
          setFetchFailed(true);
          setPhase("idle");
          return;
        }
        fetching.current = false;
        if (!alive.current) return;
        setHtml(text);
      }
    };
    void begin();
    return () => clearTimeout(retry);
  }, [visible, phase, html, fetchFailed, mid, sid, item.id, halt]);

  // the frame mounts once the bytes are in: start the sandbox runtime in it
  useEffect(() => {
    if (html === null || phase !== "loading" || run.current || !frame.current) return;
    const f = frame.current;
    const card = runCard({
      frame: f,
      html,
      info: { width: Math.round(f.clientWidth) || 320, height: HEIGHT_MAX, dpr: window.devicePixelRatio || 1, lang: currentLanguage(), dark: document.documentElement.classList.contains("dark") },
      // visualization fragments are offline artifacts: no reply/open bridge, so every call is refused
      onBridge: async () => false,
      onState: (s) => {
        if (!alive.current || !run.current) return;
        if (s.kind === "running") setPhase("running");
        else if (s.kind === "stopped" && s.reason === "crash") setPhase("crashed");
        // another stop hit a runtime limit: the frame's last drawing stays, frozen
      },
      onHeight: (h) => alive.current && setContent(h),
      onError: () => alive.current && setPhase("crashed"),
    });
    run.current = card;
  }, [html, phase]);

  // a frame that never says how tall its content is is shown at its most after a second
  useEffect(() => {
    if (phase !== "running" || content !== null) return;
    const timer = setTimeout(() => setContent((c) => c ?? HEIGHT_MAX), 1000);
    return () => clearTimeout(timer);
  }, [phase, content]);

  const failed = fetchFailed || phase === "crashed";
  const dark = typeof document !== "undefined" && document.documentElement.classList.contains("dark");
  return (
    <div ref={box} className="card-ui w-full overflow-hidden rounded-card bg-surface shadow-card" data-visualization={wide ? "wide" : "regular"} data-visualization-state={failed ? "failed" : phase} style={{ maxWidth: wide ? WIDE_MAX : REGULAR_MAX }}>
      <div className="flex min-w-0 items-center gap-2 border-b border-line py-1 pr-1.5 pl-4 text-submeta text-ink-3">
        <Icon name="chart" size={12} className="shrink-0" />
        <span className="min-w-0 flex-1 truncate">{label}</span>
        {phase === "loading" && <Spinner size={11} />}
      </div>
      {failed ? (
        <div className="flex flex-col items-start gap-2 px-4 py-3">
          <p className="text-meta leading-relaxed break-words text-ink-2 select-text">
            {t("chat-visualization.failed")}
            {viz?.path ? <span className="text-ink-3"> {baseName(viz.path)}</span> : null}
          </p>
          <Button tone="quiet" icon="retry" onClick={() => { setFetchFailed(false); halt(); }}>
            <span className="truncate">{t("chat-visualization.retry")}</span>
          </Button>
        </div>
      ) : phase === "idle" || html === null ? (
        <div className="flex items-center gap-2 px-4 py-6 text-meta text-ink-3">
          <Spinner size={13} />
          <span>{t("chat-visualization.loading")}</span>
        </div>
      ) : (
        <div className="relative" style={{ height: content === null ? FRAME_MIN : frameHeight(content, HEIGHT_MAX) }}>
          <iframe ref={frame} sandbox={FRAME_SANDBOX} referrerPolicy="no-referrer" title={label} style={{ colorScheme: dark ? "dark" : "light" }} className={cx("block size-full border-0 bg-transparent", (phase === "loading" || content === null) && "opacity-0")} />
        </div>
      )}
    </div>
  );
}
