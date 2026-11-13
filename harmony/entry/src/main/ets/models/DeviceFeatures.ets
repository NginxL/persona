import { common, abilityAccessCtrl, bundleManager, Permissions } from '@kit.AbilityKit';
import { preferences } from '@kit.ArkData';
import { util } from '@kit.ArkTS';
import { audio } from '@kit.AudioKit';
import { calendarManager } from '@kit.CalendarKit';
import { contact } from '@kit.ContactsKit';
import { storageStatistics } from '@kit.CoreFileKit';
import { i18n } from '@kit.LocalizationKit';
import { geoLocationManager } from '@kit.LocationKit';
import { connection } from '@kit.NetworkKit';
import { batteryInfo, deviceInfo, pasteboard, systemDateTime } from '@kit.BasicServicesKit';
import { ClientModel } from './ClientModel';
import { Wire } from './Wire';
import { messageDisplay } from './Compatibility';
import { HubTransport, HttpFailure } from '../platform/HubTransport';
import { VoiceAudioBridge } from '../platform/device/VoiceAudioBridge';
import { tr } from '../design/Strings';
import type { NativePhoneWritePlan } from '../platform/NativePhoneWrites';
import { prepareContactWrite, prepareCalendarWrite, searchNativePlaces, hasNativeRecurrence } from '../platform/NativePhoneWrites';

const CONTACTS: string = 'ohos.permission.READ_CONTACTS';
const CONTACTS_WRITE: string = 'ohos.permission.WRITE_CONTACTS';
const CALENDAR_READ: string = 'ohos.permission.READ_CALENDAR';
const CALENDAR_WRITE: string = 'ohos.permission.WRITE_CALENDAR';
const LOCATION: string = 'ohos.permission.APPROXIMATELY_LOCATION';
const PASTEBOARD_READ: string = 'ohos.permission.READ_PASTEBOARD';
const MICROPHONE: string = 'ohos.permission.MICROPHONE';
const TOOL_TIMEOUT_MS: number = 90000;
const COMPLETED_LIMIT: number = 128;
const VOICE_RUN_LIMIT: number = 20;
const VOICE_RUN_STATES: string[] = ['sending', 'sent', 'working', 'needs_you', 'blocked', 'held', 'done',
  'failed', 'refused', 'unknown', 'folded'];
const VOICE_RESUME_RUN_LIMIT: number = 10;
const VOICE_HISTORY_LIMIT: number = 40;
const VOICE_RETRY_DELAYS: number[] = [1000, 4000, 10000];
const VOICE_STABLE_MS: number = 60000;

type ConfirmAction = (title: string, message: string, approve: () => void, deny: () => void) => void;
interface DeviceResult {
  content: Array<Record<string, Object>>;
  is_error: boolean;
}

interface CallOwner {
  model: ClientModel;
  scope: string;
  transport: HubTransport;
}

interface CachedDeviceResult {
  owner: CallOwner;
  result: DeviceResult;
}

interface DeviceVoiceRunUpdate {
  task: string;
  state: string;
  msgIds: string[];
  baseSeq: number;
  startedAt: Object;
  code: string;
  message: string;
  draft: string;
  clearable: string;
}

class DeviceToolDefinition {
  name: string = '';
  description: string = '';
  source: string = '';
  permissions: string[] = [];
  foregroundOnly: boolean = false;
  write: boolean = false;
  inputSchema: Record<string, Object> = {};
  constructor(name: string, description: string, source: string, inputSchema: Record<string, Object>,
    permissions: string[] = [], foregroundOnly: boolean = false, write: boolean = false) {
    this.name = name; this.description = description; this.source = source;
    this.inputSchema = inputSchema; this.permissions = permissions;
    this.foregroundOnly = foregroundOnly; this.write = write;
  }
  wire(): Record<string, Object> {
    return Wire.payload(['name', 'description', 'input_schema'], [this.name, this.description, this.inputSchema]);
  }
}

@ObservedV2
export class DevicePermissionRow {
  id: string = '';
  label: string = '';
  permission: string = '';
  @Trace status: string = 'unavailable';
  constructor(id: string, label: string, permission: string, status: string) {
    this.id = id; this.label = label; this.permission = permission; this.status = status;
  }
  canRequest(): boolean { return this.status === 'not_determined' || this.status === 'denied'; }
}

@ObservedV2
export class DeviceVoiceLine {
  id: string = '';
  @Trace role: string = 'assistant';
  @Trace text: string = '';
  @Trace final: boolean = false;
  constructor(id: string, role: string, text: string, final: boolean) {
    this.id = id; this.role = role; this.text = text; this.final = final;
  }
}

@ObservedV2
export class DeviceVoiceRun {
  id: string = '';
  @Trace task: string = '';
  @Trace state: string = '';
  msgIds: string[] = [];
  baseSeq: number = 0;
  startedAt: Object = '';
  @Trace code: string = '';
  @Trace message: string = '';
  @Trace draft: string = '';
  @Trace clearable: string = '';
  @Trace seenGeneration: number = 0;
  constructor(id: string, task: string, state: string, msgIds: string[], baseSeq: number,
    startedAt: Object, code: string, message: string, draft: string, clearable: string) {
    this.id = id; this.task = task; this.state = state; this.msgIds = msgIds; this.baseSeq = baseSeq;
    this.startedAt = startedAt; this.code = code; this.message = message; this.draft = draft; this.clearable = clearable;
  }
  isOpen(): boolean { return !['done', 'failed', 'refused', 'unknown', 'folded'].includes(this.state); }
  resumeFrame(): Record<string, Object> {
    const frame = Wire.payload(['id', 'msg_ids', 'task', 'base_seq', 'state'],
      [this.id, this.msgIds, this.task, this.baseSeq, this.state]);
    if (typeof this.startedAt === 'string' && this.startedAt.length > 0 || typeof this.startedAt === 'number') {
      frame['started_at'] = this.startedAt;
    }
    return frame;
  }
}

function stringProperty(description: string): Record<string, Object> {
  return Wire.payload(['type', 'description'], ['string', description]);
}

function labeledProperty(): Record<string, Object> {
  return Wire.payload(['type', 'maxItems', 'items'], ['array', 20,
    objectSchema(Wire.payload(['value', 'label'], [stringProperty('Phone number or email address.'), stringProperty('Optional mobile, home, work, main or other label.')]), ['value'])]);
}
function contactProperties(update: boolean): Record<string, Object> {
  const properties = Wire.payload(['given_name', 'family_name', 'company', 'job_title', 'phones', 'emails'], [
    stringProperty('First name.'), stringProperty('Family name.'), stringProperty('Company.'), stringProperty('Job title.'), labeledProperty(), labeledProperty()]);
  if (update) { properties['id'] = stringProperty('Contact id returned by phone_contacts_search.'); properties['replace'] = booleanProperty('Explicitly overwrite populated fields and replace provided phone/email lists. Default false.'); }
  return properties;
}
function calendarChangeProperties(remove: boolean): Record<string, Object> {
  const properties = Wire.payload(['id', 'occurrence_start', 'span'], [stringProperty('Event id returned by phone_calendar_events.'),
    stringProperty('Occurrence changes require platform support; unsupported recurring events are refused without changing the series.'),
    stringProperty('this or future. Recurring-series operations are unavailable on this native API.')]);
  if (!remove) {
    for (const key of ['title', 'start', 'end', 'location', 'notes']) properties[key] = stringProperty('New ' + key + (key === 'start' || key === 'end' ? ', ISO 8601 with timezone.' : '.'));
    properties['all_day'] = booleanProperty('Whether the event is all-day.');
  }
  return properties;
}

function integerProperty(description: string, min: number, max: number): Record<string, Object> {
  return Wire.payload(['type', 'description', 'minimum', 'maximum'], ['integer', description, min, max]);
}

function booleanProperty(description: string): Record<string, Object> {
  return Wire.payload(['type', 'description'], ['boolean', description]);
}

function objectSchema(properties: Record<string, Object> = {}, required: string[] = []): Record<string, Object> {
  const schema = Wire.payload(['type', 'additionalProperties'], ['object', false]);
  if (Object.keys(properties).length > 0) schema['properties'] = properties;
  if (required.length > 0) schema['required'] = required;
  return schema;
}

function textSchema(description: string, required: string[]): Record<string, Object> {
  return objectSchema(Wire.payload(['text'], [stringProperty(description)]), required);
}

function integerValue(args: Record<string, Object>, key: string, fallback: number, min: number, max: number): number {
  const value = args[key];
  return typeof value === 'number' && Number.isFinite(value as number) ? Math.min(max, Math.max(min, Math.round(value as number))) : fallback;
}

function optionalText(args: Record<string, Object>, key: string): string {
  const value = args[key]; return typeof value === 'string' ? (value as string).trim() : '';
}

function dateArgument(value: string, fallback: Date): Date {
  if (!value) return fallback;
  if (!/(Z|[+-][0-9]{2}:[0-9]{2})$/i.test(value)) throw new Error('ISO 8601 time must include a timezone.');
  const parsed = new Date(value);
  if (!Number.isFinite(parsed.getTime())) throw new Error('The time is not valid ISO 8601.');
  return parsed;
}

