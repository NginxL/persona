import test from 'node:test';
import assert from 'node:assert/strict';
import {
  audioHarness, makeClient, native, pcm, permission, TestTransport, waitFor
} from './support/device-runtime.mjs';

const {DeviceFeatures} = await import('../entry/src/main/ets/models/DeviceFeatures.ets');
const {VoiceAudioBridge} = await import('../entry/src/main/ets/platform/device/VoiceAudioBridge.ets');

const context = {};

async function bindDevice() {
  const fixture = makeClient();
  const confirmations = [];
  const device = new DeviceFeatures();
  device.bind(fixture.model, context, (title, message, approve, deny) => {
    confirmations.push({title, message, approve, deny});
  });
  await device.sync();
  await device.refreshVoice();
  return {device, fixture, confirmations};
}

function registerFrame(transport) {
  return transport.sent.find((frame) => frame.type === 'register_phone');
}

function toolResults(transport, callId) {
  return transport.sent.filter((frame) => frame.type === 'tool_result' && frame.call_id === callId);
}

function readyFrame(target, features = ['resume']) {
  return {type: 'ready', protocol: 2, target, input_rate: 8000, output_rate: 8000, features};
}

async function startVoice(device, transport) {
  await device.startVoice();
  const stream = transport.streams[0];
  assert.ok(stream, 'voice opens its own authenticated extension stream');
  return stream;
}

test('rebinding phone tools retires the previous subscription and never retargets its late event',async()=>{
 native.reset();const {device,fixture:first}=await bindDevice(),old=first.state.deviceEvent,second=makeClient({manager:'work'});
 device.bind(second.model,context,()=>{});await device.sync();assert.equal(first.state.deviceEvent,undefined);assert.deepEqual(first.state.transport.sent.at(-1).tools,[]);
 old(JSON.stringify({type:'tool_call',call_id:'foreign',name:'phone_device_info',args:{}}));await new Promise(resolve=>setImmediate(resolve));assert.equal(toolResults(second.state.transport,'foreign').length,0);assert.equal(toolResults(first.state.transport,'foreign').length,0);
 second.state.deviceEvent(JSON.stringify({type:'tool_call',call_id:'current',name:'phone_device_info',args:{}}));await waitFor(()=>toolResults(second.state.transport,'current').length===1);assert.equal(toolResults(first.state.transport,'current').length,0);
 device.bind(first.model,context,()=>{});await device.sync();assert.equal(second.state.deviceEvent,undefined);assert.deepEqual(second.state.transport.sent.at(-1).tools,[]);assert.equal(typeof first.state.deviceEvent,'function');
});

test('slow old audio release cannot detach or consume a newer playback listener',async()=>{
 native.reset();const bridge=new VoiceAudioBridge();await bridge.start(16000,24000,()=>{});const oldInput=audioHarness.capturers[0],oldOutput=audioHarness.renderers[0];let release;oldInput.stop=async()=>{await new Promise(resolve=>release=resolve);};
 const stopping=bridge.stop();await waitFor(()=>!!release);await bridge.start(16000,24000,()=>{});const listener=bridge.writeListener;bridge.enqueue(pcm(8,0x55));assert.ok(oldOutput.drain(8).data.every(value=>value===0));release();await stopping;assert.equal(bridge.writeListener,listener);assert.ok(audioHarness.renderers[1].drain(8).data.every(value=>value===0x55));await bridge.stop();
});

test("old background cleanup cannot cancel a foreground owner's new phone approval",async()=>{
 native.reset();const {device,fixture}=await bindDevice();let release,stops=0;device.voiceAudio.stop=async()=>{if(++stops===1)await new Promise(resolve=>release=resolve);};const background=device.background();await waitFor(()=>!!release);
 const second=makeClient({manager:'work'}),confirmations=[];device.bind(second.model,context,(_title,_message,approve)=>confirmations.push(approve));await device.sync();second.state.deviceEvent(JSON.stringify({type:'tool_call',call_id:'new-call',name:'phone_clipboard_set',args:{text:'new owner'}}));await waitFor(()=>confirmations.length===1);
 device.voiceCaptions=[{role:'assistant',text:'new caption'}];release();await background;assert.equal(device.voiceCaptions[0].text,'new caption');confirmations[0]();await waitFor(()=>native.clipboard.writes.length===1);assert.deepEqual(native.clipboard.writes,['new owner']);
});

