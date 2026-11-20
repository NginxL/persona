// "Terminal settings" (iOS TerminalSettingsView, the parts a browser needs): long rows fold or scroll while watching, the key bar,
// the font size (pinching on iOS; ⌘+ / ⌘− stay the browser's zoom), light / dark apart from the page's (iOS's terminal
// colors), the cursor (shape, blinking) and the saved commands. Kept in this browser. The renderer is chosen in Settings › Labs
// (lib/labs.ts); this menu only says what draws this terminal and opens Labs over the page (never away from it).
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { cx, EASE } from "../../../ui/atoms";
import { Icon, type IconName } from "../../../ui/icons";
import { Switch } from "../../assistant/parts";
import { CURSOR_STYLES } from "../../../lib/terminal-details";
import { APPEARANCES, setTerminalAppearance, useTerminalAppearance, type TerminalAppearance } from "../../../lib/terminal-appearance";
import { MOUSE_MODES } from "../../../lib/terminal-mouse";
import { FONT_SIZES, type FeedView, type TerminalFeed } from "./feed";
import { t } from "../../../i18n";

const ROW = "flex min-h-10 items-center gap-2.5 px-3";
// An option row's label and its hint under it, as every row writes them.
const LABEL = "truncate text-[12.5px] text-ink";
const HINT = "truncate text-[11px] text-ink-3";

