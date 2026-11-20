// @vitest-environment jsdom
// The generic configuration form (ConfigurationForm.tsx) over the shared synthetic all-types document: every control with
// its name for assistive technology and its keys, single-step choices written at once and saved only once committed,
// typed text saved explicitly and kept through failures and conflicts, a multiselect's checks as one pending replacement,
// resets, read-only / disabled / unknown fields, a version it can't write, and a source replaced while its
// answer is still coming.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { formatList, t } from "../../../i18n";
import shared from "../../../../../schema/configuration/all-types.json";
import { ConfigurationError, type ConfigurationSource, type ConfigurationUpdate } from "../../../lib/configuration/contract";
import { ConfigurationForm } from "./ConfigurationForm";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

interface Field {
  id: string;
  kind: string;
  default: unknown;
  read_only?: boolean;
  disabled_reason?: string;
  details?: { title: string; description: string }[];
  [member: string]: unknown;
}
interface Doc {
  schema_version: number;
  id: string;
  revision: string;
  title: string;
  groups: { id: string; title: string; fields: Field[] }[];
  values: Record<string, unknown>;
  notices?: Record<string, unknown>;
}
const example = shared as unknown as Doc;
const clone = (d: unknown): Doc => JSON.parse(JSON.stringify(d));

/** The shared document with `sections` fixed to exactly one choice. */
function oneOfOne(): Doc {
  const doc = clone(example);
  for (const f of doc.groups.flatMap((g) => g.fields)) if (f.id === "sections") f.multiselect = { min_selected: 1, max_selected: 1, options: (f.multiselect as { options: unknown[] }).options };
  return doc;
}

/** The shared document with every kind's saved value missing or invalid; the definitions stay valid. */
function invalidValues(): Doc {
  const doc = clone(example);
  const values = doc.values as Record<string, unknown>;
  doc.groups[0].fields.find((f) => f.id === "compact")!.default = true;
  delete values.compact;
  values.label = 7;
  values.notes = false;
  values.layout = "grid";
  delete values.accent;
  values.motion = "slow";
  delete values.sections;
  values.interval = "soon";
  return doc;
}

/** An in-memory source: commits like a writer, or answers with `fail` once. */
function memory(start: unknown = example, key = "example-preferences") {
  let doc = clone(start);
  const updates: ConfigurationUpdate[] = [];
  const state = { fail: null as unknown, hold: null as null | ((go: () => void) => void) };
  const source: ConfigurationSource = {
    key,
    read: async () => clone(doc),
    update: async (u) => {
      updates.push(u);
      if (state.hold) await new Promise<void>((go) => state.hold!(go));
      if (state.fail) {
        const f = state.fail;
        state.fail = null;
        throw f;
      }
      if (u.revision !== doc.revision) throw new ConfigurationError("configuration_conflict");
      const next = clone(doc);
      const fields = next.groups.flatMap((g) => g.fields);
      Object.assign(next.values, u.changes ?? {});
      for (const id of u.reset ?? []) (next.values as Record<string, unknown>)[id] = fields.find((f) => f.id === id)!.default;
      next.revision = `${doc.revision}+`;
      doc = next;
      return clone(doc);
    },
  };
  return { source, updates, state, set: (d: Doc) => (doc = d) };
}

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => {});
};
async function show(source: ConfigurationSource) {
  act(() => root.render(<ConfigurationForm source={source} />));
  await settle();
}
const row = (id: string) => host.querySelector<HTMLElement>(`[data-configuration-field="${id}"]`)!;
const buttonIn = (el: Element, text: string) => [...el.querySelectorAll("button")].find((b) => b.textContent === text)!;
const labelled = (el: Element) => {
  const id = el.getAttribute("aria-labelledby");
  return id ? document.getElementById(id)?.textContent : el.getAttribute("aria-label");
};
function type(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const set = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value")!.set!;
  act(() => {
    set.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const key = (el: Element, k: string) => act(() => void el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })));
const checks = (id: string) => [...row(id).querySelectorAll<HTMLButtonElement>('[role="checkbox"]')];
const radios = (id: string) => [...row(id).querySelectorAll<HTMLButtonElement>('[role="radio"]')];