test('denied phone permissions keep sensitive tools unregistered and block direct calls', async () => {
  native.reset();
  for (const id of [permission.contacts, permission.contactsWrite, permission.calendarRead, permission.calendarWrite,
    permission.location, permission.clipboardRead]) native.setPermission(id, 'denied');

  const {device, fixture} = await bindDevice();
  const registration = registerFrame(fixture.state.transport);
  assert.ok(registration);
  const names = registration.tools.map((tool) => tool.name).sort();
  assert.deepEqual(names, ['phone_clipboard_set', 'phone_device_info', 'phone_location_search']);
  assert.equal(device.toolCount, 3);

  device.receive(JSON.stringify({type: 'tool_call', call_id: 'denied-contacts',
    name: 'phone_contacts_search', args: {query: 'Ada'}}));
  await waitFor(() => toolResults(fixture.state.transport, 'denied-contacts').length === 1);
  assert.match(toolResults(fixture.state.transport, 'denied-contacts')[0].content[0].text, /denied/i);
  assert.equal(native.contacts.queryCount, 0);
  assert.deepEqual(native.permissionRequests, []);
  await device.endVoice();
});

test('phone registration marks simulator hardware from productModel and leaves ordinary models false', async () => {
  native.reset();
  native.deviceInfo.productModel = 'HarmonyOS Emulator X';
  const simulator = await bindDevice();
  assert.equal(registerFrame(simulator.fixture.state.transport).simulator, true);
  await simulator.device.endVoice();

  native.reset();
  native.deviceInfo.productModel = 'HUAWEI-Pura-80';
  native.deviceInfo.productSeries = 'Pura';
  const phone = await bindDevice();
  assert.equal(registerFrame(phone.fixture.state.transport).simulator, false);
  await phone.device.endVoice();
});

test('a delayed phone write stays scoped to its original manager while target changes', async () => {
  native.reset();
  const {device, fixture, confirmations} = await bindDevice();
  const originalTransport = fixture.state.transport;
  const call = JSON.stringify({type: 'tool_call', call_id: 'clipboard-once', name: 'phone_clipboard_set',
    args: {text: 'approved text'}});

  device.receive(call);
  await waitFor(() => confirmations.length === 1);
  assert.deepEqual(native.clipboard.writes, []);

  fixture.state.setTarget('machine-2', 'session-2');
  device.contextChanged();
  await device.sync();
  confirmations[0].approve();
  confirmations[0].approve();
  await waitFor(() => native.clipboard.writes.length === 1 && toolResults(originalTransport, 'clipboard-once').length === 1);

  assert.deepEqual(native.clipboard.writes, ['approved text']);
  assert.equal(fixture.model.target(), 'session:session-2');
  assert.equal(toolResults(originalTransport, 'clipboard-once').length, 1);
  assert.equal(fixture.state.transport, originalTransport);
  await device.endVoice();
});

test('a manager switch cancels a pending phone confirmation before the local write', async () => {
  native.reset();
  const {device, fixture, confirmations} = await bindDevice();
  const originalTransport = fixture.state.transport;
  device.receive(JSON.stringify({type: 'tool_call', call_id: 'cancel-on-scope-change', name: 'phone_clipboard_set',
    args: {text: 'must not be copied'}}));
  await waitFor(() => confirmations.length === 1);

  fixture.state.manager = 'work';
  fixture.model.profile.manager = 'work';
  const replacement = new TestTransport('app-work');
  fixture.state.transport = replacement;
  device.contextChanged();
  await device.sync();
  confirmations[0].approve();
  await waitFor(() => device.callState.size === 0);

  assert.deepEqual(native.clipboard.writes, []);
  assert.equal(toolResults(originalTransport, 'cancel-on-scope-change').length, 0);
  assert.equal(toolResults(replacement, 'cancel-on-scope-change').length, 0);
  await device.endVoice();
});

