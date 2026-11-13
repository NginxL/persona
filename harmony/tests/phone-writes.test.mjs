import test from 'node:test';
import assert from 'node:assert/strict';
import {native, makeClient, permission, waitFor} from './support/device-runtime.mjs';
const {DeviceFeatures}=await import('../entry/src/main/ets/models/DeviceFeatures.ets');
const {prepareContactWrite,prepareCalendarWrite,searchNativePlaces}=await import('../entry/src/main/ets/platform/NativePhoneWrites.ets');
const clone=value=>JSON.parse(JSON.stringify(value));
function data() {
  native.reset();
  const state={people:new Map(),contactAdds:[],contactUpdates:[],events:new Map(),eventUpdates:[],eventDeletes:[],places:[]};
  native.contact.queryContact=async(_context,key)=>clone(state.people.get(key));
  native.contact.queryKey=async(_context,id)=>'key-'+id;
  native.contact.addContact=async(_context,value)=>{state.contactAdds.push(clone(value));return 11};
  native.contact.updateContact=async(_context,value,attrs)=>{state.contactUpdates.push({value:clone(value),attrs:clone(attrs)});};
  const calendar={getAccount:()=>({name:'Synthetic calendar'}),getEvents:async(filter,keys)=>[...state.events.values()].filter(event=>!filter.ids||filter.ids.includes(event.id)).map(event=>{const fields=keys||['id','type','title','startTime','endTime','isAllDay','description','timeZone','location','service','attendee','reminderTime','identifier'];return Object.fromEntries(fields.filter(key=>event[key]!==undefined).map(key=>[key,clone(event[key])]))}),
    updateEvent:async event=>state.eventUpdates.push(clone(event)),deleteEvent:async id=>state.eventDeletes.push(id)};
  native.calendarManager.EventFilter.filterById=ids=>({ids});
  native.calendarManager.getCalendarManager=()=>({getAllCalendars:async()=>[calendar],getCalendar:async()=>calendar});
  native.geoLocationManager.getAddressesFromLocationName=async request=>{state.places.push(request);return[{placeName:'Synthetic place',countryName:'Test',locality:'City',latitude:1,longitude:2}]};
  native.geoLocationManager.getCurrentLocation=async()=>{throw new Error('A place search must not read current position')};
  return state;
}
async function bound(){const fixture=makeClient(),confirmations=[],device=new DeviceFeatures();device.bind(fixture.model,{},(title,message,approve,deny)=>confirmations.push({title,message,approve,deny}));await device.sync();return {fixture,confirmations,device};}
const results=(transport,id)=>transport.sent.filter(frame=>frame.type==='tool_result'&&frame.call_id===id);
function call(device,id,name,args){device.receive(JSON.stringify({type:'tool_call',call_id:id,name,args}));}
const person=()=>({id:7,key:'key-7',name:{fullName:'Ada Example',givenName:'Ada',familyName:'Example'},organization:{name:'Synthetic company'},phoneNumbers:[{phoneNumber:'123-456',labelName:'work'}],emails:[],portrait:{uri:'unchanged-owned-portrait'}});
const event=()=>({id:9,type:0,title:'Synthetic event',startTime:Date.parse('2026-11-12T10:00:00Z'),endTime:Date.parse('2026-11-12T11:00:00Z'),description:'Original notes',isAllDay:false});

test('contact writes require their separate write permission and an explicit phone approval',async()=>{
  const state=data();native.setPermission(permission.contactsWrite,'denied');const denied=await bound();
  const names=denied.fixture.state.transport.sent.find(f=>f.type==='register_phone').tools.map(t=>t.name);
  assert.ok(names.includes('phone_contacts_search'));assert.ok(!names.includes('phone_contacts_create'));assert.ok(!names.includes('phone_contacts_update'));
  call(denied.device,'denied','phone_contacts_create',{given_name:'Ada'});await waitFor(()=>results(denied.fixture.state.transport,'denied').length===1);
  assert.equal(state.contactAdds.length,0);assert.equal(denied.confirmations.length,0);
  await denied.device.endVoice();
});

test('one approved contact creation runs once and a declined creation runs zero times',async()=>{
  const state=data(),f=await bound();call(f.device,'create','phone_contacts_create',{given_name:'Ada',company:'Synthetic company',phones:[{value:'123',label:'work'}]});
  await waitFor(()=>f.confirmations.length===1);assert.equal(state.contactAdds.length,0);assert.match(f.confirmations[0].message,/Ada/);
  f.confirmations[0].approve();f.confirmations[0].approve();await waitFor(()=>results(f.fixture.state.transport,'create').length===1);
  const result=results(f.fixture.state.transport,'create')[0];assert.equal(result.is_error,false);assert.equal(JSON.parse(result.content[0].text).contact.id,'11');assert.equal(state.contactAdds.length,1);
  call(f.device,'create','phone_contacts_create',{given_name:'Ada'});await waitFor(()=>results(f.fixture.state.transport,'create').length===2);assert.equal(state.contactAdds.length,1);
  call(f.device,'cancel','phone_contacts_create',{given_name:'Cancelled'});await waitFor(()=>f.confirmations.length===2);f.confirmations[1].deny();await waitFor(()=>results(f.fixture.state.transport,'cancel').length===1);assert.equal(state.contactAdds.length,1);
  await f.device.endVoice();
});