function localIso(date: Date): string {
  const shifted = new Date(date.getTime() - date.getTimezoneOffset() * 60000);
  const local = shifted.toISOString().slice(0, 19);
  const offset = -date.getTimezoneOffset();
  const sign = offset >= 0 ? '+' : '-';
  const hours = String(Math.floor(Math.abs(offset) / 60)).padStart(2, '0');
  const minutes = String(Math.abs(offset) % 60).padStart(2, '0');
  return local + sign + hours + ':' + minutes;
}

function jsonText(value: Object): string { return JSON.stringify(value); }

function resultText(text: string, isError: boolean = false): DeviceResult {
  return { content: [Wire.payload(['type', 'text'], ['text', text])], is_error: isError };
}

function jsonResult(value: Object): DeviceResult { return resultText(JSON.stringify(value)); }

interface RunningCall {
  cancelled: boolean;
  expired: boolean;
  owner: CallOwner;
  cancel: (() => void) | undefined;
  confirmCancel: (() => void) | undefined;
}

/** Native phone tools and the lifecycle boundary for an optional foreground voice call. */
@ObservedV2
export class DeviceFeatures {
  @Trace status: string = 'offline';
  @Trace toolCount: number = 0;
  @Trace voiceStatus: string = 'voice_unavailable';
  @Trace voiceExtensionId: string = '';
  @Trace voiceActive: boolean = false;
  @Trace voiceMuted: boolean = false;
  @Trace voiceCaptions: DeviceVoiceLine[] = [];
  @Trace voiceRuns: DeviceVoiceRun[] = [];
  @Trace voiceFeatures: string[] = [];
  @Trace voiceProtocol: number = 0;
  @Trace voiceToolLabel: string = '';
  @Trace voiceAskingId: string = '';
  @Trace voiceNotice: string = '';
  @Trace permissions: DevicePermissionRow[] = [];
  private model: ClientModel | undefined;
  private abilityContext: common.UIAbilityContext | undefined;
  private confirmAction: ConfirmAction | undefined;
  private scopeKey: string = '';
  private targetKey: string = '';
  private foregrounded: boolean = false;
  private callPending: Map<string, Promise<DeviceResult>> = new Map();
  private callState: Map<string, RunningCall> = new Map();
  private callOwners: Map<string, CallOwner> = new Map();
  private completed: Map<string, CachedDeviceResult> = new Map();
  private registrationKey: string = '';
  private syncing: Promise<void> = Promise.resolve();
  private voiceDiscoveryGeneration: number = 0;
  private voiceCallGeneration: number = 0;
  private voiceConnectionGeneration: number = 0;
  private voiceRetryAttempt: number = 0;
  private voiceRetryTimer: number = -1;
  private voiceReady: boolean = false;
  private voiceIdentity: string = '';
  private voiceOwnerModel: ClientModel | undefined;
  private voiceExpectedTarget: Record<string, Object> | undefined;
  private voiceResumeAvailable: boolean = false;
  private voiceEverReady: boolean = false;
  private voiceAudioStarting: boolean = false;
  private voiceAudioStarted: boolean = false;
  private voiceTransport: HubTransport | undefined;
  private voiceAudio: VoiceAudioBridge = new VoiceAudioBridge();
  private voiceReadyTimer: number = -1;
  private voiceStableTimer: number = -1;
  private voiceInputRate: number = 16000;
  private voiceOutputRate: number = 24000;
  private voiceResumePending: boolean = false;
  private voiceTools: Map<string, string> = new Map();
  private voiceStarting: boolean = false;
  private voiceStartVisit: number = 0;
  private voiceCallProtocol: number = 0;
  private registeredTransport: HubTransport | undefined;
  private registeredDeviceId: string = '';

  boundTo(model: ClientModel): boolean { return this.model === model; }
  private unregisterPhone(): void {
    this.registeredTransport?.send(JSON.stringify(Wire.payload(['type', 'device_id', 'tools'], ['register_phone', this.registeredDeviceId, []])));
    this.registeredTransport = undefined; this.registeredDeviceId = ''; this.registrationKey = '';
  }

  private readonly catalog: DeviceToolDefinition[] = [
    new DeviceToolDefinition('phone_contacts_search',
      "Search the phone's contacts by name, company, job title, email or phone number. Returns up to `limit` people with their name, company, job title, phone numbers (with labels such as mobile/work), email addresses and birthday (yyyy-MM-dd). Use this to look up a colleague's number before calling or drafting a message.",
      'contacts', objectSchema(Wire.payload(['query', 'limit'], [
        stringProperty('Text to match against name, company, job title, email or phone number. Matching is case-insensitive and looks for the text anywhere in the field; a phone number matches with or without spaces and dashes. Pass an empty string to get the first contacts in the address book\'s own order.'),
        integerProperty('Maximum number of contacts to return, default 10, max 50.', 1, 50)
      ]), ['query']), [CONTACTS]),
    new DeviceToolDefinition('phone_contacts_create', 'Create a contact with at least a name or company. The phone shows the proposed card and requires approval.', 'contacts', objectSchema(contactProperties(false)), [CONTACTS, CONTACTS_WRITE], true, true),
    new DeviceToolDefinition('phone_contacts_update', 'Update a contact by its returned id. Append phone/email values by default; populated fields require explicit replace=true. The phone confirms the exact before and after.', 'contacts', objectSchema(contactProperties(true), ['id']), [CONTACTS, CONTACTS_WRITE], true, true),
    new DeviceToolDefinition('phone_calendar_update_event', 'Update an accessible non-recurring event with phone approval. A new start keeps its duration when end is absent. Unsupported occurrence/series changes are refused.', 'calendar', objectSchema(calendarChangeProperties(false), ['id']), [CALENDAR_READ, CALENDAR_WRITE], true, true),
    new DeviceToolDefinition('phone_calendar_delete_event', 'Delete an accessible non-recurring event after phone approval. Unsupported occurrence/series operations never delete the whole series.', 'calendar', objectSchema(calendarChangeProperties(true), ['id']), [CALENDAR_READ, CALENDAR_WRITE], true, true),
    new DeviceToolDefinition('phone_location_search', 'Find places by name or address, returning candidate names, addresses and coordinates. Does not request or disclose the current phone location.', 'location', objectSchema(Wire.payload(['query', 'limit'], [stringProperty('Place name or address.'), integerProperty('Maximum candidates; default 5, maximum 10.', 1, 10)]), ['query'])),
    new DeviceToolDefinition('phone_calendar_events',
      'List calendar events in a time window. Defaults to the next 7 days starting at 00:00 today. Times are ISO 8601 with a timezone offset (e.g. 2026-10-24T14:00:00+08:00). Returns each event\'s id, title, start, end, all-day flag, calendar name, location, the first 300 characters of notes and the attendees. Use it to check whether the user is free.',
      'calendar', objectSchema(Wire.payload(['start', 'end', 'query', 'limit'], [
        stringProperty('Window start, ISO 8601 (e.g. 2026-10-24T00:00:00+08:00). Default: 00:00 today.'),
        stringProperty('Window end, ISO 8601. Default: 7 days after start.'),
        stringProperty('Only return events whose title, location or notes contain this text.'),
        integerProperty('Maximum number of events to return, default 50, max 200.', 1, 200)
      ])), [CALENDAR_READ]),
    new DeviceToolDefinition('phone_calendar_create_event',
      'Create a calendar event. `start` and `end` are ISO 8601 with a timezone (e.g. 2026-10-24T14:00:00+08:00). For an all-day event pass all_day=true and dates at 00:00. The user must approve the event on the phone before it is saved. Returns the new event\'s identifier.',
      'calendar', objectSchema(Wire.payload(['title', 'start', 'end', 'location', 'notes', 'all_day', 'calendar'], [
        stringProperty('Event title.'), stringProperty('Start time, ISO 8601 with a timezone.'),
        stringProperty('End time, ISO 8601 with a timezone. Must be after start.'), stringProperty('Optional location.'),
        stringProperty('Optional notes.'), booleanProperty('Mark the event as all-day. Default false.'),
        stringProperty('Calendar name (title) to put the event in. Default: the default calendar for new events.')
      ]), ['title', 'start', 'end']), [CALENDAR_READ, CALENDAR_WRITE], false, true),
    new DeviceToolDefinition('phone_location_current',
      "Get the phone's current location. Returns latitude, longitude, horizontal accuracy in meters, altitude, the reverse-geocoded address (country, province, city, district, street, formatted address), the device timezone and the timestamp of the fix (ISO 8601). Waits at most 15 seconds for a fix. Only call this when the user asks something that needs their location.",
      'location', objectSchema(), [LOCATION], true),
    new DeviceToolDefinition('phone_device_info',
      "Describe the HarmonyOS phone: model identifier and name, HarmonyOS version, battery percentage and charging state, free and total storage in bytes, current network type (wifi/cellular/wired/offline), timezone and locale, and the Persona app's version and build number. Cheap and safe to call when you need context about the device.",
      'device', objectSchema()),
    new DeviceToolDefinition('phone_clipboard_get',
      'Read the phone\'s clipboard as text. The system may ask the user to allow clipboard access. Returns "text" and its character count, or text: null when the clipboard is empty or holds something that is not text.',
      'clipboard', objectSchema(), [PASTEBOARD_READ], true),
    new DeviceToolDefinition('phone_clipboard_set',
      'Replace the phone\'s clipboard with the given text, for example a link or a draft the user asked for. Overwrites whatever was there.',
      'clipboard', textSchema('Text to place on the clipboard.', ['text']), [], true, true)
  ];