test('a delayed approved write never returns its result over a replacement app transport', async () => {
  native.reset();
  const {device, fixture, confirmations} = await bindDevice();
  const originalTransport = fixture.state.transport;
  device.receive(JSON.stringify({type: 'tool_call', call_id: 'transport-change', name: 'phone_clipboard_set',
    args: {text: 'approved while the old call was visible'}}));
  await waitFor(() => confirmations.length === 1);

  let releaseWrite;
  native.clipboard.writeGate = new Promise((resolve) => { releaseWrite = resolve; });
  confirmations[0].approve();
  await waitFor(() => native.clipboard.writes.length === 1);
  const replacement = new TestTransport('reconnected-app');
  fixture.state.transport = replacement;
  releaseWrite();
  await waitFor(() => device.callState.size === 0);

  assert.equal(toolResults(originalTransport, 'transport-change').length, 0);
  assert.equal(toolResults(replacement, 'transport-change').length, 0);
  await device.endVoice();
});

test('voice sends the exact selected target and rejects a ready frame for another target', async () => {
  native.reset();
  const {device, fixture} = await bindDevice();
  const stream = await startVoice(device, fixture.state.transport);
  const start = stream.sent.find((frame) => frame.type === 'start');
  assert.ok(start);
  assert.equal(start.protocol, 2);
  assert.deepEqual(start.target, {machine_id: 'machine-1', session_id: 'session-1'});
  assert.equal(audioHarness.rendererCalls, 0);
  assert.equal(audioHarness.capturerCalls, 0);

  stream.emitBinary(pcm(4, 0x11));
  assert.equal(device.voiceAudio.playbackBytes, 0);
  stream.emitText(readyFrame({machine_id: 'machine-1', session_id: 'different-session'}));
  await waitFor(() => device.voiceActive === false);

  assert.equal(device.voiceStatus, 'voice_protocol_unsupported');
  assert.equal(audioHarness.rendererCalls, 0);
  assert.equal(audioHarness.capturerCalls, 0);
  await device.endVoice();
});

test('a target change invalidates an old voice stream before it can start audio', async () => {
  native.reset();
  const {device, fixture} = await bindDevice();
  const stream = await startVoice(device, fixture.state.transport);
  const oldTarget = stream.sent.find((frame) => frame.type === 'start').target;

  fixture.state.setTarget('machine-2', 'session-2');
  device.contextChanged();
  await waitFor(() => device.voiceActive === false);
  stream.emitText(readyFrame(oldTarget));
  await new Promise((resolve) => setTimeout(resolve, 0));

  assert.equal(audioHarness.rendererCalls, 0);
  assert.equal(audioHarness.capturerCalls, 0);
  assert.equal(device.voiceStatus, 'voice_ready');
  await device.endVoice();
});

test('voice cannot capture or send PCM before a valid ready frame', async () => {
  native.reset();
  const {device, fixture} = await bindDevice();
  const stream = await startVoice(device, fixture.state.transport);

  stream.emitBinary(pcm(8, 0x22));
  assert.equal(device.voiceReady, false);
  assert.equal(audioHarness.rendererCalls, 0);
  assert.equal(audioHarness.capturerCalls, 0);
  assert.equal(stream.binarySent.length, 0);

  const start = stream.sent.find((frame) => frame.type === 'start');
  stream.emitText(readyFrame(start.target, []));
  await waitFor(() => audioHarness.capturers.length === 1);
  assert.equal(stream.binarySent.length, 0);
  audioHarness.capturers[0].emitRead(pcm(8, 0x33));
  assert.equal(stream.binarySent.length, 1);
  await device.endVoice();
});

