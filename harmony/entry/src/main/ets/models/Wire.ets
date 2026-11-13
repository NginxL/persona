// Decode untrusted JSON at the transport boundary. Unknown keys never become executable actions.
export class Wire {
  static payload(keys: string[], values: Object[]): Record<string, Object> {
    const result: Record<string, Object> = {}; keys.forEach((key: string, index: number) => result[key] = values[index]); return result;
  }
  static readonly maxBytes: number = 4 * 1024 * 1024;
  static parse(raw: string): Record<string, Object> {
    if (raw.length > Wire.maxBytes) throw new Error('response_too_large');
    if (!raw.trim()) return {};
    const value: Object = JSON.parse(raw) as Object;
    return Wire.record(value);
  }
  static record(value: Object | undefined | null): Record<string, Object> {
    if (value === undefined || value === null || typeof value !== 'object' || Array.isArray(value)) return {};
    return value as Record<string, Object>;
  }
  static object(value: Record<string, Object>, key: string): Record<string, Object> { return Wire.record(value[key]); }
  static has(value: Record<string, Object>, key: string): boolean { return Object.keys(value).includes(key); }
  static text(value: Record<string, Object>, key: string, fallback: string = ''): string {
    const result = value[key];
    return typeof result === 'string' ? result as string : fallback;
  }
  static number(value: Record<string, Object>, key: string, fallback: number = 0): number {
    const result = value[key];
    return typeof result === 'number' && Number.isFinite(result as number) ? result as number : fallback;
  }
  static identifier(value: Record<string, Object>, key: string): string {
    const result = value[key];
    if (typeof result === 'string') return result as string;
    return typeof result === 'number' && Number.isSafeInteger(result as number) && (result as number) > 0 ? String(result) : '';
  }
  static bool(value: Record<string, Object>, key: string, fallback: boolean = false): boolean {
    const result = value[key]; return typeof result === 'boolean' ? result as boolean : fallback;
  }
  static rows(value: Record<string, Object>, key: string): Record<string, Object>[] {
    const result = value[key];
    if (!Array.isArray(result)) return [];
    return (result as Object[]).map((item: Object) => Wire.record(item));
  }
  static strings(value: Record<string, Object>, key: string): string[] {
    const result = value[key];
    if (!Array.isArray(result)) return [];
    return (result as Object[]).filter((item: Object) => typeof item === 'string').map((item: Object) => item as string);
  }
}

export class ResourceRow {
  id: string = '';
  title: string = '';
  detail: string = '';
  kind: string = '';
  state: string = '';
  manager: string = '';
  group: string = '';
  machine: string = '';
  session: string = '';
  path: string = '';
  raw: string = '{}';
  constructor(id: string = '', title: string = '', kind: string = '') { this.id = id; this.title = title; this.kind = kind; }
  data(): Record<string, Object> { return Wire.parse(this.raw); }
  static from(data: Record<string, Object>, kind: string): ResourceRow {
    const row = new ResourceRow(Wire.text(data, 'id', Wire.text(data, 'session_id')),
      Wire.text(data, 'title', Wire.text(data, 'name', Wire.text(data, 'subject', Wire.text(data, 'label')))), kind);
    row.detail = Wire.text(data, 'summary', Wire.text(data, 'notes', Wire.text(data, 'preview', Wire.text(data, 'description', Wire.text(data, 'one_line', Wire.text(data, 'content', Wire.text(data, 'text', Wire.text(data, 'body'))))))));
    row.state = Wire.text(data, 'state', Wire.text(data, 'status'));
    row.manager = Wire.text(data, 'manager', Wire.text(data, 'manager_id'));
    row.machine = Wire.text(data, 'machine_id', Wire.text(data, 'machine'));
    row.session = Wire.text(data, 'session_id', kind === 'session' ? row.id : '');
    row.path = Wire.text(data, 'cwd', Wire.text(data, 'path'));
    row.raw = JSON.stringify(data);
    return row;
  }
  isDone(): boolean { const data = this.data(); return !!Wire.text(data, 'done_at') || Wire.bool(data, 'done'); }
  key(): string { return JSON.stringify(this.group ? [this.group, this.kind, this.manager, this.machine, this.id] : [this.kind, this.manager, this.machine, this.id]); }
}

export class ChatMessage {
  id: string = '';
  role: string = 'assistant';
  at: number = 0;
  text: string = '';
  work: string = '';
  imageId: string = '';
  itemId: string = '';
  ownerTarget: string = '';
  imagePaths: string[] = [];
  filePaths: string[] = [];
  running: boolean = false;
  failed: boolean = false;
  deliveryState: string = '';
  localOperation: string = '';
  localImageCount: number = 0;
  source: string = '';
  raw: string = '{}';
  constructor(id: string = '', role: string = 'assistant', text: string = '') { this.id = id; this.role = role; this.text = text; }
}

@ObservedV2
export class Decision {
  id: string = '';
  machine: string = '';
  session: string = '';
  kind: string = '';
  prompt: string = '';
  options: string[] = [];
  fields: DecisionField[] = [];
  @Trace status: string = 'open';
  revision: string = '';
  raw: string = '{}';
}
export class DecisionField {
  key: string = '';
  title: string = '';
  kind: string = 'text';
  required: boolean = false;
  choices: string[] = [];
}

export class HubProfile {
  id: string = '';
  name: string = '';
  baseUrl: string = '';
  addresses: string[] = [];
  hubId: string = '';
  pin: string = '';
  manager: string = '';
  constructor(id: string = '', name: string = '', baseUrl: string = '') { this.id = id; this.name = name; this.baseUrl = baseUrl; }
}
