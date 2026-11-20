import { Pairing } from '../../../main/ets/platform/Pairing';
import { afterAll, beforeAll, describe, it, expect } from '@ohos/hypium';
import { abilityDelegatorRegistry } from '@kit.TestKit';
import { util } from '@kit.ArkTS';
import { relationalStore } from '@kit.ArkData';
import { connection, socket } from '@kit.NetworkKit';
import { startIPv6Relay } from './NativeIPv6Relay';
import { HubTransport, EndpointConfig, HttpFailure } from '../../../main/ets/platform/HubTransport';
import { HubRepository } from '../../../main/ets/models/HubRepository';
import { LocalStore } from '../../../main/ets/platform/LocalStore';
import { Wire } from '../../../main/ets/models/Wire';
import { nativeTestContext } from './NativeContext';
import { setClientLanguage } from '../../../main/ets/core/ClientLocale';

export function fixtureArgument(key: string): string {
  const parameters = abilityDelegatorRegistry.getArguments().parameters;
  return parameters[key] || parameters['-s ' + key] || '';
}
function fixture(pin: string = fixtureArgument('fixturePin')): HubTransport {
  return new HubTransport(new EndpointConfig(fixtureArgument('fixtureAddress'), '00000000-0000-4000-8000-000000000001', 'synthetic-fixture-token', pin, 'synthetic'));
}
async function pause(): Promise<void> { await new Promise<void>((resolve) => setTimeout(resolve, 20)); }
async function count(name: string, pin: string = fixtureArgument('fixturePin')): Promise<number> {
  const transport = fixture(pin);
  try { return Wire.number(Wire.parse(await transport.request('/api/v1/test/stats')), name); } finally { transport.close(); }
}

const ipv6FixtureHost = 'hub-v6.test';

function ipv6Fixture(host: string, pin: string = fixtureArgument('fixturePin')): HubTransport {
  const port = Number(fixtureArgument('ipv6FixturePort'));
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('invalid_fixture_port');
  return new HubTransport(new EndpointConfig('https://' + host + ':' + port + '/',
    '00000000-0000-4000-8000-000000000001', 'synthetic-fixture-token', pin, 'synthetic'));
}

async function receiveHello(transport: HubTransport): Promise<Record<string, Object>> {
  let hello: Record<string, Object> = {};
  await transport.connect((payload: string): void => {
    const message = Wire.parse(payload);
    if (Wire.text(message, 'type') === 'hello') hello = message;
  });
  for (let attempt = 0; attempt < 100 && !Wire.has(hello, 'type'); attempt++) await pause();
  expect(Wire.text(hello, 'type')).assertEqual('hello');
  expect(transport.hello(['elicitation'])).assertTrue();
  return Wire.object(hello, 'fixture_identity');
}