describe("drawing every kind", () => {
  it("shows the document's title, groups, fields, descriptions, notices and details as given", async () => {
    await show(memory().source);
    expect(host.querySelector("h2")?.textContent).toBe("Preview preferences");
    expect(host.textContent).toContain("Appearance");
    expect(host.textContent).toContain("Use less space between rows.");
    expect(row("notes").textContent).toContain("Notes are optional.");
    const dl = row("managed").querySelector("dl")!;
    expect([...dl.querySelectorAll("dt")].map((d) => d.textContent)).toEqual(["Applies to", "Takes effect"]);
    expect([...dl.querySelectorAll("dd")].map((d) => d.textContent)).toEqual(["This preview only.", "When a new preview opens."]);
    expect(row("managed").textContent).toContain(`${t("settings-configuration.default")} ${t("settings-configuration.on")}`);
    expect(row("sections").textContent).toContain(`${t("settings-configuration.default")} ${formatList(["Summary"])}`);
  });

  it("names every control by its field's title", async () => {
    await show(memory().source);
    expect(row("compact").querySelector('[role="switch"]')?.getAttribute("aria-label")).toBe("Compact layout");
    expect(labelled(row("label").querySelector("input")!)).toBe("Display label");
    expect(labelled(row("notes").querySelector("textarea")!)).toBe("Notes");
    expect(row("layout").querySelector('[role="radiogroup"]')?.getAttribute("aria-label")).toBe("Layout");
    expect(row("motion").querySelector('[role="radiogroup"]')?.getAttribute("aria-label")).toBe("Motion");
    expect(labelled(row("accent").querySelector("select")!)).toBe("Accent");
    expect(labelled(row("sections").querySelector('[role="group"]')!)).toBe("Visible sections");
    expect(labelled(row("interval").querySelector("input")!)).toBe("Refresh interval");
  });

  it("shows read-only and disabled fields without a way to change them", async () => {
    await show(memory().source);
    expect((row("managed").querySelector('[role="switch"]') as HTMLButtonElement).disabled).toBe(true);
    expect(row("managed").textContent).toContain("This preference is managed elsewhere.");
    const plain = clone(example);
    plain.groups[0].fields.push({ id: "fixed", kind: "toggle", title: "Fixed", default: true, read_only: true });
    plain.values.fixed = true;
    await show(memory(plain, "plain").source);
    expect((row("fixed").querySelector('[role="switch"]') as HTMLButtonElement).disabled).toBe(true);
    expect(row("fixed").textContent).toContain(t("settings-configuration.read_only"));
  });

  it("keeps an unknown kind with its words and no control", async () => {
    const doc = clone(example);
    doc.groups[0].fields.push({ id: "future", kind: "slider", title: "From a newer version", description: "A newer Persona draws this.", default: 3 });
    doc.values.future = 3;
    await show(memory(doc).source);
    expect(row("future").textContent).toContain("From a newer version");
    expect(row("future").textContent).toContain(t("settings-configuration.unsupported_kind"));
    expect(row("future").querySelector("button, input, select, textarea")).toBeNull();
  });
});

