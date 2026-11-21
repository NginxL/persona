// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import type { HubUpdateStatus } from "../../lib/hub-update";

let status: HubUpdateStatus;
const saved: unknown[] = [];
let backupReads = 0;
const events = new Map<string, (message: { update?: HubUpdateStatus }) => void>();
vi.mock("../../lib/hub-update", async (original) => ({
  ...await original<typeof import("../../lib/hub-update")>(),
  hubUpdate: async () => status,
  hubUpdateHistory: async () => [],
  hubUpdateBackups: async () => { backupReads++; return [{ id: "snapshot-fixture", version: "web-dev-backup", build: 202611120001,
    commit: "c".repeat(40), updated_at: "2026-11-12T10:00:00Z", created_at: "2026-11-13T10:00:00Z" }]; },
  applyHubUpdateBackup: async (id: string) => {
    saved.push({ backup: id });
    status = { ...status, state: "applying_backup", auto: false };
    return status;
  },
  setHubUpdateSettings: async (input: { source?: "release" | "beta" | "web-dev"; development_backup_directory?: string }) => {
    saved.push(input);
    status = { ...status, ...input, auto: false };
    if (input.development_backup_directory !== undefined || (input as { development_backup_retain?: number }).development_backup_retain !== undefined) {
      status = { ...status, development: { ...status.development!,
        backup_directory: input.development_backup_directory === undefined ? status.development!.backup_directory
          : input.development_backup_directory || status.development!.backup_default_directory,
        backup_retain: (input as { development_backup_retain?: number }).development_backup_retain ?? status.development!.backup_retain } };
    }
    return status;
  },
}));
vi.mock("../../lib/store", async (original) => ({
  ...await original<typeof import("../../lib/store")>(),
  onHubEvent: (type: string, callback: (message: { update?: HubUpdateStatus }) => void) => {
    events.set(type, callback); return () => { events.delete(type); };
  },
  useStore: () => [],
}));
vi.mock("./ExtensionDownloadMirrors", () => ({ ExtensionDownloadMirrors: () => null }));
const { HubSettings } = await import("./HubSettings");
let root: ReturnType<typeof createRoot> | undefined;
let host: HTMLDivElement;
afterEach(() => { act(() => root?.unmount()); host?.remove(); saved.length = 0; backupReads = 0; events.clear(); });

async function mount(state = "available") {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await initI18n("en");
  status = {
    version: "web-dev-old", build: 202611130000, channel: "release", source: "web-dev", can_update: true,
    state, busy: [], auto: false, auto_notice: false, confirmed: true, auto_update_nodes: false,
    development: { configured: true, branch: "main", current_commit: "a".repeat(40), current_updated_at: "2026-11-13T10:00:00Z",
      backup_directory: "/var/snapshots", backup_default_directory: "/home/persona/.persona/update/development-backups",
      backup_retain: 3, builtin_quiesce: true },
    latest: { version: "web-dev-new", build: 202611130001, newer: true, bad: false, commit: "b".repeat(40), branch: "main",
      updated_at: "2026-11-14T10:00:00Z",
      backup_id: state === "applying_backup" ? "snapshot-fixture" : undefined },
    backup: { id: "snapshot-fixture", retained: 3, pruned: ["snapshot-old"] }
  };
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root!.render(<HubSettings />));
}

it("offers beta as an explicit reversible source without enabling it by default", async () => {
  await mount();
  await act(async () => events.get("hub_update")!({ update: { ...status, source: "release" } }));
  const source = host.querySelector('select[aria-label="Update source"]') as HTMLSelectElement;
  expect(source.value).toBe("release");
  expect(source.textContent).toContain("Beta (prereleases)");
  await act(async () => { source.value = "beta"; source.dispatchEvent(new Event("change", { bubbles: true })); });
  expect(saved).toEqual([{ source: "beta" }]);
  expect(source.value).toBe("beta");
  expect(host.textContent).toContain("Choose Release to return to stable updates");
  await act(async () => { source.value = "release"; source.dispatchEvent(new Event("change", { bubbles: true })); });
  expect(saved).toEqual([{ source: "beta" }, { source: "release" }]);
  expect(host.textContent).not.toContain("Choose Release to return to stable updates");
});

it("puts historical program versions in the development update selector and hides them on release", async () => {
  await mount();
  expect(host.textContent).toContain("a".repeat(12));
  expect(host.textContent).toContain("b".repeat(12));
  const versionGroup = host.querySelector('[data-settings-group="hub-version"]')!;
  expect(versionGroup.textContent).toContain(`Installed commit2026-11-13 · ${"a".repeat(12)}`);
  expect(versionGroup.textContent).toContain(`Latest commit2026-11-14 · ${"b".repeat(12)}`);
  expect(versionGroup.textContent).not.toContain("Updated at");
  expect(host.querySelector('select[aria-label="Update version"]')?.textContent).toContain("c".repeat(12));
  const automatic = host.querySelector('[role="switch"]') as HTMLButtonElement;
  expect(automatic.getAttribute("aria-checked")).toBe("false");
  const source = host.querySelector('select[aria-label="Update source"]') as HTMLSelectElement;
  await act(async () => { source.value = "release"; source.dispatchEvent(new Event("change", { bubbles: true })); });
  expect(saved).toEqual([{ source: "release" }]);
  expect(host.textContent).not.toContain("snapshot-fixture");
  expect(host.querySelector('select[aria-label="Update version"]')).toBeNull();
});

