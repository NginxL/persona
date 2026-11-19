import { credentialOwner, subscribeCredentialOwner } from "../../lib/api";
import { conversationDeletionRevision, conversationDeletionScope } from "../../lib/conversation-deleted";
import { useStore } from "../../lib/store";
// Ctrl+R in the composer: a panel over the box that searches the messages sent from any conversation (^R, as a shell's
// reverse search). Words are split on spaces and all must match; ↑↓ / Ctrl+R / Ctrl+P and Ctrl+S / Ctrl+N move,
// Enter or Tab puts the message in the box, Esc or Ctrl+G cancels. lib/send-history.ts holds the matching and the keys.
import { useEffect, useId, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { t } from "../../i18n";
import { readHistory } from "../../lib/send-history";
import { useComposition } from "../../lib/ime";
import { moveActive, readAllHistory, searchAction, searchSent } from "../../lib/send-history-search";
import { cx, EASE } from "../../ui/atoms";
import { splitSkillDraft } from "../../lib/skill-mentions";

function historyLabel(text: string): string {
  const split = splitSkillDraft(text);
  return [...split.skills.map((s) => `${s.title} · ${s.text.split("\n")[0]}`), split.text].filter(Boolean).join("\n");
}

export function HistorySearch({ onPick, onClose }: { onPick: (text: string) => void; onClose: () => void }) {
  const id = useId();
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  // the IME's Enter (also Safari's, just after compositionend) picks a candidate, not a sent message
  const ime = useComposition();
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  const revision = useStore(conversationDeletionRevision);
  const [opened] = useState(() => ({ owner, scope: conversationDeletionScope() }));
  const current = owner === opened.owner && conversationDeletionScope() === opened.scope;
  // Keep source identities: a deleted result must not be substituted by identical text from another chat.
  const candidates = useMemo(() => current ? readAllHistory() : [], [current, revision]);
  useEffect(() => { if (!current) onClose(); }, [current, onClose]);
  const results = useMemo(() => searchSent(candidates, query), [candidates, query]);
  const rows = useRef<(HTMLButtonElement | null)[]>([]);
  useEffect(() => {
    setActive(0);
  }, [query, revision, owner]);
  useEffect(() => {
    rows.current[active]?.scrollIntoView({ block: "nearest" });
  }, [active]);

  const pickAt = (i: number) => {
    const text = results[i];
    const source = candidates.find((entry) => entry.text === text);
    // Recheck synchronously as well: a click may arrive before React commits a deletion or account change.
    if (!source || credentialOwner() !== opened.owner || conversationDeletionScope() !== opened.scope) return;
    if (readHistory(source.draftKey).some((entry) => entry.text === source.text && entry.at === source.at)) onPick(source.text);
  };

  return (
    <div
      className="absolute inset-x-0 bottom-full z-10 mb-2 rounded-[10px] bg-surface p-1 shadow-raised"
      style={{ animation: `pop-in 180ms ${EASE} both`, transformOrigin: "bottom center" }}
    >
      <div className="flex items-center gap-2 px-2 pt-1 pb-1.5">
        <span aria-hidden className="shrink-0 font-mono text-[12.5px] font-medium text-ink-3">^R</span>
        <input
          autoFocus
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onBlur={onClose}
          {...ime.handlers}
          onKeyDown={(e) => {
            if (ime.busy(e.nativeEvent)) return; // the Enter or arrow that picks an input-method candidate is not ours
            const action = searchAction(e.nativeEvent);
            if (!action) return;
            e.preventDefault();
            e.stopPropagation();
            if (action === "cancel") onClose();
            else if (action === "pick") pickAt(active);
            else setActive((a) => moveActive(a, results.length, action));
          }}
          placeholder={t("composer.history_placeholder")}
          aria-label={t("composer.history_placeholder")}
          role="combobox"
          aria-expanded
          aria-controls={`${id}-list`}
          aria-activedescendant={results.length ? `${id}-${active}` : undefined}
          className="min-w-0 flex-1 bg-transparent text-[13px] text-ink outline-none placeholder:text-ink-3"
        />
      </div>
      <div id={`${id}-list`} role="listbox" aria-label={t("composer.history_list")} className="max-h-[240px] overflow-y-auto border-t border-line pt-1">
        {results.length === 0 ? (
          <div role="status" className="px-2 py-3 text-center text-[12px] text-ink-3">{t("composer.history_empty")}</div>
        ) : (
          results.map((text, i) => (
            <button
              key={text}
              ref={(el) => {
                rows.current[i] = el;
              }}
              id={`${id}-${i}`}
              type="button"
              role="option"
              aria-selected={i === active}
              title={historyLabel(text)}
              onMouseDown={(e) => e.preventDefault()}
              onMouseEnter={() => setActive(i)}
              onClick={() => pickAt(i)}
              className={cx("flex h-9 w-full items-center rounded-[6px] px-2 text-left text-[12.5px] text-ink", i === active && "bg-hover")}
            >
              <span className="min-w-0 flex-1 truncate">{historyLabel(text).replace(/\s*\n\s*/g, " ⏎ ")}</span>
            </button>
          ))
        )}
      </div>
      <div className="mt-1 border-t border-line px-2 pt-1.5 pb-1 text-[11px] text-ink-3">{t("composer.history_footer")}</div>
    </div>
  );
}
