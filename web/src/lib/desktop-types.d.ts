import type { DesktopBridge } from "../../../spec/desktop-bridge";

declare global {
  interface Window { readonly personaDesktop?: DesktopBridge }
}

export {};
