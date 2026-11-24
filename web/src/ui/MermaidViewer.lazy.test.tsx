// @vitest-environment jsdom
/// <reference types="node" />
// A reply's Mermaid diagram is drawn on the first screen; its zoomed view (pan, pinch, zoom controls) is not: it loads
// from a chunk of its own when "Enlarge" is reached or clicked (Mermaid.tsx).
import { readFileSync } from "node:fs";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("../lib/mermaid", () => ({
  cachedMermaid: () => '<svg data-drawn=""></svg>',
  renderMermaid: () => Promise.resolve('<svg data-drawn=""></svg>'),
  mermaidProblem: () => ({ summary: "", detail: "" }),
}));
import { t } from "../i18n";
import { MermaidBlock, MermaidViewer } from "./Mermaid";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");

describe("the Mermaid zoom boundary", () => {
  it("keeps the zoomed view out of Mermaid.tsx's static imports", () => {
    const source = read("./Mermaid.tsx");
    expect(source).not.toMatch(/^import\s+(?!type\b)[^;]*from\s+["']\.\/MermaidViewer["']/m);
    expect(source).toMatch(/lazyOverlay\(\(\) => import\("\.\/MermaidViewer"\)\.then\(\(m\) => m\.MermaidViewer\)\)/);
  });

  it("opens the zoomed view once its chunk is in, after reaching Enlarge starts it", async () => {
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    act(() => root.render(<MermaidBlock code="graph TD; A-->B" />));
    const zoom = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("ui-mermaid.zoom")}"]`)!;
    const preload = vi.spyOn(MermaidViewer, "preload");
    act(() => zoom.dispatchEvent(new PointerEvent("pointerover", { bubbles: true })));
    expect(preload).toHaveBeenCalledTimes(1);
    act(() => zoom.click());
    await act(async () => {
      await MermaidViewer.preload();
    });
    expect(document.querySelector(`[role="dialog"][aria-label="${t("ui-mermaid.diagram")}"]`)).not.toBeNull();
    act(() => root.unmount());
    host.remove();
  });
});
