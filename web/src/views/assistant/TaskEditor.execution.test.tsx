// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { TaskEditor } from "./TaskEditor";
import { blankTask, type Task } from "../../lib/routines";
import { getState } from "../../lib/store";
import { chooseManager, MANAGER_HEADER, resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { loadPages } from "../../lib/lazy-page";
import { t } from "../../i18n";
import { Toasts } from "../../ui/atoms";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
beforeAll(() => loadPages(),60_000);
let host:HTMLDivElement, root:Root;
let saved:Task, writes:Record<string,unknown>[], reject:boolean;
let catalog:'available'|'missing'|'failed'|'omitted';
const onSaved=vi.fn(), onClose=vi.fn();
const flush=async()=>{for(let i=0;i<12;i++) await act(async()=>{await Promise.resolve();});};
const field=(label:string)=>[...document.querySelectorAll('label')].find((el)=>el.textContent?.startsWith(label))!.querySelector<HTMLInputElement|HTMLSelectElement>('input,select')!;
const change=async(el:HTMLInputElement|HTMLSelectElement,value:string)=>{
 await act(async()=>{
  const prototype=el instanceof HTMLSelectElement?HTMLSelectElement.prototype:HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(prototype,'value')!.set!.call(el,value);
  el.dispatchEvent(new Event(el instanceof HTMLSelectElement?'change':'input',{bubbles:true}));
 });await flush();
};
const click=async(label:string)=>{
 const button=[...document.querySelectorAll('button')].find((b)=>b.textContent?.trim()===label)!;
 expect(button).toBeTruthy();await act(async()=>button.click());await flush();
};
beforeEach(()=>{
 resetManagerChoice();setRosterDefault('assistant');
 writes=[];reject=false;catalog='available';onSaved.mockClear();onClose.mockClear();
 saved={...blankTask(),id:'job',title:'Check files',prompt:'Summarize files',model_revision:0};
 Object.assign(getState(),{features:['task_execution'],machines:[{id:'box',name:'Computer',online:true}]});
 vi.stubGlobal('fetch',vi.fn(async(input:RequestInfo|URL,init?:RequestInit)=>{
  const path=String(input);
  const reply=(body:unknown,status=200)=>new Response(JSON.stringify(body),{status});
  if(path.includes('/model-config/purposes/assistant'))return reply({purpose:{id:'assistant',machine_id:'box',effective:{provider:'launcher.claude',model:'main-model',effort:'high'},override:{}},providers:[]});
  if(path.includes('/models'))return reply({agent:path.includes('codex')?'codex':'claude',models:[{id:'agent-default',name:'Agent default',default:true,efforts:['low','high'],default_effort:'high'},{id:'test-model',name:'Test model',efforts:['low','high']}]});
  if(path.includes('/launchers')){
   if(catalog==='failed')return reply({error:'Launcher discovery unavailable'},503);
   if(catalog==='missing')return reply({launchers:[]});
   if(catalog==='omitted')return reply({});
   return reply({launchers:[{name:'claude',display_name:'Assistant',agent:'claude'},{name:'codex',display_name:'agents',agent:'codex'}]});
  }
  if(path.includes('/runs'))return reply({runs:[]});
  if(path.includes('/assistant/tasks')){
   const body=JSON.parse(String(init?.body));writes.push(body);
   if(reject)return reply({error:'Directory unavailable'},400);
   saved={...saved,...body};return reply({task:saved});
  }
  throw new Error(`unexpected request ${path}`);
 }));
 host=document.createElement('div');document.body.append(host);root=createRoot(host);
});
afterEach(async()=>{await act(async()=>root.unmount());host.remove();resetManagerChoice();vi.unstubAllGlobals();});
const expand=async(open=true)=>{await act(async()=>{document.querySelector<HTMLDetailsElement>('details')!.open=open;document.querySelector('details')!.dispatchEvent(new Event('toggle'));});await flush();};
const render=async(task=saved,key='initial',expanded=true)=>{await act(async()=>root.render(<><TaskEditor key={key} task={task} onSaved={onSaved} onClose={onClose} onDeleted={()=>{}}/><Toasts/></>));await flush();if(expanded)await expand();};

it('reads execution defaults and catalogs through the same chosen assistant as the task PUT',async()=>{
 chooseManager('other');
 const fetcher=globalThis.fetch;
 const requests:{path:string;manager:string|null;method:string}[]=[];
 vi.stubGlobal('fetch',vi.fn((input:RequestInfo|URL,init?:RequestInit)=>{
  requests.push({path:String(input),manager:new Headers(init?.headers).get(MANAGER_HEADER),method:init?.method??'GET'});
  return fetcher(input,init);
 }));
 await render();
 await change(field(t('assistant-taskeditor.title_label')),'Other assistant task');
 await click(t('common.save'));
 for(const fragment of ['/purposes/assistant','/launchers?purpose=task','/models?','/assistant/tasks/job']){
  const request=requests.find(r=>r.path.includes(fragment));
  expect(request,fragment).toBeTruthy();
  expect(request?.manager).toBe('other');
 }
 expect(requests.find(r=>r.method==='PUT')?.manager).toBe('other');
 expect(writes).toHaveLength(1);
 for(const field of ['launcher','model','effort','execution_machine_id'])expect(writes[0]).not.toHaveProperty(field);
});

it('previews inherited execution above the prompt while collapsed and keeps defaults inherited on save',async()=>{
 await render(saved,'defaults',false);
 const details=document.querySelector<HTMLDetailsElement>('details')!;
 expect(details.open).toBe(false);
 expect(details.querySelector('summary')!.textContent).toContain('Assistant · claude · main-model · high');
 expect(details.compareDocumentPosition(document.querySelector('textarea')!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
 await expand();
 for(const label of ['execution_computer','execution_agent','execution_directory']) expect(field(t(`assistant-taskeditor.${label}`)).value).toBe('');
 expect(field(t('settings-modelconfiguration.effort')).value).toBe('');
 expect(details.textContent).toContain(t('assistant-taskeditor.follow_assistant'));
 await change(field(t('assistant-taskeditor.title_label')),'Another title');
 await click(t('common.save'));
 for(const key of ['launcher','model','effort','cwd','execution_machine_id']) expect(writes[0]).not.toHaveProperty(key);
});

it('saves an agent, a model and a directory without changing Main chat; reopens the saved choices',async()=>{
 await render();
 await change(field(t('assistant-taskeditor.execution_agent')),'codex');
 await expand(false);
 expect(document.querySelector('summary')!.textContent).toContain('agents · codex · agent-default · high');
 await expand();
 const modelButton=[...document.querySelectorAll('button')].find((b)=>b.getAttribute('aria-expanded')==='false'&&b.querySelector('span'))!;
 await act(async()=>modelButton.click());await flush();
 const modelChoice=[...document.querySelectorAll('button')].find((b)=>b.textContent?.includes('Test model'))!;
 expect(modelChoice).toBeTruthy();await act(async()=>modelChoice.click());await flush();
 await change(field(t('settings-modelconfiguration.effort')),'high');
 await expand(false);
 expect(document.querySelector('summary')!.textContent).toContain('agents · codex · test-model · high');
 await expand();
 await change(field(t('assistant-taskeditor.execution_directory')),'/work/project');
 await click(t('common.save'));
 expect(writes).toEqual([expect.objectContaining({launcher:'codex',model:'test-model',effort:'high',cwd:'/work/project',model_revision:0})]);
 expect(onSaved).toHaveBeenCalledOnce();expect(onClose).toHaveBeenCalledOnce();
 await render(saved,'reopen');
 expect(field(t('assistant-taskeditor.execution_agent')).value).toBe('codex');
 expect(field(t('assistant-taskeditor.execution_directory')).value).toBe('/work/project');
 expect(document.body.textContent).toContain('Test model');
});

it('keeps a directory draft after a refused save',async()=>{
 await render();await change(field(t('assistant-taskeditor.execution_directory')),'/work/missing');reject=true;
 await click(t('common.save'));
 expect(onSaved).not.toHaveBeenCalled();expect(onClose).not.toHaveBeenCalled();
 expect(field(t('assistant-taskeditor.execution_directory')).value).toBe('/work/missing');
});

it.each(['model','effort'] as const)('submits only the %s edit and accepts the server-pinned provider and computer',async(kind)=>{
 const fetcher=globalThis.fetch;
 vi.stubGlobal('fetch',vi.fn(async(input:RequestInfo|URL,init?:RequestInit)=>{
  const path=String(input);
  if(path.endsWith('/assistant/tasks/job/run'))return new Response(JSON.stringify({error:'Run unavailable'}),{status:503});
  const response=await fetcher(input,init);
  if(path.endsWith('/assistant/tasks/job')&&init?.method==='PUT'&&writes.length===1){
   saved={...saved,launcher:'claude',execution_machine_id:'box',model_revision:1};
   return new Response(JSON.stringify({task:saved}));
  }
  return response;
 }));
 await render();
 if(kind==='model'){
  const picker=[...document.querySelectorAll('button')].find(b=>b.getAttribute('aria-expanded')==='false'&&b.querySelector('span'))!;
  await act(async()=>picker.click());await flush();
  const choice=[...document.querySelectorAll('button')].find(b=>b.textContent?.includes('Test model'))!;
  await act(async()=>choice.click());await flush();
 }else await change(field(t('settings-modelconfiguration.effort')),'low');
 await click(t('assistant-taskeditor.run_now'));
 expect(writes).toHaveLength(1);
 expect(writes[0]).toMatchObject({[kind]:kind==='model'?'test-model':'low',model_revision:0});
 for(const key of ['launcher','execution_machine_id',kind==='model'?'effort':'model'])expect(writes[0]).not.toHaveProperty(key);
 expect(onSaved).toHaveBeenCalledWith(expect.objectContaining({launcher:'claude',execution_machine_id:'box',model_revision:1}));
 expect(onClose).not.toHaveBeenCalled();
 expect(field(t('assistant-taskeditor.execution_agent')).value).toBe('claude');
 expect(field(t('assistant-taskeditor.execution_computer')).value).toBe('box');
 // Clear from the retained form, without reopening it to refresh the draft.
 await change(field(t('assistant-taskeditor.execution_computer')),'');
 await click(t('common.save'));
 expect(writes[1]).toMatchObject({execution_machine_id:'',model_revision:1});
 for(const key of ['launcher','model','effort'])expect(writes[1]).not.toHaveProperty(key);
 expect(saved.execution_machine_id).toBe('');
});

it.each(['model','effort'] as const)('retains canonical pins after a failed Run now and an unrelated pending %s save edit',async(kind)=>{
 const fetcher=globalThis.fetch;
 let answer!:(response:Response)=>void;
 let moveDefaults=false,runRequests=0;
 vi.stubGlobal('fetch',vi.fn(async(input:RequestInfo|URL,init?:RequestInit)=>{
  const path=String(input);
  if(path.endsWith('/assistant/tasks/job/run')){
   runRequests++;
   return new Response(JSON.stringify({error:'Run unavailable'}),{status:503});
  }
  if(path.includes('/model-config/purposes/assistant')&&moveDefaults)return new Response(JSON.stringify({purpose:{id:'assistant',machine_id:'remote',effective:{provider:'launcher.codex',model:'other-model'},override:{}},providers:[]}));
  const response=await fetcher(input,init);
  if(path.endsWith('/assistant/tasks/job')&&init?.method==='PUT'&&writes.length===1){
   saved={...saved,launcher:'claude',execution_machine_id:'box',model_revision:1,execution:{machine_id:'box',launcher:'claude',model:kind==='model'?'test-model':undefined,effort:kind==='effort'?'low':undefined,cwd:'/work/project'}};
   return new Promise<Response>(resolve=>{answer=resolve;});
  }
  if(path.endsWith('/assistant/tasks/job')&&init?.method==='PUT'&&moveDefaults){
   saved={...saved,execution:{machine_id:saved.execution_machine_id||'remote',launcher:saved.launcher||'codex',model:saved.model,effort:saved.effort,cwd:saved.cwd}};
   return new Response(JSON.stringify({task:saved}));
  }
  return response;
 }));
 await render();
 await change(field(t('assistant-taskeditor.execution_directory')),'/work/project');
 if(kind==='model'){
  const picker=[...document.querySelectorAll('button')].find(b=>b.getAttribute('aria-expanded')==='false'&&b.querySelector('span'))!;
  await act(async()=>picker.click());await flush();
  const choice=[...document.querySelectorAll('button')].find(b=>b.textContent?.includes('Test model'))!;
  await act(async()=>choice.click());await flush();
 }else await change(field(t('settings-modelconfiguration.effort')),'low');
 await click(t('assistant-taskeditor.run_now'));
 expect(writes).toHaveLength(1);
 await change(field(t('assistant-taskeditor.title_label')),'Edited while saving');
 await act(async()=>answer(new Response(JSON.stringify({task:saved}))));await flush();
 expect(onClose).not.toHaveBeenCalled();
 expect(runRequests).toBe(1);
 expect(document.body.textContent).toContain('Run unavailable');
 expect(document.body.textContent).not.toContain(t('assistant-taskeditor.run_started',{title:saved.title}));
 expect(field(t('assistant-taskeditor.title_label')).value).toBe('Edited while saving');
 expect.soft(field(t('assistant-taskeditor.execution_agent')).value).toBe('claude');
 expect.soft(field(t('assistant-taskeditor.execution_computer')).value).toBe('box');
 expect(field(t('assistant-taskeditor.execution_directory')).value).toBe('/work/project');
 // Stay in the same form after defaults move; reopening would hide the stale-draft bug.
 moveDefaults=true;
 await change(field(t('assistant-taskeditor.title_label')),'Edited after failure');
 await click(t('common.save'));
 expect(writes[1]).toMatchObject({title:'Edited after failure'});
 for(const key of ['launcher','model','effort','cwd','execution_machine_id','model_revision'])expect(writes[1]).not.toHaveProperty(key);
 expect(saved).toMatchObject({launcher:'claude',execution_machine_id:'box',model_revision:1,[kind]:kind==='model'?'test-model':'low',cwd:'/work/project'});
 expect(saved.execution).toMatchObject({machine_id:'box',launcher:'claude'});
 expect(runRequests).toBe(1);
 expect(onClose).toHaveBeenCalledOnce();
});

it.each([{launcher:'claude'},{effort:'high'},{launcher:'claude',effort:'high'}])('edits a legacy source task without turning its saved choices into new overrides: %j',async(binding)=>{
 saved={...saved,...binding,session_id:'origin',machine_id:'box'};
 await render();
 await change(field(t('assistant-taskeditor.title_label')),'Updated legacy task');
 const save=()=>[...document.querySelectorAll('button')].find(b=>b.textContent?.trim()===t('common.save'))!;
 expect(save().disabled).toBe(false);
 expect(document.body.textContent).not.toContain(t('assistant-taskeditor.execution_own_chat'));
 await click(t('common.save'));
 expect(writes).toHaveLength(1);
 expect(writes[0]).toMatchObject({title:'Updated legacy task',session_id:'origin'});
 for(const key of ['launcher','model','effort','cwd','execution_machine_id','model_revision'])expect(writes[0]).not.toHaveProperty(key);
 await change(field(t('settings-modelconfiguration.effort')),'low');
 expect(save().disabled).toBe(true);
 expect(document.body.textContent).toContain(t('assistant-taskeditor.execution_own_chat'));
 expect(writes).toHaveLength(1);
});

it('blocks a pinned inherited computer with a source chat and validates toggling the source back on',async()=>{
 saved={...saved,session_id:'origin',machine_id:'box'};
 await render();
 await change(field(t('assistant-taskeditor.execution_computer')),'box');
 const save=()=>[...document.querySelectorAll('button')].find(b=>b.textContent?.trim()===t('common.save'))!;
 expect(save().disabled).toBe(true);
 expect(document.body.textContent).toContain(t('assistant-taskeditor.execution_own_chat'));
 const toggle=()=>document.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t('assistant-taskeditor.resume_chat')}"]`)!;
 await act(async()=>toggle().click());await flush();
 expect(save().disabled).toBe(false);
 await act(async()=>toggle().click());await flush();
 expect(save().disabled).toBe(true);
 expect(writes).toEqual([]);
 await change(field(t('assistant-taskeditor.execution_computer')),'');
 await change(field(t('assistant-taskeditor.title_label')),'Updated source task');
 expect(save().disabled).toBe(false);
 await click(t('common.save'));
 expect(writes[0].session_id).toBe('origin');
 expect(writes[0]).not.toHaveProperty('execution_machine_id');
});

it('saves execution choices for a fresh recurring task while retaining its source chat', async () => {
 const source = { ...saved, repeat: 'interval' as const, every_minutes: 30, session_id: 'origin', machine_id: 'box' };
 await render(source);
 await change(field(t('assistant-taskeditor.execution_computer')), 'box');
 await change(field(t('assistant-taskeditor.execution_directory')), '/work/project');
 const save = () => [...document.querySelectorAll('button')].find(b => b.textContent?.trim() === t('common.save'))!;
 expect(save().disabled).toBe(true);
 const fresh = field(t('assistant-taskeditor.fresh_each_run'));
 await act(async () => fresh.click());
 await flush();
 expect(save().disabled).toBe(false);
 expect(document.body.textContent).not.toContain(t('assistant-taskeditor.execution_own_chat'));
 await click(t('common.save'));
 expect(writes[0]).toMatchObject({ session_id: 'origin', fresh_each_run: true, execution_machine_id: 'box', cwd: '/work/project', model_revision: 0 });
 await act(async () => fresh.click());
 await flush();
 expect(save().disabled).toBe(true);
});

it('lets a historical source task clear a saved execution choice back to inheritance',async()=>{
 saved={...saved,session_id:'origin',execution_machine_id:'box'};
 await render();
 await change(field(t('assistant-taskeditor.title_label')),'Updated source task');
 const save=[...document.querySelectorAll('button')].find(b=>b.textContent?.trim()===t('common.save'))!;
 expect(save.disabled).toBe(true);
 await change(field(t('assistant-taskeditor.execution_computer')),'');
 expect(save.disabled).toBe(false);
 await click(t('common.save'));
 expect(writes[0]).toMatchObject({session_id:'origin',execution_machine_id:''});
});

it.each(['missing','failed','omitted'] as const)('clears a saved launcher to inheritance with a %s catalog and reopens the cleared values',async(status)=>{
 catalog=status;
 saved={...saved,session_id:'origin',launcher:'unavailable-agent',model:'saved-model',effort:'high'};
 await render();
 const agent=field(t('assistant-taskeditor.execution_agent')) as HTMLSelectElement;
 expect(agent.value).toBe('unavailable-agent');
 expect(agent.selectedOptions[0].textContent).toContain(t('assistant-taskeditor.execution_unavailable'));
 const save=()=>[...document.querySelectorAll('button')].find(b=>b.textContent?.trim()===t('common.save'))!;
 expect(save().disabled).toBe(true);
 expect(writes).toEqual([]);
 expect(agent.disabled).toBe(false);
 if(status==='failed')expect(document.body.textContent).toContain('Launcher discovery unavailable');
 await change(agent,'');
 expect(save().disabled).toBe(false);
 expect(document.querySelector('summary')!.textContent).toContain('claude · main-model · high');
 await click(t('common.save'));
 expect(writes).toEqual([expect.objectContaining({session_id:'origin',launcher:'',model:'',effort:''})]);
 expect(onSaved).toHaveBeenCalledOnce();
 await render(saved,'reopen-cleared');
 expect(field(t('assistant-taskeditor.execution_agent')).value).toBe('');
 expect(field(t('settings-modelconfiguration.effort')).value).toBe('');
 expect(document.body.textContent).not.toContain('saved-model');
 expect(document.body.textContent).not.toContain('unavailable-agent');
});

it('preserves saved launcher, model and effort after discovery fails until the user clears them',async()=>{
 catalog='failed';
 saved={...saved,launcher:'codex',model:'saved-model',effort:'high'};
 await render();
 expect(field(t('assistant-taskeditor.execution_agent')).value).toBe('codex');
 await change(field(t('assistant-taskeditor.title_label')),'Updated title');
 await click(t('common.save'));
 for(const key of ['launcher','model','effort'])expect(writes[0]).not.toHaveProperty(key);
 expect(saved).toMatchObject({launcher:'codex',model:'saved-model',effort:'high'});
});

it.each([[404,false],[200,false],[503,false],[404,true]] as const)('edits and saves an unknown model with catalog status %s (delayed: %s)',async(status,delayed)=>{
 const fetcher=globalThis.fetch;
 let releaseCatalog:(response:Response)=>void=()=>{};
 const response=()=>new Response(JSON.stringify(status===200?{agent:'claude',models:[]}:{error:'No model catalog'}),{status});
 const catalogResponse=delayed?new Promise<Response>(resolve=>{releaseCatalog=resolve;}):Promise.resolve(response());
 vi.stubGlobal('fetch',vi.fn((input:RequestInfo|URL,init?:RequestInit)=>String(input).includes('/models')
  ? catalogResponse
  : fetcher(input,init)));
 saved={...saved,launcher:'claude',model:'saved-unknown',effort:'low',execution_machine_id:'box',model_revision:4};
 await render();
 if(delayed){
  expect(document.querySelector<HTMLFieldSetElement>('fieldset')!.disabled).toBe(true);
  await change(field(t('assistant-taskeditor.title_label')),'Edited while catalog loads');
  await act(async()=>releaseCatalog(response()));await flush();
 }
 expect(document.body.textContent).toContain('saved-unknown');
 expect(field(t('settings-modelconfiguration.effort')).value).toBe('low');
 const picker=document.querySelector<HTMLButtonElement>('fieldset button[aria-expanded]')!;
 expect(picker.closest('fieldset')!.disabled).toBe(false);
 await act(async()=>picker.click());await flush();
 const search=document.querySelector<HTMLInputElement>(`input[aria-label="${t('ui-modelpicker.search_label')}"]`)!;
 await change(search,'manual-model');
 const choice=[...document.querySelectorAll<HTMLButtonElement>('button[role="option"]')].find(b=>b.textContent?.includes('manual-model'))!;
 expect(choice).toBeTruthy();await act(async()=>choice.click());await flush();
 await click(t('common.save'));
 expect(writes).toEqual([expect.objectContaining({model:'manual-model',model_revision:4})]);
 for(const key of ['launcher','effort','execution_machine_id'])expect(writes[0]).not.toHaveProperty(key);
 expect(saved).toMatchObject({model:'manual-model',launcher:'claude',effort:'low',execution_machine_id:'box'});
 expect(onClose).toHaveBeenCalledOnce();
});
