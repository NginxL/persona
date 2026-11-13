export interface DeviceLookAvatarFile {
  pixels: number;
  file: string;
}

export interface DeviceLookPackageColors {
  accentLight?: string;
  accentDark?: string;
  bubbleLight?: string;
  bubbleDark?: string;
  bubbleTextLight?: string;
  bubbleTextDark?: string;
}

export interface DeviceLookPackage {
  id: string;
  rev: string;
  bytes: number;
  name: string;
  subtitle: string;
  about: string;
  dotIdleFile: string;
  avatarFiles: DeviceLookAvatarFile[];
  colors?: DeviceLookPackageColors;
}

export interface DeviceLookColorSet {
  brand: string;
  emphasize: string;
  userBubble: string;
  userBubbleText: string;
}

export interface DeviceLookPalette {
  light: DeviceLookColorSet;
  dark: DeviceLookColorSet;
}

export const DEVICE_LOOK_MAX_AVATAR_BYTES = 4 * 1024 * 1024;
export const DEVICE_LOOK_MAX_PACKAGE_BYTES = 16 * 1024 * 1024;
const ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,31}$/;
const OWNER_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const REV_PATTERN = /^[A-Za-z0-9_-]{1,128}$/;
const HEX_PATTERN = /^#[0-9a-fA-F]{6}$/;
const RESERVED_PACKAGE_IDS: string[] = ['persona', 'persona-visor', 'persona-circuit', 'persona-pixel', 'persona-facet', 'persona-blob',
  'jarvis', 'jarvis-circuit', 'jarvis-pixel', 'jarvis-facet', 'jarvis-blob'];
const DOT_STATES: string[] = ['idle', 'thinking', 'working', 'needs_you', 'offline'];
const MAX_AVATAR_SIZES: number = 64;

type JsonRecord = Record<string, unknown>;

function record(value: unknown): JsonRecord | undefined {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return undefined;
  return value as JsonRecord;
}

function text(value: unknown): string { return typeof value === 'string' ? value : ''; }

function number(value: unknown): number | undefined {
  if (typeof value === 'number' && Number.isFinite(value)) return value;
  if (typeof value === 'string' && /^\d{1,5}$/.test(value)) return Number(value);
  return undefined;
}

function pair(light: JsonRecord | undefined, dark: JsonRecord | undefined, key: string): [string | undefined, string | undefined] {
  const l = normalizeHex(text(light?.[key]));
  const d = normalizeHex(text(dark?.[key]));
  if (!l && !d) return [undefined, undefined];
  return [l || d, d || l];
}

/** Persona ids follow the Hub package contract; the bundled Mochi id is never a remote package. */
export function validDeviceLookPackageId(value: string): boolean {
  return !RESERVED_PACKAGE_IDS.includes(value) && ID_PATTERN.test(value);
}

/** Owner identifiers are path components only after this check. */
export function validDeviceLookOwner(value: string): boolean { return OWNER_PATTERN.test(value); }

export function validDeviceLookRev(value: string): boolean { return REV_PATTERN.test(value); }

/** Validates an ASCII package-relative path and rejects traversal, hidden files, URL escapes and empty segments. */
export function validDeviceLookFile(value: string): boolean {
  if (!value || value.length > 512 || value.startsWith('/') || value.includes('\\') || value.includes('%')) return false;
  const parts = value.split('/');
  if (parts.length > 16) return false;
  return parts.every((part: string) => part.length > 0 && part !== '.' && part !== '..' && !part.startsWith('.') &&
    /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(part));
}

