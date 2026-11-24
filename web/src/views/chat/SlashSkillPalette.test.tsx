// @vitest-environment jsdom
// The skill stage's capture listener on the composer (#11237): added once, Shift+Enter left as a new line, and the Enter
// an input method sends right after compositionend left alone.
import { act, createRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { MentionSkill } from "../../lib/skill-mentions";

const skill = { id: "review", name: "/review", description: "Review the change", source: "project" } as unknown as MentionSkill;
const useSkillCatalog = vi.fn((_source?: unknown, _machine?: unknown, _cwd?: unknown, _memory?: unknown, enabled = true) =>
  enabled ? { catalog: { skills: [skill] }, status: undefined } : {});
vi.mock("./useSkillCatalog", () => ({ useSkillCatalog: (...args: unknown[]) => useSkillCatalog(...args) }));
const { SlashSkillPalette } = await import("./SlashSkillPalette");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let now = 1_000;
beforeEach(() => {
  now = 1_000;
  useSkillCatalog.mockClear();
  vi.spyOn(performance, "now").mockImplementation(() => now);
  Element.prototype.scrollIntoView ??= () => {};
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

describe("SlashSkillPalette's keys", () => {
  it("listens once per composer, picks on a plain Enter only, and leaves the IME's Enter alone", () => {
    const within = createRef<HTMLDivElement>();
    const composer = document.createElement("div");
    const field = document.createElement("textarea");
    composer.append(field);
    document.body.append(composer);
    (within as { current: HTMLDivElement | null }).current = composer;
    const added = vi.spyOn(composer, "addEventListener");
    const onPick = vi.fn();
    const props = { rows: [], active: 0, onHover: vi.fn(), onPick, onClose: vi.fn(), query: "rev", within };
    act(() => root.render(<SlashSkillPalette {...props} />));
    act(() => root.render(<SlashSkillPalette {...props} active={0} />));
    act(() => root.render(<SlashSkillPalette {...props} onHover={vi.fn()} />));
    expect(added.mock.calls.filter(([type]) => type === "keydown")).toHaveLength(1);

    const key = (init: KeyboardEventInit) => {
      const e = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true, ...init });
      field.dispatchEvent(e);
      return e;
    };
    expect(key({ shiftKey: true }).defaultPrevented).toBe(false);
    expect(key({ altKey: true }).defaultPrevented).toBe(false);
    field.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true }));
    field.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true }));
    now += 10;
    expect(key({}).defaultPrevented).toBe(false);
    expect(onPick).not.toHaveBeenCalled();
    now += 500;
    expect(key({}).defaultPrevented).toBe(true);
    expect(onPick).toHaveBeenCalledWith(expect.objectContaining({ kind: "skill", id: "skill:review" }));
    composer.remove();
  });

  it("with the composer's own state, an Enter right after a compositionend it saw before mounting is not a pick", () => {
    const within = createRef<HTMLDivElement>();
    const composer = document.createElement("div");
    const field = document.createElement("textarea");
    composer.append(field);
    document.body.append(composer);
    (within as { current: HTMLDivElement | null }).current = composer;
    const onPick = vi.fn();
    // the composition ended just before the palette mounted
    const ime = { current: { active: false, endedAt: now } };
    act(() => root.render(<SlashSkillPalette rows={[]} active={0} onHover={vi.fn()} onPick={onPick} onClose={vi.fn()} query="rev" within={within} ime={ime} />));
    now += 10;
    const enter = () => {
      const e = new KeyboardEvent("keydown", { key: "Enter", keyCode: 13, bubbles: true, cancelable: true });
      field.dispatchEvent(e);
      return e;
    };
    expect(enter().defaultPrevented).toBe(false);
    expect(onPick).not.toHaveBeenCalled();
    now += 500;
    expect(enter().defaultPrevented).toBe(true);
    expect(onPick).toHaveBeenCalledTimes(1);
    composer.remove();
  });

  it("without one, a blur in the middle of a composition does not leave its keys dead", () => {
    const within = createRef<HTMLDivElement>();
    const composer = document.createElement("div");
    const field = document.createElement("textarea");
    composer.append(field);
    document.body.append(composer);
    (within as { current: HTMLDivElement | null }).current = composer;
    const onPick = vi.fn();
    act(() => root.render(<SlashSkillPalette rows={[]} active={0} onHover={vi.fn()} onPick={onPick} onClose={vi.fn()} query="rev" within={within} />));
    field.focus();
    field.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true }));
    field.blur();
    now += 500;
    const e = new KeyboardEvent("keydown", { key: "Enter", keyCode: 13, bubbles: true, cancelable: true });
    field.dispatchEvent(e);
    expect(e.defaultPrevented).toBe(true);
    expect(onPick).toHaveBeenCalledTimes(1);
    composer.remove();
  });

  it("keeps bare / command-only without fetching skills and shows matching skills on a non-empty keyword", () => {
    const within = createRef<HTMLDivElement>();
    const composer = document.createElement("div");
    document.body.append(composer);
    (within as { current: HTMLDivElement | null }).current = composer;
    const command = { word: "/compact", desc: "Compact context", text: "/compact" };
    const source = { files: () => [], skills: { manager: "assistant", environment: "test" } };
    act(() => root.render(<SlashSkillPalette rows={[command]} active={0} onHover={vi.fn()} onPick={vi.fn()} onClose={vi.fn()} query="" source={source} within={within} />));
    expect(useSkillCatalog).toHaveBeenLastCalledWith(source.skills, undefined, undefined, undefined, false);
    expect([...host.querySelectorAll("[role=option]")].map((o) => o.textContent)).toEqual(["compact"]);
    expect(host.querySelectorAll("[data-picker-group]")).toHaveLength(0);

    act(() => root.render(<SlashSkillPalette rows={[command]} active={0} onHover={vi.fn()} onPick={vi.fn()} onClose={vi.fn()} query="rev" source={source} within={within} />));
    expect(useSkillCatalog).toHaveBeenLastCalledWith(source.skills, undefined, undefined, undefined, true);
    expect([...host.querySelectorAll("[role=option]")].map((o) => o.textContent)).toEqual(["compact", expect.stringContaining("Review the change")]);
    expect([...host.querySelectorAll("[data-picker-group]")].map((g) => g.textContent)).toHaveLength(2);
    composer.remove();
  });
});
