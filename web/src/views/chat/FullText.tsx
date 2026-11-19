// "View full text (12 KB)" under a text the hub cut for the page (Item.full_size; Android FullTextButton, iOS ResultBlock's
// cut): a click fetches the whole item (GET …/items/{id}/full) and it takes the cut one's place; "Loading…" meanwhile,
// "Failed to load, tap to retry" when it fails. The hub's own note at the end of the cut text goes: the button says it.
import { useRef, useState, type ReactNode } from "react";
import { fullItem } from "../../lib/api";
import { bytes } from "../../lib/format";
import { withoutCutMark } from "../../lib/tool-detail";
import type { Item } from "../../lib/types";
import { cx, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { t } from "../../i18n";

/** The visible text and full-text control. `getText` shares that control's pending load and cached complete text. */
export function useFullText(item: Item | undefined, mid: string | undefined, sid: string | undefined): { text: string; more: ReactNode; getText: () => Promise<string> } {
  const key = item?.full_size ? JSON.stringify([mid, sid, item.id, item.text, item.full_size]) : "";
  const current = useRef(key);
  current.current = key;
  const pending = useRef<{ key: string; promise: Promise<string> } | null>(null);
  const [whole, setWhole] = useState<{ key: string; text: string } | null>(null);
  const [status, setStatus] = useState<{ key: string; state: "idle" | "loading" | "failed" }>();
  const state = status?.key === key ? status.state : "idle";
  const raw = item?.text ?? "";
  const size = item?.full_size;
  const getText = (): Promise<string> => {
    if (whole?.key === key) return Promise.resolve(whole.text);
    if (!size) return Promise.resolve(raw);
    if (!item || !mid || !sid) return Promise.reject(new Error(t("chat-fulltext.failed")));
    if (pending.current?.key === key) return pending.current.promise;
    setStatus({ key, state: "loading" });
    const promise = fullItem(mid, sid, item.id).then((it) => {
      const text = it.text ?? "";
      if (current.current === key) {
        setWhole({ key, text });
        setStatus({ key, state: "idle" });
      }
      return text;
    }, (error: unknown) => {
      if (current.current === key) setStatus({ key, state: "failed" });
      throw error;
    }).finally(() => {
      if (pending.current?.promise === promise) pending.current = null;
    });
    pending.current = { key, promise };
    return promise;
  };
  if (whole?.key === key) return { text: whole.text, more: null, getText };
  if (!size || !mid || !sid) return { text: raw, more: null, getText };
  return {
    text: withoutCutMark(raw),
    more: <FullTextButton size={size} state={state} onClick={() => { void getText().catch(() => {}); }} />,
    getText,
  };
}

function FullTextButton({ size, state, onClick }: { size: number; state: "idle" | "loading" | "failed"; onClick: () => void }) {
  const loading = state === "loading";
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation();
        if (!loading) onClick();
      }}
      disabled={loading}
      aria-busy={loading || undefined}
      className={cx(
        "mt-1.5 flex h-7 w-fit items-center gap-1.5 rounded-control px-1 font-sans text-[12.5px] font-medium whitespace-nowrap transition-colors hover:bg-hover disabled:opacity-60",
        state === "failed" ? "text-red" : "text-accent-ink",
      )}
      data-full-text={state}
    >
      {loading ? <Spinner size={12} /> : <Icon name={state === "failed" ? "retry" : "external"} size={12} />}
      {loading ? t("chat-fulltext.loading") : state === "failed" ? t("chat-fulltext.failed") : t("chat-fulltext.show_all", { size: bytes(size) })}
    </button>
  );
}
