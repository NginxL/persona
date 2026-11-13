import type { ClientModel } from './ClientModel';
import { Wire } from './Wire';
import { extensionCatalogueEvent, extensionIdentity, extensionSlotsPath, hasExtensionSlots, extensionUIEvent } from './Compatibility';

@ObservedV2
export class ExtensionNavigationEntry {
  readonly key: string;
  readonly extensionId: string;
  readonly uiId: string;
  @Trace title: string = '';
  @Trace badge: string = '';
  @Trace icon: string = '';
  constructor(key: string, extensionId: string, uiId: string) { this.key = key; this.extensionId = extensionId; this.uiId = uiId; }
}

/** A directory is display data; only a fresh read for its captured owner authorizes opening it. */
@ObservedV2
export class ExtensionNavigation {
  @Trace entries: ExtensionNavigationEntry[] = [];
  @Trace ready: boolean = false;
  @Trace loading: boolean = false;
  @Trace error: string = '';
  @Trace visible: boolean = false;
  @Trace revision: number = 0;
  private client: ClientModel | undefined;
  private owner: string = '';
  private sequence: number = 0;
  private generation: number = -1;
  private eventsOff: (() => void) | undefined;
  private timer: number = -1;
  private pollTimer: number = -1;
  private featureGeneration: number = -1;
  private negotiatedFeatures: string[] = [];