  bind(model: ClientModel, context: common.UIAbilityContext, confirm: ConfirmAction): void {
    const previousModel = this.model;
    const previousScope = this.scopeKey;
    if (previousModel && previousModel !== model) {
      previousModel.setDeviceEvents(undefined); this.unregisterPhone(); this.cancelAll('scope_changed');
    }
    this.model = model; this.abilityContext = context; this.confirmAction = confirm; this.foregrounded = true;
    model.setDeviceEvents((payload: string): void => this.receive(payload, model));
    this.scopeKey = model.scope(); this.targetKey = model.target();
    if (previousModel && previousModel !== model) this.stopVoice('voice_ready', true).catch(() => undefined);
    if (previousScope && previousScope !== this.scopeKey) this.cancelAll('scope_changed');
    this.refreshPermissions();
    this.context();
  }

  /** Re-checks profile and target identity before registering or continuing a scoped operation. */
  contextChanged(): void { this.context(); }

  /** Main-shell integration name retained for the feature lifecycle hooks. */
  context(): void {
    const model = this.model;
    if (!model) return;
    const nextScope = model.scope(), nextTarget = model.target();
    if (nextScope !== this.scopeKey || nextTarget !== this.targetKey) {
      if (nextScope !== this.scopeKey) this.cancelAll('scope_changed');
      this.stopVoice('voice_ready', true).catch(() => undefined); this.scopeKey = nextScope; this.targetKey = nextTarget;
    }
    this.sync().catch(() => this.status = 'offline');
    this.refreshVoice().catch(() => this.voiceStatus = 'voice_unavailable');
  }

  receive(text: string, source: ClientModel | undefined = this.model): void {
    let data: Record<string, Object>;
    try { data = Wire.parse(text); } catch (_) { return; }
    if (Wire.text(data, 'type') !== 'tool_call') return;
    const callId = Wire.text(data, 'call_id'), name = Wire.text(data, 'name');
    if (!callId || !name) return;
    const model = this.model;
    if (!model || source !== model || !model.connected || model.demo) return;
    const scope = model.scope();
    if (scope !== this.scopeKey) return;
    let transport: HubTransport;
    try { transport = model.repo().transport; } catch (_) { return; }
    const owner: CallOwner = { model: model, scope: scope, transport: transport };
    const args = Wire.object(data, 'args');
    this.handleCall(callId, name, args, owner).catch(() => this.sendResult(callId,
      resultText('The phone action failed.', true), owner));
  }

  receiveBinary(_payload: ArrayBuffer): void {
    // Voice audio belongs to its authenticated extension stream, not the app socket.
  }

  async requestAccess(id: string): Promise<void> {
    const permission = id === 'contacts' ? CONTACTS : id === 'contacts_write' ? CONTACTS_WRITE : id === 'calendar_read' ? CALENDAR_READ :
      id === 'calendar_write' ? CALENDAR_WRITE : id === 'location' ? LOCATION : id === 'clipboard_read' ? PASTEBOARD_READ : '';
    const context = this.abilityContext;
    if (!permission || !context) return;
    const status = this.permissionStatus(permission);
    try {
      if (status === 'not_determined') await abilityAccessCtrl.createAtManager().requestPermissionsFromUser(
        context, [permission] as Permissions[]);
      else if (status === 'denied') await abilityAccessCtrl.createAtManager().requestPermissionOnSetting(context, [permission] as Permissions[]);
      else if (status === 'invalid') this.status = 'permission_unavailable';
    } catch (_) { this.status = 'permission_unavailable'; }
    this.refreshPermissions(); await this.sync();
  }

  async foreground(): Promise<void> {
    this.foregrounded = true; this.refreshPermissions(); await this.sync(); await this.refreshVoice();
  }

  async background(): Promise<void> {
    this.foregrounded = false; this.cancelAll('background'); await this.stopVoice('voice_ready', true);
    if (this.foregrounded) return;
    this.refreshPermissions(); await this.sync();
  }

  hide(): void { this.stopVoice('voice_ready').catch(() => undefined); }

  interrupt(): void {
    if (!this.voiceActive) return;
    if (this.voiceReady) this.voiceTransport?.send(JSON.stringify(Wire.payload(['type'], ['interrupt'])));
    this.voiceAudio.clear();
  }

  canStartVoice(): boolean {
    const target = this.voiceTarget();
    return !this.voiceActive && !this.voiceStarting && this.foregrounded && !!this.model?.connected && !this.model.demo &&
      !!this.voiceExtensionId && !!target && ['voice_ready', 'voice_idle', 'voice_microphone_denied',
        'voice_microphone_unavailable', 'voice_audio_unavailable', 'voice_connection_failed',
        'voice_timeout', 'voice_disconnected'].includes(this.voiceStatus);
  }

  async startVoice(): Promise<void> {
    const model = this.model, context = this.abilityContext;
    if (this.voiceActive || this.voiceStarting || !model || !context || !this.foregrounded || !model.connected || model.demo) return;
    if (!this.voiceExtensionId) await this.refreshVoice();
    if (!model.connected || model.demo || model !== this.model || !this.voiceExtensionId ||
      !['voice_ready', 'voice_connection_failed', 'voice_timeout', 'voice_disconnected',
        'voice_audio_unavailable', 'voice_microphone_denied', 'voice_microphone_unavailable'].includes(this.voiceStatus)) return;
    const extensionId = this.voiceExtensionId;
    const protocol = this.voiceProtocol;
    if (protocol !== 1 && protocol !== 2) return;
    const target = this.voiceTarget(protocol);
    if (!target) { this.voiceStatus = 'voice_target_unavailable'; return; }
    const scope = model.scope(), targetKey = model.target();
    const identity = JSON.stringify([scope, targetKey, extensionId]);
    const startVisit = ++this.voiceStartVisit; this.voiceStarting = true; this.voiceDiscoveryGeneration++;
    try {
    const permission = this.permissionStatus(MICROPHONE);
    if (permission === 'not_determined') {
      try { await abilityAccessCtrl.createAtManager().requestPermissionsFromUser(context, [MICROPHONE] as Permissions[]); }
      catch (_) { this.voiceStatus = 'voice_microphone_denied'; return; }
      if (this.permissionStatus(MICROPHONE) !== 'granted') { this.voiceStatus = 'voice_microphone_denied'; return; }
    } else if (permission === 'denied') {
      try { await abilityAccessCtrl.createAtManager().requestPermissionOnSetting(context, [MICROPHONE] as Permissions[]); }
      catch (_) { this.voiceStatus = 'voice_microphone_denied'; return; }
      if (this.permissionStatus(MICROPHONE) !== 'granted') { this.voiceStatus = 'voice_microphone_denied'; return; }
    } else if (permission !== 'granted') {
      this.voiceStatus = 'voice_microphone_unavailable'; return;
    }
    if (startVisit !== this.voiceStartVisit || protocol !== this.voiceProtocol || !this.voiceTarget(protocol) ||
      !this.foregrounded || model !== this.model || !model.connected || model.demo || model.scope() !== scope ||
      model.target() !== targetKey || scope !== this.scopeKey || targetKey !== this.targetKey || extensionId !== this.voiceExtensionId) return;

    this.voiceCallProtocol = protocol;
    const generation = ++this.voiceCallGeneration;
    this.voiceOwnerModel = model;
    this.voiceIdentity = identity;
    this.voiceExpectedTarget = target;
    this.voiceReady = false;
    this.voiceEverReady = false;
    this.voiceResumePending = false;
    this.voiceResumeAvailable = (this.voiceActive ? this.voiceCallProtocol : this.voiceProtocol) === 2 && this.voiceFeatures.includes('resume');
    this.voiceRetryAttempt = 0;
    this.voiceActive = true;
    this.voiceMuted = false;
    this.voiceAudio.setMuted(false);
    this.voiceAudioStarting = false;
    this.voiceAudioStarted = false;
    this.voiceCaptions = [];
    this.voiceRuns = [];
    this.voiceAskingId = '';
    this.voiceNotice = '';
    this.voiceStatus = 'voice_connecting';
    await this.openVoiceConnection(generation, identity, target, false);
    } finally { if (startVisit === this.voiceStartVisit) this.voiceStarting = false; }
  }

  async endVoice(): Promise<void> { await this.stopVoice('voice_ready'); }