test('the actual voice audio bridge bounds early playback and clear drops queued PCM', async () => {
  native.reset();
  audioHarness.delayNextRenderer = true;
  const bridge = new VoiceAudioBridge();
  const starting = bridge.start(8000, 8000, () => {});
  await waitFor(() => audioHarness.rendererCalls === 1);

  bridge.enqueue(pcm(32000, 0x11));
  bridge.enqueue(pcm(32000, 0x22));
  assert.equal(bridge.playbackBytes, 48000);
  audioHarness.resolveRenderer();
  await starting;

  const rendered = audioHarness.renderers[0].drain(48000).data;
  assert.ok(rendered.subarray(0, 16000).every((byte) => byte === 0x11));
  assert.ok(rendered.subarray(16000).every((byte) => byte === 0x22));
  assert.equal(bridge.playbackBytes, 0);

  bridge.enqueue(pcm(64, 0x44));
  bridge.clear();
  assert.equal(audioHarness.renderers[0].flushCalls, 1);
  assert.ok(audioHarness.renderers[0].drain(64).data.every((byte) => byte === 0));
  await bridge.stop();
});

test('voice interrupt flushes queued PCM and end releases audio and clears later frames', async () => {
  native.reset();
  const {device, fixture} = await bindDevice();
  const stream = await startVoice(device, fixture.state.transport);
  const start = stream.sent.find((frame) => frame.type === 'start');
  stream.emitText(readyFrame(start.target, []));
  await waitFor(() => audioHarness.capturers.length === 1);

  stream.emitBinary(pcm(32, 0x55));
  assert.equal(device.voiceAudio.playbackBytes, 32);
  device.interrupt();
  assert.equal(device.voiceAudio.playbackBytes, 0);
  assert.equal(stream.sent.some((frame) => frame.type === 'interrupt'), true);
  assert.equal(audioHarness.renderers[0].flushCalls, 1);

  stream.emitBinary(pcm(32, 0x66));
  assert.equal(device.voiceAudio.playbackBytes, 32);
  await device.endVoice();
  assert.equal(device.voiceAudio.playbackBytes, 0);
  assert.equal(audioHarness.renderers[0].released, true);
});

test('late audio initialization is released after end, background, and scope change', async () => {
  for (const stopKind of ['end', 'background', 'scope']) {
    native.reset();
    audioHarness.delayNextRenderer = true;
    const {device, fixture} = await bindDevice();
    const stream = await startVoice(device, fixture.state.transport);
    const start = stream.sent.find((frame) => frame.type === 'start');
    stream.emitText(readyFrame(start.target, []));
    await waitFor(() => audioHarness.rendererCalls === 1);

    if (stopKind === 'end') await device.endVoice();
    else if (stopKind === 'background') await device.background();
    else {
      fixture.state.manager = 'work';
      fixture.model.profile.manager = 'work';
      device.contextChanged();
      await waitFor(() => device.voiceActive === false);
    }

    audioHarness.resolveRenderer();
    await waitFor(() => audioHarness.renderers.length === 1 && audioHarness.renderers[0].released);
    await new Promise((resolve) => setTimeout(resolve, 0));
    assert.equal(audioHarness.capturerCalls, 0, stopKind + ' must stop stale initialization before capture');
    await device.endVoice();
  }
});

