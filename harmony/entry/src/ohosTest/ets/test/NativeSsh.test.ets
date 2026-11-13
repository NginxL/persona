import { describe, it, expect } from '@ohos/hypium';
import { fileIo } from '@kit.CoreFileKit';
import { util, url } from '@kit.ArkTS';
import nativeSsh from 'libpersona_ssh.so';
import type { NativeSshConfig, NativeSshLease } from 'libpersona_ssh.so';
import { SshCredentialVault } from '../../../main/ets/platform/SshCredentialVault';
import { HubTransport, EndpointConfig, TransportProxy } from '../../../main/ets/platform/HubTransport';
import { Wire } from '../../../main/ets/models/Wire';
import { fixtureArgument } from './NativeTransport.test';
import { nativeTestContext } from './NativeContext';
import { connection } from '@kit.NetworkKit';
import { asset } from '@kit.AssetStoreKit';
import { BusinessError } from '@kit.BasicServicesKit';

async function expectErased(profile: string): Promise<void> {
  const query: asset.AssetMap = new Map();
  query.set(asset.Tag.DATA_LABEL_CRITICAL_1, new util.TextEncoder().encodeInto('persona.harmony.ssh.v1'));
  query.set(asset.Tag.DATA_LABEL_CRITICAL_2, new util.TextEncoder().encodeInto(profile));
  query.set(asset.Tag.RETURN_TYPE, asset.ReturnType.ATTRIBUTES);
  let count = 0;
  try { count = (await asset.query(query)).length; }
  catch (error) { if ((error as BusinessError).code !== 24000002) throw error as Error; }
  expect(count).assertEqual(0);
}