describe("single-step choices", () => {
  it("a switch, a list, a segmented choice and a dropdown each write that field only", async () => {
    const m = memory();
    await show(m.source);
    act(() => (row("compact").querySelector('[role="switch"]') as HTMLButtonElement).click());
    await settle();
    act(() => row("layout").querySelector<HTMLButtonElement>('[data-choice="1"]')!.click());
    await settle();
    act(() => [...row("motion").querySelectorAll<HTMLButtonElement>('[role="radio"]')][1].click());
    await settle();
    const select = row("accent").querySelector("select")!;
    act(() => {
      select.value = "green";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await settle();
    expect(m.updates.map((u) => u.changes)).toEqual([{ compact: true }, { layout: "dense" }, { motion: "reduced" }, { accent: "green" }]);
    expect(m.updates.map((u) => u.revision)).toEqual(["revision-1", "revision-1+", "revision-1++", "revision-1+++"]);
  });

  it("reverts to the saved value and says why when its write fails", async () => {
    const m = memory();
    await show(m.source);
    m.state.fail = new ConfigurationError("storage_failed", "The disk is full");
    act(() => [...row("motion").querySelectorAll<HTMLButtonElement>('[role="radio"]')][1].click());
    await settle();
    expect(m.updates).toHaveLength(1);
    expect([...row("motion").querySelectorAll('[role="radio"]')].map((r) => r.getAttribute("aria-checked"))).toEqual(["true", "false"]);
    expect(row("motion").querySelector('[role="alert"]')?.textContent).toBe("The disk is full");
  });

  it("shows Saving… (not Saved) while the source hasn't answered", async () => {
    const m = memory();
    let go = () => {};
    m.state.hold = (g) => (go = g);
    await show(m.source);
    act(() => row("layout").querySelector<HTMLButtonElement>('[data-choice="1"]')!.click());
    await settle();
    expect(row("layout").textContent).toContain(t("settings-configuration.saving"));
    expect(row("layout").textContent).not.toContain(t("settings-configuration.saved"));
    m.state.hold = null;
    go();
    await settle();
    expect(row("layout").textContent).toContain(t("settings-configuration.saved"));
  });

  it("arrow keys move a list's choice", async () => {
    const m = memory();
    await show(m.source);
    const group = row("layout").querySelector('[role="radiogroup"]')!;
    key(group, "ArrowDown"); // from Comfortable to Dense
    await settle();
    expect(m.updates.at(-1)?.changes).toEqual({ layout: "dense" });
  });
});

describe("a saved value this build can't read", () => {
  it("keeps the hint and lets every control be corrected, never showing a default as committed", async () => {
    const m = memory(invalidValues());
    await show(m.source);
    const hint = t("settings-configuration.invalid_value");
    for (const id of ["compact", "label", "notes", "layout", "accent", "motion", "sections", "interval"]) expect(row(id).textContent, id).toContain(hint);

    // An unreadable toggle with an On default can be saved as Off in one write.
    expect(radios("compact").map((r) => r.getAttribute("aria-checked"))).toEqual(["false", "false"]);
    act(() => radios("compact").find((r) => r.textContent === t("settings-configuration.off"))!.click());
    await settle();
    expect(m.updates.at(-1)?.changes).toEqual({ compact: false });
    expect(row("compact").querySelector('[role="switch"]')?.getAttribute("aria-checked")).toBe("false");
    expect(row("compact").textContent).not.toContain(hint);

    // text and number: empty, typed, saved
    const label = row("label").querySelector("input")!;
    expect(label.value).toBe("");
    type(label, "Recovered");
    act(() => buttonIn(row("label"), t("settings-configuration.save")).click());
    await settle();
    expect(m.updates.at(-1)?.changes).toEqual({ label: "Recovered" });
    expect(label.value).toBe("Recovered");
    const notes = row("notes").querySelector("textarea")!;
    type(notes, "Draft");
    type(notes, "");
    act(() => buttonIn(row("notes"), t("settings-configuration.save")).click());
    await settle();
    expect(m.updates.at(-1)?.changes).toEqual({ notes: "" });
    const interval = row("interval").querySelector("input")!;
    expect(interval.value).toBe("");
    type(interval, "45");
    act(() => buttonIn(row("interval"), t("settings-configuration.save")).click());
    await settle();
    expect(m.updates.at(-1)?.changes).toEqual({ interval: 45 });

    // list and segmented: no choice appears selected; picking one writes it
    expect(radios("layout").every((r) => r.getAttribute("aria-checked") === "false")).toBe(true);
    act(() => radios("layout")[1].click());
    await settle();
    expect(m.updates.at(-1)?.changes).toEqual({ layout: "dense" });
    expect(radios("motion").every((r) => r.getAttribute("aria-checked") === "false")).toBe(true);
    act(() => radios("motion")[1].click());
    await settle();
    expect(m.updates.at(-1)?.changes).toEqual({ motion: "reduced" });

    // native dropdown: a disabled placeholder, never the default/first option; choosing writes
    const accent = row("accent").querySelector("select")!;
    expect(accent.value).toBe("");
    expect(accent.selectedOptions[0]?.disabled).toBe(true);
    act(() => {
      accent.value = "green";
      accent.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await settle();
    expect(m.updates.at(-1)?.changes).toEqual({ accent: "green" });

    // multiselect: nothing checked; checking and Save write the array
    expect(checks("sections").every((c) => c.getAttribute("aria-checked") === "false")).toBe(true);
    act(() => checks("sections")[0].click());
    act(() => buttonIn(row("sections"), t("settings-configuration.save")).click());
    await settle();
    expect(m.updates.at(-1)?.changes).toEqual({ sections: ["summary"] });
  });

  it("describes each control with the invalid-value hint", async () => {
    await show(memory(invalidValues()).source);
    const hint = t("settings-configuration.invalid_value");
    const hintId = (id: string) => [...row(id).querySelectorAll("p")].find((p) => p.textContent === hint)?.id;
    for (const [id, control] of [
      ["compact", "fieldset"],
      ["label", "input"],
      ["layout", "fieldset"],
      ["accent", "select"],
      ["motion", "fieldset"],
      ["interval", "input"],
    ] as const) {
      const element = row(id).querySelector(control)!;
      expect(element.getAttribute("aria-describedby"), id).toContain(hintId(id)!);
    }
    expect(row("label").querySelector("input")!.getAttribute("aria-invalid")).toBe("true");
  });
});

describe("a single-choice multiselect's Tab entry", () => {
  const tabStops = (id: string) => radios(id).filter((r) => r.tabIndex === 0);
  it("has exactly one: the saved choice", async () => {
    await show(memory(oneOfOne()).source);
    expect(radios("sections").map((r) => r.getAttribute("aria-checked"))).toEqual(["true", "false", "false"]);
    expect(tabStops("sections")).toEqual([radios("sections")[0]]);
  });

  it("keeps exactly one when a save is refused, with the draft still shown", async () => {
    const m = memory(oneOfOne());
    await show(m.source);
    act(() => radios("sections")[1].click());
    m.state.fail = new ConfigurationError("storage_failed", "The disk is full");
    act(() => buttonIn(row("sections"), t("settings-configuration.save")).click());
    await settle();
    expect(row("sections").querySelector('[role="alert"]')?.textContent).toBe("The disk is full");
    expect(radios("sections").map((r) => r.getAttribute("aria-checked"))).toEqual(["false", "true", "false"]);
    expect(tabStops("sections")).toEqual([radios("sections")[1]]);
  });

  it("keeps exactly one after Cancel, back on the saved choice", async () => {
    await show(memory(oneOfOne()).source);
    act(() => radios("sections")[1].click());
    act(() => buttonIn(row("sections"), t("settings-configuration.cancel")).click());
    expect(radios("sections").map((r) => r.getAttribute("aria-checked"))).toEqual(["true", "false", "false"]);
    expect(tabStops("sections")).toEqual([radios("sections")[0]]);
  });

  it("has exactly one tab stop and no checked radio when no saved value is valid", async () => {
    const doc = oneOfOne();
    delete (doc.values as Record<string, unknown>).sections;
    await show(memory(doc).source);
    expect(radios("sections").every((r) => r.getAttribute("aria-checked") === "false")).toBe(true);
    expect(tabStops("sections")).toEqual([radios("sections")[0]]);
    // an arrow key from the empty state picks the first option, without pretending it was saved
    key(row("sections").querySelector('[role="radiogroup"]')!, "ArrowDown");
    expect(radios("sections").map((r) => r.getAttribute("aria-checked"))).toEqual(["true", "false", "false"]);
    expect(tabStops("sections")).toEqual([radios("sections")[0]]);
  });
});

describe("a multiselect's pending replacement", () => {
  it("draws a maximum of one as a single choice: a direct A → B click replaces it at once, and Save sends [B]", async () => {
    const m = memory(oneOfOne());
    await show(m.source);
    expect(row("sections").querySelector('[role="radiogroup"]')).not.toBeNull();
    expect(radios("sections")).toHaveLength(3);
    expect(radios("sections")[0].getAttribute("aria-checked")).toBe("true");
    act(() => radios("sections")[1].click()); // Activity: replaces Summary in one click
    expect(m.updates).toHaveLength(0);
    expect(radios("sections").map((r) => r.getAttribute("aria-checked"))).toEqual(["false", "true", "false"]);
    expect(row("sections").querySelector('[role="alert"]')).toBeNull();
    expect(buttonIn(row("sections"), t("settings-configuration.save"))).toBeDefined();
    act(() => buttonIn(row("sections"), t("settings-configuration.save")).click());
    await settle();
    expect(m.updates.at(-1)?.changes).toEqual({ sections: ["activity"] });
    expect(radios("sections").map((r) => r.getAttribute("aria-checked"))).toEqual(["false", "true", "false"]);
    expect(row("sections").textContent).toContain(t("settings-configuration.saved"));
  });

  it("moves a single choice with the arrow keys", async () => {
    const m = memory(oneOfOne());
    await show(m.source);
    const group = row("sections").querySelector('[role="radiogroup"]')!;
    key(group, "ArrowDown"); // Summary -> Activity
    expect(radios("sections").map((r) => r.getAttribute("aria-checked"))).toEqual(["false", "true", "false"]);
    act(() => buttonIn(row("sections"), t("settings-configuration.save")).click());
    await settle();
    expect(m.updates.at(-1)?.changes).toEqual({ sections: ["activity"] });
  });

  it("still checks a count for an ordinary multiselect, and Cancel puts the checks back", async () => {
    const m = memory();
    await show(m.source);
    act(() => checks("sections")[0].click()); // uncheck the only choice (min_selected 1)
    act(() => buttonIn(row("sections"), t("settings-configuration.save")).click());
    await settle();
    expect(m.updates).toHaveLength(0);
    expect(row("sections").querySelector('[role="alert"]')?.textContent).toBe(t("settings-configuration.error_min_selected"));
    act(() => buttonIn(row("sections"), t("settings-configuration.cancel")).click());
    expect(checks("sections")[0].getAttribute("aria-checked")).toBe("true");
    expect(row("sections").querySelector('[role="alert"]')).toBeNull();
  });

  it("keeps checks changed while their save is on the way", async () => {
    const m = memory();
    let go = () => {};
    m.state.hold = (g) => (go = g);
    await show(m.source);
    act(() => checks("sections")[1].click()); // summary + activity
    act(() => buttonIn(row("sections"), t("settings-configuration.save")).click());
    await settle(); // the write is held
    act(() => checks("sections")[2].click()); // summary + activity + files (over the max, local only)
    act(() => checks("sections")[1].click()); // summary + files
    m.state.hold = null;
    act(() => go());
    await settle();
    expect(checks("sections")[0].getAttribute("aria-checked")).toBe("true");
    expect(checks("sections")[1].getAttribute("aria-checked")).toBe("false");
    expect(checks("sections")[2].getAttribute("aria-checked")).toBe("true");
    expect(buttonIn(row("sections"), t("settings-configuration.save"))).toBeDefined();
  });
});

describe("typed text", () => {
  it("Save appears once it differs; Enter saves a single line; Escape puts it back", async () => {
    const m = memory();
    await show(m.source);
    const input = row("label").querySelector("input")!;
    expect(buttonIn(row("label"), t("settings-configuration.save"))).toBeUndefined();
    type(input, "Sam");
    expect(buttonIn(row("label"), t("settings-configuration.save"))).toBeDefined();
    key(input, "Escape");
    expect(input.value).toBe("Preview");
    type(input, "Sam 👋");
    expect(row("label").textContent).toContain(t("settings-configuration.count", { n: 5, max: 20 }));
    key(input, "Enter");
    await settle();
    expect(m.updates.at(-1)?.changes).toEqual({ label: "Sam 👋" });
    expect(row("label").textContent).toContain(t("settings-configuration.saved"));
  });

  it("keeps text typed while its save is on the way", async () => {
    const m = memory();
    let go = () => {};
    m.state.hold = (g) => (go = g);
    await show(m.source);
    const input = row("label").querySelector("input")!;
    type(input, "A");
    key(input, "Enter");
    await settle(); // the write is held
    type(input, "AB"); // typed while the promise is pending
    expect(input.value).toBe("AB");
    m.state.hold = null;
    act(() => go());
    await settle();
    expect(input.value).toBe("AB"); // the newer text stays, not saved
    expect(row("label").textContent).not.toContain(t("settings-configuration.saved"));
    expect(buttonIn(row("label"), t("settings-configuration.save"))).toBeDefined();
  });

  it("keeps the typed text, and says why, when saving fails", async () => {
    const m = memory();
    await show(m.source);
    const area = row("notes").querySelector("textarea")!;
    type(area, "Line one\nline two");
    m.state.fail = new ConfigurationError("storage_failed", "The disk is full");
    act(() => buttonIn(row("notes"), t("settings-configuration.save")).click());
    await settle();
    expect(area.value).toBe("Line one\nline two");
    expect(row("notes").querySelector('[role="alert"]')?.textContent).toBe("The disk is full");
  });

  it("an answer that never arrives keeps the draft, says the save is unconfirmed, and blocks Save until Reload", async () => {
    const m = memory();
    m.state.fail = new TypeError("Failed to fetch");
    await show(m.source);
    const input = row("label").querySelector("input")!;
    type(input, "Mine");
    key(input, "Enter");
    await settle();
    expect(host.querySelector('[role="alert"]')?.textContent).toBe(t("settings-configuration.error_unconfirmed"));
    expect(input.value).toBe("Mine");
    act(() => buttonIn(row("label"), t("settings-configuration.save")).click());
    await settle();
    expect(m.updates).toHaveLength(1); // Save is disabled while the outcome is unknown
    act(() => buttonIn(host, t("settings-configuration.reload")).click());
    await settle();
    expect(host.textContent).not.toContain(t("settings-configuration.error_unconfirmed"));
    act(() => buttonIn(row("label"), t("settings-configuration.save")).click());
    await settle();
    expect(m.updates).toHaveLength(2);
  });

  it("a conflict keeps the text and offers Reload, which keeps it too", async () => {
    const m = memory();
    await show(m.source);
    m.set({ ...clone(example), revision: "elsewhere", values: { ...example.values, label: "Theirs" } });
    const input = row("label").querySelector("input")!;
    type(input, "Mine");
    key(input, "Enter");
    await settle();
    const alert = host.querySelector('[role="alert"]')!;
    expect(alert.textContent).toBe(t("settings-configuration.error_configuration_conflict"));
    act(() => buttonIn(host, t("settings-configuration.reload")).click());
    await settle();
    expect(host.textContent).not.toContain(t("settings-configuration.error_configuration_conflict"));
    expect(row("label").querySelector("input")!.value).toBe("Mine");
    act(() => buttonIn(row("label"), t("settings-configuration.cancel")).click());
    expect(row("label").querySelector("input")!.value).toBe("Theirs");
  });

  it("a number: arrows step it, and a value out of range is said before sending", async () => {
    const m = memory();
    await show(m.source);
    const input = row("interval").querySelector("input")!;
    key(input, "ArrowUp");
    expect(input.value).toBe("35");
    type(input, "500");
    key(input, "Enter");
    expect(m.updates).toHaveLength(0);
    expect(row("interval").querySelector('[role="alert"]')?.textContent).toBe(t("settings-configuration.error_maximum"));
    type(input, "40");
    key(input, "Enter");
    await settle();
    expect(m.updates.at(-1)?.changes).toEqual({ interval: 40 });
  });
});

describe("resetting", () => {
  it("offers Reset only where the value differs, and sends reset", async () => {
    const m = memory({ ...clone(example), values: { ...example.values, interval: 40 } });
    await show(m.source);
    expect(buttonIn(row("layout"), t("settings-configuration.reset"))).toBeUndefined();
    expect(buttonIn(row("managed"), t("settings-configuration.reset"))).toBeUndefined();
    act(() => buttonIn(row("interval"), t("settings-configuration.reset")).click());
    await settle();
    expect(m.updates.at(-1)).toMatchObject({ reset: ["interval"] });
    expect(row("interval").querySelector("input")!.value).toBe("30");
  });

  it("is not offered while a multiselect has unsaved checks", async () => {
    const m = memory({ ...clone(example), values: { ...example.values, sections: ["summary", "activity"] } });
    await show(m.source);
    expect(buttonIn(row("sections"), t("settings-configuration.reset"))).toBeDefined();
    act(() => checks("sections")[2].click()); // summary + activity + files: a pending (over-max) check
    expect(buttonIn(row("sections"), t("settings-configuration.reset"))).toBeUndefined();
    expect(m.updates).toHaveLength(0);
  });
});

describe("what can't be read or written", () => {
  it("a version it doesn't know is shown with every control off", async () => {
    await show(memory({ ...clone(example), schema_version: 2 }).source);
    expect(host.textContent).toContain(t("settings-configuration.unsupported_version"));
    expect((row("compact").querySelector('[role="switch"]') as HTMLButtonElement).disabled).toBe(true);
    expect(row("label").querySelector("input")!.disabled).toBe(true);
  });

  it("a read that fails says why and has Try again", async () => {
    let fail = true;
    const source: ConfigurationSource = { key: "x", read: async () => { if (fail) throw new ConfigurationError("configuration_not_found"); return clone(example); }, update: async () => clone(example) };
    await show(source);
    expect(host.textContent).toContain(t("settings-configuration.error_configuration_not_found"));
    fail = false;
    act(() => buttonIn(host, t("settings-configuration.retry")).click());
    await settle();
    expect(host.querySelector("h2")?.textContent).toBe("Preview preferences");
  });
});

describe("another source", () => {
  it("starts over for another key, and the first source's late answer is ignored", async () => {
    let release = (_: unknown) => {};
    const slow: ConfigurationSource = { key: "slow", read: () => new Promise((r) => (release = r)), update: async () => clone(example) };
    await show(slow);
    const other = memory({ ...clone(example), title: "Other settings" }, "other");
    await show(other.source);
    expect(host.querySelector("h2")?.textContent).toBe("Other settings");
    release({ ...clone(example), title: "Too late" });
    await settle();
    expect(host.querySelector("h2")?.textContent).toBe("Other settings");
  });
});