/** Parses a Hub package entry and rejects any part that is unsafe to cache or fetch. */
export function parseDeviceLookPackage(value: unknown): DeviceLookPackage | undefined {
  const info = record(value);
  if (!info) return undefined;
  const id = text(info.id), rev = text(info.rev), bytes = number(info.bytes);
  if (!validDeviceLookPackageId(id) || !validDeviceLookRev(rev) || bytes === undefined || bytes < 1 || bytes > DEVICE_LOOK_MAX_PACKAGE_BYTES) return undefined;
  const manifest = record(info.manifest);
  if (!manifest || number(manifest.format) !== 1 || text(manifest.id) !== id || !text(manifest.name).trim()) return undefined;
  const avatar = record(manifest.avatar), sizes = record(avatar?.sizes), dot = record(manifest.dot), states = record(dot?.states);
  if (!sizes || Object.keys(sizes).length > MAX_AVATAR_SIZES || !states || !record(states.idle)) return undefined;

  const avatarFiles: DeviceLookAvatarFile[] = [];
  const allFiles: string[] = [];
  for (const key of Object.keys(sizes)) {
    const pixels = number(key), file = text(sizes[key]);
    if (!pixels || pixels < 1 || pixels > 4096 || !validDeviceLookFile(file)) return undefined;
    avatarFiles.push({ pixels: pixels, file: file });
    allFiles.push(file);
  }
  if (!avatarFiles.length) return undefined;
  for (const state of Object.keys(states)) {
    if (!DOT_STATES.includes(state)) return undefined;
    const entry = record(states[state]);
    const file = text(entry?.file);
    if (!file || !validDeviceLookFile(file)) return undefined;
    allFiles.push(file);
  }
  if (!allFiles.length || !allFiles.includes(text(record(states.idle)?.file))) return undefined;

  const rawColors = record(manifest.colors);
  const light = record(rawColors?.light), dark = record(rawColors?.dark);
  const [accentLight, accentDark] = pair(light, dark, 'accent');
  const [bubbleLight, bubbleDark] = pair(light, dark, 'bubble');
  const [bubbleTextLight, bubbleTextDark] = pair(light, dark, 'bubble_text');
  const colors: DeviceLookPackageColors = {
    accentLight: accentLight,
    accentDark: accentDark,
    bubbleLight: bubbleLight,
    bubbleDark: bubbleDark,
    bubbleTextLight: bubbleTextLight,
    bubbleTextDark: bubbleTextDark
  };
  const hasColors = !!(accentLight || bubbleLight);
  avatarFiles.sort((left: DeviceLookAvatarFile, right: DeviceLookAvatarFile) => left.pixels - right.pixels);
  const result: DeviceLookPackage = {
    id: id,
    rev: rev,
    bytes: bytes,
    name: text(manifest.name).trim().slice(0, 128),
    subtitle: text(manifest.subtitle).trim().slice(0, 256),
    about: text(manifest.about).trim().slice(0, 256),
    dotIdleFile: text(record(states.idle)?.file),
    avatarFiles: avatarFiles,
    colors: hasColors ? colors : undefined
  };
  return result;
}

/** A bounded, normalized record sufficient to restore this device's selected avatar and palette. */
export function deviceLookPackageJSON(pack: DeviceLookPackage): string {
  const sizes: Record<string, Object> = {};
  for (const item of pack.avatarFiles) sizes[String(item.pixels)] = item.file;
  const colors: JsonRecord = {};
  if (pack.colors) {
    const light: JsonRecord = {}, dark: JsonRecord = {};
    if (pack.colors.accentLight) light['accent'] = pack.colors.accentLight;
    if (pack.colors.accentDark) dark['accent'] = pack.colors.accentDark;
    if (pack.colors.bubbleLight) light['bubble'] = pack.colors.bubbleLight;
    if (pack.colors.bubbleDark) dark['bubble'] = pack.colors.bubbleDark;
    if (pack.colors.bubbleTextLight) light['bubble_text'] = pack.colors.bubbleTextLight;
    if (pack.colors.bubbleTextDark) dark['bubble_text'] = pack.colors.bubbleTextDark;
    colors['light'] = light;
    colors['dark'] = dark;
  }
  const manifest: JsonRecord = {
    format: 1, id: pack.id, name: pack.name, subtitle: pack.subtitle, about: pack.about,
    avatar: { sizes: sizes }, dot: { states: { idle: { file: pack.dotIdleFile } } }
  };
  if (pack.colors) manifest['colors'] = colors;
  return JSON.stringify({ id: pack.id, rev: pack.rev, bytes: pack.bytes, manifest: manifest });
}

