import {registerHooks, stripTypeScriptTypes} from 'node:module';
import {readFileSync} from 'node:fs';

globalThis.canIUse = () => true;

const PERMISSIONS = {
  contacts: 'ohos.permission.READ_CONTACTS',
  contactsWrite: 'ohos.permission.WRITE_CONTACTS',
  calendarRead: 'ohos.permission.READ_CALENDAR',
  calendarWrite: 'ohos.permission.WRITE_CALENDAR',
  location: 'ohos.permission.APPROXIMATELY_LOCATION',
  clipboardRead: 'ohos.permission.READ_PASTEBOARD',
  microphone: 'ohos.permission.MICROPHONE'
};

function makeEmitter() {
  const listeners = new Map();
  return {
    on(name, callback) { listeners.set(name, callback); },
    off(name, callback) { if (listeners.get(name) === callback) listeners.delete(name); },
    listener(name) { return listeners.get(name); }
  };
}

function copyBuffer(source) {
  const bytes = new Uint8Array(source);
  const result = new ArrayBuffer(bytes.length);
  new Uint8Array(result).set(bytes);
  return result;
}

export const audioHarness = {
  rendererCalls: 0,
  capturerCalls: 0,
  renderers: [],
  capturers: [],
  delayNextRenderer: false,
  pendingRenderer: undefined,
  createAudioRenderer(options) {
    this.rendererCalls++;
    return new Promise((resolve) => {
      const finish = () => {
        const events = makeEmitter();
        const renderer = {
          options,
          state: audioApi.AudioState.STATE_STOPPED,
          flushCalls: 0,
          released: false,
          on: events.on,
          off: events.off,
          async start() { this.state = audioApi.AudioState.STATE_RUNNING; },
          async stop() { this.state = audioApi.AudioState.STATE_STOPPED; },
          async release() { this.released = true; },
          async flush() { this.flushCalls++; },
          drain(length) {
            const data = new ArrayBuffer(length);
            const callback = events.listener('writeData');
            const result = callback ? callback(data) : undefined;
            return {data: new Uint8Array(data), result};
          }
        };
        this.renderers.push(renderer);
        resolve(renderer);
      };
      if (this.delayNextRenderer) {
        this.delayNextRenderer = false;
        this.pendingRenderer = finish;
      } else finish();
    });
  },
  resolveRenderer() {
    const finish = this.pendingRenderer;
    this.pendingRenderer = undefined;
    if (finish) finish();
  },
  async createAudioCapturer(options) {
    this.capturerCalls++;
    const events = makeEmitter();
    const capturer = {
      options,
      state: audioApi.AudioState.STATE_STOPPED,
      released: false,
      on: events.on,
      off: events.off,
      async start() { this.state = audioApi.AudioState.STATE_RUNNING; },
      async stop() { this.state = audioApi.AudioState.STATE_STOPPED; },
      async release() { this.released = true; },
      emitRead(data) { events.listener('readData')?.(copyBuffer(data)); },
      readSnapshot() { return events.listener('readData'); }
    };
    this.capturers.push(capturer);
    return capturer;
  },
  reset() {
    this.rendererCalls = 0;
    this.capturerCalls = 0;
    this.renderers.length = 0;
    this.capturers.length = 0;
    this.delayNextRenderer = false;
    this.pendingRenderer = undefined;
  }
};

const audioApi = {
  AudioChannel: {CHANNEL_1: 1},
  AudioSampleFormat: {SAMPLE_FORMAT_S16LE: 1},
  AudioEncodingType: {ENCODING_TYPE_RAW: 1},
  StreamUsage: {STREAM_USAGE_VOICE_COMMUNICATION: 1},
  SourceType: {SOURCE_TYPE_VOICE_COMMUNICATION: 1},
  AudioState: {STATE_RUNNING: 'running', STATE_STOPPED: 'stopped'},
  AudioDataCallbackResult: {VALID: 0},
  createAudioRenderer(options) { return audioHarness.createAudioRenderer(options); },
  createAudioCapturer(options) { return audioHarness.createAudioCapturer(options); }
};

