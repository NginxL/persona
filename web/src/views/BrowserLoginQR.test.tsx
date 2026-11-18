// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { t } from "../i18n";
const fake = vi.hoisted(() => ({ paired: vi.fn() }));
vi.mock("../lib/api", () => ({ errorText: (e: Error) => e.message, setPairedToken: fake.paired }));
vi.mock("./settings/LoginSteps", () => ({ QRCode: () => <svg aria-label="sign-in QR" /> }));
import { BrowserLoginQR } from "./BrowserLoginQR";
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root, host: HTMLDivElement;
let status = "pending"; // what the hub's poll answers
let number = "47"; // what the phone shows
let pollCount = 0;
let starts = 0;
let clockOffset = 0;
let pollFailure: "" | "network" | "503" = "";
let confirmFailure: "" | "network" = "";
let pollBodies: string[] = [];
let startBodies: string[] = [];
let confirmBodies: string[] = [];
let oldHub = false;
let onDone = vi.fn<() => void>();
const reply = (data: unknown) => Promise.resolve({ ok: true, json: () => Promise.resolve(data) });
const refuse = (status: number, body: unknown = {}) => Promise.resolve({ ok: false, status, json: () => Promise.resolve(body) });
const approved = { state: "approved", token: "device-token", id: "device-id" };
beforeEach(() => {
 vi.useFakeTimers(); fake.paired.mockReset(); status = "pending"; number = "47"; pollCount = 0; starts = 0; clockOffset = 0;
 pollFailure = ""; confirmFailure = ""; pollBodies = []; startBodies = []; confirmBodies = []; oldHub = false; onDone = vi.fn<() => void>();
 localStorage.clear();
 Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
 vi.stubGlobal("fetch", vi.fn((url: string, options: RequestInit) => {
  const body = String(options.body);
  if (url.endsWith("/start")) {
    startBodies.push(body);
    if (oldHub && body.includes("replaces")) return refuse(400);
    starts++; return reply({ id: `request-${starts}`, secret: "browser-only", expires_at: new Date(Date.now()+clockOffset+300000).toISOString(), qr: { rows: ["1"] } });
  }
  if (url.endsWith("/confirm")) {
    confirmBodies.push(body);
    if (status !== "confirm") return refuse(409, { code: "not_waiting", state: status });
    if (JSON.parse(body).number !== number) { status = "voided"; return refuse(409, { code: "number_mismatch", state: "voided" }); }
    status = "approved";
    if (confirmFailure) { confirmFailure = ""; return Promise.reject(new TypeError("fixture lost response")); }
    return reply(approved);
  }
  pollCount++;
  pollBodies.push(body);
  if (pollFailure) {
    const failure = pollFailure; pollFailure = "";
    return failure === "network" ? Promise.reject(new TypeError("fixture lost response")) : refuse(503);
  }
  if (status === "expired") return refuse(410);
  return reply(status === "approved" ? approved : { state: status });
 }));
 host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); vi.useRealTimers(); vi.unstubAllGlobals(); });
async function mount() { await act(async () => { root.render(<BrowserLoginQR onDone={onDone} />); }); }
async function tick(ms = 1500) { await act(async () => { await vi.advanceTimersByTimeAsync(ms); }); }
const field = () => host.querySelector<HTMLInputElement>("#login-qr-number");
async function type(value: string) {
 await act(async () => {
  const input = field()!;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
 });
 await act(async () => { host.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });
}

it("asks for the phone's number after it allows, and signs in once it matches", async () => {
 await mount(); expect(fake.paired).not.toHaveBeenCalled(); expect(field()).toBeNull();
 status = "scanned"; await tick(); expect(host.textContent).toContain(t("login.qr_scanned"));
 status = "confirm"; await tick();
 expect(field()).not.toBeNull(); expect(host.textContent).toContain(t("login.qr_number_hint"));
 expect(host.querySelector('svg[aria-label="sign-in QR"]')).toBeNull();
 expect(fake.paired).not.toHaveBeenCalled();
 await type("47");
 expect(JSON.parse(confirmBodies[0])).toEqual({ id: "request-1", secret: "browser-only", number: "47" });
 expect(fake.paired).toHaveBeenCalledExactlyOnceWith("device-token", "device-id"); expect(onDone).toHaveBeenCalledOnce();
 await tick(6000); expect(fake.paired).toHaveBeenCalledOnce();
});