export function nativeIPv6TransportTests(): void {
  describe('NativeIPv6Transport', (): void => {
    let relay: socket.TCPSocketServer | undefined;
    beforeAll(async (): Promise<void> => {
      if (fixtureArgument('ipv6ForwardPort')) relay = await startIPv6Relay(Number(fixtureArgument('ipv6FixturePort')),
        Number(fixtureArgument('ipv6ForwardPort')));
    });
    afterAll(async (): Promise<void> => { if (relay) await relay.close(); });
    it('receives_a_hub_hello_over_the_ipv6_loopback_literal', 0, async (): Promise<void> => {
      const literal = ipv6Fixture('[::1]');
      try { await receiveHello(literal); } finally { literal.close(); }
    });
    it('resolves_only_ipv6_for_the_fixture_domain', 0, async (): Promise<void> => {
      await connection.addCustomDnsRule(ipv6FixtureHost, ['::1']);
      try {
        const results = await connection.getAddressesByName(ipv6FixtureHost);
        expect(results.length > 0).assertTrue();
        expect(results.every((address: connection.NetAddress): boolean => address.family === 2)).assertTrue();
      } finally { await connection.removeCustomDnsRule(ipv6FixtureHost); }
    });
    it('connects_the_hub_and_child_stream_with_the_aaaa_only_domain_and_pin', 0, async (): Promise<void> => {
      await connection.addCustomDnsRule(ipv6FixtureHost, ['::1']);
      const main = ipv6Fixture(ipv6FixtureHost), child = main.stream('/api/v1/ws?stream=ipv6');
      const wrongPin = ipv6Fixture(ipv6FixtureHost, 'A'.repeat(43));
      try {
        const primary = await receiveHello(main), stream = await receiveHello(child);
        for (const peer of [primary, stream]) {
          expect(Wire.text(peer, 'host')).assertEqual(ipv6FixtureHost + ':' + fixtureArgument('ipv6FixturePort'));
          expect(Wire.text(peer, 'sni')).assertEqual(ipv6FixtureHost);
        }
        expect(Wire.text(primary, 'path')).assertEqual('/api/v1/ws');
        expect(Wire.text(stream, 'path')).assertEqual('/api/v1/ws?stream=ipv6');
        let rejected = false;
        try { await wrongPin.connect((): void => {}); } catch (_) { rejected = true; }
        expect(rejected).assertTrue();
      } finally {
        wrongPin.close(); child.close(); main.close();
        await connection.removeCustomDnsRule(ipv6FixtureHost);
      }
    });
  });
}

