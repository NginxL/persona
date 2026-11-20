// Settings › General › Appearance's "Theme color" and "Text size" (ui/look.ts; the app's ThemeColorPicker and StyleSettingsPage's "Text size"),
// and the web's own "Recent chats in the sidebar" (lib/sidebar-recent.ts) and "Sidebar icons" (lib/sidebar-icons.ts).
import { useEffect, useState, useSyncExternalStore } from "react";
import { t } from "../../i18n";
import { cx } from "../../ui/atoms";
import { pageToken } from "../../ui/extension-theme";
import { useActiveExtensionTheme } from "../../ui/ExtensionThemePicker";
import { Icon } from "../../ui/icons";
import {
  ACCENT_PRESETS, accentTokens, PRESET_COLORS, setAccentChoice, setTextSize, TEXT_SIZES, useAccentChoice, usePersonaAccent, useTextSize,
  type AccentChoice, type TextSize,
} from "../../ui/look";
import { RECENT_COUNTS, setRecentCount, useRecentCount } from "../../lib/sidebar-recent";
import { setSidebarIconMode, SIDEBAR_ICON_MODES, useSidebarIconMode, type SidebarIconMode } from "../../lib/sidebar-icons";
import { usePersona } from "../assistant/Persona";
import { Segmented } from "../assistant/parts";
import { Row } from "./SettingsParts";
import { FONT_PRESETS, quoteFont, setFontChoice, useFontPreference, validFontFamily, type FontChoice, type FontKind } from "../../ui/font-preference";

function subscribeScheme(l: () => void) {
  const o = new MutationObserver(l);
  o.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });
  return () => o.disconnect();
}
const darkNow = () => document.documentElement.classList.contains("dark");

/**
 * "Theme color": "Default" (the page's own blue), "Match avatar" when the persona brings colors (or it is the choice already, so it stays in
 * sight), then the five fixed colors, each a disc of its accent with where it comes from under its name.
 */
export function ThemeColorPicker() {
  const choice = useAccentChoice();
  const personaAccent = usePersonaAccent();
  const persona = usePersona();
  const extension = useActiveExtensionTheme();
  const scheme = useSyncExternalStore(subscribeScheme, darkNow) ? "dark" : "light";
  const options: AccentChoice[] = ["default", ...(personaAccent || choice === "persona" ? (["persona"] as const) : []), ...ACCENT_PRESETS];
  // The disc is the color the page gets: the same contrast floor as accentTokens; "Default"'s is styles.css's own.
  const disc = (c: AccentChoice): string => {
    if (c === "default") return pageToken("accent", scheme);
    const pair = c === "persona" ? personaAccent : PRESET_COLORS[c].accent;
    return pair ? accentTokens(pair)[scheme]["--accent"] : pageToken("accent", scheme);
  };
  const sub = (c: AccentChoice) => (c === "persona" ? (personaAccent ? persona.name : t("ui-look.persona_none")) : t(`ui-look.${c}_sub`));
  return (
    <div className="px-4 py-3" data-settings-row="theme-color">
      <div className="text-[13px] text-ink">{t("ui-look.accent")}</div>
      <div className="mt-0.5 text-[11.5px] text-ink-3">{t("ui-look.accent_hint")}</div>
      <div role="radiogroup" aria-label={t("ui-look.accent")} className="mt-3 grid grid-cols-4 gap-x-1 gap-y-3 sm:grid-cols-7">
        {options.map((c) => {
          const selected = c === choice;
          const color = disc(c);
          return (
            <button
              key={c}
              type="button"
              role="radio"
              aria-checked={selected}
              aria-label={t("ui-look.swatch_label", { name: t(`ui-look.${c}`), origin: sub(c) })}
              onClick={() => !selected && setAccentChoice(c)}
              className="group flex min-w-0 flex-col items-center gap-1.5 rounded-control px-0.5 py-1 transition-colors hover:bg-hover"
            >
              <span
                className="flex size-10 items-center justify-center rounded-full p-[3px] transition-shadow duration-150"
                style={selected ? { boxShadow: `inset 0 0 0 2px ${color}` } : undefined}
              >
                {/* the disc is data (a color to choose), like the persona picker's swatches */}
                <span className="flex size-full items-center justify-center rounded-full text-white shadow-hairline" style={{ background: color }}>
                  {selected && <Icon name="check" size={14} stroke={2.6} />}
                </span>
              </span>
              {/* a color's name wraps in a phone's four columns rather than being cut ("システムブルー") */}
              <span className={cx("max-w-full text-center text-[12px] break-words", selected ? "font-medium text-ink" : "text-ink-2")}>{t(`ui-look.${c}`)}</span>
              <span className="-mt-1 max-w-full truncate text-[11px] whitespace-nowrap text-ink-3">{sub(c)}</span>
            </button>
          );
        })}
      </div>
      {extension && <p className="mt-2.5 text-[11.5px] leading-relaxed text-ink-3">{t("ui-look.extension_over", { name: extension.name })}</p>}
    </div>
  );
}