const permissionCodes = {granted: 0, not_determined: 1, denied: 2, restricted: 3, invalid: 99};

export const native = {
  permissions: new Map(),
  permissionRequests: [],
  preferenceValues: new Map(),
  contacts: {queryCount: 0},
  clipboard: {writes: [], text: '', mime: 'text/plain', changeCount: 0, writeGate: undefined},
  repositoryCalls: [],
  extensionList: {extensions: [{id: 'voice.extension', kind: 'voice', status: 'running'}]},
  extensionInfo: {protocol: 2, features: ['resume']},
  decisionRefreshes: 0,
  decisionMutations: 0,
  uuidCounter: 1,
  deviceInfo: {
    productModel: 'Harmony Test Phone', productModelAlias: 'Harmony Test Phone',
    softwareModel: 'Harmony Test Phone', productSeries: 'Harmony', brand: 'Test', osFullName: 'HarmonyOS 7'
  },
  batteryInfo: {chargingStatus: 0, batterySOC: 75},
  audioHarness,
  audioApi,
  reset() {
    this.permissions.clear();
    for (const permission of Object.values(PERMISSIONS)) this.permissions.set(permission, permissionCodes.granted);
    this.permissionRequests.length = 0;
    this.preferenceValues.clear();
    this.contacts.queryCount = 0;
    this.clipboard.writes.length = 0;
    this.clipboard.text = '';
    this.clipboard.mime = 'text/plain';
    this.clipboard.changeCount = 0;
    this.clipboard.writeGate = undefined;
    this.repositoryCalls.length = 0;
    this.extensionList = {extensions: [{id: 'voice.extension', kind: 'voice', status: 'running'}]};
    this.extensionInfo = {protocol: 2, features: ['resume']};
    this.decisionRefreshes = 0;
    this.decisionMutations = 0;
    this.uuidCounter = 1;
    Object.assign(this.deviceInfo, {
      productModel: 'Harmony Test Phone', productModelAlias: 'Harmony Test Phone',
      softwareModel: 'Harmony Test Phone', productSeries: 'Harmony', brand: 'Test', osFullName: 'HarmonyOS 7'
    });
    this.batteryInfo.chargingStatus = 0;
    this.batteryInfo.batterySOC = 75;
    this.audioHarness.reset();
  },
  setPermission(permission, status) { this.permissions.set(permission, permissionCodes[status] ?? permissionCodes.invalid); }
};

globalThis.__personaDeviceTest = native;

const abilityAccessCtrl = {
  PermissionStatus: {GRANTED: 0, NOT_DETERMINED: 1, DENIED: 2, RESTRICTED: 3},
  createAtManager() {
    return {
      getSelfPermissionStatus(permission) { return native.permissions.get(permission) ?? 99; },
      async requestPermissionsFromUser(context, permissions) {
        native.permissionRequests.push({kind: 'user', context, permissions: [...permissions]});
      },
      async requestPermissionOnSetting(context, permissions) {
        native.permissionRequests.push({kind: 'settings', context, permissions: [...permissions]});
      }
    };
  }
};

const preferences = {
  async getPreferences() {
    return {
      getSync(key, fallback) { return native.preferenceValues.get(key) ?? fallback; },
      putSync(key, value) { native.preferenceValues.set(key, value); },
      async flush() {}
    };
  }
};

const util = {generateRandomUUID() {
  const suffix = String(native.uuidCounter++).padStart(12, '0');
  return '00000000-0000-4000-8000-' + suffix;
}};

const contact = {
  Contact: {INVALID_CONTACT_ID:-1}, Attribute:{ATTR_NAME:4,ATTR_ORGANIZATION:7,ATTR_PHONE:8,ATTR_EMAIL:1},
  Event: {EVENT_BIRTHDAY: 1},
  async queryContacts() { native.contacts.queryCount++; return []; }
};

const calendarManager = {
  EventType: {NORMAL: 0},
  EventFilter: {filterByTime(start, end) { return {start, end}; }},
  getCalendarManager() {
    return {async getAllCalendars() { return []; }, async getCalendar() { return undefined; }};
  }
};