test('same-target reconnect resumes only server runs and transcript history; old mic PCM is not replayed', async () => {
  native.reset();
  const {device, fixture} = await bindDevice();
  const transport = fixture.state.transport;
  const first = await startVoice(device, transport);
  const firstTarget = first.sent.find((frame) => frame.type === 'start').target;
  first.emitText(readyFrame(firstTarget, ['resume']));
  await waitFor(() => audioHarness.capturers.length === 1);

  const oldCapture = audioHarness.capturers[0];
  const staleMicrophoneCallback = oldCapture.readSnapshot();
  oldCapture.emitRead(pcm(8, 0x12));
  assert.equal(first.binarySent.length, 1);

  first.emitText({type: 'transcript', id: 'line-1', role: 'assistant', text: 'partial', final: false});
  first.emitText({type: 'transcript', id: 'line-1', role: 'assistant', text: 'completed line', final: true});
  first.emitText({type: 'run', id: 'run-working', task: 'Continue task', state: 'working', msg_ids: ['message-1'], base_seq: 4});
  first.emitText({type: 'run', id: 'run-new-state', task: 'Future state', state: 'future_server_state'});
  first.emitText({type: 'ask', interaction_id: 'touch-decision', state: 'asking'});

  assert.equal(device.voiceCaptions.length, 1);
  assert.equal(device.voiceCaptions[0].text, 'completed line');
  assert.equal(device.voiceRuns.find((run) => run.id === 'run-new-state').state, 'unknown');
  assert.equal(native.decisionRefreshes, 1);
  assert.equal(native.decisionMutations, 0);

  first.emitState('closed');
  await waitFor(() => transport.streams.length === 2 && transport.streams[1].sent.some((frame) => frame.type === 'start'), 3000);
  const second = transport.streams[1];
  const resumed = second.sent.find((frame) => frame.type === 'start');
  assert.deepEqual(resumed.target, firstTarget);
  assert.deepEqual(resumed.history, [{role: 'assistant', text: 'completed line'}]);
  assert.deepEqual(resumed.resume.runs.map((run) => run.id), ['run-working']);
  assert.equal(resumed.resume.runs.some((run) => run.id === 'run-new-state'), false);

  staleMicrophoneCallback(pcm(8, 0x77));
  assert.equal(second.binarySent.length, 0);
  second.emitText(readyFrame(firstTarget, ['resume']));
  await waitFor(() => audioHarness.capturers.length === 2);
  assert.equal(second.binarySent.length, 0);
  audioHarness.capturers[1].emitRead(pcm(8, 0x88));
  assert.equal(second.binarySent.length, 1);
  await device.endVoice();
});

async function bindLegacyVoice(info = {protocol: 1}) {
  const fixture = makeClient(); fixture.model.selected.kind = 'conversation'; fixture.model.mainId = fixture.model.selected.id;
  native.extensionInfo = info;
  const device = new DeviceFeatures(); device.bind(fixture.model, context, () => {});
  await device.sync(); await device.refreshVoice();
  return {device, fixture};
}

test('legacy voice uses only the main chat and waits for its V1 ready before PCM', async () => {
  native.reset(); const {device, fixture} = await bindLegacyVoice();
  assert.equal(device.voiceProtocol, 1); assert.equal(device.canStartVoice(), true);
  const stream = await startVoice(device, fixture.state.transport);
  assert.deepEqual(stream.sent[0], {type:'start'});
  assert.equal(audioHarness.capturers.length,0);stream.emitBinary(pcm(8));assert.equal(device.voiceAudio.playbackBytes,0);
  stream.emitText({type:'ready',input_rate:8000,output_rate:8000});
  await waitFor(()=>audioHarness.capturers.length===1);
  audioHarness.capturers[0].emitRead(pcm(8));assert.equal(stream.binarySent.length,1);
  stream.emitText({type:'transcript',id:'v1',role:'assistant',text:'partial',final:false});
  stream.emitText({type:'transcript',id:'v1',role:'assistant',text:'complete',final:true});
  stream.emitText({type:'transcript',id:'v1',role:'assistant',text:'late overwrite',final:false});
  assert.equal(device.voiceCaptions[0].text,'complete');
  stream.emitText({type:'tool',id:'first',label:'Search'});stream.emitText({type:'tool',id:'second',label:'Read'});
  stream.emitText({type:'tool',id:'first',done:true});assert.equal(device.voiceToolLabel,'Read');
  stream.emitText({type:'tool',id:'second',done:true});assert.equal(device.voiceToolLabel,'');
  stream.emitText({type:'run',id:'unsupported',state:'working'});stream.emitText({type:'ask',interaction_id:'unsupported'});
  assert.equal(device.voiceRuns.length,0);assert.equal(native.decisionRefreshes,0);
  await device.endVoice();assert.equal(audioHarness.capturers[0].released,true);
});