it("confirms program-only restoration and applies the selected backup", async () => {
  await mount();
  const version = host.querySelector('select[aria-label="Update version"]') as HTMLSelectElement;
  expect(version).not.toBeNull();
  await act(async () => { version.value = "snapshot-fixture"; version.dispatchEvent(new Event("change", { bubbles: true })); });
  const apply = [...host.querySelectorAll("button")].find(b => b.textContent === "Apply backup")!;
  await act(async () => apply.click());
  const dialog = document.querySelector('[role="dialog"]')!;
  expect(dialog.textContent).toContain("current database and history");
  expect(saved).toEqual([]);
  const confirm = [...dialog.querySelectorAll("button")].find(b => b.textContent === "Apply backup")!;
  await act(async () => confirm.click());
  expect(saved).toEqual([{ backup: "snapshot-fixture" }]);
  expect((host.querySelector('select[aria-label="Update version"]') as HTMLSelectElement).disabled).toBe(true);
});

it("keeps the current-release message hidden when a historical backup is selected", async () => {
  await mount();
  await act(async () => events.get("hub_update")!({ update: {
    ...status, last_check: "2026-11-14T10:00:00Z", latest: { ...status.latest!, newer: false },
  } }));
  expect(host.textContent).toContain("Up to date");
  const version = host.querySelector('select[aria-label="Update version"]') as HTMLSelectElement;
  await act(async () => { version.value = "snapshot-fixture"; version.dispatchEvent(new Event("change", { bubbles: true })); });
  expect(host.textContent).toContain("Apply backup");
  expect(host.textContent).not.toContain("Up to date");
});

it("refreshes backups when an update finishes without a changed build or receipt", async () => {
  await mount();
  expect(backupReads).toBe(1);
  for (const state of ["building", "backing_up", "waiting_idle", "confirming"]) {
    await act(async () => events.get("hub_update")!({ update: { ...status, state } }));
  }
  expect(backupReads).toBe(1);
  await act(async () => events.get("hub_update")!({ update: { ...status, state: "updated" } }));
  expect(backupReads).toBe(2);
});

it.each(["building", "backing_up", "applying_backup"])("keeps channel and duplicate actions disabled while %s", async state => {
  await mount(state);
  expect((host.querySelector("select") as HTMLSelectElement).disabled).toBe(true);
  expect([...host.querySelectorAll("button")].some(button => button.textContent === "Update hub")).toBe(false);
  expect(host.textContent).toContain(state === "building" ? "Building" : state === "backing_up" ? "Backing up" : "Preparing backup");
  if (state === "applying_backup") {
    const version = host.querySelector('select[aria-label="Update version"]') as HTMLSelectElement;
    expect(version.value).toBe("snapshot-fixture");
    expect(version.options[0].textContent).not.toContain("b".repeat(12));
  }
});

it("saves a declarative backup folder and retention, and resets to the recommended folder", async () => {
  await mount();
  const group = host.querySelector('[data-settings-group="hub-dev-backup"]')!;
  expect(group.textContent).toContain("Development backup");
  expect(group.textContent).toContain("The hub pauses its own writers");
  const folder = group.querySelector('input[aria-label="Backup folder"]') as HTMLInputElement;
  const retain = group.querySelector('select[aria-label="Snapshots to keep"]') as HTMLSelectElement;
  expect(folder.placeholder).toBe("/home/persona/.persona/update/development-backups");
  expect(folder.value).toBe("/var/snapshots");
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setValue.call(folder, "/srv/persona-snapshots");
    folder.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => { retain.value = "10"; retain.dispatchEvent(new Event("change", { bubbles: true })); });
  const save = () => [...group.querySelectorAll("button")].find((b) => b.textContent === "Save")!;
  await act(async () => save().click());
  expect(saved).toContainEqual({ development_backup_directory: "/srv/persona-snapshots", development_backup_retain: 10 });
  await act(async () => [...group.querySelectorAll("button")].find((b) => b.textContent === "Use recommended")!.click());
  expect((group.querySelector('input[aria-label="Backup folder"]') as HTMLInputElement).value).toBe("");
  await act(async () => save().click());
  expect(saved).toContainEqual({ development_backup_directory: "", development_backup_retain: 10 });
});

it("reloads the snapshot list after the folder changes", async () => {
  await mount();
  expect(backupReads).toBe(1);
  const group = host.querySelector('[data-settings-group="hub-dev-backup"]')!;
  const folder = group.querySelector('input[aria-label="Backup folder"]') as HTMLInputElement;
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setValue.call(folder, "/srv/persona-snapshots");
    folder.dispatchEvent(new Event("input", { bubbles: true }));
  });
  const save = [...group.querySelectorAll("button")].find((b) => b.textContent === "Save")!;
  await act(async () => save.click());
  // A snapshot listed from the old folder cannot be found in the new one.
  expect(backupReads).toBe(2);
});