  setVoiceMuted(muted: boolean): void {
    if (!this.voiceActive) return;
    this.voiceMuted = muted;
    this.voiceAudio.setMuted(muted);
  }

  private voiceTarget(protocol: number = this.voiceActive ? this.voiceCallProtocol : this.voiceProtocol): Record<string, Object> | undefined {
    const model = this.model, row = model?.selected;
    if (!model || !row || !['session', 'conversation'].includes(row.kind)) return undefined;
    const machine = row.machine, session = row.session || row.id;
    if (!machine || !session || protocol === 1 && (row.kind !== 'conversation' || row.id !== model.mainId)) return undefined;
    return Wire.payload(['machine_id', 'session_id'], [machine, session]);
  }

  private voiceCallCurrent(generation: number, identity: string): boolean {
    const model = this.model;
    return generation === this.voiceCallGeneration && this.voiceActive && !!model && model === this.voiceOwnerModel &&
      model.scope() === this.scopeKey && model.target() === this.targetKey && identity === this.voiceIdentity &&
      identity === JSON.stringify([model.scope(), model.target(), this.voiceExtensionId]) && this.foregrounded;
  }

  private voiceCurrent(generation: number, connectionGeneration: number, identity: string): boolean {
    return connectionGeneration === this.voiceConnectionGeneration && this.voiceCallCurrent(generation, identity);
  }

  private async openVoiceConnection(generation: number, identity: string, target: Record<string, Object>, resume: boolean): Promise<void> {
    if (!this.voiceCallCurrent(generation, identity)) return;
    const model = this.voiceOwnerModel;
    if (!model) return;
    const connectionGeneration = ++this.voiceConnectionGeneration;
    let stream: HubTransport | undefined;
    let socketOpen = false;
    this.voiceReady = false;
    this.voiceStatus = resume ? 'voice_reconnecting' : 'voice_connecting';
    try {
      stream = model.repo().transport.stream('/api/v1/extensions/' + encodeURIComponent(this.voiceExtensionId) + '/ws');
      this.voiceTransport = stream;
      await stream.connect((payload: string) => {
        if (this.voiceCurrent(generation, connectionGeneration, identity)) {
          this.receiveVoiceFrame(payload, generation, connectionGeneration, identity, target);
        }
      }, (state: string) => {
        if (state === 'ready') { socketOpen = true; return; }
        if (socketOpen && this.voiceCurrent(generation, connectionGeneration, identity)) {
          this.handleVoiceDisconnect(generation, connectionGeneration, identity, target, stream, undefined).catch(() => undefined);
        }
      }, (payload: ArrayBuffer) => {
        if (this.voiceCurrent(generation, connectionGeneration, identity) && this.voiceReady) this.voiceAudio.enqueue(payload);
      });
      if (!this.voiceCurrent(generation, connectionGeneration, identity)) { stream.close(); return; }
      if (!stream.send(JSON.stringify(this.voiceStartFrame(target, resume)))) throw new Error('voice_start_not_queued');
      this.voiceReadyTimer = setTimeout(() => {
        if (this.voiceCurrent(generation, connectionGeneration, identity) && !this.voiceReady) {
          this.handleVoiceDisconnect(generation, connectionGeneration, identity, target, stream, undefined).catch(() => undefined);
        }
      }, 15000);
    } catch (error) {
      if (this.voiceCurrent(generation, connectionGeneration, identity)) {
        await this.handleVoiceDisconnect(generation, connectionGeneration, identity, target, stream, error as Error);
      }
    }
  }

  private voiceStartFrame(target: Record<string, Object>, resume: boolean): Record<string, Object> {
    // Legacy voice always addresses this manager's main chat and has no target/resume frames.
    if (this.voiceCallProtocol === 1) return Wire.payload(['type'], ['start']);
    const start = Wire.payload(['type', 'protocol', 'target'], ['start', 2, target]);
    const recent = this.recentVoiceLines();
    if (recent.length > 0) start['recent'] = recent;
    if (resume && this.voiceResumeAvailable) {
      const history = this.voiceCaptions.filter((line: DeviceVoiceLine) => !!line.text.trim()).slice(-VOICE_HISTORY_LIMIT)
        .map((line: DeviceVoiceLine): Object => Wire.payload(['role', 'text'], [line.role, line.text]));
      const runs = this.voiceRuns.filter((run: DeviceVoiceRun) => run.isOpen()).slice(-VOICE_RESUME_RUN_LIMIT)
        .map((run: DeviceVoiceRun): Object => run.resumeFrame());
      if (history.length > 0) start['history'] = history;
      if (runs.length > 0) start['resume'] = Wire.payload(['runs'], [runs]);
    }
    return start;
  }

  private async handleVoiceDisconnect(generation: number, connectionGeneration: number, identity: string,
    target: Record<string, Object>, stream: HubTransport | undefined, error: Error | undefined): Promise<void> {
    if (!this.voiceCurrent(generation, connectionGeneration, identity) || this.voiceTransport !== stream) return;
    if (this.voiceReadyTimer !== -1) clearTimeout(this.voiceReadyTimer);
    if (this.voiceStableTimer !== -1) clearTimeout(this.voiceStableTimer);
    this.voiceReadyTimer = -1;
    this.voiceStableTimer = -1;
    this.voiceConnectionGeneration++;
    this.voiceTransport = undefined;
    this.voiceReady = false;
    this.voiceAudioStarting = false;
    this.voiceAudioStarted = false;
    this.voiceAskingId = '';
    this.voiceAudio.clear();
    stream?.close();
    await this.voiceAudio.stop();
    if (!this.voiceCallCurrent(generation, identity)) return;

    const failure = error instanceof HttpFailure ? error : undefined;
    if (failure && (failure.status === 401 || failure.status === 403 || (failure.status === 404 && !this.voiceEverReady))) {
      await this.stopVoice('voice_connection_failed');
      return;
    }
    if (this.voiceEverReady && !this.voiceResumeAvailable) {
      await this.stopVoice('voice_disconnected');
      return;
    }
    if (this.voiceRetryAttempt >= VOICE_RETRY_DELAYS.length) {
      await this.stopVoice('voice_disconnected');
      return;
    }
    const delay = VOICE_RETRY_DELAYS[this.voiceRetryAttempt++];
    this.voiceResumePending = this.voiceEverReady && this.voiceResumeAvailable;
    this.voiceStatus = 'voice_reconnecting';
    this.voiceRetryTimer = setTimeout(() => {
      this.voiceRetryTimer = -1;
      if (!this.voiceCallCurrent(generation, identity)) return;
      const expected = this.voiceExpectedTarget;
      if (!expected) { this.stopVoice('voice_target_unavailable').catch(() => undefined); return; }
      this.openVoiceConnection(generation, identity, expected, this.voiceResumePending).catch(() => undefined);
    }, delay);
  }

  private recentVoiceLines(): Record<string, Object>[] {
    const model = this.model;
    if (!model) return [];
    const lines: Record<string, Object>[] = [];
    for (const message of model.messages) {
      if (message.role !== 'user' && message.role !== 'assistant') continue;
      const text = messageDisplay(message.text).text.replace(/\s+/g, ' ').trim();
      if (!text) continue;
      lines.push(Wire.payload(['role', 'text'], [message.role, text.length > 300 ? text.slice(0, 299) + '…' : text]));
    }
    const userLines: number[] = [];
    lines.forEach((line: Record<string, Object>, index: number) => { if (Wire.text(line, 'role') === 'user') userLines.push(index); });
    if (userLines.length > 3) lines.splice(0, userLines[userLines.length - 3]);
    return lines.slice(-12);
  }

  private async stopVoice(status: string, clearConversation: boolean = false): Promise<void> {
    if (this.voiceReadyTimer !== -1) clearTimeout(this.voiceReadyTimer);
    if (this.voiceStableTimer !== -1) clearTimeout(this.voiceStableTimer);
    if (this.voiceRetryTimer !== -1) clearTimeout(this.voiceRetryTimer);
    this.voiceReadyTimer = -1;
    this.voiceStableTimer = -1;
    this.voiceRetryTimer = -1;
    this.voiceStartVisit++; this.voiceStarting = false; this.voiceCallProtocol = 0;
    this.voiceCallGeneration++;
    this.voiceConnectionGeneration++;
    this.voiceReady = false;
    this.voiceIdentity = '';
    this.voiceOwnerModel = undefined;
    this.voiceExpectedTarget = undefined;
    this.voiceResumeAvailable = false;
    this.voiceEverReady = false;
    this.voiceResumePending = false;
    this.voiceRetryAttempt = 0;
    this.voiceFeatures = []; this.voiceTools.clear(); this.voiceToolLabel = '';
    this.voiceAudioStarting = false;
    this.voiceAudioStarted = false;
    this.voiceAskingId = '';
    const stream = this.voiceTransport;
    this.voiceTransport = undefined;
    this.voiceActive = false;
    this.voiceMuted = false;
    if (this.voiceExtensionId) this.voiceStatus = status;
    else this.voiceStatus = 'voice_unavailable';
    this.voiceAudio.clear();
    stream?.close();
    if (clearConversation) {
      this.voiceRuns = [];
      this.voiceCaptions = [];
      this.voiceNotice = '';
    }
    await this.voiceAudio.stop();
  }

