import { EndpointConfig, HttpFailure, HubTransport } from '../platform/HubTransport';
import type { BytesResponse } from '../platform/HubTransport';

/** A transport boundary that keeps every demo request inside its in-memory repository. */
export class DemoTransport extends HubTransport {
  constructor(config: EndpointConfig) { super(config); }

  stream(_path: string): HubTransport { throw new HttpFailure(501, 'demo_unsupported'); }

  hello(_features: string[]): boolean { return false; }

  async request(_path: string, _method: string = 'GET', _body: string = '', _opId: string = ''): Promise<string> {
    throw new HttpFailure(501, 'demo_unsupported');
  }

  async requestBytes(_path: string, _method: string = 'GET', _body?: ArrayBuffer,
    _contentType: string = 'application/octet-stream', _opId: string = ''): Promise<BytesResponse> {
    throw new HttpFailure(501, 'demo_unsupported');
  }

  async connect(_onMessage: (payload: string) => void, _onState?: (state: string) => void,
    _onBinary?: (payload: ArrayBuffer) => void): Promise<void> {
    throw new HttpFailure(501, 'demo_unsupported');
  }

  send(_payload: string): boolean { return false; }

  sendBinary(_payload: ArrayBuffer): boolean { return false; }

  close(): void { /* No demo transport resources are opened. */ }
}