const pasteboard = {
  MIMETYPE_TEXT_PLAIN: 'text/plain',
  createData(mimeType, text) { return {mimeType, text}; },
  getSystemPasteboard() {
    return {
      getChangeCount() { return native.clipboard.changeCount; },
      async getData() {
        return {getPrimaryMimeType() { return native.clipboard.mime; }, getPrimaryText() { return native.clipboard.text; }};
      },
      async setData(data) {
        native.clipboard.writes.push(data.text);
        native.clipboard.text = data.text;
        native.clipboard.mime = data.mimeType;
        native.clipboard.changeCount++;
        if (native.clipboard.writeGate) await native.clipboard.writeGate;
      }
    };
  }
};

const kitSources = {
  '@kit.AbilityKit': 'export const common = {}; export const abilityAccessCtrl = globalThis.__personaDeviceTest.abilityAccessCtrl; export const bundleManager = globalThis.__personaDeviceTest.bundleManager; export const Permissions = {};',
  '@kit.ArkData': 'export const preferences = globalThis.__personaDeviceTest.preferences;',
  '@kit.ArkTS': 'export const util = globalThis.__personaDeviceTest.util;',
  '@kit.AudioKit': 'export const audio = globalThis.__personaDeviceTest.audioApi;',
  '@kit.CalendarKit': 'export const calendarManager = globalThis.__personaDeviceTest.calendarManager;',
  '@kit.ContactsKit': 'export const contact = globalThis.__personaDeviceTest.contact;',
  '@kit.CoreFileKit': 'export const storageStatistics = globalThis.__personaDeviceTest.storageStatistics;',
  '@kit.LocalizationKit': 'export const i18n = globalThis.__personaDeviceTest.i18n; export const resourceManager = {};',
  '@kit.LocationKit': 'export const geoLocationManager = globalThis.__personaDeviceTest.geoLocationManager;',
  '@kit.NetworkKit': 'export const connection = globalThis.__personaDeviceTest.connection;',
  '@kit.BasicServicesKit': 'export const batteryInfo = globalThis.__personaDeviceTest.batteryInfo; export const deviceInfo = globalThis.__personaDeviceTest.deviceInfo; export const pasteboard = globalThis.__personaDeviceTest.pasteboard; export const systemDateTime = globalThis.__personaDeviceTest.systemDateTime;'
};

native.abilityAccessCtrl = abilityAccessCtrl;
native.preferences = preferences;
native.util = util;
native.contact = contact;
native.calendarManager = calendarManager;
native.pasteboard = pasteboard;
native.bundleManager = {
  BundleFlag: {GET_BUNDLE_INFO_WITH_APPLICATION: 1},
  getBundleInfoForSelfSync() { return {versionName: 'test', versionCode: 1}; }
};
native.storageStatistics = {async getFreeSize() { return 0; }, async getTotalSize() { return 0; }};
native.i18n = {getSystemLocale() { return 'en-US'; }};
native.geoLocationManager = {};
native.connection = {
  NetBearType: {BEARER_WIFI: 1, BEARER_CELLULAR: 2, BEARER_ETHERNET: 3},
  async hasDefaultNet() { return false; }
};
native.systemDateTime = {getTimezoneSync() { return 'UTC'; }};
native.batteryInfo.BatteryChargeState = {ENABLE: 1, FULL: 2, DISABLE: 3};

class TestVoiceStream {
  constructor(path) { this.path = path; this.sent = []; this.binarySent = []; this.closed = false; }
  async connect(onText, onState, onBinary) {
    this.onText = onText;
    this.onState = onState;
    this.onBinary = onBinary;
    onState('ready');
  }
  send(text) { this.sent.push(JSON.parse(text)); return true; }
  sendBinary(data) { this.binarySent.push(copyBuffer(data)); return true; }
  close() {
    if (this.closed) return;
    this.closed = true;
    this.onState?.('closed');
  }
  emitText(frame) { this.onText?.(JSON.stringify(frame)); }
  emitState(state) { this.onState?.(state); }
  emitBinary(data) { this.onBinary?.(copyBuffer(data)); }
}

