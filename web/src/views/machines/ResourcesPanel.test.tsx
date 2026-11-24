// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { jobAgeSeconds, ResourceCard, ResourcesPanel, type ResourceStatus } from "./ResourcesPanel";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
beforeEach(async () => { await initI18n("en"); host = document.createElement("div"); document.body.append(host); root = createRoot(host); });
afterEach(() => { act(() => root.unmount()); host.remove(); vi.unstubAllGlobals(); });
const resource: ResourceStatus = {
  id: "ci.example", capacity: 3, mirror: { machine: "computer-a", target: "runner.example.com" },
  mirror_state: { unknown: true, observed_at: "2026-11-22T10:00:00Z", queue: { slots: 3, running: [{ ticket: "one", project: "example", commit: "a".repeat(40), groups: ["web"], started_at: "2026-11-22T09:59:00Z" }], waiting: [{ ticket: "two", project: "example", commit: "a".repeat(40), groups: ["web"], position: 1, duplicate_of: "one" }] } },
  leases: [{ id: "lease-one", state: "granted", holder: { label: "Example build", machine: "computer-a" }, joiners: ["session-two"] }],
};
it("retains stale tickets and shows duplicate and joined results", () => {
  act(() => root.render(<ResourceCard resource={resource} now={Date.parse("2026-11-22T10:01:00Z")} />));
  expect(host.textContent).toContain("Mirror is stale: 60 seconds");
  expect(host.textContent).toContain("Same result as one");
  expect(host.textContent).toContain("1 more waiting for this result");
  expect(host.querySelectorAll("tbody tr")).toHaveLength(2);
});
it("loads the registry and keeps configuration behind an explicit action", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ resources: [] }), { headers: { "Content-Type": "application/json" } })));
  await act(async () => { root.render(<ResourcesPanel />); await new Promise((resolve) => setTimeout(resolve, 0)); });
  expect(host.textContent).toContain("No shared runners are registered.");
  expect(host.querySelector("textarea")).toBeNull();
  act(() => (host.querySelector("button") as HTMLButtonElement).click());
  expect(host.querySelector("textarea")?.value).toBe("[]");
});
it.each(["en", "zh-Hans", "ja"])("renders the unknown state in %s", async (language) => {
  await initI18n(language);
  act(() => root.render(<ResourceCard resource={{ ...resource, mirror_state: { unknown: true, observed_at: "0001-01-01T00:00:00Z" } }} now={Date.now()} />));
  expect(host.textContent).not.toContain("resources.");
  expect(host.querySelector("[role=status]")).not.toBeNull();
});
it("keeps separate legacy rows whose ticket and commit are empty", () => {
  const legacy: ResourceStatus = { ...resource, mirror_state: { unknown: false, observed_at: "2026-11-22T10:00:00Z", queue: { slots: 3, running: [
    { ticket: "", run: "example-legacy-one", slot: 1, project: "example", commit: "", groups: ["web"] },
    { ticket: "", run: "example-legacy-two", slot: 2, project: "example", commit: "", groups: ["web"] },
  ], waiting: [] } } };
  act(() => root.render(<ResourceCard resource={legacy} now={Date.parse("2026-11-22T10:00:05Z")} />));
  expect(host.querySelectorAll("tbody tr")).toHaveLength(2);
  expect(host.textContent).toContain("example-legacy-one");
  expect(host.textContent).toContain("example-legacy-two");
  expect(host.textContent).not.toContain("Same result as");
});

it("uses a running job start when the wire includes a zero enqueue time", () => {
  expect(jobAgeSeconds({ ticket: "one", project: "example", commit: "", groups: ["web"], enqueued_at: "0001-01-01T00:00:00Z", started_at: "2026-11-22T10:00:00Z" }, Date.parse("2026-11-22T10:01:00Z"))).toBe(60);
  expect(jobAgeSeconds({ ticket: "one", project: "example", commit: "", groups: ["web"] }, Date.now())).toBeNull();
});