  private receiveVoiceFrame(payload: string, generation: number, connectionGeneration: number,
    identity: string, expected: Record<string, Object>): void {
    let frame: Record<string, Object>;
    try { frame = Wire.parse(payload); } catch (_) { return; }
    const type = Wire.text(frame, 'type');
    if (type === 'ready') {
      const target = Wire.object(frame, 'target');
      const protocol = Wire.number(frame, 'protocol', 1);
      const hasTarget = Object.keys(target).length > 0;
      if (protocol !== this.voiceCallProtocol || Wire.has(frame, 'protocol') && typeof frame['protocol'] !== 'number' ||
        (protocol === 2 || hasTarget) && (Wire.text(target, 'machine_id') !== Wire.text(expected, 'machine_id') ||
        Wire.text(target, 'session_id') !== Wire.text(expected, 'session_id'))) {
        this.stopVoice('voice_protocol_unsupported').catch(() => undefined);
        return;
      }
      const inputRate = Wire.number(frame, 'input_rate', 16000), outputRate = Wire.number(frame, 'output_rate', 24000);
      if (!Number.isInteger(inputRate) || !Number.isInteger(outputRate) || inputRate < 8000 || inputRate > 96000 || outputRate < 8000 || outputRate > 96000) {
        this.stopVoice('voice_audio_unavailable').catch(() => undefined);
        return;
      }
      if (this.voiceReady) return;
      if (this.voiceReadyTimer !== -1) clearTimeout(this.voiceReadyTimer);
      this.voiceReadyTimer = -1;
      this.voiceReady = true;
      this.voiceEverReady = true;
      if (this.voiceStableTimer !== -1) clearTimeout(this.voiceStableTimer);
      this.voiceStableTimer = setTimeout(() => {
        this.voiceStableTimer = -1;
        if (this.voiceCurrent(generation, connectionGeneration, identity) && this.voiceReady) this.voiceRetryAttempt = 0;
      }, VOICE_STABLE_MS);
      this.voiceResumePending = false;
      this.voiceFeatures = protocol === 2 ? Wire.strings(frame, 'features') : [];
      this.voiceResumeAvailable = (this.voiceActive ? this.voiceCallProtocol : this.voiceProtocol) === 2 && this.voiceFeatures.includes('resume');
      this.voiceInputRate = inputRate;
      this.voiceOutputRate = outputRate;
      this.voiceStatus = 'voice_starting_audio';
      this.voiceAudioStarting = true;
      this.voiceAudio.setPlaybackRate(outputRate);
      this.voiceAudio.start(inputRate, outputRate, (data: ArrayBuffer) => {
        if (this.voiceCurrent(generation, connectionGeneration, identity) && this.voiceReady) this.voiceTransport?.sendBinary(data);
      }).then(() => {
        if (!this.voiceCurrent(generation, connectionGeneration, identity)) return;
        this.voiceAudioStarting = false;
        this.voiceAudioStarted = true;
        if (this.voiceReady) this.voiceStatus = 'voice_listening';
      }).catch(() => {
        if (this.voiceCurrent(generation, connectionGeneration, identity)) this.stopVoice('voice_audio_unavailable').catch(() => undefined);
      });
      return;
    }
    if (type === 'clear') { this.voiceAudio.clear(); return; }
    if (type === 'transcript') { this.updateVoiceCaption(frame, connectionGeneration); return; }
    if (type === 'tool') {
      const id = Wire.text(frame, 'id');
      if (!id) return;
      if (Wire.bool(frame, 'done')) this.voiceTools.delete(id);
      else if (this.voiceTools.has(id) || this.voiceTools.size < 16) this.voiceTools.set(id, Wire.text(frame, 'label', tr('working')).slice(0, 256));
      const labels = Array.from(this.voiceTools.values()); this.voiceToolLabel = labels.length ? labels[labels.length - 1] : '';
      return;
    }
    if (this.voiceCallProtocol === 1 && ['run', 'ask', 'link'].includes(type)) return;
    if (type === 'run') { this.updateVoiceRun(frame, connectionGeneration); return; }
    if (type === 'ask') { this.updateVoiceAsk(frame); return; }
    if (type === 'link') {
      const state = Wire.text(frame, 'state');
      if (state === 'paused') this.voiceStatus = 'voice_paused';
      else if (state === 'waking') this.voiceStatus = 'voice_waking';
      else if (state === 'reconnecting') this.voiceStatus = 'voice_reconnecting';
      else if (state === 'live' && this.voiceReady) this.voiceStatus = 'voice_listening';
      return;
    }
    if (type === 'error') {
      this.voiceNotice = Wire.text(frame, 'message');
      if (Wire.bool(frame, 'fatal')) this.stopVoice('voice_connection_failed').catch(() => undefined);
      else this.voiceStatus = 'voice_error';
    }
  }

  private updateVoiceCaption(frame: Record<string, Object>, connectionGeneration: number): void {
    const id = Wire.text(frame, 'id'), role = Wire.text(frame, 'role'), text = Wire.text(frame, 'text');
    if (!id || !['user', 'assistant'].includes(role)) return;
    const key = String(connectionGeneration) + ':' + id;
    const existing = this.voiceCaptions.find((line: DeviceVoiceLine) => line.id === key);
    if (existing) {
      if (existing.final) return;
      existing.text = text;
      existing.final = Wire.bool(frame, 'final');
      return;
    }
    if (!text.trim()) return;
    const line = new DeviceVoiceLine(key, role, text, Wire.bool(frame, 'final'));
    const before = Wire.text(frame, 'before');
    const position = this.voiceCallProtocol === 2 && before ? this.voiceCaptions.findIndex((item: DeviceVoiceLine) => item.id === String(connectionGeneration) + ':' + before) : -1;
    if (position >= 0) this.voiceCaptions.splice(position, 0, line);
    else this.voiceCaptions.push(line);
    if (this.voiceCaptions.length > VOICE_HISTORY_LIMIT) this.voiceCaptions.splice(0, this.voiceCaptions.length - VOICE_HISTORY_LIMIT);
  }

  private updateVoiceRun(frame: Record<string, Object>, connectionGeneration: number): void {
    const id = Wire.text(frame, 'id');
    if (!id) return;
    const rawState = Wire.text(frame, 'state');
    const state = VOICE_RUN_STATES.includes(rawState) ? rawState : 'unknown';
    const startedValue = frame['started_at'];
    const startedAt: Object = typeof startedValue === 'string' && startedValue.length > 0 ? startedValue as Object :
      typeof startedValue === 'number' && Number.isFinite(startedValue as number) ? startedValue as Object : '';
    const base = Math.max(0, Math.floor(Wire.number(frame, 'base_seq')));
    const values: DeviceVoiceRunUpdate = {
      task: Wire.text(frame, 'task'), state: state, msgIds: Wire.strings(frame, 'msg_ids'),
      baseSeq: base, startedAt: startedAt, code: Wire.text(frame, 'code'), message: Wire.text(frame, 'message'),
      draft: Wire.text(frame, 'draft'), clearable: Wire.text(frame, 'clearable')
    };
    const existing = this.voiceRuns.find((run: DeviceVoiceRun) => run.id === id);
    if (existing) {
      existing.task = values.task; existing.state = values.state; existing.msgIds = values.msgIds;
      existing.baseSeq = values.baseSeq; existing.startedAt = values.startedAt; existing.code = values.code;
      existing.message = values.message; existing.draft = values.draft; existing.clearable = values.clearable;
      existing.seenGeneration = connectionGeneration;
      return;
    }
    this.voiceRuns.push(new DeviceVoiceRun(id, values.task, values.state, values.msgIds, values.baseSeq,
      values.startedAt, values.code, values.message, values.draft, values.clearable));
    this.voiceRuns[this.voiceRuns.length - 1].seenGeneration = connectionGeneration;
    if (this.voiceRuns.length > VOICE_RUN_LIMIT) {
      const completed = this.voiceRuns.findIndex((run: DeviceVoiceRun) => !run.isOpen());
      this.voiceRuns.splice(completed >= 0 ? completed : 0, 1);
    }
  }

  private updateVoiceAsk(frame: Record<string, Object>): void {
    const id = Wire.text(frame, 'interaction_id'), state = Wire.text(frame, 'state');
    if (!id) return;
    if (state === 'asking') {
      this.voiceAskingId = id;
      const model = this.model;
      if (model && model.connected && model.isChat()) model.refreshDecisions().catch(() => undefined);
      return;
    }
    if ((state === 'answered' || state === 'released') && this.voiceAskingId === id) {
      this.voiceAskingId = '';
      const by = Wire.text(frame, 'by');
      this.voiceNotice = by === 'elsewhere' ? tr('voice_ask_released_elsewhere') : '';
      const model = this.model;
      if (model && model.connected && model.isChat()) model.refreshDecisions().catch(() => undefined);
    }
  }

