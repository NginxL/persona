/** A control grant is valid only while its captured app owner/machine/connection remains current. */
export class RFBOwnerLease {
  private current: () => boolean;
  private enabled: boolean = false;
  private revoked: boolean = false;

  constructor(current: () => boolean) { this.current = current; }

  isCurrent(): boolean { return !this.revoked && this.current(); }

  grant(): boolean {
    if (!this.isCurrent()) return false;
    this.enabled = true;
    return true;
  }

  allowsInput(): boolean { return this.enabled && this.isCurrent(); }

  disable(): void { this.enabled = false; }

  revoke(): void { this.enabled = false; this.revoked = true; }
}