test('legacy voice cannot accidentally route an Agent or secondary chat to the main chat', async () => {
  native.reset(); const {device, fixture} = await bindLegacyVoice();
  fixture.model.selected.kind='session'; await device.refreshVoice();
  assert.equal(device.voiceStatus,'voice_main_only');assert.equal(device.canStartVoice(),false);
  await device.startVoice();assert.equal(fixture.state.transport.streams.length,0);
  fixture.model.selected.kind='conversation';fixture.model.mainId='another-main';await device.refreshVoice();
  assert.equal(device.canStartVoice(),false);await device.endVoice();
});

test('voice negotiates known versions and never starts audio for an unnegotiated ready', async () => {
  for (const highest of [1,2,3]) {
    native.reset(); const {device,fixture}=await bindLegacyVoice({protocol:highest,features:['resume']});
    assert.equal(device.voiceProtocol,Math.min(2,highest));
    const stream=await startVoice(device,fixture.state.transport);
    const start=stream.sent[0];assert.equal(start.protocol,highest===1?undefined:2);
    stream.emitText({type:'ready',protocol:3,target:start.target,input_rate:8000,output_rate:8000});
    await waitFor(()=>!device.voiceActive);assert.equal(audioHarness.capturers.length,0);assert.equal(device.voiceStatus,'voice_protocol_unsupported');
  }
  for(const protocol of [0,-1,2.5,'2']) {
    native.reset();const {device,fixture}=await bindLegacyVoice({protocol});assert.equal(device.canStartVoice(),false);
    await device.startVoice();assert.equal(fixture.state.transport.streams.length,0);await device.endVoice();
  }
});

test('only an authenticated missing legacy info route selects V1 fallback', async () => {
  const {HttpFailure} = await import('persona-device:hub-transport');
  for(const status of [404,401,403,503]) {
    native.reset();const fixture=makeClient();fixture.model.selected.kind='conversation';fixture.model.mainId=fixture.model.selected.id;
    const originalRepo=fixture.model.repo.bind(fixture.model);
    fixture.model.repo=()=>{const repo=originalRepo();const original=repo.call;repo.call=async path=>{if(path.endsWith('/info'))throw new HttpFailure(status,'test');return original(path)};return repo};
    const device=new DeviceFeatures();device.bind(fixture.model,context,()=>{});await device.sync();await device.refreshVoice();
    assert.equal(device.canStartVoice(),status===404);if(status===404)assert.equal(device.voiceProtocol,1);
    await device.endVoice();
  }
});

test('a delayed legacy discovery cannot replace a captured Agent call protocol while connecting', async () => {
  native.reset();const {device,fixture}=await bindDevice();
  const originalRepo=fixture.model.repo.bind(fixture.model);let resolveInfo,resolveConnect,infoStarted=false;
  fixture.model.repo=()=>{const repo=originalRepo();const call=repo.call;repo.call=async path=>{
    if(path.endsWith('/info')){infoStarted=true;return new Promise(resolve=>resolveInfo=resolve)}return call(path)
  };return repo};
  const discovery=device.refreshVoice();await waitFor(()=>infoStarted);
  const originalStream=fixture.state.transport.stream.bind(fixture.state.transport);
  fixture.state.transport.stream=path=>{const stream=originalStream(path);const connect=stream.connect.bind(stream);stream.connect=async(...args)=>{await connect(...args);await new Promise(resolve=>resolveConnect=resolve)};return stream};
  const opening=device.startVoice();await waitFor(()=>!!resolveConnect);
  resolveInfo({protocol:1});await discovery;assert.equal(device.voiceProtocol,2);
  resolveConnect();await opening;
  const stream=fixture.state.transport.streams[0],start=stream.sent[0];assert.equal(start.protocol,2);assert.equal(start.target.session_id,fixture.model.selected.id);
  stream.emitText(readyFrame(start.target));await waitFor(()=>audioHarness.capturers.length===1);
  await device.endVoice();
});