  permissionRows(): DevicePermissionRow[] { return this.permissions; }

  private permissionStatus(permission: string): string {
    try {
      const state = abilityAccessCtrl.createAtManager().getSelfPermissionStatus(permission as Permissions);
      if (state === abilityAccessCtrl.PermissionStatus.GRANTED) return 'granted';
      if (state === abilityAccessCtrl.PermissionStatus.NOT_DETERMINED) return 'not_determined';
      if (state === abilityAccessCtrl.PermissionStatus.DENIED || state === abilityAccessCtrl.PermissionStatus.RESTRICTED) return 'denied';
    } catch (_) { }
    return 'invalid';
  }

  private refreshPermissions(): void {
    this.permissions = [
      new DevicePermissionRow('contacts', 'contacts', CONTACTS, this.permissionStatus(CONTACTS)),
      new DevicePermissionRow('contacts_write', 'contacts_write', CONTACTS_WRITE, this.permissionStatus(CONTACTS_WRITE)),
      new DevicePermissionRow('calendar_read', 'calendar', CALENDAR_READ, this.permissionStatus(CALENDAR_READ)),
      new DevicePermissionRow('calendar_write', 'calendar', CALENDAR_WRITE, this.permissionStatus(CALENDAR_WRITE)),
      new DevicePermissionRow('location', 'location', LOCATION, this.permissionStatus(LOCATION)),
      new DevicePermissionRow('clipboard_read', 'clipboard', PASTEBOARD_READ, this.permissionStatus(PASTEBOARD_READ))
    ];
  }

  private usable(tool: DeviceToolDefinition): boolean {
    if (tool.foregroundOnly && !this.foregrounded) return false;
    if (tool.name === 'phone_location_search' && !canIUse('SystemCapability.Location.Location.Geocoder')) return false;
    for (const permission of tool.permissions) {
      if (this.permissionStatus(permission) !== 'granted') return false;
    }
    return true;
  }

  private owns(owner: CallOwner): boolean {
    const model = this.model;
    if (model !== owner.model || !model.connected || model.demo || this.scopeKey !== owner.scope || model.scope() !== owner.scope) return false;
    try { return model.repo().transport === owner.transport; } catch (_) { return false; }
  }

  private async sync(): Promise<void> {
    const model = this.model;
    if (!model) return;
    const scope = model.scope();
    if (!model.connected || model.demo || scope !== this.scopeKey) {
      this.status = 'offline'; this.toolCount = 0; return;
    }
    let transport: HubTransport;
    try { transport = model.repo().transport; } catch (_) { this.status = 'offline'; this.toolCount = 0; return; }
    const owner: CallOwner = { model: model, scope: scope, transport: transport };
    const task = this.syncing.then(async () => {
      if (!this.owns(owner)) return;
      const id = await this.deviceId();
      if (!this.owns(owner)) return;
      const selected = this.catalog.filter((tool: DeviceToolDefinition) => this.usable(tool));
      const tools = selected.map((tool: DeviceToolDefinition): Object => tool.wire());
      const modelName = deviceInfo.productModel || deviceInfo.softwareModel || '';
      const series = deviceInfo.productSeries || '';
      const simulator = /emulator|simulator/i.test(modelName + ' ' + series);
      const sent = transport.send(JSON.stringify(Wire.payload(
        ['type', 'device_id', 'platform', 'device_name', 'simulator', 'tools'],
        ['register_phone', id, 'harmony', modelName || deviceInfo.brand || 'HarmonyOS phone', simulator, tools])));
      if (!this.owns(owner)) return;
      if (sent) { this.registeredTransport = transport; this.registeredDeviceId = id; }
      this.registrationKey = JSON.stringify([scope, id]);
      this.toolCount = sent ? selected.length : 0;
      this.status = sent ? 'connected' : 'offline';
    }).catch(() => {
      if (this.owns(owner)) { this.status = 'offline'; this.toolCount = 0; }
    });
    this.syncing = task;
    await task;
  }

  private async deviceId(): Promise<string> {
    const context = this.abilityContext;
    if (!context) throw new Error('storage_unavailable');
    const store = await preferences.getPreferences(context, 'persona.device-features');
    const existing = store.getSync('device_id', '') as string;
    if (existing && /^[a-f0-9-]{36}$/i.test(existing)) return existing;
    const id = util.generateRandomUUID();
    store.putSync('device_id', id); await store.flush();
    return id;
  }

  private async refreshVoice(): Promise<void> {
    const model = this.model;
    if (this.voiceActive || this.voiceStarting) return;
    const discovery = ++this.voiceDiscoveryGeneration;
    const scope = model?.scope() || '', target = model?.target() || '';
    if (!model?.connected || model.demo) {
      this.voiceExtensionId = ''; this.voiceFeatures = []; this.voiceResumeAvailable = false;
      this.voiceStatus = 'voice_unavailable'; return;
    }
    try {
      const repo = model.repo();
      const response = await repo.call('/api/v1/extensions');
      if (this.voiceActive || this.voiceStarting || discovery !== this.voiceDiscoveryGeneration || this.model !== model || !model.connected || model.demo ||
        model.scope() !== scope || model.target() !== target) return;
      const rows = Wire.rows(response, 'extensions');
      const voice = rows.find((row: Record<string, Object>) => Wire.text(row, 'kind') === 'voice' && Wire.text(row, 'status') === 'running');
      if (!voice) {
        this.voiceExtensionId = ''; this.voiceFeatures = []; this.voiceResumeAvailable = false;
        this.voiceStatus = 'voice_unavailable'; return;
      }
      const id = Wire.text(voice, 'id');
      if (!id || !/^[A-Za-z0-9._-]{1,128}$/.test(id)) {
        this.voiceExtensionId = ''; this.voiceFeatures = []; this.voiceResumeAvailable = false;
        this.voiceStatus = 'voice_unavailable'; return;
      }
      this.voiceExtensionId = id;
      let info: Record<string, Object>;
      try { info = await repo.call('/api/v1/extensions/' + encodeURIComponent(id) + '/info'); }
      catch (error) {
        if (!(error instanceof HttpFailure) || error.status !== 404) throw error as Error;
        info = Wire.payload(['protocol'], [1]);
      }
      if (this.voiceActive || this.voiceStarting || discovery !== this.voiceDiscoveryGeneration || this.model !== model || !model.connected || model.demo ||
        model.scope() !== scope || model.target() !== target) return;
      const highest = Wire.number(info, 'protocol');
      if (!Number.isSafeInteger(highest) || highest < 1) { this.voiceProtocol = 0; this.voiceStatus = 'voice_protocol_unsupported'; return; }
      // /info advertises its highest version. Opt into a version this client implements.
      this.voiceProtocol = highest >= 2 ? 2 : 1;
      this.voiceFeatures = this.voiceProtocol === 2 ? Wire.strings(info, 'features') : [];
      this.voiceResumeAvailable = (this.voiceActive ? this.voiceCallProtocol : this.voiceProtocol) === 2 && this.voiceFeatures.includes('resume');
      if (Wire.bool(info, 'needs_setup') || Wire.text(info, 'setup') === 'required') { this.voiceStatus = 'voice_needs_setup'; return; }
      this.voiceStatus = this.voiceTarget() ? 'voice_ready' : this.voiceProtocol === 1 ? 'voice_main_only' : 'voice_target_unavailable';
    } catch (_) {
      if (discovery === this.voiceDiscoveryGeneration && this.model === model && model.scope() === scope && model.target() === target) {
        this.voiceExtensionId = ''; this.voiceFeatures = []; this.voiceResumeAvailable = false;
        this.voiceStatus = 'voice_unavailable';
      }
    }
  }

  private sameOwner(left: CallOwner | undefined, right: CallOwner): boolean {
    return !!left && left.model === right.model && left.scope === right.scope && left.transport === right.transport;
  }

  private async handleCall(callId: string, name: string, args: Record<string, Object>, owner: CallOwner): Promise<void> {
    if (!this.owns(owner)) return;
    const key = JSON.stringify([owner.scope, callId]);
    const cached = this.completed.get(key);
    if (cached) {
      if (this.sameOwner(cached.owner, owner)) this.sendResult(callId, cached.result, owner);
      return;
    }
    const existing = this.callPending.get(key);
    if (existing) {
      if (this.sameOwner(this.callOwners.get(key), owner)) this.sendResult(callId, await existing, owner);
      return;
    }
    if (!this.foregrounded) {
      this.sendResult(callId, resultText('Persona entered the background before the phone action started. Nothing was run.', true), owner);
      return;
    }

    const call: RunningCall = { cancelled: false, expired: false, owner: owner, cancel: undefined, confirmCancel: undefined };
    this.callOwners.set(key, owner);
    this.callState.set(key, call);
    const promise = this.runWithDeadline(key, call, name, args);
    this.callPending.set(key, promise);
    const result = await promise;
    if (this.callPending.get(key) === promise) this.callPending.delete(key);
    this.callState.delete(key);
    this.remember(key, owner, result);
    if (this.foregrounded) this.sendResult(callId, result, owner);
  }