/** The smallest avatar asset at or above the requested pixel size, or the largest one available. */
export function chooseDeviceLookAvatarFile(files: DeviceLookAvatarFile[], desiredPixels: number): DeviceLookAvatarFile | undefined {
  if (!files.length) return undefined;
  const sorted = files.slice().sort((left: DeviceLookAvatarFile, right: DeviceLookAvatarFile) => left.pixels - right.pixels);
  return sorted.find((item: DeviceLookAvatarFile) => item.pixels >= desiredPixels) || sorted[sorted.length - 1];
}

export function normalizeHex(value: string): string | undefined {
  if (!HEX_PATTERN.test(value.trim())) return undefined;
  return value.trim().toUpperCase();
}

function rgb(hex: string): [number, number, number] {
  const value = Number.parseInt(hex.slice(1), 16);
  return [(value >> 16) & 255, (value >> 8) & 255, value & 255];
}

function channel(value: number): number {
  const c = value / 255;
  return c <= 0.04045 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
}

function luminance(hex: string): number {
  const values = rgb(hex);
  return channel(values[0]) * 0.2126 + channel(values[1]) * 0.7152 + channel(values[2]) * 0.0722;
}

export function contrastRatio(left: string, right: string): number {
  const l1 = luminance(left), l2 = luminance(right);
  return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05);
}

/** Foreground for native emphasized controls; text on the canvas uses a separate token. */
export function deviceLookAccentText(brand: string): string {
  return contrastRatio(brand, '#000000') >= contrastRatio(brand, '#FFFFFF') ? '#000000' : '#FFFFFF';
}

function mix(left: string, right: string, amount: number): string {
  const a = rgb(left), b = rgb(right);
  const channels = [0, 1, 2].map((index: number) => Math.round(a[index] * (1 - amount) + b[index] * amount));
  return '#' + channels.map((value: number) => value.toString(16).padStart(2, '0')).join('').toUpperCase();
}

function meet(color: string, background: string, floor: number): string {
  if (contrastRatio(color, background) >= floor) return color;
  const target = background === '#FFFFFF' ? '#000000' : '#FFFFFF';
  for (let step = 1; step <= 100; step++) {
    const candidate = mix(color, target, step / 100);
    if (contrastRatio(candidate, background) >= floor) return candidate;
  }
  return target;
}

function readableBubble(fill: string, suppliedText: string | undefined): [string, string] {
  const textColor = suppliedText || (contrastRatio(fill, '#000000') >= contrastRatio(fill, '#FFFFFF') ? '#000000' : '#FFFFFF');
  const target = contrastRatio(textColor, '#000000') >= contrastRatio(textColor, '#FFFFFF') ? '#000000' : '#FFFFFF';
  let result = fill;
  for (let step = 1; step <= 100 && contrastRatio(textColor, result) < 4.5; step++) result = mix(fill, target, step / 100);
  return [result, textColor];
}

function styleColors(style: string, dark: boolean): DeviceLookColorSet {
  const brand = dark ? '#9BB4FF' : '#0055FF';
  const bubbles: Record<string, [string, string]> = {
    glass: ['#E0EDFF', '#263E61'],
    brief: ['#F1EADB', '#403B2E'],
    board: ['#DCE8F4', '#283A4D']
  };
  const selected = bubbles[style] || bubbles.glass;
  const bubble = dark ? selected[1] : selected[0];
  const text = dark ? '#FFFFFF' : '#000000';
  return { brand: brand, emphasize: brand, userBubble: bubble, userBubbleText: text };
}

interface PresetColors {
  light: DeviceLookColorSet;
  dark: DeviceLookColorSet;
}