  bind(client: ClientModel): void {
    const owner = JSON.stringify([client.profile.id, client.profile.manager, client.scope()]);
    if (this.client === client && this.owner === owner) return;
    this.dispose(); this.client = client; this.owner = owner; this.entries = []; this.error = ''; this.visible = false;
    if (client.connected && client.features.length) { this.featureGeneration = client.connectionGeneration(); this.negotiatedFeatures = client.features.slice(); }
    this.eventsOff = client.observeEvents((payload: string): void => {
      const data = Wire.parse(payload), kind = Wire.text(data, 'type'), manager = Wire.text(data, 'manager');
      // The built-in manager omits this field; other managers always carry it, independently of the chosen default.
      if (kind !== 'hello' && client.profile.manager && (manager || 'assistant') !== client.profile.manager) return;
      if (kind === 'hello' && Array.isArray(data['features'])) {
        this.featureGeneration = client.connectionGeneration(); this.negotiatedFeatures = Wire.strings(data, 'features');
      }
      const uiEvent = extensionUIEvent(kind), invalidated = uiEvent && Wire.bool(data, 'invalidate');
      const retired = kind === 'extension_ui_stream' && Wire.text(data, 'kind') === 'closed' && !Wire.text(data, 'request_id');
      if (uiEvent && !invalidated && Wire.has(data, 'badge')) {
        const entry = this.entries.find((item: ExtensionNavigationEntry): boolean => item.extensionId === extensionIdentity(data) && item.uiId === Wire.text(data, 'id'));
        if (entry && this.accepts(entry)) entry.badge = Wire.text(Wire.object(data, 'badge'), 'text').slice(0, 8);
        return;
      }
      if (kind === 'hello' || extensionCatalogueEvent(kind) || invalidated || retired) {
        // A UI content invalidation is not a permission change. Keep its mounted input until the read says otherwise.
        if (!invalidated) this.invalidate();
        if (this.timer !== -1) clearTimeout(this.timer);
        this.timer = setTimeout((): void => { this.timer = -1; this.refresh(!invalidated); }, 100);
      }
    });
  }
  private invalidate(): void { this.sequence++; this.ready = false; this.loading = false; }
  private stopPoll(): void { if (this.pollTimer !== -1) clearTimeout(this.pollTimer); this.pollTimer = -1; }
  private schedulePoll(): void {
    this.stopPoll();
    const client = this.client;
    if (!client?.connected || this.featureGeneration !== client.connectionGeneration() || !hasExtensionSlots(this.negotiatedFeatures)) return;
    // Older lifecycle routes have no catalogue event. Discovery continues even after a successful empty response.
    this.pollTimer = setTimeout((): void => { this.pollTimer = -1; this.refresh(false); }, 5000);
  }
  connectionChanged(): void {
    const client = this.client;
    if (!client) return;
    this.bind(client); this.invalidate(); this.stopPoll();
    if (!client.connected) { if (this.visible) this.error = 'offline'; return; }
    this.refresh();
  }
  accepts(entry: ExtensionNavigationEntry): boolean {
    const client = this.client;
    return !!client && client.connected && this.ready && hasExtensionSlots(client.features) &&
      client.owns(this.generation, client.scope()) && this.owner === JSON.stringify([client.profile.id, client.profile.manager, client.scope()]) &&
      this.entries.some((current: ExtensionNavigationEntry): boolean => current === entry);
  }
  allows(profile: string, manager: string, extensionId: string, uiId: string): boolean {
    const entry = this.entries.find((item: ExtensionNavigationEntry): boolean => item.extensionId === extensionId && item.uiId === uiId);
    return !!entry && this.client?.profile.id === profile && this.client?.profile.manager === manager && this.accepts(entry);
  }
  async refresh(revoke: boolean = true): Promise<void> {
    const client = this.client;
    if (!client) return;
    this.stopPoll();
    if (this.timer !== -1) { clearTimeout(this.timer); this.timer = -1; }
    const request = ++this.sequence, generation = client.connectionGeneration(), scope = client.scope(), owner = this.owner;
    const reauthorizing = revoke || !this.ready;
    if (revoke) this.ready = false;
    this.error = ''; this.loading = false;
    if (!client.connected) { if (this.visible) this.error = 'offline'; return; }
    if (this.featureGeneration !== generation && client.features.length) { this.featureGeneration = generation; this.negotiatedFeatures = client.features.slice(); }
    if (this.featureGeneration !== generation) return;
    if (!hasExtensionSlots(this.negotiatedFeatures)) { this.entries = []; this.visible = false; return; }
    this.loading = true;
    try {
      const result = await client.repo().call(extensionSlotsPath(this.negotiatedFeatures));
      if (request !== this.sequence || client !== this.client || owner !== this.owner || !client.owns(generation, scope)) return;
      if (!Array.isArray(result['ui'])) throw new Error('failed');
      const next: ExtensionNavigationEntry[] = [], seen: Set<string> = new Set<string>();
      const previous: Map<string, ExtensionNavigationEntry> = new Map<string, ExtensionNavigationEntry>();
      for (const entry of this.entries) previous.set(entry.key, entry);
      for (const data of Wire.rows(result, 'ui')) {
        const extensionId = extensionIdentity(data), uiId = Wire.text(data, 'id');
        if (Wire.text(data, 'placement') !== 'nav_page' || !extensionId || !uiId) continue;
        const key = JSON.stringify([owner, extensionId, uiId]); if (seen.has(key)) continue; seen.add(key);
        const entry = previous.get(key) || new ExtensionNavigationEntry(key, extensionId, uiId);
        entry.title = Wire.text(data, 'title') || uiId;
        entry.badge = Wire.text(Wire.object(data, 'badge'), 'text').slice(0, 8);
        entry.icon = Wire.text(data, 'icon'); next.push(entry);
      }
      const membershipChanged = previous.size !== next.length || next.some((entry: ExtensionNavigationEntry): boolean => !previous.has(entry.key));
      this.entries = next; this.generation = generation; this.visible = next.length > 0; this.ready = true;
      if (reauthorizing || membershipChanged) this.revision++;
    } catch (_) {
      if (request === this.sequence && client === this.client && owner === this.owner && client.owns(generation, scope)) { this.ready = false; this.error = 'failed'; this.visible = true; }
    } finally { if (request === this.sequence && client === this.client && owner === this.owner) { this.loading = false; this.schedulePoll(); } }
  }
  dispose(): void {
    this.invalidate(); this.eventsOff?.(); this.eventsOff = undefined;
    if (this.timer !== -1) clearTimeout(this.timer); this.timer = -1; this.client = undefined; this.owner = '';
    this.stopPoll(); this.featureGeneration = -1; this.negotiatedFeatures = [];
  }
}
