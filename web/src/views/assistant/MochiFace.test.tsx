// @vitest-environment jsdom
// Asleep (offline) on the dark disc the face keeps the dark icon's ink outline, a lighter gray body and a lighter shadow
// (#12090, as on iOS): a gray outline there sat halfway between the disc and the body, so the body's edge came in two small
// steps and looked blurred. The paper disc's asleep face and every awake face draw the colors they drew before, and the
// closed eyes and the cheek keep their gray. The colors are read as the browser would: the face's own paint resolved
// through the --mochi-* tokens of styles.css, :root for light and .dark over it for dark.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { PersonaState } from "../../lib/persona";
import { MochiFace } from "./MochiFace";

const css = readFileSync(join(__dirname, "..", "..", "styles.css"), "utf8");
const tokens = (selector: string) => {
  const block = new RegExp(`${selector}\\s*\\{([^}]*--mochi-disc-top[^}]*)\\}`).exec(css)?.[1] ?? "";
  return Object.fromEntries([...block.matchAll(/--(mochi-[\w-]+):\s*([^;}]+)/g)].map((m) => [m[1], m[2].trim()]));
};
const light = tokens(":root");
const schemes = { light, dark: { ...light, ...tokens("\\.dark") } };
type Scheme = keyof typeof schemes;

/** What a face paints, its token references resolved for the scheme. */
function paint(state: PersonaState, scheme: Scheme, size = 40) {
  const svg = new DOMParser().parseFromString(renderToStaticMarkup(<MochiFace size={size} state={state} />), "text/html");
  const resolve = (value: string | null | undefined) =>
    (value ?? "").replace(/var\(--(mochi-[\w-]+)\)/g, (_, name: string) => schemes[scheme][name] ?? `unset --${name}`);
  const stops = (suffix: string) =>
    [...svg.querySelectorAll(`[id$="${suffix}"] > stop`)].map((s) => resolve(/stop-color:\s*([^;]+)/.exec(s.getAttribute("style") ?? "")?.[1]));
  const body = svg.querySelector('path[stroke-linejoin="round"]');
  // Shut, each eye is one curve; open, a dot in the ink (the thinking bubble's dots are in the cheek's orange).
  const eyes =
    state === "offline"
      ? [...svg.querySelectorAll("path")].filter((p) => /^M[\d.]+ [\d.]+Q[\d.]+ [\d.]+ [\d.]+ [\d.]+$/.test(p.getAttribute("d") ?? ""))
      : [...svg.querySelectorAll("circle")].filter((c) => c.getAttribute("fill") === "var(--mochi-ink)");
  expect(eyes).toHaveLength(2);
  return {
    disc: stops("d"),
    body: stops("b"),
    outline: resolve(body?.getAttribute("stroke")),
    shadow: resolve(svg.querySelector("ellipse[filter]")?.getAttribute("fill")),
    cheek: resolve(svg.querySelector("ellipse:not([filter])")?.getAttribute("fill")),
    eyes: [...new Set(eyes.map((e) => resolve(e.getAttribute(state === "offline" ? "stroke" : "fill"))))],
  };
}

// What the faces painted before #12090, from the stylesheet as it was then.
const before = {
  light: {
    awake: { disc: ["#f4eadf", "#e7d7c4"], body: ["#fff", "#e9e3de"], outline: "#1d1816", shadow: "rgb(0 0 0 / .14)", cheek: "#f2703c", eyes: ["#1d1816"] },
    asleep: { disc: ["#f4eadf", "#e7d7c4"], body: ["#d9d4d0", "#bdb6b1"], outline: "#6e6763", shadow: "rgb(0 0 0 / .14)", cheek: "#a59d98", eyes: ["#6e6763"] },
  },
  dark: {
    awake: { disc: ["#332a26", "#1d1816"], body: ["#fff", "#e9e3de"], outline: "#0e0b0a", shadow: "rgb(0 0 0 / .45)", cheek: "#f2703c", eyes: ["#1d1816"] },
  },
};
const awake: PersonaState[] = ["idle", "thinking", "working", "needs_you"];

describe("Mochi's colors", () => {
  it.each([40, 24])("paint every awake face, light and dark, as before (%i px)", (size) => {
    for (const scheme of ["light", "dark"] as const) {
      for (const state of awake) expect(paint(state, scheme, size), `${scheme} ${state}`).toEqual(before[scheme].awake);
    }
  });

  it.each([40, 24])("paint the paper disc's asleep face as before (%i px)", (size) => {
    expect(paint("offline", "light", size)).toEqual(before.light.asleep);
  });

  it.each([40, 24])("keep the dark icon's ink outline asleep on the dark disc, with a lighter body and shadow (%i px)", (size) => {
    const asleep = paint("offline", "dark", size);
    expect(asleep).toEqual({ disc: ["#332a26", "#1d1816"], body: ["#e2ddd9", "#c8c1bc"], outline: "#0e0b0a", shadow: "rgb(0 0 0 / .3)", cheek: "#a59d98", eyes: ["#6e6763"] });
    // One step at the edge, as awake: the same ink outline on the same disc.
    expect(asleep.outline).toBe(paint("idle", "dark", size).outline);
    expect(asleep.disc).toEqual(paint("idle", "dark", size).disc);
  });
});