test('contact append deduplicates values and restricts updates to the requested attributes',async()=>{
  const state=data();state.people.set('key-7',person());const plan=await prepareContactWrite({}, {id:'key-7',phones:[{value:'123456'},{value:'789',label:'home'}]},true);
  assert.equal(plan.before.name,'Ada Example');assert.equal(plan.after.phones.length,2);
  await plan.execute(()=>true);assert.equal(state.contactUpdates.length,1);const update=state.contactUpdates[0];
  assert.deepEqual(update.attrs.attributes,[native.contact.Attribute.ATTR_PHONE]);assert.equal(update.value.portrait,undefined);assert.equal(update.value.name,undefined);assert.equal(update.value.key,'key-7');
  await assert.rejects(prepareContactWrite({}, {id:'key-7',given_name:'Changed'},true),/replace_required/);
  state.people.set('key-7',{...person(),name:{fullName:'Legacy full name'}});
  await assert.rejects(prepareContactWrite({}, {id:'key-7',given_name:'Changed'},true),/replace_required/);
});

test('a changed contact or revoked admission cannot be overwritten by an old approved snapshot',async()=>{
  const state=data();state.people.set('key-7',person());const plan=await prepareContactWrite({}, {id:'key-7',given_name:'Approved',replace:true},true);
  state.people.get('key-7').organization.name='Changed on another device';await assert.rejects(plan.execute(()=>true),/contact_changed/);assert.equal(state.contactUpdates.length,0);
  state.people.set('key-7',person());const original=native.contact.queryContact;let release;
  native.contact.queryContact=async(...args)=>{const result=await original(...args);await new Promise(resolve=>release=()=>resolve());return result};
  let admitted=true;const executing=plan.execute(()=>admitted);await waitFor(()=>!!release);admitted=false;release();await assert.rejects(executing,/cancelled/);assert.equal(state.contactUpdates.length,0);
});

test('backgrounding while a contact confirmation is open never performs the pending write',async()=>{
  const state=data();state.people.set('key-7',person());const f=await bound();call(f.device,'background','phone_contacts_update',{id:'key-7',phones:[{value:'789'}]});
  await waitFor(()=>f.confirmations.length===1);await f.device.background();f.confirmations[0].approve();await f.device.foreground();call(f.device,'background','phone_contacts_update',{id:'key-7',phones:[{value:'789'}]});await waitFor(()=>results(f.fixture.state.transport,'background').length===1);assert.equal(results(f.fixture.state.transport,'background')[0].is_error,true);assert.equal(state.contactUpdates.length,0);await f.device.endVoice();
});

test('calendar updates preserve duration and use the exact current event after confirmation',async()=>{
  const state=data();state.events.set(9,event());const plan=await prepareCalendarWrite({}, {id:'9',start:'2026-11-12T12:30:00Z',notes:''},false);
  assert.equal(plan.after.end,'2026-11-12T13:30:00.000Z');assert.equal(plan.after.notes,'');await plan.execute(()=>true);assert.equal(state.eventUpdates.length,1);
  const stale=await prepareCalendarWrite({}, {id:'9',title:'New title'},false);state.events.get(9).description='Changed elsewhere';await assert.rejects(stale.execute(()=>true),/event_changed/);assert.equal(state.eventUpdates.length,1);
  await assert.rejects(prepareCalendarWrite({}, {id:'9',start:'next Tuesday'},false),/invalid_calendar_time/);
});

test('calendar deletion confirms its actual title and executes only once',async()=>{
  const state=data();state.events.set(9,event());const f=await bound();call(f.device,'delete','phone_calendar_delete_event',{id:'9'});
  await waitFor(()=>f.confirmations.length===1);assert.match(f.confirmations[0].message,/Synthetic event/);assert.equal(state.eventDeletes.length,0);f.confirmations[0].approve();f.confirmations[0].approve();await waitFor(()=>results(f.fixture.state.transport,'delete').length===1);assert.deepEqual(state.eventDeletes,[9]);await f.device.endVoice();
});

