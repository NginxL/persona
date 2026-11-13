import { url } from '@kit.ArkTS';

// Reader navigation has no Hub authorization headers or native application bridge.
export function readerLink(value: string): string {
  if (!value || value.length > 8192 || /[\r\n\\]/.test(value)) return '';
  try {
    const parsed = url.URL.parseURL(value);
    if (!['https:', 'http:'].includes(parsed.protocol.toLowerCase()) || !parsed.hostname || parsed.username || parsed.password) return '';
    return parsed.toString();
  } catch (_) { return ''; }
}
export function readerHost(value: string): string {
  try { return url.URL.parseURL(readerLink(value)).hostname; } catch (_) { return ''; }
}