/** A gear's menu: a card under `anchor`, closed by a press outside it or Esc (Single pane's options, Full view's Appearance). */
export function OptionsPopover({ anchor, label, onClose, children }: { anchor: HTMLElement; label: string; onClose: () => void; children: React.ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<React.CSSProperties>({ visibility: "hidden" });
  const width = 280;

  useLayoutEffect(() => {
    const r = anchor.getBoundingClientRect();
    const w = Math.min(width, innerWidth - 16);
    setPos({ top: r.bottom + 6, left: Math.max(8, Math.min(r.right - w, innerWidth - w - 8)), width: w });
  }, [anchor]);

  useEffect(() => {
    const down = (e: PointerEvent) => {
      if (ref.current?.contains(e.target as Node) || anchor.contains(e.target as Node)) return;
      onClose();
    };
    const key = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    addEventListener("pointerdown", down, true);
    addEventListener("keydown", key);
    return () => {
      removeEventListener("pointerdown", down, true);
      removeEventListener("keydown", key);
    };
  }, [anchor, onClose]);

  return createPortal(
    <div
      ref={ref}
      role="dialog"
      aria-label={label}
      className="fixed z-50 overflow-hidden rounded-[10px] bg-surface py-1 shadow-overlay"
      style={{ ...pos, animation: `pop-in 160ms ${EASE} both`, transformOrigin: "top right" }}
    >
      {children}
    </div>,
    document.body,
  );
}

export function TerminalOptions({
  anchor,
  feed,
  v,
  keyBar,
  onKeyBar,
  onEditShortcuts,
  onOpenLabs,
  onClose,
}: {
  anchor: HTMLElement;
  feed: TerminalFeed;
  v: FeedView;
  keyBar: boolean;
  onKeyBar: (on: boolean) => void;
  onEditShortcuts: () => void;
  /** Labs over the page (the panel's LabsDialog), as Edit shortcuts opens its editor: the menu closes first. */
  onOpenLabs: () => void;
  onClose: () => void;
}) {
  const driving = v.control === "active" || v.control === "starting";

  return (
    <OptionsPopover anchor={anchor} label={t("session-terminal-terminaloptions.title")} onClose={onClose}>
      <div className={ROW}>
        <Icon name="wrap" size={15} className="shrink-0 text-ink-3" />
        <div className="min-w-0 flex-1">
          <div className={LABEL}>{t("session-terminal-terminaloptions.wrap")}</div>
          <div className={HINT} title={t("session-terminal-terminaloptions.wrap_hint")}>
            {driving ? t("session-terminal-terminaloptions.wrap_driving") : v.wrap ? t("session-terminal-terminaloptions.wrap_on") : t("session-terminal-terminaloptions.wrap_off")}
          </div>
        </div>
        <Switch checked={v.wrap} onChange={(on) => feed.setWrap(on)} label={t("session-terminal-terminaloptions.wrap")} />
      </div>
      <div className={ROW}>
        <Icon name="keyboard" size={15} className="shrink-0 text-ink-3" />
        <div className="min-w-0 flex-1">
          <div className={LABEL}>{t("session-terminal-terminaloptions.key_bar")}</div>
          <div className={HINT}>{t("session-terminal-terminaloptions.key_bar_hint")}</div>
        </div>
        <Switch checked={keyBar} onChange={onKeyBar} label={t("session-terminal-terminaloptions.key_bar")} />
      </div>
      <div className={ROW}>
        <Icon name="textSize" size={15} className="shrink-0 text-ink-3" />
        <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink">{t("session-terminal-terminaloptions.font_size")}</span>
        <div className="flex h-7 shrink-0 items-center rounded-control bg-field p-0.5">
          <StepButton label={t("session-terminal-terminaloptions.font_smaller")} disabled={v.fontSize <= FONT_SIZES.min} onClick={() => feed.setFontSize(v.fontSize - 1)}>
            −
          </StepButton>
          <button
            type="button"
            title={t("session-terminal-terminaloptions.font_default")}
            onClick={() => feed.setFontSize(FONT_SIZES.default)}
            className="h-6 min-w-8 whitespace-nowrap rounded-chip px-1 font-mono text-[12px] text-ink tabular-nums hover:bg-surface"
          >
            {v.fontSize}
          </button>
          <StepButton label={t("session-terminal-terminaloptions.font_larger")} disabled={v.fontSize >= FONT_SIZES.max} onClick={() => feed.setFontSize(v.fontSize + 1)}>
            +
          </StepButton>
        </div>
      </div>
      <AppearanceRow />
      <div className={ROW}>
        <Icon name="hand" size={15} className="shrink-0 text-ink-3" />
        <div className="min-w-0 flex-1">
          <div className={LABEL}>{t("terminal-mouse.label")}</div>
          <div
            className={HINT}
            title={t("terminal-mouse.tooltip")}
          >
            {v.mouse === "program" ? t("terminal-mouse.hint_program") : t("terminal-mouse.hint_local")}
          </div>
        </div>
        <div role="radiogroup" aria-label={t("terminal-mouse.label")} className="flex h-7 shrink-0 items-center rounded-control bg-field p-0.5">
          {MOUSE_MODES.map((m) => (
            <button
              key={m.mode}
              type="button"
              role="radio"
              aria-checked={v.mouse === m.mode}
              onClick={() => feed.setMouse(m.mode)}
              className={cx(
                "h-6 whitespace-nowrap rounded-chip px-2 text-[12px] font-medium transition-[background-color,color,box-shadow] duration-150",
                v.mouse === m.mode ? "bg-surface text-ink shadow-btn" : "text-ink-3 hover:text-ink",
              )}
            >
              {m.label}
            </button>
          ))}
        </div>
      </div>
      <div className={ROW}>
        <Icon name="square" size={15} className="shrink-0 text-ink-3" />
        <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink">{t("session-terminal-terminaloptions.cursor_style")}</span>
        <div role="radiogroup" aria-label={t("session-terminal-terminaloptions.cursor_style")} className="flex h-7 shrink-0 items-center rounded-control bg-field p-0.5">
          {CURSOR_STYLES.map((c) => (
            <button
              key={c.value}
              type="button"
              role="radio"
              aria-checked={v.cursorStyle === c.value}
              onClick={() => feed.setCursorStyle(c.value)}
              className={cx(
                "h-6 whitespace-nowrap rounded-chip px-2 text-[12px] font-medium transition-[background-color,color,box-shadow] duration-150",
                v.cursorStyle === c.value ? "bg-surface text-ink shadow-btn" : "text-ink-3 hover:text-ink",
              )}
            >
              {c.label}
            </button>
          ))}
        </div>
      </div>
      <div className={ROW}>
        <Icon name="spark" size={15} className="shrink-0 text-ink-3" />
        <div className="min-w-0 flex-1">
          <div className={LABEL}>{t("session-terminal-terminaloptions.cursor_blink")}</div>
          <div className={HINT}>{t("session-terminal-terminaloptions.cursor_blink_hint")}</div>
        </div>
        <Switch checked={v.cursorBlink} onChange={(on) => feed.setCursorBlink(on)} label={t("session-terminal-terminaloptions.cursor_blink")} />
      </div>
      <RendererRow
        v={v}
        onOpenLabs={() => {
          onClose();
          onOpenLabs();
        }}
      />
      <div className="mx-3 my-1 border-t border-line" />
      <button
        type="button"
        onClick={() => {
          onClose();
          onEditShortcuts();
        }}
        className={cx(ROW, "w-full text-left hover:bg-hover")}
      >
        <Icon name="bolt" size={15} className="shrink-0 text-ink-3" />
        <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink">{t("session-terminal-terminaloptions.edit_shortcuts")}</span>
      </button>
    </OptionsPopover>
  );
}

/** What draws this terminal (it opened with Settings › Labs › Terminal renderer, feed.ts rendererChosen), and the way to
 *  Labs to change it for the next terminal. Not a switch: one per terminal would be a second, separate setting. Labs opens
 *  in a dialog over the page rather than navigating to Settings, so the chat beside the terminal (its composer and an
 *  unsent draft's images, which live only in that component) stays mounted. */
export function RendererRow({ v, onOpenLabs }: { v: Pick<FeedView, "renderer" | "rendererFellBack">; onOpenLabs: () => void }) {
  const now = v.rendererFellBack
    ? t("session-terminal-terminaloptions.renderer_fell_back")
    : v.renderer === "webgl"
      ? t("session-terminal-terminaloptions.renderer_webgl")
      : t("session-terminal-terminaloptions.renderer_standard");
  return (
    <div className={ROW} data-terminal-renderer={v.rendererFellBack ? "fell-back" : v.renderer}>
      <Icon name="gauge" size={15} className="shrink-0 text-ink-3" />
      <div className="min-w-0 flex-1">
        <div className={LABEL}>{t("session-terminal-terminaloptions.renderer")}</div>
        <div className={HINT} title={t("session-terminal-terminaloptions.renderer_tooltip")}>
          {now}
        </div>
      </div>
      <button
        type="button"
        aria-haspopup="dialog"
        onClick={onOpenLabs}
        className="flex h-7 shrink-0 items-center gap-1 whitespace-nowrap rounded-control px-2 text-label font-medium text-accent-ink hover:bg-hover"
      >
        {t("session-terminal-terminaloptions.renderer_open_labs")}
        <Icon name="chevronRight" size={11} stroke={2} />
      </button>
    </div>
  );
}

const APPEARANCE: Record<TerminalAppearance, { icon: IconName; label: string; hint: string }> = {
  page: { icon: "appWindow", label: t("session-terminal-terminaloptions.appearance_page"), hint: t("session-terminal-terminaloptions.appearance_hint_page") },
  light: { icon: "sun", label: t("session-terminal-terminaloptions.appearance_light"), hint: t("session-terminal-terminaloptions.appearance_hint_light") },
  dark: { icon: "moon", label: t("session-terminal-terminaloptions.appearance_dark"), hint: t("session-terminal-terminaloptions.appearance_hint_dark") },
};

/**
 * "Appearance" (lib/terminal-appearance.ts): Match page / Light / Dark, one setting for both surfaces. The choices are
 * Settings' icons for the same meanings (the words would not fit beside the label), named for a screen reader and on
 * hover; the line under the label says the one chosen.
 */
export function AppearanceRow() {
  const appearance = useTerminalAppearance();
  return (
    <div className={ROW}>
      <Icon name="sun" size={15} className="shrink-0 text-ink-3" />
      <div className="min-w-0 flex-1">
        <div className={LABEL}>{t("session-terminal-terminaloptions.appearance")}</div>
        <div className={HINT} title={t("session-terminal-terminaloptions.appearance_tooltip")}>
          {APPEARANCE[appearance].hint}
        </div>
      </div>
      <div role="radiogroup" aria-label={t("session-terminal-terminaloptions.appearance")} className="flex h-7 shrink-0 items-center rounded-control bg-field p-0.5">
        {APPEARANCES.map((a) => (
          <button
            key={a}
            type="button"
            role="radio"
            aria-checked={appearance === a}
            aria-label={APPEARANCE[a].label}
            title={APPEARANCE[a].label}
            onClick={() => setTerminalAppearance(a)}
            className={cx(
              "flex h-6 items-center rounded-chip px-2 transition-[background-color,color,box-shadow] duration-150",
              appearance === a ? "bg-surface text-ink shadow-btn" : "text-ink-3 hover:text-ink",
            )}
          >
            <Icon name={APPEARANCE[a].icon} size={13} />
          </button>
        ))}
      </div>
    </div>
  );
}

function StepButton({ label, disabled, onClick, children }: { label: string; disabled?: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      disabled={disabled}
      onClick={onClick}
      className="flex h-6 w-6 items-center justify-center whitespace-nowrap rounded-chip text-[14px] text-ink-2 enabled:hover:bg-surface enabled:hover:text-ink disabled:opacity-35"
    >
      {children}
    </button>
  );
}
