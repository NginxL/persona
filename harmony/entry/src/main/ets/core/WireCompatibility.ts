/** Legacy wire identifiers are confined to this compatibility boundary. */
export const legacyClientHeader: string = 'X-Jarvis-Client';
export const legacyOperationHeader: string = 'X-Jarvis-Op-Id';
export function isPairingScheme(value: string): boolean { return value === 'persona:' || value === 'jarvis:'; }
export function isPairingAddress(value: string): boolean { return value.startsWith('persona:') || value.startsWith('jarvis:'); }
export function isPairingLink(value: string): boolean { return value.startsWith('persona://pair') || value.startsWith('jarvis://pair'); }

/** Normalize supported Persona deep links at the compatibility boundary. */
export function canonicalPersonaLink(value: string): string {
  const colon = value.indexOf(':');
  if (colon < 0 || !isPairingScheme(value.slice(0, colon).toLowerCase() + ':')) return '';
  return 'persona:' + value.slice(colon + 1);
}