export default function nativeTransportTests(): void {
  describe('NativeTransport', (): void => {
    it('committed_app_language_updates_http_and_existing_websocket_locale', 0, async (): Promise<void> => {
      const transport = fixture();
      try {
        await transport.connect((): void => {});
        for (const language of ['en-US', 'ja-JP', 'zh-CN']) {
          const expected = language === 'zh-CN' ? 'zh-Hans' : language === 'ja-JP' ? 'ja' : 'en';
          setClientLanguage(language); expect(transport.hello(['elicitation'])).assertTrue();
          let response: Record<string, Object> = {};
          for (let attempt = 0; attempt < 100; attempt++) {
            response = Wire.parse(await transport.request('/api/v1/test/client'));
            if (Wire.text(Wire.object(response, 'hello'), 'locale') === expected) break;
            await pause();
          }
          expect(Wire.text(response, 'locale')).assertEqual(expected);
          expect(Wire.text(Wire.object(response, 'hello'), 'locale')).assertEqual(expected);
        }
      } finally { setClientLanguage('default'); transport.close(); }
    });
    it('http_client_identity_matches_the_websocket_capability_registration', 0, async (): Promise<void> => {
      const transport = fixture();
      try {
        await transport.connect(() => {}, () => {}); expect(transport.hello(['elicitation'])).assertTrue();
        let response: Record<string, Object> = Wire.payload([], []);
        for (let attempt = 0; attempt < 100; attempt++) {
          response = Wire.parse(await transport.request('/api/v1/test/client'));
          if (Wire.text(Wire.object(response, 'hello'), 'platform')) break; await pause();
        }
        const header = Wire.text(response, 'client'), hello = Wire.object(response, 'hello');
        expect(header).assertEqual('harmony/' + Wire.text(hello, 'version'));
        expect(Wire.text(response, 'legacy')).assertEqual(header); expect(Wire.text(response, 'source')).assertEqual('app');
        expect(Wire.strings(hello, 'features').includes('elicitation')).assertTrue();
      } finally { transport.close(); }
    });

    it('pairing_verifies_candidates_anonymously_before_the_confirmed_exchange', 0, async (): Promise<void> => {
      const address = fixtureArgument('fixtureAddress'), pin = fixtureArgument('fixturePin');
      const query = 'persona://pair?url=' + encodeURIComponent(address) + '&token=synthetic-fixture-token&hub=00000000-0000-4000-8000-000000000001&fp=' + encodeURIComponent(pin) + '&alt=' + encodeURIComponent('https://127.0.0.1:38664/');
      const pairing = Pairing.parse(query), target = await pairing.inspect(), endpoint = await pairing.redeem(target, 'Synthetic native test');
      expect(endpoint.hubId).assertEqual('00000000-0000-4000-8000-000000000001');
      expect(endpoint.token).assertEqual('synthetic-fixture-token'); expect(endpoint.alternateUrls.length).assertEqual(1);
      expect(await count('anonymous_health') >= 2).assertTrue(); expect(await count('authenticated_health')).assertEqual(0);
      let rejected = false; try { await pairing.redeem(target); } catch (_) { rejected = true; } expect(rejected).assertTrue();
    });
    it('a_pinned_reconnect_uses_a_verified_advertised_address_without_bearer_probes', 0, async (): Promise<void> => {
      const address = fixtureArgument('fixtureAddress'), pin = fixtureArgument('fixturePin');
      expect(await Pairing.reachable('https://127.0.0.1:1/', [address], '00000000-0000-4000-8000-000000000001', pin)).assertEqual(address.endsWith('/') ? address : address + '/');
      let rejected = false;
      try { await Pairing.reachable(address, [address], 'a-different-hub', pin); } catch (_) { rejected = true; }
      expect(rejected).assertTrue(); expect(await count('authenticated_health')).assertEqual(0);
    });

    it('wrong_pins_send_neither_http_authorization_nor_websocket_upgrade', 0, async (): Promise<void> => {
      const before = await count('forbidden'), upgrades = await count('ws');
      const bad = fixture('A'.repeat(43)); let rejected = false;
      try { await bad.request('/api/v1/test/forbidden'); } catch (_) { rejected = true; } finally { bad.close(); }
      expect(rejected).assertTrue();
      const wrongSocket = fixture('A'.repeat(43)); rejected = false;
      try { await wrongSocket.connect(() => {}, () => {}); } catch (_) { rejected = true; } finally { wrongSocket.close(); }
      expect(rejected).assertTrue(); expect(await count('forbidden')).assertEqual(before); expect(await count('ws')).assertEqual(upgrades);
    });
    it('http_accepted_operations_remain_nonreleasable_in_the_real_transport_and_database', 0, async (): Promise<void> => {
      const context = nativeTestContext(), name = 'native-pending-' + util.generateRandomUUID() + '.db', local = new LocalStore(name);
      const config = new EndpointConfig(fixtureArgument('fixtureAddress'), '00000000-0000-4000-8000-000000000001', 'synthetic-fixture-token', fixtureArgument('fixturePin'), 'synthetic');
      const repo = new HubRepository(config, local);
      try {
        await local.open(context); let pending = false;
        try { await repo.call('/api/v1/test/pending', 'POST'); } catch (error) { pending = error instanceof HttpFailure && error.resultPending; }
        expect(pending).assertTrue(); const record = repo.mutations?.blocked()[0]; expect(!!record).assertTrue();
        if (record) { expect(record.state).assertEqual('pending'); await repo.mutations?.acknowledge(record.id); }
        expect(repo.mutations?.blocked().length).assertEqual(1);
      } finally { repo.close(); await local.close(); await relationalStore.deleteRdbStore(context, name); }
    });
    it('transfer_acceptance_returns_its_task_id_without_releasing_other_pending_writes', 0, async (): Promise<void> => {
      const transport = fixture();
      try {
        const accepted = Wire.parse(await transport.requestAccepted('/api/v1/files/transfer', 'POST', '{}'));
        expect(Wire.text(accepted, 'id')).assertEqual('synthetic-transfer');
        expect(Wire.text(accepted, 'state')).assertEqual('running');
        let blocked = false;
        try { await transport.request('/api/v1/files/transfer', 'POST', '{}'); } catch (error) { blocked = error instanceof HttpFailure && error.resultPending; }
        expect(blocked).assertTrue();
        let restricted = false;
        try { await transport.requestAccepted('/api/v1/test/pending', 'POST', '{}'); } catch (_) { restricted = true; }
        expect(restricted).assertTrue();
      } finally { transport.close(); }
    });
    it('an_invalid_utf8_transfer_receipt_remains_an_unknown_dispatched_result', 0, async (): Promise<void> => {
      const transport = fixture();
      try {
        let uncertain = false;
        try { await transport.requestAccepted('/api/v1/files/transfer?invalid_utf8=1', 'POST', '{}'); }
        catch (error) { uncertain = error instanceof HttpFailure && error.resultUnknown; }
        expect(uncertain).assertTrue();
      } finally { transport.close(); }
    });
    it('invalid_utf8_put_and_delete_responses_keep_dispatched_writes_unknown', 0, async (): Promise<void> => {
      const transport = fixture();
      try {
        const methods: string[] = ['PUT', 'DELETE'];
        for (const method of methods) {
          let uncertain = false;
          try { await transport.request('/api/v1/test/invalid-utf8', method, '{}'); }
          catch (error) { uncertain = error instanceof HttpFailure && error.resultUnknown; }
          expect(uncertain).assertTrue();
        }
      } finally { transport.close(); }
    });
    it('fragmented_hello_is_reassembled_and_close_discards_queued_application_frames', 0, async (): Promise<void> => {
      const transport = fixture(), before = await count('data'); let hello = false;
      try {
        await transport.connect((payload: string) => { if (Wire.text(Wire.parse(payload), 'type') === 'hello') hello = true; }, () => {});
        for (let attempt = 0; attempt < 100 && !hello; attempt++) await pause();
        expect(hello).assertTrue(); expect(transport.send('{"must_not_send_after_close":true}')).assertTrue(); transport.close();
        for (let attempt = 0; attempt < 30; attempt++) await pause();
        expect(await count('data')).assertEqual(before); expect(transport.send('{}')).assertFalse();
      } finally { transport.close(); }
    });
    it('oversized_http_bodies_and_websocket_headers_close_without_unbounded_reads', 0, async (): Promise<void> => {
      const transport = fixture(); let rejected = false;
      try { await transport.request('/api/v1/test/oversized'); } catch (_) { rejected = true; } finally { transport.close(); }
      expect(rejected).assertTrue();
      const owner = fixture(), stream = owner.stream('/api/v1/test/large-ws'); let ended = false;
      try {
        await stream.connect(() => {}, (state: string) => { if (state === 'offline' || state === 'closed') ended = true; }).catch(() => { ended = true; });
        for (let attempt = 0; attempt < 100 && !ended; attempt++) await pause();
        expect(ended).assertTrue(); expect(stream.send('{}')).assertFalse();
      } finally { owner.close(); }
    });
    it('an_overfull_send_queue_closes_and_rejects_further_work', 0, async (): Promise<void> => {
      const transport = fixture(); let ended = false;
      try {
        await transport.connect(() => {}, (state: string) => { if (state === 'offline' || state === 'closed') ended = true; });
        const bytes = new ArrayBuffer(1024 * 1024); let refused = false;
        for (let index = 0; index < 10; index++) { if (!transport.sendBinary(bytes)) refused = true; }
        expect(refused).assertTrue(); for (let attempt = 0; attempt < 100 && !ended; attempt++) await pause();
        expect(ended).assertTrue(); expect(transport.send('{}')).assertFalse();
      } finally { transport.close(); }
    });
    it('a_rotated_certificate_cannot_receive_authorization_under_the_previous_pin', 0, async (): Promise<void> => {
      const transport = fixture(), before = await count('forbidden');
      try { await transport.request('/api/v1/test/rotate', 'POST', '{}'); } finally { transport.close(); }
      const stale = fixture(); let rejected = false;
      try { await stale.request('/api/v1/test/forbidden'); } catch (_) { rejected = true; } finally { stale.close(); }
      expect(rejected).assertTrue(); expect(await count('forbidden', fixtureArgument('fixtureRotatedPin'))).assertEqual(before);
    });
  });
}
