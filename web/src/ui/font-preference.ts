// Browser-local reading preferences; defaults leave the page and extension theme untouched.
import { useSyncExternalStore } from "react";
export const FONT_KEY = "persona.font";
export const FONT_STYLE_ID = "persona-font";
export const FONT_PRESETS = {
  sans: { system: "system-ui", georgia: "Georgia", sourceHan: "Source Han Serif SC" },
  mono: { system: "ui-monospace", menlo: "Menlo", consolas: "Consolas" },
} as const;
export type FontKind = keyof typeof FONT_PRESETS;
export interface FontChoice { preset: string; family?: string }
export interface FontPreference { sans: FontChoice; mono: FontChoice }
const DEFAULT: FontChoice = { preset: "default" };
// index.html's before-paint copy is checked against this implementation by font-preference.test.ts.
export const FONT_FALLBACKS = {
  sans: 'Inter, -apple-system, BlinkMacSystemFont, "SF Pro Text", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", ui-sans-serif, system-ui, sans-serif',
  mono: '"JetBrains Mono", "SF Mono", "DejaVu Sans Mono", "Liberation Mono", ui-monospace, Menlo, Consolas, monospace',
};
/** One installed family, not CSS, a list, a URL, or a control sequence. */
export function validFontFamily(value: unknown): value is string {
  return typeof value === "string" && value.trim().length > 0 && value.trim().length <= 100 && !/[\u0000-\u001f\u007f,;{}<>]/u.test(value);
}
export function normalizeFonts(raw: unknown): FontPreference {
  const obj = raw && typeof raw === "object" ? raw as Record<string, unknown> : {};
  const one = (kind: FontKind): FontChoice => {
    const value = obj[kind];
    if (!value || typeof value !== "object") return DEFAULT;
    const { preset, family } = value as FontChoice;
    if (preset === "custom" && validFontFamily(family)) return { preset, family: family.trim() };
    if (typeof preset === "string" && Object.hasOwn(FONT_PRESETS[kind], preset)) return { preset };
    return DEFAULT;
  };
  return { sans: one("sans"), mono: one("mono") };
}
export function fontPreference(): FontPreference {
  try { return normalizeFonts(JSON.parse(localStorage.getItem(FONT_KEY) ?? "null")); }
  catch { return normalizeFonts(null); }
}
export function fontFamily(kind: FontKind, choice: FontChoice): string | null {
  if (choice.preset === "custom") return validFontFamily(choice.family) ? choice.family.trim() : null;
  return (FONT_PRESETS[kind] as Record<string, string>)[choice.preset] ?? null;
}
export const quoteFont = (family: string) => `"${family.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
export function fontCSS(value: FontPreference, inline: CSSStyleDeclaration): string {
  const declarations = (["sans", "mono"] as const).flatMap((kind) => {
    const family = fontFamily(kind, value[kind]);
    if (!family) return [];
    const first = value[kind].preset === "system" ? family : quoteFont(family);
    // Explicit choices win over inline theme defaults. Reset removes only the chosen family's declaration.
    return [`--font-${kind}: ${first}, ${inline.getPropertyValue(`--font-${kind}`) || FONT_FALLBACKS[kind]} !important;`, ...(kind === "mono" ? ["--font-inline-code: var(--font-mono);"] : [])];
  });
  return declarations.length ? `:root { ${declarations.join(" ")} }` : "";
}
export function applyFonts(doc: Document = document) {
  const css = fontCSS(fontPreference(), doc.documentElement.style);
  let style = doc.getElementById(FONT_STYLE_ID);
  if (!css) { style?.remove(); return; }
  if (!style) { style = doc.createElement("style"); style.id = FONT_STYLE_ID; doc.head.append(style); }
  if (style.textContent !== css) style.textContent = css;
}
const listeners = new Set<() => void>();
const subscribe = (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener); }; };
const snapshot = () => JSON.stringify(fontPreference());
export function useFontPreference(): FontPreference {
  return JSON.parse(useSyncExternalStore(subscribe, snapshot)) as FontPreference;
}
export function setFontChoice(kind: FontKind, choice: FontChoice) {
  const next = normalizeFonts({ ...fontPreference(), [kind]: choice });
  try {
    if (next.sans.preset === "default" && next.mono.preset === "default") localStorage.removeItem(FONT_KEY);
    else localStorage.setItem(FONT_KEY, JSON.stringify(next));
  } catch { /* Continue showing the preference that could be saved. */ }
  applyFonts(); listeners.forEach((l) => l());
}
if (typeof window !== "undefined") {
  addEventListener("storage", (event) => {
    if (event.key !== null && event.key !== FONT_KEY) return;
    listeners.forEach((l) => l());
  });
}