  private async runWithDeadline(key: string, call: RunningCall, name: string, args: Record<string, Object>): Promise<DeviceResult> {
    let deadline: number = -1;
    const expired = new Promise<DeviceResult>((resolve) => {
      deadline = setTimeout(() => {
        call.expired = true; call.confirmCancel?.();
        resolve(resultText('The phone action timed out after 90 seconds. Check whether it completed before trying again.', true));
      }, TOOL_TIMEOUT_MS);
    });
    const cancelled = new Promise<DeviceResult>((resolve) => { call.cancel = () => resolve(resultText('The phone action was stopped because Persona left the foreground. Nothing was queued or replayed.', true)); });
    const work = this.runTool(call, name, args).then((value: DeviceResult) => value).catch((error: Error) => resultText(error.message || 'Phone action failed.', true));
    const result = await Promise.race([work, expired, cancelled]);
    if (deadline !== -1) clearTimeout(deadline);
    call.cancel = undefined; call.confirmCancel = undefined;
    return result;
  }

  private async runTool(call: RunningCall, name: string, args: Record<string, Object>): Promise<DeviceResult> {
    const tool = this.catalog.find((item: DeviceToolDefinition) => item.name === name);
    if (!tool) return resultText('Unknown phone tool: ' + name + '.', true);
    if (tool.foregroundOnly && !this.foregrounded) return resultText('This phone action is only available while Persona is on screen.', true);
    for (const permission of tool.permissions) {
      if (this.permissionStatus(permission) === 'not_determined') {
        await this.requestNativePermission(permission);
        this.refreshPermissions(); await this.sync();
      }
      if (call.cancelled || call.expired) return resultText('The phone action ended before permission was granted. Nothing was run.', true);
      if (this.permissionStatus(permission) !== 'granted') return resultText('Permission is unavailable or denied: ' + permission + '. Review Persona in system permission settings.', true);
    }
    if (call.cancelled || call.expired) return resultText('The phone action ended before it started.', true);
    let plan: NativePhoneWritePlan | undefined;
    if (tool.write) {
      const context = this.abilityContext;
      if (!context) return resultText('The phone window is unavailable.', true);
      if (name === 'phone_contacts_create' || name === 'phone_contacts_update') plan = await prepareContactWrite(context, args, name === 'phone_contacts_update');
      else if (name === 'phone_calendar_update_event' || name === 'phone_calendar_delete_event') plan = await prepareCalendarWrite(context, args, name === 'phone_calendar_delete_event');
      if (call.cancelled || call.expired || !this.owns(call.owner)) return resultText('The phone action ended before confirmation.', true);
      if (!this.foregrounded || !this.confirmAction) return resultText('The phone cannot show a native confirmation right now. Nothing was changed.', true);
      const summary = plan ? [tr('phone_before'), JSON.stringify(plan.before, null, 2), tr('phone_after'), JSON.stringify(plan.after, null, 2)].join('\n') : '';
      const approved = await this.confirmWrite(call, tool, args, summary);
      if (!approved) return resultText(call.expired ? 'No approval arrived within 90 seconds. Nothing was changed.' : 'The user declined on the phone.', true);
    }
    if (call.cancelled || call.expired || !this.owns(call.owner)) return resultText('The phone action ended before it started.', true);
    if (plan) return jsonResult(await plan.execute((): boolean => !call.cancelled && !call.expired && this.foregrounded && this.owns(call.owner) && this.usable(tool)));
    switch (name) {
      case 'phone_location_search': return jsonResult(await searchNativePlaces(args));
      case 'phone_contacts_search': return await this.searchContacts(args);
      case 'phone_calendar_events': return await this.calendarEvents(args);
      case 'phone_calendar_create_event': return await this.createCalendarEvent(args, (): boolean => !call.cancelled && !call.expired && this.foregrounded && this.owns(call.owner) && this.usable(tool));
      case 'phone_location_current': return await this.currentLocation();
      case 'phone_device_info': return await this.deviceInfo();
      case 'phone_clipboard_get': return await this.readClipboard();
      case 'phone_clipboard_set': return await this.writeClipboard(args);
      default: return resultText('Unknown phone tool: ' + name + '.', true);
    }
  }

  private async requestNativePermission(permission: string): Promise<void> {
    const context = this.abilityContext;
    if (!context) throw new Error('Permission request is unavailable without an active phone window.');
    const current = this.permissionStatus(permission);
    if (current === 'granted') return;
    if (current === 'not_determined') {
      await abilityAccessCtrl.createAtManager().requestPermissionsFromUser(context, [permission] as Permissions[]);
      return;
    }
    if (current === 'denied') {
      await abilityAccessCtrl.createAtManager().requestPermissionOnSetting(context, [permission] as Permissions[]);
      return;
    }
    throw new Error('Permission is not declared or supported by this HarmonyOS build: ' + permission + '.');
  }

  private async confirmWrite(call: RunningCall, tool: DeviceToolDefinition, args: Record<string, Object>, summary: string = ''): Promise<boolean> {
    return new Promise<boolean>((resolve) => {
      let settled = false;
      const finish = (allowed: boolean): void => {
        if (settled) return;
        settled = true; call.confirmCancel = undefined; resolve(allowed);
      };
      call.confirmCancel = () => finish(false);
      const title = tr(tool.source);
      const detail = summary || (tool.name === 'phone_calendar_create_event' ?
        [optionalText(args, 'title'), optionalText(args, 'start'), optionalText(args, 'end'), optionalText(args, 'location'), optionalText(args, 'calendar')].filter((part: string) => part.length > 0).join('\n') :
        JSON.stringify({ clipboard_text: args['text'] }));
      this.confirmAction?.(title, detail, () => finish(true), () => finish(false));
    });
  }

  private async searchContacts(args: Record<string, Object>): Promise<DeviceResult> {
    const query = optionalText(args, 'query');
    const limit = integerValue(args, 'limit', 10, 1, 50);
    const context = this.abilityContext;
    if (!context) return resultText('Contacts are unavailable right now.', true);
    const all = await contact.queryContacts(context);
    const needle = query.toLocaleLowerCase();
    const digits = query.replace(/[^0-9]/g, '');
    const results: Record<string, Object>[] = [];
    for (const person of all) {
      const name = person.name?.fullName || '';
      const company = person.organization?.name || '';
      const job = person.organization?.title || '';
      const phones = (person.phoneNumbers || []).map((item: contact.PhoneNumber): Object =>
        Wire.payload(['label', 'number'], [item.labelName || 'other', item.phoneNumber]));
      const emails = (person.emails || []).map((item: contact.Email): Object =>
        Wire.payload(['label', 'email'], [item.labelName || 'other', item.email]));
      const searchable = [name, company, job].concat((person.phoneNumbers || []).map((item: contact.PhoneNumber) => item.phoneNumber),
        (person.emails || []).map((item: contact.Email) => item.email)).join(' ').toLocaleLowerCase();
      const phoneDigits = (person.phoneNumbers || []).map((item: contact.PhoneNumber) => item.phoneNumber.replace(/[^0-9]/g, '')).join(' ');
      if (query && !searchable.includes(needle) && !(digits.length >= 3 && phoneDigits.includes(digits))) continue;
      const birthday = (person.events || []).find((item: contact.Event) => item.labelId === contact.Event.EVENT_BIRTHDAY)?.eventDate || '';
      results.push(Wire.payload(['id', 'name', 'company', 'job_title', 'phones', 'emails', 'birthday'], [
        person.key || String(person.id || ''), name, company, job, phones, emails, birthday
      ]));
      if (results.length >= limit) break;
    }
    return jsonResult(Wire.payload(['query', 'count', 'results'], [query, results.length, results]));
  }

  private async calendarList(): Promise<calendarManager.Calendar[]> {
    const context = this.abilityContext;
    if (!context) throw new Error('Calendar is unavailable right now.');
    return calendarManager.getCalendarManager(context).getAllCalendars();
  }