/** "Text size": "Small" / "Standard" / "Large", the whole page's text (src/lib/text-scale.ts). */
export function TextSizeRow() {
  const size = useTextSize();
  return (
    <Row label={t("ui-look.text_size")} hint={t("ui-look.text_size_hint")} wrapHint anchor="text-size">
      <Segmented<TextSize> label={t("ui-look.text_size")} value={size} options={TEXT_SIZES.map((v) => ({ v, label: t(`ui-look.${v}`) }))} onChange={setTextSize} />
    </Row>
  );
}

/** "Recent chats in the sidebar": how many Persona chats › Recent lists before Show more (lib/sidebar-recent.ts, #5146).
 *  On a phone its four choices go under the sentence, so the name is not cut beside them (audit item 17). */
export function RecentChatsRow() {
  const count = useRecentCount();
  return (
    <Row label={t("ui-look.recent_chats")} hint={t("ui-look.recent_chats_hint")} wrapHint stackOnNarrow anchor="recent-chats">
      <Segmented label={t("ui-look.recent_chats")} value={String(count)} options={RECENT_COUNTS.map((n) => ({ v: String(n), label: String(n) }))} onChange={(v) => setRecentCount(Number(v))} />
    </Row>
  );
}

/** Local fonts are opt-in; a missing face keeps its choice and uses the normal fallback stack. */
export function FontRows() {
  const saved = useFontPreference();
  return <div data-settings-row="font">
    <div className="px-4 pt-3 text-meta font-medium text-ink">{t("ui-look.font")}</div>
    {(["sans", "mono"] as const).map((kind) => <FontRow key={kind} kind={kind} choice={saved[kind]} />)}
  </div>;
}
function FontRow({ kind, choice }: { kind: FontKind; choice: FontChoice }) {
  const [custom, setCustom] = useState(choice.preset === "custom");
  const [draft, setDraft] = useState(choice.family ?? "");
  useEffect(() => { setCustom(choice.preset === "custom"); setDraft(choice.family ?? ""); }, [choice.preset, choice.family]);
  const valid = validFontFamily(draft);
  let unavailable = false;
  if (custom && valid) {
    try { unavailable = document.fonts?.check(`1em ${quoteFont(draft.trim())}`) === false; }
    catch { /* Font availability checks are only advisory, never a reason to discard a choice. */ }
  }
  const label = t(`ui-look.font_${kind}`);
  return <div>
    <Row label={label} hint={t("ui-look.font_hint")} wrapHint stackOnNarrow>
      <select aria-label={label} className="h-11 max-w-full rounded-control bg-field px-2.5 text-label text-ink shadow-inset-field outline-none" value={custom ? "custom" : choice.preset} onChange={(event) => {
        const preset = event.target.value;
        setCustom(preset === "custom");
        if (preset !== "custom") setFontChoice(kind, { preset });
      }}>
        <option value="default">{t("ui-look.default")}</option>
        {Object.entries(FONT_PRESETS[kind]).map(([preset, family]) => <option key={preset} value={preset}>{preset === "system" ? t(`ui-look.font_system_${kind}`) : family}</option>)}
        <option value="custom">{t("ui-look.font_custom")}</option>
      </select>
    </Row>
    {custom && <div className="space-y-1 px-4 pb-3">
      <label className="block text-label text-ink-2">{t("ui-look.font_family")}
        <input aria-label={t("ui-look.font_custom_label", { kind: label })} aria-invalid={draft.length > 0 && !valid} value={draft} maxLength={100} spellCheck={false} autoComplete="off" className="mt-1 h-11 w-full rounded-control bg-field px-2.5 text-label text-ink shadow-inset-field outline-none" onChange={(event) => {
          const family = event.target.value;
          setDraft(family);
          if (validFontFamily(family)) setFontChoice(kind, { preset: "custom", family });
        }} />
      </label>
      <p className="text-submeta text-ink-3" role={draft.length > 0 && !valid ? "alert" : undefined}>{draft.length > 0 && !valid ? t("ui-look.font_invalid") : unavailable ? t("ui-look.font_unavailable") : t("ui-look.font_local")}</p>
    </div>}
  </div>;
}

/** "Sidebar icons": Monochrome (the default, the neutral ink) or Colored, a color for each page's icon (#10822).
 *  Stacked on a phone like Recent chats, so the name is not cut beside the choices. */
export function SidebarIconsRow() {
  const mode = useSidebarIconMode();
  return (
    <Row label={t("ui-look.sidebar_icons")} hint={t("ui-look.sidebar_icons_hint")} wrapHint stackOnNarrow anchor="sidebar-icons">
      <Segmented<SidebarIconMode> label={t("ui-look.sidebar_icons")} value={mode} options={SIDEBAR_ICON_MODES.map((v) => ({ v, label: t(`ui-look.sidebar_icons_${v}`) }))} onChange={setSidebarIconMode} />
    </Row>
  );
}
