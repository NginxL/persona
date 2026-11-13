export interface DesktopNotification {
  title: string;
  body: string;
  tag: string;
  route: string;
  silent?: boolean;
  ix?: string;
}

export interface DesktopBridge {
  readonly version: 1;
  readonly platform: string;
  readonly deviceName: string;
  notificationPermission(): string;
  setBadge(count: number): void;
  notify(notification: DesktopNotification): boolean;
  closeNotification(tag: string): void;
  clearNotifications(): void;
  onOpenNotification(callback: (route: string) => void): () => void;
  desktopPresence(): { active: boolean; notifies: boolean };
  onPresenceChange(callback: (presence: { active: boolean; notifies: boolean }) => void): () => void;
}

declare global {
  interface Window { readonly personaDesktop?: DesktopBridge }
}

export {};
