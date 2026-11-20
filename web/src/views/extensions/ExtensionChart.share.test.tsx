// A stacked bar with one category (a persona-card's single day, #5386) is a horizontal segmented bar, not a lone
// vertical column; several categories keep their columns. Rendered to static markup: the share bar's segments carry
// their series index and the same numbers stay in the screen-reader table.
import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { UIItem } from "../../lib/extension-ui";
import { ExtensionChart } from "./ExtensionChart";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia ??= (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
});

const oneDay: UIItem = {
  type: "chart",
  kind: "bar",
  stacked: true,
  title: "今日热量",
  unit: "kcal",
  series: [
    { name: "午餐", points: [{ label: "今天", value: 900, display: "900 kcal" }] },
    { name: "加餐", points: [{ label: "今天", value: 150, display: "150 kcal" }] },
    { name: "还能吃", tone: "neutral", points: [{ label: "今天", value: 900, display: "900 kcal" }] },
  ],
};

const twoDays: UIItem = {
  type: "chart",
  kind: "bar",
  stacked: true,
  title: "近两天热量对比",
  unit: "kcal",
  series: [
    { name: "午餐", points: [{ label: "周一", value: 800 }, { label: "周二", value: 700 }] },
    { name: "加餐", points: [{ label: "周一", value: 60 }, { label: "周二", value: 90 }] },
  ],
};

describe("ExtensionChart 的单类目横向分段条（#5386）", () => {
  it("draws one category's stacked bar as a horizontal segmented bar", () => {
    const html = renderToStaticMarkup(<ExtensionChart item={oneDay} compact />);
    expect(html).toContain("data-share-bar");
    expect(html).toContain('data-share-segment="0"');
    expect(html).toContain('data-share-segment="1"');
    expect(html).toContain('data-share-segment="2"');
    // the legend still names every part and the rows' numbers stay available to screen readers
    expect(html).toContain("午餐");
    expect(html).toContain("900 kcal");
  });

  it("keeps finite parts visible when their sum overflows", () => {
    const huge: UIItem = {
      ...oneDay,
      series: [
        { name: "a", points: [{ label: "今天", value: Number.MAX_VALUE }] },
        { name: "b", points: [{ label: "今天", value: Number.MAX_VALUE }] },
      ],
    };
    const html = renderToStaticMarkup(<ExtensionChart item={huge} compact />);
    expect(html).toContain('data-share-segment="0"');
    expect(html).toContain('data-share-segment="1"');
    expect(html.match(/flex-grow:0\.5/g)).toHaveLength(2);
  });

  it("routes several categories through the column chart, not the share bar", () => {
    const html = renderToStaticMarkup(<ExtensionChart item={twoDays} compact />);
    expect(html).not.toContain("data-share-bar");
    // the XY chart's screen-reader table keeps both categories' values (SSR has no layout width, so no <svg>)
    expect(html).toContain("周一");
    expect(html).toContain("周二");
  });
});
