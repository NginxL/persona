/// <reference types="node" />
// The two rings of Health's "Today" (water, protein; styles.css `--health-water` and `--health-protein`) keep clear of
// Apple's Activity rings: Apple's guidelines reserve the Move red, Exercise green and Stand cyan for them and ask that
// other rings be told apart from them (docs/design/DESIGN_SYSTEM.md). So each color stays at least 40 degrees
// of hue from all three, in light and dark, and reads on the card's surface as an icon would (3:1).
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { contrast } from "../../ui/extension-theme";

const css = readFileSync(new URL("../../styles.css", import.meta.url), "utf8");
const ACTIVITY = { move: "#FA114F", exercise: "#A6FF00", stand: "#00FFF6" };
const MIN_HUE_DISTANCE = 40;

function block(scheme: "light" | "dark"): Record<string, string> {
  const text = (scheme === "light" ? /:root\s*\{([^}]+)\}/ : /\.dark\s*\{([^}]+)\}/).exec(css)?.[1] ?? "";
  return Object.fromEntries([...text.matchAll(/--([\w-]+):\s*(#[0-9a-f]{3,8})\s*;/gi)].map((m) => [m[1], m[2]]));
}
/** The health tokens' own declaration: a `:root` or `.dark` rule that holds both. */
function health(scheme: "light" | "dark"): { water: string; protein: string } {
  const re = scheme === "light" ? /:root\s*\{\s*--health-water:\s*(#[0-9a-f]{6});\s*--health-protein:\s*(#[0-9a-f]{6});\s*\}/i : /\.dark\s*\{\s*--health-water:\s*(#[0-9a-f]{6});\s*--health-protein:\s*(#[0-9a-f]{6});\s*\}/i;
  const m = re.exec(css);
  expect(m, `${scheme} --health-water / --health-protein`).not.toBeNull();
  return { water: m![1], protein: m![2] };
}
function hue(hex: string): number {
  const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255);
  const max = Math.max(r, g, b), d = max - Math.min(r, g, b);
  const h = d === 0 ? 0 : max === r ? ((g - b) / d) % 6 : max === g ? (b - r) / d + 2 : (r - g) / d + 4;
  return (h * 60 + 360) % 360;
}
const apart = (a: number, b: number) => Math.min(Math.abs(a - b), 360 - Math.abs(a - b));

describe("Health's ring colors", () => {
  for (const scheme of ["light", "dark"] as const) {
    const { water, protein } = health(scheme);
    for (const [name, color] of Object.entries({ water, protein })) {
      it(`${scheme}: ${name} is at least ${MIN_HUE_DISTANCE}° of hue from the Move, Exercise and Stand colors`, () => {
        for (const [ring, apple] of Object.entries(ACTIVITY)) {
          expect(apart(hue(color), hue(apple)), `${name} ${color} vs ${ring} ${apple}`).toBeGreaterThanOrEqual(MIN_HUE_DISTANCE);
        }
      });
      it(`${scheme}: ${name} reads on the card's surface and the page's inset (3:1)`, () => {
        const t = block(scheme);
        for (const bg of ["surface", "inset"]) expect(contrast(color, t[bg]), `${name} on --${bg}`).toBeGreaterThanOrEqual(3);
      });
    }
    it(`${scheme}: water is a blue and protein an amber (the families the design asks for)`, () => {
      expect(hue(water)).toBeGreaterThan(200);
      expect(hue(water)).toBeLessThan(250);
      expect(hue(protein)).toBeGreaterThan(25);
      expect(hue(protein)).toBeLessThan(50);
    });
  }

  it("the rings use these tokens and not a hex", () => {
    const source = readFileSync(new URL("./HealthToday.tsx", import.meta.url), "utf8");
    expect(source).toContain("var(--health-water)");
    expect(source).toContain("var(--health-protein)");
    expect(source).not.toMatch(/#[0-9a-f]{6}/i);
  });
});