async function configuration(wrongPin: boolean = false): Promise<NativeSshConfig> {
  const path = fixtureArgument('sshFixtureFile');
  if (!/^persona-ssh-[a-z0-9-]+\.json$/.test(path)) throw new Error('invalid_fixture');
  const fixture = Wire.parse(fileIo.readTextSync(nativeTestContext().filesDir + '/' + path));
  const targetHost = Wire.text(fixture, 'targetHost');
  const literal = targetHost.includes(':') || /^\d{1,3}(\.\d{1,3}){3}$/.test(targetHost);
  const resolvedHubIps: string[] = literal ? [] : (await connection.getAddressesByName(targetHost)).slice(0, 16).map((value: connection.NetAddress): string => value.address);
  return { id: util.generateRandomUUID(), sshHost: '127.0.0.1', sshPort: Wire.number(fixture, 'sshPort'),
    username: Wire.text(fixture, 'username'), hostKey: wrongPin ? 'SHA256:' + 'A'.repeat(43) : Wire.text(fixture, 'hostKey'),
    targetHost: targetHost, targetPort: Wire.number(fixture, 'targetPort'), resolvedHubIps: resolvedHubIps,
    privateKey: new util.TextEncoder().encodeInto(Wire.text(fixture, 'privateKey')), passphrase: new Uint8Array(0) };
}
class FixtureProxy implements TransportProxy {
  readonly host: string; readonly port: number; readonly username: string; readonly password: string;
  private readonly id: string;
  constructor(lease: NativeSshLease) { this.host = lease.host; this.port = lease.port; this.username = lease.username; this.password = lease.password; this.id = lease.id; }
  isCurrent(): boolean { return nativeSsh.isOpen(this.id); }
}
function transport(proxy: TransportProxy, badPin: boolean = false): HubTransport {
  return new HubTransport(new EndpointConfig(fixtureArgument('fixtureAddress'), '00000000-0000-4000-8000-000000000001',
    'synthetic-fixture-token', badPin ? 'A'.repeat(43) : fixtureArgument('fixturePin'), 'synthetic', [], proxy));
}
export default function nativeSshTests(): void {
  describe('NativeSsh', (): void => {
    it('device_only_asset_chunks_round_trip_and_forget_without_cross_profile_access', 0, async (): Promise<void> => {
      const vault = new SshCredentialVault(), profile = 'ssh-fixture-' + util.generateRandomUUID(), other = profile + '-other';
      const key = 'synthetic-key\n'.repeat(240), password = 'synthetic-passphrase'.repeat(110);
      vault.allow(profile); vault.allow(other);
      try {
        const reference = await vault.put(profile, key, password, (): boolean => true);
        const unrelated = await vault.put(other, key, '', (): boolean => true);
        const value = await vault.get(profile, reference, (): boolean => true);
        expect(value.privateKey.length).assertEqual(new util.TextEncoder().encodeInto(key).length);
        expect(value.passphrase.length).assertEqual(new util.TextEncoder().encodeInto(password).length);
        expect(util.TextDecoder.create('utf-8').decodeToString(value.privateKey)).assertEqual(key);
        expect(util.TextDecoder.create('utf-8').decodeToString(value.passphrase)).assertEqual(password); value.clear();
        await vault.forget(profile);
        await expectErased(profile);
        let denied = false; try { await vault.get(profile, reference, (): boolean => true); } catch (_) { denied = true; } expect(denied).assertTrue();
        const retained = await vault.get(other, unrelated, (): boolean => true); expect(retained.privateKey.length > 1024).assertTrue(); retained.clear();
        await vault.forget(other);
        await expectErased(other);
      } finally { await vault.forget(profile).catch((): void => {}); await vault.forget(other).catch((): void => {}); }
    });
    it('native_ssh_rejects_an_untrusted_host_key', 0, async (): Promise<void> => {
      const config = await configuration(true); let rejected = false;
      try { await nativeSsh.open(config); } catch (_) { rejected = true; }
      finally { config.privateKey.fill(0); await nativeSsh.close(config.id); }
      expect(rejected).assertTrue(); expect(nativeSsh.isOpen(config.id)).assertFalse();
    });
    it('native_authenticated_proxies_preserve_https_pinning_websocket_and_child_streams', 0, async (): Promise<void> => {
      const config = await configuration();
      let main: HubTransport | undefined, child: HubTransport | undefined, bad: HubTransport | undefined;
      try {
        const pending = nativeSsh.open(config); config.privateKey.fill(0); const lease = await pending;
        expect(lease.host).assertEqual('127.0.0.1'); const proxy = new FixtureProxy(lease); main = transport(proxy);
        const before = Wire.parse(await main.request('/api/v1/test/stats'));
        bad = transport(proxy, true); let rejected = false;
        try { await bad.request('/api/v1/test/forbidden'); } catch (_) { rejected = true; } expect(rejected).assertTrue();
        rejected = false; try { await bad.connect((): void => {}); } catch (_) { rejected = true; } expect(rejected).assertTrue(); bad.close(); bad = undefined;
        const after = Wire.parse(await main.request('/api/v1/test/stats'));
        expect(Wire.number(after, 'forbidden')).assertEqual(Wire.number(before, 'forbidden'));
        expect(Wire.number(after, 'ws')).assertEqual(Wire.number(before, 'ws'));
        await main.connect((): void => {}); expect(main.hello(['elicitation'])).assertTrue();
        child = main.stream('/api/v1/ws'); await child.connect((): void => {});
        const stats = Wire.parse(await main.request('/api/v1/test/stats')); expect(Wire.number(stats, 'ws') >= Wire.number(before, 'ws') + 2).assertTrue();
        const identity = Wire.parse(await main.request('/api/v1/test/identity')), logical = url.URL.parseURL(fixtureArgument('fixtureAddress'));
        const prefix = logical.pathname.replace(/\/+$/, ''), http = Wire.object(identity, 'http');
        expect(Wire.text(http, 'host')).assertEqual(logical.host); expect(Wire.text(http, 'path')).assertEqual(prefix + '/api/v1/test/identity');
        const hostname = logical.hostname.replace(/^\[|\]$/g, '');
        const domain = !hostname.includes(':') && !/^\d{1,3}(\.\d{1,3}){3}$/.test(hostname);
        if (domain) expect(Wire.text(http, 'sni')).assertEqual(hostname);
        const identities = Wire.rows(identity, 'websocket').slice(-2); expect(identities.length).assertEqual(2);
        for (const entry of identities) {
          expect(Wire.text(entry, 'host')).assertEqual(logical.host); expect(Wire.text(entry, 'path')).assertEqual(prefix + '/api/v1/ws');
          if (domain) expect(Wire.text(entry, 'sni')).assertEqual(hostname);
        }
        await nativeSsh.close(config.id); expect(proxy.isCurrent()).assertFalse();
        rejected = false; try { await main.request('/api/v1/test/forbidden'); } catch (_) { rejected = true; } expect(rejected).assertTrue();
        expect(child.send('{"type":"no_dispatch_after_close"}')).assertFalse();
      } finally { config.privateKey.fill(0); child?.close(); bad?.close(); main?.close(); await nativeSsh.close(config.id); }
    });
  });
}
