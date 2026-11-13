import { isPairingScheme } from '../core/WireCompatibility';
import { url } from '@kit.ArkTS';
import { EndpointConfig, HubTransport, normalizeBaseUrl } from './HubTransport';

export class PairingTarget {
  readonly baseUrl: string;
  readonly hubId: string;
  readonly name: string;
  readonly pin: string;
  constructor(baseUrl: string, hubId: string, name: string, pin: string) {
    this.baseUrl = baseUrl; this.hubId = hubId; this.name = name; this.pin = pin;
  }
}

function safeString(data: Record<string, Object>, key: string): string {
  const value = data[key];
  return typeof value === 'string' ? value as string : '';
}

function only(params: url.URLParams, name: string, required: boolean = false): string {
  const values = params.getAll(name);
  if (values.length > 1 || (required && values.length !== 1)) throw new Error('invalid_pairing');
  return values.length === 1 ? values[0] : '';
}

export class Pairing {
  private readonly baseUrl: string;
  private selectedUrl: string;
  private readonly addresses: string[];
  private readonly pairingCode: string;
  private readonly expectedHubId: string;
  private readonly pin: string;
  private inspectedKey: string = '';
  private used: boolean = false;
  private redeeming: boolean = false;

  private constructor(baseUrl: string, code: string, hubId: string, pin: string, alternates: string[] = []) {
    this.baseUrl = normalizeBaseUrl(baseUrl); this.selectedUrl = this.baseUrl;
    this.addresses = Pairing.addresses(this.baseUrl, alternates);
    this.pairingCode = Pairing.validateCode(code);
    this.expectedHubId = hubId;
    this.pin = pin;
  }

  private static validateCode(code: string): string {
    const value = code.trim();
    if (!value || value.length > 8192 || /[\r\n\u0000]/.test(value)) throw new Error('invalid_pairing');
    return value;
  }

  static manual(baseUrl: string, code: string, pin: string = ''): Pairing {
    return new Pairing(baseUrl, code, '', pin);
  }

  static parse(input: string): Pairing {
    if (!input || input.length > 16384) throw new Error('invalid_pairing');
    let parsed: url.URL;
    try { parsed = url.URL.parseURL(input.trim()); } catch (_) { throw new Error('invalid_pairing'); }
    const scheme = parsed.protocol.toLowerCase();
    if (!isPairingScheme(scheme) || parsed.hostname.toLowerCase() !== 'pair' || (parsed.pathname !== '' && parsed.pathname !== '/'))
      throw new Error('invalid_pairing');
    if (parsed.username || parsed.password || parsed.hash) throw new Error('invalid_pairing');
    const params = parsed.params;
    let baseUrl = only(params, 'url', true);
    const code = only(params, 'token', true);
    const hubId = only(params, 'hub');
    const pin = only(params, 'fp');
    const tlsText = only(params, 'tls');
    // Hub pairing QR codes keep the legacy HTTP URL and carry the TLS listener port separately.
    if (tlsText) {
      const tlsPort = Number(tlsText);
      if (!Number.isInteger(tlsPort) || tlsPort < 1 || tlsPort > 65535) throw new Error('invalid_pairing');
      try {
        const endpoint = url.URL.parseURL(baseUrl);
        endpoint.protocol = 'https:';
        endpoint.port = String(tlsPort);
        baseUrl = endpoint.toString();
      } catch (_) { throw new Error('invalid_pairing'); }
    }
    const allowed = ['url', 'token', 'hub', 'fp', 'tls', 'alt'];
    params.forEach((_value: string, key: string) => { if (!allowed.includes(key)) throw new Error('invalid_pairing'); });
    if (hubId && hubId.length > 128) throw new Error('invalid_pairing');
    return new Pairing(baseUrl, code, hubId, pin, params.getAll('alt'));
  }

  private targetKey(target: PairingTarget): string {
    return JSON.stringify([target.baseUrl, target.hubId, target.name, target.pin]);
  }

