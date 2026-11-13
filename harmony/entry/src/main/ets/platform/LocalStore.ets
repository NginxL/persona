import { common } from '@kit.AbilityKit';
import { relationalStore } from '@kit.ArkData';
import { isHubStateKey } from '../core/StorageOwnership';

// Application-sandbox data only. Credentials are kept separately in Asset Store.
export class LocalStore {
  private static retired: Map<string, Set<string>> = new Map();
  private static writes: Map<string, Promise<void>> = new Map();
  private static epochs: Map<string, Map<string, number>> = new Map();
  private store: relationalStore.RdbStore | undefined;
  private readonly name: string;
  constructor(name: string = 'persona.db') {
    if (!/^[a-zA-Z0-9_-]+\.db$/.test(name) || name.length > 96) throw new Error('invalid_store_name');
    this.name = name;
  }
  private pendingWrites(): Promise<void> { return LocalStore.writes.get(this.name) || Promise.resolve(); }
  private queue(action: () => Promise<void>): Promise<void> {
    const write = this.pendingWrites().then(action); LocalStore.writes.set(this.name, write.catch(() => {})); return write;
  }
  private retiredKey(key: string): boolean {
    const hubs = LocalStore.retired.get(this.name); if (!hubs) return false;
    for (const hub of hubs) if (isHubStateKey(key, hub)) return true;
    return false;
  }
  allowHub(hub: string): void { LocalStore.retired.get(this.name)?.delete(hub); }
  private keyEpoch(key: string): number {
    const epochs = LocalStore.epochs.get(this.name); if (!epochs) return 0;
    for (const hub of epochs.keys()) if (isHubStateKey(key, hub)) return epochs.get(hub) || 0;
    return 0;
  }
  revision(key: string): number { return this.keyEpoch(key); }
  accepts(key: string, revision: number): boolean { return revision === this.keyEpoch(key) && !this.retiredKey(key); }
  async open(context: common.UIAbilityContext): Promise<void> {
    this.store = await relationalStore.getRdbStore(context, { name: this.name, securityLevel: relationalStore.SecurityLevel.S3 });
    await this.store.executeSql('CREATE TABLE IF NOT EXISTS client_state (key TEXT PRIMARY KEY, value TEXT NOT NULL)');
  }
  async get(key: string): Promise<string> {
    if (!this.store || this.retiredKey(key)) return '';
    const epoch = this.keyEpoch(key), result = await this.store.querySql('SELECT value FROM client_state WHERE key = ?', [key]);
    try { return epoch === this.keyEpoch(key) && !this.retiredKey(key) && result.goToFirstRow() ? result.getString(0) : ''; } finally { result.close(); }
  }
  async put(key: string, value: string, expectedEpoch: number = this.keyEpoch(key)): Promise<void> {
    if (!this.store) throw new Error('storage_unavailable');
    const store = this.store, epoch = expectedEpoch;
    await this.queue(async () => {
      if (epoch !== this.keyEpoch(key) || this.retiredKey(key)) throw new Error('scope_retired');
      await store.insert('client_state', { key: key, value: value }, relationalStore.ConflictResolution.ON_CONFLICT_REPLACE);
    });
  }
  async remove(key: string, expectedEpoch: number = this.keyEpoch(key)): Promise<void> {
    if (!this.store) return;
    const store = this.store, epoch = expectedEpoch;
    await this.queue(async () => { if (epoch !== this.keyEpoch(key)) return; await store.executeSql('DELETE FROM client_state WHERE key = ?', [key]); });
  }
  async keys(): Promise<string[]> {
    if (!this.store) return [];
    await this.pendingWrites();
    const result = await this.store.querySql('SELECT key FROM client_state');
    const keys: string[] = [];
    try { while (result.goToNextRow()) keys.push(result.getString(0)); } finally { result.close(); }
    return keys;
  }
  async purgeHub(hub: string): Promise<void> {
    if (!hub) return;
    let hubs = LocalStore.retired.get(this.name);
    if (!hubs) { hubs = new Set(); LocalStore.retired.set(this.name, hubs); }
    hubs.add(hub);
    let epochs = LocalStore.epochs.get(this.name);
    if (!epochs) { epochs = new Map(); LocalStore.epochs.set(this.name, epochs); }
    epochs.set(hub, (epochs.get(hub) || 0) + 1);
    if (!this.store) return;
    const store = this.store;
    await this.queue(async () => {
      const result = await store.querySql('SELECT key FROM client_state');
      const keys: string[] = [];
      try { while (result.goToNextRow()) { const key = result.getString(0); if (isHubStateKey(key, hub)) keys.push(key); } } finally { result.close(); }
      for (const key of keys) await store.executeSql('DELETE FROM client_state WHERE key = ?', [key]);
    });
  }
  async close(): Promise<void> { await this.pendingWrites(); if (this.store) { await this.store.close(); this.store = undefined; } }
}
