import type { DesktopBridge } from "../../../spec/desktop-bridge.d.ts";

declare global {
  interface Window { readonly personaDesktop?: DesktopBridge }
}

export {};
