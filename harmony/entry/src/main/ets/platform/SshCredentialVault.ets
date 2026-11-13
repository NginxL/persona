import { asset } from '@kit.AssetStoreKit';
import { util } from '@kit.ArkTS';
import { hilog } from '@kit.PerformanceAnalysisKit';

const LABEL: string = 'persona.harmony.ssh.v1';
const MAX_KEY: number = 32768;
const MAX_PASSPHRASE: number = 4096;
const ASSET_BYTES: number = 1024;

function bytes(value: string): Uint8Array { return value ? new util.TextEncoder().encodeInto(value) : new Uint8Array(0); }
function validProfile(profile: string): void {
  if (!profile || profile.length > 128 || /[\u0000-\u001f\u007f]/.test(profile)) throw new Error('ssh_invalid_config');
}
function errorCode(error: Error): number {
  const value = (error as Object as Record<string, Object>)['code'];
  return typeof value === 'number' ? value as number : 0;
}
function storageFailure(error: Error, operation: string): Error {
  hilog.error(0x0000, 'PersonaSSH', 'asset_%{public}s:%{public}d', operation, errorCode(error));
  return new Error('ssh_storage_failed');
}
function queryFor(profile: string, reference: string = '', kind: string = ''): asset.AssetMap {
  validProfile(profile);
  if (kind && !reference) throw new Error('ssh_key_required');
  const query: asset.AssetMap = new Map();
  query.set(asset.Tag.DATA_LABEL_CRITICAL_1, bytes(LABEL));
  query.set(asset.Tag.DATA_LABEL_CRITICAL_2, bytes(profile));
  if (reference) {
    if (!/^[a-fA-F0-9-]{36}$/.test(reference)) throw new Error('ssh_invalid_config');
    query.set(asset.Tag.DATA_LABEL_CRITICAL_3, bytes(reference));
    if (kind) query.set(asset.Tag.ALIAS, bytes(LABEL + ':' + kind + ':' + reference));
  }
  return query;
}

export class SshSecrets {
  privateKey: Uint8Array = new Uint8Array(0);
  passphrase: Uint8Array = new Uint8Array(0);
  clear(): void { this.privateKey.fill(0); this.passphrase.fill(0); }
}

/** Separate from device tokens: multiline private keys never pass through token validation. */
export class SshCredentialVault {
  private static epochs: Map<string, number> = new Map();
  private static retired: Set<string> = new Set();
  private static writes: Map<string, Promise<void>> = new Map();

