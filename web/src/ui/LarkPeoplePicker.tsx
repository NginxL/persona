// @ someone on Feishu from a text box (the phone's LarkMentionPicker): typing @ and a name looks people up, ↑↓ and
// Enter (or a click) put `@name ` in the text and remember who it is. Where the people come from is the caller's
// (`find`): a document's comments ask documents/people, a chat would ask its members. lib/lark-people.ts holds the
// text side; send with `mentionsIn(text, picked)`.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { insertPerson, personAt, type LarkPerson, type PersonToken } from "../lib/lark-people";
import { EASE, Spinner } from "./atoms";
import { t } from "../i18n";
import { LarkAvatar } from "../views/inbox/InboxParts";

export type FindPeople = (query: string, signal: AbortSignal) => Promise<LarkPerson[]>;

export interface PeoplePicker {
  token: PersonToken | null;
  rows: LarkPerson[];
  loading: boolean;
  active: number;
  setActive: (i: number) => void;
  pick: (p: LarkPerson) => void;
  /**
   * For the box's onKeyDown, first: true when the picker took the key (↑↓, Enter, Tab, Esc while it is open). Enter and
   * Tab are also taken, without a pick, while the query being typed has not been answered yet.
   */
  onKeyDown: (e: React.KeyboardEvent) => boolean;
}

/**
 * The picker for a box whose text and caret the caller keeps. `apply` gets the new text and caret after a pick; the
 * person picked goes to `onPicked` (keep them for `mentionsIn`). `open` is false while the box is not focused.
 */
export function useLarkPeoplePicker({ text, caret, open, find, apply, onPicked }: { text: string; caret: number; open: boolean; find: FindPeople; apply: (text: string, caret: number) => void; onPicked: (p: LarkPerson) => void }): PeoplePicker {
  const [dismissed, setDismissed] = useState<number | null>(null);
  const found = personAt(text, caret);
  const token = open && found && dismissed !== found.start ? found : null;
  const query = token?.query;
  // the rows on screen and the query they answer: while a new query loads, the last answer stays shown (arrows still
  // move over it), but Enter / Tab only pick from the answer to the query being typed
  const [shown, setShown] = useState<{ query: string | null; rows: LarkPerson[] }>({ query: null, rows: [] });
  const rows = shown.rows;
  const [loading, setLoading] = useState(false);
  const [active, setActive] = useState(0);
  const cache = useRef(new Map<string, LarkPerson[]>());
  const findRef = useRef(find);
  findRef.current = find;

  useEffect(() => {
    if (query === undefined) return;
    setActive(0);
    const have = cache.current.get(query);
    if (have) {
      setShown({ query, rows: have });
      setLoading(false);
      return;
    }
    const ac = new AbortController();
    setLoading(true);
    const t = setTimeout(() => {
      findRef
        .current(query, ac.signal)
        .then((people) => {
          cache.current.set(query, people);
          if (!ac.signal.aborted) setShown({ query, rows: people });
        })
        .catch(() => {
          if (!ac.signal.aborted) setShown({ query, rows: [] });
        })
        .finally(() => {
          if (!ac.signal.aborted) setLoading(false);
        });
    }, 140);
    return () => {
      clearTimeout(t);
      ac.abort();
    };
  }, [query]);

  // a new @ (or the box emptied) opens it again
  const foundAt = found?.start;
  useEffect(() => {
    if (dismissed !== null && foundAt !== dismissed) setDismissed(null);
  }, [foundAt, dismissed]);

  const pick = (p: LarkPerson) => {
    if (!token) return;
    const out = insertPerson(text, token, p);
    onPicked(p);
    apply(out.text, out.caret);
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (!token || e.nativeEvent.isComposing || e.keyCode === 229) return false;
    const n = rows.length;
    switch (e.key) {
      case "ArrowDown":
      case "ArrowUp":
        if (!n) return false;
        e.preventDefault();
        setActive((i) => (i + (e.key === "ArrowDown" ? 1 : n - 1)) % n);
        return true;
      case "Enter":
      case "Tab":
        if (e.shiftKey) return false;
        if (shown.query !== token.query) {
          // still looking up what is typed now: the rows shown answer an older query, so take the key (the text is not
          // sent) without picking from them
          e.preventDefault();
          return true;
        }
        if (!n) return false;
        e.preventDefault();
        pick(rows[Math.min(active, n - 1)]);
        return true;
      case "Escape":
        e.preventDefault();
        e.stopPropagation();
        setDismissed(token.start);
        return true;
    }
    return false;
  };

  return { token, rows: token ? rows : [], loading: Boolean(token) && loading, active, setActive, pick, onKeyDown };
}

/** The menu over the box (place it in a `relative` wrapper around the box). Nothing while no `@` is being typed. */
export function LarkPeopleMenu({ picker, manager, account, label = t("ui-larkpeoplepicker.label") }: { picker: PeoplePicker; manager?: string; account?: number; label?: string }) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const [box, setBox] = useState<{ top: number; height: number } | null>(null);
  const { token, rows, active, loading } = picker;
  useLayoutEffect(() => {
    const el = refs.current[active];
    setBox(el ? { top: el.offsetTop, height: el.offsetHeight } : null);
    el?.scrollIntoView({ block: "nearest" });
  }, [active, rows.length]);
  if (!token) return null;
  return (
    <div className="absolute inset-x-0 bottom-full z-10 mb-2 rounded-[10px] bg-surface p-1 shadow-raised" style={{ animation: `pop-in 180ms ${EASE} both`, transformOrigin: "bottom center" }} role="listbox" aria-label={label}>
      <div className="relative max-h-[min(40vh,240px)] overflow-y-auto">
        <span aria-hidden className="pointer-events-none absolute inset-x-0 rounded-[6px] bg-hover" style={{ top: box?.top ?? 0, height: box?.height ?? 0, opacity: box && rows.length ? 1 : 0, transition: `top 200ms ${EASE}, height 200ms ${EASE}` }} />
        {rows.map((p, i) => (
          <button
            key={p.user_id}
            ref={(el) => {
              refs.current[i] = el;
            }}
            type="button"
            role="option"
            aria-selected={i === active}
            onMouseDown={(e) => e.preventDefault()}
            onMouseEnter={() => picker.setActive(i)}
            onClick={() => picker.pick(p)}
            className="relative z-10 flex h-9 w-full items-center gap-2.5 rounded-[6px] px-2 text-left"
          >
            <LarkAvatar name={p.name} size={24} neutral avatarURL={p.avatar_url} avatar={p.avatar} account={account} manager={manager} />
            <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium text-ink">{p.name}</span>
          </button>
        ))}
        {!rows.length && (
          <div className="flex h-9 items-center gap-2 px-2 text-[12px] text-ink-3">
            {loading ? (
              <>
                <Spinner /> {t("ui-larkpeoplepicker.searching")}
              </>
            ) : token.query ? (
              t("ui-larkpeoplepicker.not_found", { query: token.query })
            ) : (
              t("ui-larkpeoplepicker.prompt")
            )}
          </div>
        )}
      </div>
      <div className="mt-1 border-t border-line px-2 pt-1.5 pb-1 text-[11px] text-ink-3">{t("ui-larkpeoplepicker.keys")}</div>
    </div>
  );
}