const PRESETS: Record<string, PresetColors> = {
  blue: {
    light: { brand: '#007AFF', emphasize: '#007AFF', userBubble: '#DCEAFF', userBubbleText: '#000000' },
    dark: { brand: '#0A84FF', emphasize: '#0A84FF', userBubble: '#1D3557', userBubbleText: '#FFFFFF' }
  },
  coral: {
    light: { brand: '#D4683F', emphasize: '#D4683F', userBubble: '#B24C25', userBubbleText: '#FFFFFF' },
    dark: { brand: '#CD6E45', emphasize: '#CD6E45', userBubble: '#AF4C28', userBubbleText: '#FFFFFF' }
  },
  lavender: {
    light: { brand: '#7A5AF8', emphasize: '#7A5AF8', userBubble: '#6759D3', userBubbleText: '#FFFFFF' },
    dark: { brand: '#8979EB', emphasize: '#8979EB', userBubble: '#6756D9', userBubbleText: '#FFFFFF' }
  },
  teal: {
    light: { brand: '#27747F', emphasize: '#27747F', userBubble: '#14201E', userBubbleText: '#FFFFFF' },
    dark: { brand: '#2A9798', emphasize: '#2A9798', userBubble: '#E8F3F1', userBubbleText: '#0B1211' }
  },
  lark: {
    light: { brand: '#3370FF', emphasize: '#3370FF', userBubble: '#D1E3FF', userBubbleText: '#000000' },
    dark: { brand: '#4C88FF', emphasize: '#4C88FF', userBubble: '#133063', userBubbleText: '#FFFFFF' }
  }
};

function packageColors(style: string, dark: boolean, colors: DeviceLookPackageColors | undefined): DeviceLookColorSet {
  const fallback = styleColors(style, dark);
  if (!colors) return fallback;
  const suppliedAccent = dark ? colors.accentDark : colors.accentLight;
  const suppliedBubble = dark ? colors.bubbleDark : colors.bubbleLight;
  const suppliedText = dark ? colors.bubbleTextDark : colors.bubbleTextLight;
  const background = dark ? '#000000' : '#FFFFFF';
  const brand = suppliedAccent ? meet(suppliedAccent, background, 4.5) : fallback.brand;
  const emphasize = meet(brand, background, 4.5);
  const derivedBubble = mix(brand, background, dark ? 0.72 : 0.88);
  const [bubble, text] = readableBubble(suppliedBubble || derivedBubble, suppliedBubble ? suppliedText : undefined);
  return { brand: brand, emphasize: emphasize, userBubble: bubble, userBubbleText: text };
}

function safeColorSet(colors: DeviceLookColorSet, dark: boolean): DeviceLookColorSet {
  const background = dark ? '#000000' : '#FFFFFF';
  const emphasize = meet(colors.emphasize, background, 4.5);
  const [bubble, text] = readableBubble(colors.userBubble, colors.userBubbleText);
  // Preset brand matches native tokens exactly; emphasized text has its own 4.5:1 floor.
  return { brand: colors.brand, emphasize: emphasize, userBubble: bubble, userBubbleText: text };
}

/** The independent Accent choice, with the existing native style bubble when no custom bubble applies. */
export function deviceLookPalette(accent: string, style: string, avatar: DeviceLookPackageColors | undefined): DeviceLookPalette {
  if (accent === 'avatar') return {
    light: packageColors(style, false, avatar),
    dark: packageColors(style, true, avatar)
  };
  if (accent === 'style') return { light: styleColors(style, false), dark: styleColors(style, true) };
  const preset = PRESETS[accent] || PRESETS.blue;
  return {
    light: safeColorSet(preset.light, false),
    dark: safeColorSet(preset.dark, true)
  };
}

export function validAccentId(value: string): boolean {
  return value === 'style' || value === 'avatar' || Object.keys(PRESETS).includes(value);
}