test('unsupported recurrence changes cannot silently modify or delete a whole series',async()=>{
  const state=data();state.events.set(9,{...event(),recurrenceRule:{recurrenceFrequency:0}});
  for(const remove of [false,true]) {
    await assert.rejects(prepareCalendarWrite({}, {id:'9',title:'Changed'},remove),/occurrence_unsupported/);
    await assert.rejects(prepareCalendarWrite({}, {id:'9',occurrence_start:'2026-11-12T10:00:00Z',span:'this',title:'Changed'},remove),/occurrence_unsupported/);
  }
  assert.equal(state.eventUpdates.length,0);assert.equal(state.eventDeletes.length,0);
});

test('the native non-repeating sentinel permits ordinary writes and is listed as non-recurring',async()=>{
 const state=data();state.events.set(9,{...event(),recurrenceRule:{recurrenceFrequency:-1}});
 const change=await prepareCalendarWrite({}, {id:'9',title:'Updated ordinary event'},false);assert.equal(change.before.recurring,false);await change.execute(()=>true);assert.equal(state.eventUpdates.length,1);
 const removal=await prepareCalendarWrite({}, {id:'9'},true);await removal.execute(()=>true);assert.deepEqual(state.eventDeletes,[9]);
 const f=await bound();call(f.device,'list-ordinary','phone_calendar_events',{start:'2026-11-12T00:00:00Z',end:'2026-11-13T00:00:00Z'});await waitFor(()=>results(f.fixture.state.transport,'list-ordinary').length===1);
 assert.equal(JSON.parse(results(f.fixture.state.transport,'list-ordinary')[0].content[0].text).events[0].recurring,false);await f.device.endVoice();
});

test('unknown or additional recurrence metadata never turns into a whole-series mutation',async()=>{
 for(const rule of [{recurrenceFrequency:7},{recurrenceFrequency:-1,count:3},{}]){const state=data();state.events.set(9,{...event(),recurrenceRule:rule});for(const remove of[false,true])await assert.rejects(prepareCalendarWrite({}, {id:'9',title:'Unsafe'},remove),/occurrence_unsupported/);assert.equal(state.eventUpdates.length,0);assert.equal(state.eventDeletes.length,0);}
});

test('place search is bounded and never reads the current device location',async()=>{
  const state=data();const result=await searchNativePlaces({query:'Synthetic place',limit:5});assert.equal(result.count,1);assert.equal(result.results[0].latitude,1);assert.deepEqual(state.places.map(p=>[p.description,p.maxItems]),[['Synthetic place',5]]);
  for(const limit of [0,11,'5',1.5])await assert.rejects(searchNativePlaces({query:'Test',limit}),/invalid_phone_argument/);assert.equal(state.places.length,1);
});

test('the visible contacts-write permission action reaches the native write permission request',async()=>{
  data();native.setPermission(permission.contactsWrite,'not_determined');const f=await bound();
  assert.ok(f.device.permissionRows().some(row=>row.id==='contacts_write'&&row.canRequest()));
  await f.device.requestAccess('contacts_write');assert.deepEqual(native.permissionRequests[0].permissions,[permission.contactsWrite]);
  await f.device.endVoice();
});

test('requested name edits preserve unrequested phonetic fields in the native payload',async()=>{
  const state=data(),original=person();Object.assign(original.name,{givenNamePhonetic:'given phonetic',familyNamePhonetic:'family phonetic',middleNamePhonetic:'middle phonetic',hasName:true});state.people.set('key-7',original);
  const plan=await prepareContactWrite({}, {id:'key-7',given_name:'Changed',replace:true},true);await plan.execute(()=>true);
  const updated=state.contactUpdates[0].value.name;assert.equal(updated.givenName,'Changed');
  for(const key of ['givenNamePhonetic','familyNamePhonetic','middleNamePhonetic','hasName'])assert.equal(updated[key],original.name[key]);
});

test('calendar listing explicitly projects recurrence before exposing an event to an agent',async()=>{
  const state=data();state.events.set(9,{...event(),recurrenceRule:{recurrenceFrequency:0}});const f=await bound();
  call(f.device,'list-recurring','phone_calendar_events',{start:'2026-11-12T00:00:00Z',end:'2026-11-13T00:00:00Z'});
  await waitFor(()=>results(f.fixture.state.transport,'list-recurring').length===1);const response=JSON.parse(results(f.fixture.state.transport,'list-recurring')[0].content[0].text);assert.equal(response.events[0].recurring,true);await f.device.endVoice();
});

test('a confirmation from a replaced app transport cannot perform a delayed clipboard write',async()=>{
  data();const f=await bound();call(f.device,'old-clipboard','phone_clipboard_set',{text:'Old request'});await waitFor(()=>f.confirmations.length===1);
  const original=f.fixture.state.transport;f.fixture.state.transport={...original,send:frame=>{throw new Error('No stale result may be published')}};
  f.confirmations[0].approve();await new Promise(resolve=>setImmediate(resolve));assert.deepEqual(native.clipboard.writes,[]);await f.device.endVoice();
});
