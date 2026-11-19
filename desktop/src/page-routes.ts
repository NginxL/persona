/** The routes for one hub window's page (`desktop:notification-open`: a persona:// link, a native notification or an
 *  island row). The page hears them only once its app has started and called the bridge's `onOpenNotification`, which
 *  can be well after the window's first paint. A route that comes earlier (the link that started the desktop app, a
 *  click while the page reloads) waits for it. Only the latest one waits: a later click replaces an earlier one (#10282).
 *  The preload says when a new document starts and when its app listens (preload.cts, ipc.ts). */
export class PageRoutes {
  private listening = false;
  private waiting: string | undefined;

  constructor(private readonly deliver: (route: string) => void) {}

  /** A route for the page: handed over now when its app listens, else kept until it does. */
  open(route: string): void {
    if (this.listening) this.deliver(route);
    else this.waiting = route;
  }

  /** A new document started in the window: its app does not listen yet. A route that waits stays for it. */
  started(): void {
    this.listening = false;
  }

  /** The page's app listens: the route that waited goes to it now. */
  listen(): void {
    this.listening = true;
    const route = this.waiting;
    this.waiting = undefined;
    if (route !== undefined) this.deliver(route);
  }
}
