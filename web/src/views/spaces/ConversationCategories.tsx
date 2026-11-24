// A conversation's information categories as chips (#13500, docs/assistant/health.md "Using information by scene"):
// the kinds of personal information it holds (health, money…), which decide how carefully it is used outside its
// space. The user may take one away (the space's own ones stay) and put a taken one back; only the user loosens.
import { useCallback, useEffect, useState } from "react";
import { errorText } from "../../lib/api";
import { changeConversationCategories, readConversationCategories, type ConversationCategories as Categories } from "../../lib/tracks";
import { cx, Menu, toast, useMenu } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { categoryLabel } from "./labels";
import { t } from "../../i18n";

export function ConversationCategories({ sid }: { sid: string }) {
  const [cats, setCats] = useState<Categories | null>(null);
  const [busy, setBusy] = useState("");
  const menu = useMenu();
  const load = useCallback(async () => {
    try {
      setCats(await readConversationCategories(sid));
    } catch {
      // an assistant without categories, or one that could not be read: no chips
      setCats(null);
    }
  }, [sid]);
  useEffect(() => void load(), [load]);

  const change = async (c: string, op: "add" | "remove") => {
    setBusy(c);
    try {
      setCats(await changeConversationCategories(sid, op === "add" ? { add: [c] } : { remove: [c] }));
      toast(t(op === "add" ? "spaces.category_added" : "spaces.category_removed", { name: categoryLabel(c) }), "ok");
    } catch (e) {
      toast(t("spaces.category_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy("");
    }
  };

  if (!cats || (!cats.categories.length && !cats.removed.length)) return null;
  return (
    <span className="flex min-w-0 items-center gap-1" data-testid="conversation-categories">
      {cats.categories.map((c) => {
        const kept = cats.floor.includes(c);
        return (
          <span key={c} className="inline-flex h-6 shrink-0 items-center gap-0.5 whitespace-nowrap rounded-full bg-field pr-1 pl-2 text-label font-medium text-ink-2" title={kept ? t("spaces.category_kept") : undefined}>
            {categoryLabel(c)}
            {kept ? (
              <span className="px-0.5 text-ink-3"><Icon name="lock" size={10} /><span className="sr-only">{t("spaces.category_kept")}</span></span>
            ) : (
              <button
                type="button"
                disabled={busy === c}
                onClick={() => void change(c, "remove")}
                aria-label={t("spaces.category_remove", { name: categoryLabel(c) })}
                title={t("spaces.category_remove", { name: categoryLabel(c) })}
                className={cx("touch-hit flex size-4 items-center justify-center rounded-full text-ink-3 hover:bg-hover hover:text-ink disabled:opacity-40")}
              >
                <Icon name="x" size={10} stroke={2.4} />
              </button>
            )}
          </span>
        );
      })}
      {cats.removed.length > 0 && (
        <button type="button" onClick={menu.open} aria-label={t("spaces.category_restore")} title={t("spaces.category_restore")} className="touch-hit flex size-6 shrink-0 items-center justify-center rounded-full text-ink-3 hover:bg-hover hover:text-ink">
          <Icon name="plus" size={12} stroke={2.2} />
        </button>
      )}
      {menu.anchor && (
        <Menu
          anchor={menu.anchor}
          onClose={menu.close}
          align="right"
          header={t("spaces.category_restore")}
          items={cats.removed.map((c) => ({ key: c, label: categoryLabel(c), onSelect: () => void change(c, "add") }))}
        />
      )}
    </span>
  );
}
