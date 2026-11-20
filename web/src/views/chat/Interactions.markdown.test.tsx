// @vitest-environment jsdom
// Script asks opt into the chat's Markdown renderer with format: "markdown" (#10857). Missing and unknown formats
// keep their literal card containers, and rendering a question never changes its original answer key.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const calls = vi.hoisted(() => [] as { path: string; body: Record<string, unknown> }[]);
vi.mock("../../lib/ops", () => ({
  apiOp: vi.fn(async (path: string, init: { json: Record<string, unknown> }) => {
    calls.push({ path, body: init.json });
    return {};
  }),
}));
import { InteractionCard } from "./Interactions";
import { getState } from "../../lib/store";
import type { Interaction, Session } from "../../lib/types";
import { loadPages } from "../../lib/lazy-page";

await loadPages();

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const source = [
  "**Deploy** the release?",
  "",
  "- Run tests",
  "- Check logs",
  "",
  "| Step | State |",
  "| --- | --- |",
  "| Build | Ready |",
].join("\n");

// The wire field predates its Interaction declaration: parse real JSON rather than widening the production model here.
const permission = (format?: string) => JSON.parse(JSON.stringify({
  id: "p_markdown", machine_id: "m", session_id: "s", kind: "permission", tool_name: "personad_ask",
  agent: "script", summary: source, ...(format === undefined ? {} : { format }),
})) as Interaction;
const question = (format?: string) => JSON.parse(JSON.stringify({
  id: "q_markdown", machine_id: "m", session_id: "s", kind: "question", tool_name: "personad_ask",
  agent: "script", questions: [{ question: source, multi_select: false, options: [{ label: "A" }, { label: "B" }] }],
  ...(format === undefined ? {} : { format }),
})) as Interaction;

const plainFormats = [
  { name: "missing", value: undefined },
  { name: "unknown", value: "html" },
];

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const show = (ix: Interaction) => act(() => root.render(<InteractionCard ix={ix} />));
const card = () => host.querySelector<HTMLElement>("[data-card]")!;

function expectChatMarkdown() {
  const rendered = card().querySelector<HTMLElement>(".prose-j");
  expect(rendered).not.toBeNull();
  expect(rendered!.querySelector("strong")?.textContent).toBe("Deploy");
  expect([...rendered!.querySelectorAll("ul > li")].map((li) => li.textContent)).toEqual(["Run tests", "Check logs"]);
  expect([...rendered!.querySelectorAll("table th")].map((th) => th.textContent)).toEqual(["Step", "State"]);
  expect([...rendered!.querySelectorAll("table td")].map((td) => td.textContent)).toEqual(["Build", "Ready"]);
}

function expectNoMarkdown() {
  expect(card().querySelector(".prose-j")).toBeNull();
  expect(card().querySelector("strong, ul, ol, table")).toBeNull();
}

beforeEach(() => {
  calls.length = 0;
  Object.assign(getState(), { sessions: { m: [{ machine_id: "m", session_id: "s" } as Session] }, interactions: [], answered: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("script permission summaries", () => {
  it("uses the chat Markdown renderer when format is markdown", () => {
    show(permission("markdown"));
    expectChatMarkdown();
  });

  it.each(plainFormats)("keeps the literal preformatted summary when format is $name", ({ value }) => {
    show(permission(value));
    expect(host.querySelector<HTMLPreElement>("[data-card] pre")?.textContent).toBe(source);
    expectNoMarkdown();
  });
});

describe("script question cards", () => {
  it("uses the chat Markdown renderer and preserves the raw answer key when format is markdown", async () => {
    show(question("markdown"));
    expectChatMarkdown();
    const option = [...host.querySelectorAll<HTMLButtonElement>("button[aria-pressed]")].find((button) => button.textContent?.includes("A"))!;
    act(() => option.click());
    await flush();
    expect(calls).toEqual([{ path: "/interactions/q_markdown/resolve", body: { answers: { [source]: "A" } } }]);
  });

  it.each(plainFormats)("keeps the literal question title when format is $name", ({ value }) => {
    show(question(value));
    const literalTitle = [...card().querySelectorAll<HTMLElement>("div")].find((el) => el.children.length === 0 && el.textContent === source);
    expect(literalTitle).toBeDefined();
    expectNoMarkdown();
  });
});