  private static addresses(primary: string, alternates: string[]): string[] {
    if (alternates.length > 8) throw new Error('invalid_pairing');
    const values = new Set<string>([normalizeBaseUrl(primary)]);
    for (const value of alternates) { const address = normalizeBaseUrl(value); if (!address.startsWith('https://')) throw new Error('invalid_pairing'); values.add(address); }
    return Array.from(values);
  }
  private static async verifyAddress(address: string, expectedHubId: string, pin: string): Promise<PairingTarget> {
    const anonymous = new HubTransport(new EndpointConfig(address, expectedHubId, '', pin));
    try {
      const raw = await anonymous.request('/api/v1/health');
      const data = JSON.parse(raw) as Record<string, Object>;
      if (data['ok'] !== true) throw new Error('hub_unavailable');
      const hubId = safeString(data, 'hub_id'), name = safeString(data, 'hub_name');
      if (!hubId || (expectedHubId && hubId !== expectedHubId)) throw new Error('hub_identity_mismatch');
      return new PairingTarget(address, hubId, name || 'Persona hub', pin);
    } finally { anonymous.close(); }
  }
  static async reachable(primary: string, alternatives: string[], hubId: string, pin: string, admit: () => boolean = (): boolean => true): Promise<string> {
    if (!admit()) throw new Error('request_updated');
    // Reconnection can transfer credentials only between advertised addresses of the same pinned Hub.
    if (!hubId || !pin || !alternatives.length) return primary;
    let failure: Error = new Error('hub_unavailable');
    for (const address of Pairing.addresses(primary, alternatives)) {
      if (!admit()) throw new Error('request_updated');
      try { const target = await Pairing.verifyAddress(address, hubId, pin); if (!admit()) throw new Error('request_updated'); return target.baseUrl; }
      catch (error) { failure = error as Error; }
    }
    throw failure;
  }
  async inspect(): Promise<PairingTarget> {
    if (this.used) throw new Error('pairing_already_used');
    let failure: Error = new Error('hub_unavailable');
    for (const address of this.addresses) {
      try {
        const target = await Pairing.verifyAddress(address, this.expectedHubId, this.pin);
        this.selectedUrl = address; this.inspectedKey = this.targetKey(target); return target;
      } catch (error) { failure = error as Error; }
    }
    throw failure;
  }

  async redeem(target: PairingTarget, deviceName: string = 'HarmonyOS device', admit: () => boolean = (): boolean => true): Promise<EndpointConfig> {
    if (this.used || this.redeeming || !admit() || !this.inspectedKey || this.targetKey(target) !== this.inspectedKey)
      throw new Error('pairing_target_not_confirmed');
    const baseUrl = normalizeBaseUrl(target.baseUrl);
    if (baseUrl !== this.selectedUrl || target.pin !== this.pin || target.hubId !== this.expectedHubId && this.expectedHubId !== '')
      throw new Error('pairing_target_changed');

    this.redeeming = true;
    try {
    // Recheck the exact endpoint and hub id after the user's confirmation, without sending the pairing token.
    const verified = await Pairing.verifyAddress(this.selectedUrl, target.hubId, this.pin);
    if (!admit()) throw new Error('request_updated');
    if (this.targetKey(verified) !== this.inspectedKey) throw new Error('hub_identity_changed');

    const name = deviceName.trim().slice(0, 128) || 'HarmonyOS device';
    const transport = new HubTransport(new EndpointConfig(target.baseUrl, target.hubId, this.pairingCode, target.pin));
    this.used = true;
    try {
      const raw = await transport.request('/api/v1/auth/tokens', 'POST', JSON.stringify({ name: name, kind: 'phone' }));
      const result = JSON.parse(raw) as Record<string, Object>;
      const deviceToken = safeString(result, 'token');
      const deviceId = safeString(result, 'id');
      if (!deviceToken || !deviceId) throw new Error('pairing_response_invalid');
      if (!admit()) throw new Error('request_updated');
      return new EndpointConfig(target.baseUrl, target.hubId, deviceToken, target.pin, '', this.addresses.filter((address: string) => address !== target.baseUrl && address.startsWith('https://')));
    } finally { transport.close(); }
    } finally { this.redeeming = false; }
  }
}