export class TestTransport {
  constructor(label = 'transport') { this.label = label; this.sent = []; this.streams = []; }
  send(text) { this.sent.push(JSON.parse(text)); return true; }
  sendBinary(data) { this.binarySent ||= []; this.binarySent.push(copyBuffer(data)); return true; }
  stream(path) {
    const stream = new TestVoiceStream(path);
    this.streams.push(stream);
    return stream;
  }
}

export function makeClient({manager = 'personal', machine = 'machine-1', session = 'session-1'} = {}) {
  const state = {
    manager,
    target: 'session:' + session,
    selected: {kind: 'session', id: session, session, machine, title: 'Session one'},
    transport: new TestTransport('app-' + manager)
  };
  const model = {
    connected: true,
    demo: false,
    profile: {manager},
    selected: state.selected,
    messages: [
      {role: 'user', text: 'Earlier question'},
      {role: 'assistant', text: 'Earlier response'}
    ],
    decision: undefined,
    setDeviceEvents(handler) { state.deviceEvent=handler; },
    scope() { return 'hub:' + state.manager; },
    target() { return state.target; },
    repo() {
      return {
        transport: state.transport,
        async call(path) {
          native.repositoryCalls.push(path);
          if (path === '/api/v1/extensions') return native.extensionList;
          if (path.endsWith('/info')) return native.extensionInfo;
          return {};
        }
      };
    },
    isChat() { return true; },
    async refreshDecisions() { native.decisionRefreshes++; },
    watchDecisions(_visible) {},
    answerDecision() { native.decisionMutations++; },
    resolveDecision() { native.decisionMutations++; }
  };
  state.setTarget = (nextMachine, nextSession) => {
    state.target = 'session:' + nextSession;
    state.selected = {kind: 'session', id: nextSession, session: nextSession, machine: nextMachine, title: 'Selected ' + nextSession};
    model.selected = state.selected;
  };
  return {model, state};
}

registerHooks({
  resolve(specifier, context, next) {
    if (kitSources[specifier]) return {url: 'persona-device:' + specifier, shortCircuit: true};
    if (specifier === './ClientModel' && context.parentURL?.endsWith('/DeviceFeatures.ets'))
      return {url: 'persona-device:client-model', shortCircuit: true};
    if (specifier === './Compatibility' && context.parentURL?.endsWith('/DeviceFeatures.ets'))
      return {url: 'persona-device:compatibility', shortCircuit: true};
    if (specifier.endsWith('/platform/HubTransport') && context.parentURL?.endsWith('/DeviceFeatures.ets'))
      return {url: 'persona-device:hub-transport', shortCircuit: true};
    if (specifier.endsWith('.ets')) return {url: new URL(specifier, context.parentURL).href, shortCircuit: true};
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.')) {
      return {url: new URL(specifier + '.ets', context.parentURL).href, shortCircuit: true};
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url.startsWith('persona-device:@kit.')) return {format: 'module', source: kitSources[url.slice('persona-device:'.length)], shortCircuit: true};
    if (url === 'persona-device:client-model') return {format: 'module', source: 'export class ClientModel {}', shortCircuit: true};
    if (url === 'persona-device:compatibility') return {format: 'module', source: 'export function messageDisplay(text){return {text:String(text)}}', shortCircuit: true};
    if (url === 'persona-device:hub-transport') return {format: 'module', source: 'export class HubTransport {} export class HttpFailure extends Error {constructor(status,code){super(code);this.status=status;this.code=code}}', shortCircuit: true};
    if (url.endsWith('.ets')) {
      const source = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
      return {format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true};
    }
    return next(url, context);
  }
});

export const permission = PERMISSIONS;
export async function waitFor(predicate, timeoutMs = 1500) {
  const deadline = Date.now() + timeoutMs;
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error('Timed out waiting for the device boundary state.');
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
}

export function pcm(size, value) {
  const data = new Uint8Array(size);
  data.fill(value);
  return data.buffer;
}
