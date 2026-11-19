import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { CountBadge } from "./CountBadge";

describe("count badge", () => {
  it("hides at zero, caps at 99+ and says the exact number", () => {
    expect(renderToStaticMarkup(<CountBadge count={0} />)).toBe("");
    const big = renderToStaticMarkup(<CountBadge count={120} />);
    expect(big).toContain(">99+</span>");
    expect(big).toContain('<span class="sr-only">120</span>');
    expect(renderToStaticMarkup(<CountBadge count={3} />)).toMatch(/data-count-badge="needs"[^>]*class="[^"]*bg-field text-attention"[^>]*>3<\/span>$/);
  });

  it("takes a passive or accent tone and the dense size", () => {
    expect(renderToStaticMarkup(<CountBadge count={4} tone="neutral" dense />)).toMatch(/h-4 min-w-4[^"]*bg-field text-ink-2/);
    expect(renderToStaticMarkup(<CountBadge count={4} tone="accent" label={false} />)).toMatch(/aria-hidden="true"[^>]*bg-accent-ink text-surface/);
  });
});