  allow(profile: string): void { validProfile(profile); SshCredentialVault.retired.delete(profile); }
  private epoch(profile: string): number { return SshCredentialVault.epochs.get(profile) || 0; }
  private current(profile: string, epoch: number, admit: () => boolean): boolean {
    return epoch === this.epoch(profile) && !SshCredentialVault.retired.has(profile) && admit();
  }
  private queue(profile: string, action: () => Promise<void>): Promise<void> {
    const task = (SshCredentialVault.writes.get(profile) || Promise.resolve()).then(action);
    SshCredentialVault.writes.set(profile, task.catch(() => {})); return task;
  }
  private async removeQuery(query: asset.AssetMap): Promise<void> {
    // API 26 accepts critical labels for query, but remove rejects them. Read only
    // bounded attributes and delete by each exact alias; never retrieve secret bytes.
    const attributes: asset.AssetMap = new Map(query);
    attributes.set(asset.Tag.RETURN_TYPE, asset.ReturnType.ATTRIBUTES);
    attributes.set(asset.Tag.RETURN_LIMIT, 64);
    while (true) {
      let rows: Array<asset.AssetMap>;
      try { rows = await asset.query(attributes); }
      catch (error) { if (errorCode(error as Error) === 24000002) return; throw storageFailure(error as Error, 'query'); }
      if (rows.length === 0) return;
      for (const row of rows) {
        const alias = row.get(asset.Tag.ALIAS);
        if (!(alias instanceof Uint8Array) || alias.length === 0 || alias.length > 256) throw new Error('ssh_storage_failed');
        const exact: asset.AssetMap = new Map(); exact.set(asset.Tag.ALIAS, alias);
        try { await asset.remove(exact); }
        catch (error) { if (errorCode(error as Error) !== 24000002) throw storageFailure(error as Error, 'remove'); }
      }
    }
  }
  private async add(profile: string, reference: string, kind: string, secret: Uint8Array): Promise<void> {
    const entry = queryFor(profile, reference, kind);
    entry.set(asset.Tag.SECRET, secret);
    entry.set(asset.Tag.ACCESSIBILITY, asset.Accessibility.DEVICE_UNLOCKED);
    entry.set(asset.Tag.AUTH_TYPE, asset.AuthType.NONE);
    entry.set(asset.Tag.SYNC_TYPE, asset.SyncType.NEVER);
    try { await asset.add(entry); } catch (error) { throw storageFailure(error as Error, 'add'); }
  }
  private async writeChunks(profile: string, reference: string, kind: string, secret: Uint8Array, current: () => boolean): Promise<void> {
    for (let offset: number = 0; offset < secret.length; offset += ASSET_BYTES) {
      if (!current()) throw new Error('ssh_cancelled');
      const chunk = secret.slice(offset, Math.min(offset + ASSET_BYTES, secret.length));
      try { await this.add(profile, reference, kind + '.' + String(offset / ASSET_BYTES), chunk); }
      finally { chunk.fill(0); }
    }
  }
  async put(profile: string, privateKey: string, passphrase: string, admit: () => boolean): Promise<string> {
    validProfile(profile);
    const key = bytes(privateKey), phrase = bytes(passphrase), epoch = this.epoch(profile);
    const reference: string = util.generateRandomUUID();
    if (key.length === 0 || key.length > MAX_KEY || phrase.length > MAX_PASSPHRASE || privateKey.includes('\u0000') || passphrase.includes('\u0000')) {
      key.fill(0); phrase.fill(0); throw new Error('ssh_invalid_key');
    }
    try {
      await this.queue(profile, async (): Promise<void> => {
        if (!this.current(profile, epoch, admit)) throw new Error('ssh_cancelled');
        const current = (): boolean => this.current(profile, epoch, admit);
        try {
          await this.writeChunks(profile, reference, 'key', key, current);
          await this.writeChunks(profile, reference, 'passphrase', phrase, current);
          if (!current()) throw new Error('ssh_cancelled');
          const manifest = new Uint8Array([0, 0, key.length >>> 8, key.length & 255, 0, 0, phrase.length >>> 8, phrase.length & 255]);
          try { await this.add(profile, reference, 'manifest', manifest); } finally { manifest.fill(0); }
          if (!current()) throw new Error('ssh_cancelled');
        } catch (error) {
          await this.removeQuery(queryFor(profile, reference));
          throw error as Error;
        }
      });
      return reference;
    } finally { key.fill(0); phrase.fill(0); }
  }
  private async read(profile: string, reference: string, kind: string, maximum: number): Promise<Uint8Array> {
    const query = queryFor(profile, reference, kind); query.set(asset.Tag.RETURN_TYPE, asset.ReturnType.ALL);
    let rows: Array<asset.AssetMap>;
    try { rows = await asset.query(query); }
    catch (error) { if (errorCode(error as Error) === 24000002) return new Uint8Array(0); throw storageFailure(error as Error, 'query'); }
    if (rows.length === 0) return new Uint8Array(0);
    const secret = rows[0].get(asset.Tag.SECRET);
    if (!(secret instanceof Uint8Array)) throw new Error('ssh_storage_failed');
    try {
      if (secret.length > maximum) throw new Error('ssh_invalid_key');
      return new Uint8Array(secret);
    } finally { secret.fill(0); }
  }
  private async readChunks(profile: string, reference: string, kind: string, length: number, current: () => boolean): Promise<Uint8Array> {
    const result = new Uint8Array(length);
    try {
      for (let offset: number = 0; offset < length; offset += ASSET_BYTES) {
        if (!current()) throw new Error('ssh_cancelled');
        const chunk = await this.read(profile, reference, kind + '.' + String(offset / ASSET_BYTES), ASSET_BYTES);
        try {
          if (!current()) throw new Error('ssh_cancelled');
          if (chunk.length !== Math.min(ASSET_BYTES, length - offset)) throw new Error('ssh_invalid_key');
          result.set(chunk, offset);
        } finally { chunk.fill(0); }
      }
      return result;
    } catch (error) { result.fill(0); throw error as Error; }
  }
  async get(profile: string, reference: string, admit: () => boolean): Promise<SshSecrets> {
    validProfile(profile); const epoch = this.epoch(profile), result = new SshSecrets();
    await (SshCredentialVault.writes.get(profile) || Promise.resolve());
    if (!this.current(profile, epoch, admit)) throw new Error('ssh_cancelled');
    try {
      const manifest = await this.read(profile, reference, 'manifest', 8);
      if (!this.current(profile, epoch, admit)) { manifest.fill(0); throw new Error('ssh_cancelled'); }
      if (manifest.length !== 8) throw new Error('ssh_key_required');
      const keyLength = manifest[0] * 16777216 + manifest[1] * 65536 + manifest[2] * 256 + manifest[3];
      const phraseLength = manifest[4] * 16777216 + manifest[5] * 65536 + manifest[6] * 256 + manifest[7]; manifest.fill(0);
      if (keyLength === 0 || keyLength > MAX_KEY || phraseLength > MAX_PASSPHRASE) throw new Error('ssh_invalid_key');
      const current = (): boolean => this.current(profile, epoch, admit);
      result.privateKey = await this.readChunks(profile, reference, 'key', keyLength, current);
      result.passphrase = await this.readChunks(profile, reference, 'passphrase', phraseLength, current);
      if (!this.current(profile, epoch, admit)) throw new Error('ssh_cancelled');
      if (result.privateKey.length === 0) throw new Error('ssh_key_required');
      return result;
    } catch (error) { result.clear(); throw error as Error; }
  }
  async remove(profile: string, reference: string): Promise<void> {
    if (!reference) throw new Error('ssh_key_required');
    await this.queue(profile, async (): Promise<void> => {
      await this.removeQuery(queryFor(profile, reference));
    });
  }
  async forget(profile: string): Promise<void> {
    validProfile(profile);
    SshCredentialVault.epochs.set(profile, this.epoch(profile) + 1);
    SshCredentialVault.retired.add(profile);
    await this.queue(profile, async (): Promise<void> => { await this.removeQuery(queryFor(profile)); });
  }
}