  private async calendarEvents(args: Record<string, Object>): Promise<DeviceResult> {
    const now = new Date(), today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
    const start = dateArgument(optionalText(args, 'start'), today);
    const end = dateArgument(optionalText(args, 'end'), new Date(start.getTime() + 7 * 86400000));
    if (end.getTime() <= start.getTime()) return resultText('end must be after start.', true);
    const query = optionalText(args, 'query').toLocaleLowerCase();
    const limit = integerValue(args, 'limit', 50, 1, 200);
    const calendars = await this.calendarList();
    const found: Record<string, Object>[] = [];
    const filter = calendarManager.EventFilter.filterByTime(start.getTime(), end.getTime());
    for (const calendar of calendars) {
      const account = calendar.getAccount();
      const label = account.displayName || account.name || String(calendar.id);
      const events = await calendar.getEvents(filter, ['id', 'title', 'startTime', 'endTime', 'isAllDay', 'description', 'timeZone', 'location', 'attendee', 'recurrenceRule']);
      for (const event of events) {
        const location = event.location?.location || '';
        const notes = (event.description || '').slice(0, 300);
        const searchable = [event.title || '', location, notes].join(' ').toLocaleLowerCase();
        if (query && !searchable.includes(query)) continue;
        const attendees = (event.attendee || []).map((item: calendarManager.Attendee): Object => Wire.payload(['name', 'email'], [item.name || '', item.email || '']));
        found.push(Wire.payload(['id', 'title', 'start', 'end', 'all_day', 'calendar', 'location', 'notes', 'attendees', 'recurring'], [
          String(event.id || ''), event.title || '', new Date(event.startTime).toISOString(), new Date(event.endTime).toISOString(),
          !!event.isAllDay, label, location, notes, attendees, hasNativeRecurrence(event)
        ]));
        if (found.length >= limit) break;
      }
      if (found.length >= limit) break;
    }
    found.sort((a: Record<string, Object>, b: Record<string, Object>) => Date.parse(String(a['start'])) - Date.parse(String(b['start'])));
    return jsonResult(Wire.payload(['count', 'events'], [found.length, found]));
  }

  private async createCalendarEvent(args: Record<string, Object>, admit: () => boolean): Promise<DeviceResult> {
    const context = this.abilityContext;
    const title = optionalText(args, 'title'), startRaw = optionalText(args, 'start'), endRaw = optionalText(args, 'end');
    if (!context || !title || !startRaw || !endRaw) return resultText('title, start, and end are required.', true);
    const start = dateArgument(startRaw, new Date());
    const end = dateArgument(endRaw, new Date());
    if (end.getTime() <= start.getTime()) return resultText('end must be after start.', true);
    const manager = calendarManager.getCalendarManager(context);
    const requested = optionalText(args, 'calendar').toLocaleLowerCase();
    let selected: calendarManager.Calendar | undefined;
    if (requested) {
      const calendars = await manager.getAllCalendars();
      selected = calendars.find((calendar: calendarManager.Calendar) => {
        const account = calendar.getAccount();
        const values = [account.displayName || '', account.name || ''].map((value: string) => value.toLocaleLowerCase());
        return values.some((value: string) => value === requested || value.includes(requested));
      });
      if (!selected) return resultText('No matching calendar was found.', true);
    } else selected = await new Promise<calendarManager.Calendar>((resolve, reject) => {
      manager.getCalendar().then(resolve).catch(reject);
    });
    if (!selected) return resultText('The default calendar is unavailable.', true);
    const location = optionalText(args, 'location');
    const event: calendarManager.Event = {
      type: calendarManager.EventType.NORMAL,
      title: title,
      startTime: start.getTime(),
      endTime: end.getTime(),
      timeZone: systemDateTime.getTimezoneSync(),
      isAllDay: Wire.bool(args, 'all_day'),
      description: optionalText(args, 'notes')
    };
    if (location) event.location = { location: location };
    if (!admit()) return resultText('The phone action ended before the event was saved.', true);
    const id = await selected.addEvent(event);
    if (id <= 0) return resultText('The calendar did not confirm that the event was saved.', true);
    return jsonResult(Wire.payload(['ok', 'id'], [true, String(id)]));
  }

  private async currentLocation(): Promise<DeviceResult> {
    try {
      const location = await geoLocationManager.getCurrentLocation({ timeoutMs: 15000, maxAccuracy: 100 });
      let address: Record<string, Object> | undefined;
      try {
        const matches = await geoLocationManager.getAddressesFromLocation({ latitude: location.latitude, longitude: location.longitude, maxItems: 1, locale: i18n.getSystemLocale() });
        const place = matches[0];
        if (place) address = Wire.payload(['name', 'country', 'province', 'city', 'district', 'street', 'formatted'], [
          place.placeName || '', place.countryName || '', place.administrativeArea || '', place.locality || '',
          place.subLocality || '', place.roadName || '',
          [place.countryName || '', place.administrativeArea || '', place.locality || '', place.subLocality || '',
            place.roadName || '', place.premises || ''].filter((part: string) => part.length > 0).join(', ')
        ]);
      } catch (_) { }
      const result = Wire.payload(['latitude', 'longitude', 'accuracy_meters', 'altitude_meters', 'timestamp', 'timezone'], [
        location.latitude, location.longitude, location.accuracy, location.altitude,
        new Date(location.timeStamp).toISOString(), systemDateTime.getTimezoneSync()
      ]);
      if (address) result['address'] = address;
      return jsonResult(result);
    } catch (error) { return resultText('Location could not be obtained within 15 seconds: ' + (error as Error).message, true); }
  }

  private async deviceInfo(): Promise<DeviceResult> {
    const bundle = bundleManager.getBundleInfoForSelfSync(bundleManager.BundleFlag.GET_BUNDLE_INFO_WITH_APPLICATION);
    let storageFree: number | undefined;
    let storageTotal: number | undefined;
    try { storageFree = await storageStatistics.getFreeSize(); } catch (_) { }
    try { storageTotal = await storageStatistics.getTotalSize(); } catch (_) { }
    let network = 'offline';
    try {
      if (await connection.hasDefaultNet()) {
        const handle = await connection.getDefaultNet();
        const capability = await connection.getNetCapabilities(handle);
        if (capability.bearerTypes.includes(connection.NetBearType.BEARER_WIFI)) network = 'wifi';
        else if (capability.bearerTypes.includes(connection.NetBearType.BEARER_CELLULAR)) network = 'cellular';
        else if (capability.bearerTypes.includes(connection.NetBearType.BEARER_ETHERNET)) network = 'wired';
      }
    } catch (_) { network = 'unknown'; }
    const charging = batteryInfo.chargingStatus === batteryInfo.BatteryChargeState.ENABLE ? 'charging' :
      batteryInfo.chargingStatus === batteryInfo.BatteryChargeState.FULL ? 'full' :
      batteryInfo.chargingStatus === batteryInfo.BatteryChargeState.DISABLE ? 'unplugged' : 'unknown';
    return jsonResult(Wire.payload(['model', 'model_name', 'os_version', 'battery_percent', 'battery_state',
      'storage_free_bytes', 'storage_total_bytes', 'network', 'timezone', 'utc_offset_seconds', 'locale',
      'app_version', 'app_build'], [
      deviceInfo.productModel || deviceInfo.softwareModel || '', deviceInfo.productModelAlias || deviceInfo.productModel || '',
      deviceInfo.osFullName, batteryInfo.batterySOC, charging, storageFree || 0, storageTotal || 0,
      network, systemDateTime.getTimezoneSync(), -new Date().getTimezoneOffset() * 60, i18n.getSystemLocale(),
      bundle.versionName || '', String(bundle.versionCode)
    ]));
  }

  private async readClipboard(): Promise<DeviceResult> {
    if (!this.foregrounded) return resultText('Clipboard access requires Persona to be on screen.', true);
    const board: pasteboard.SystemPasteboard = pasteboard.getSystemPasteboard();
    const data: pasteboard.PasteData = await board.getData();
    const mime: string = data.getPrimaryMimeType();
    if (!mime.includes('text')) return resultText('{"text":null,"note":"The clipboard is empty or does not hold text."}');
    const text: string = data.getPrimaryText();
    return jsonResult(Wire.payload(['text', 'characters'], [text, text.length]));
  }

  private async writeClipboard(args: Record<string, Object>): Promise<DeviceResult> {
    if (!this.foregrounded) return resultText('Clipboard access requires Persona to be on screen.', true);
    if (typeof args['text'] !== 'string') return resultText('text is required.', true);
    const value = args['text'] as string;
    const board: pasteboard.SystemPasteboard = pasteboard.getSystemPasteboard();
    const before = board.getChangeCount();
    const data: pasteboard.PasteData = pasteboard.createData(pasteboard.MIMETYPE_TEXT_PLAIN, value);
    await board.setData(data);
    if (board.getChangeCount() === before) return resultText('The text did not reach the clipboard. Nothing was copied.', true);
    return jsonResult(Wire.payload(['ok', 'characters'], [true, value.length]));
  }

  private sendResult(callId: string, result: DeviceResult, owner: CallOwner): void {
    if (!this.owns(owner)) return;
    try {
      owner.transport.send(JSON.stringify(Wire.payload(['type', 'call_id', 'content', 'is_error'],
        ['tool_result', callId, result.content, result.is_error])));
    } catch (_) { }
  }

  private remember(key: string, owner: CallOwner, result: DeviceResult): void {
    this.completed.set(key, { owner: owner, result: result });
    if (this.completed.size > COMPLETED_LIMIT) {
      const oldest = this.completed.keys().next().value as string;
      this.completed.delete(oldest); this.callOwners.delete(oldest);
    }
  }

  private cancelAll(_reason: string): void {
    this.callState.forEach((call: RunningCall) => {
      call.cancelled = true; call.confirmCancel?.(); call.cancel?.();
    });
    this.stopVoice('voice_ready');
  }
}

export const deviceFeatures: DeviceFeatures = new DeviceFeatures();