it("keeps only two digits", async () => {
 status = "confirm"; await mount();
 await act(async () => {
  const input = field()!;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "4a75");
  input.dispatchEvent(new Event("input", { bubbles: true }));
 });
 expect(field()!.value).toBe("47");
});

it("a wrong number cancels the sign-in for good", async () => {
 status = "confirm"; await mount(); await type("12");
 expect(host.textContent).toContain(t("login.qr_voided")); expect(field()).toBeNull();
 expect(host.textContent).not.toContain(t("login.qr_hint")); expect(fake.paired).not.toHaveBeenCalled();
 const n = pollCount; await tick(6000); expect(pollCount).toBe(n);
});

it("picks the credential up by polling when the number's reply is lost", async () => {
 status = "confirm"; confirmFailure = "network"; await mount(); await type("47");
 expect(fake.paired).not.toHaveBeenCalled();
 await tick(); expect(fake.paired).toHaveBeenCalledExactlyOnceWith("device-token", "device-id");
});

it("stops after denial and refreshes into a new request", async () => {
 await mount(); status = "denied"; await tick(); expect(host.textContent).toContain(t("login.qr_denied"));
 expect(host.textContent).not.toContain(t("login.qr_hint"));
 const n = pollCount; await tick(6000); expect(pollCount).toBe(n);
 status = "pending"; await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());
 expect(starts).toBe(2); expect(fake.paired).not.toHaveBeenCalled();
});

it("pauses network polling while hidden and refuses an expired code", async () => {
 await mount(); const n = pollCount;
 Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
 await tick(6000); expect(pollCount).toBe(n);
 status = "expired";
 Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
 await tick(); expect(host.textContent).toContain(t("login.qr_expired")); expect(fake.paired).not.toHaveBeenCalled();
});

it("uses the hub expiry response even when the browser clock is ahead", async () => {
 clockOffset = -360000; await mount();
 expect(pollCount).toBe(1); expect(host.textContent).not.toContain(t("login.qr_expired"));
 status = "expired"; await tick(); expect(host.textContent).toContain(t("login.qr_expired"));
});

it.each(["network", "503"] as const)("retries the same approved challenge after %s failure", async (failure) => {
 status = "approved"; pollFailure = failure; await mount();
 expect(fake.paired).not.toHaveBeenCalled(); expect(host.textContent).toContain(t("login.qr_retry"));
 await tick(3000); expect(starts).toBe(1); expect(pollBodies).toHaveLength(2); expect(pollBodies[0]).toBe(pollBodies[1]);
 expect(fake.paired).toHaveBeenCalledExactlyOnceWith("device-token", "device-id");
});

it("names this browser's previous credential for the hub to replace, then forgets the kept one", async () => {
 localStorage.setItem("persona.unrevokedTokenId", "dev_old");
 status = "approved"; await mount();
 expect(JSON.parse(startBodies[0])).toMatchObject({ replaces: "dev_old" });
 expect(fake.paired).toHaveBeenCalledExactlyOnceWith("device-token", "device-id");
 expect(localStorage.getItem("persona.unrevokedTokenId")).toBeNull();
});

it("starts without replacing on a hub that does not know the field", async () => {
 localStorage.setItem("persona.unrevokedTokenId", "dev_old"); oldHub = true;
 status = "approved"; await mount();
 expect(startBodies).toHaveLength(2); expect(JSON.parse(startBodies[1])).not.toHaveProperty("replaces");
 expect(fake.paired).toHaveBeenCalledOnce();
 expect(localStorage.getItem("persona.unrevokedTokenId")).toBe("dev_old");
});
