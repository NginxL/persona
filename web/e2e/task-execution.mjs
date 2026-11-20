// Scheduled execution form with synthetic HTTP/WS. Never connects to a user's hub or a model.
// node web/e2e/task-execution.mjs <loopback web URL> --shots <directory> --chromium <executable>
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";
const args=process.argv.slice(2);
const flag=(name)=>args[args.indexOf(name)+1];
const base=args[0], shots=flag("--shots");
assert(["localhost","127.0.0.1"].includes(new URL(base).hostname));
mkdirSync(shots,{recursive:true});
const watchdog=setTimeout(()=>{console.error("TIMEOUT task execution verification");process.exit(124);},180_000);
const browser=await chromium.launch({executablePath:flag("--chromium"),headless:true,args:["--no-sandbox"]});
try {
 for(const width of [1280,390]) for(const theme of ["light","dark"]) {
  const manager=theme==="dark"?"other":"assistant";
  const context=await browser.newContext({viewport:{width,height:900},colorScheme:theme,reducedMotion:"reduce"});
  await context.addInitScript(({theme,manager})=>{
   localStorage.setItem("persona.token","synthetic-task-token");localStorage.setItem("persona.language","en");localStorage.setItem("persona.theme",theme);
   if(manager!=="assistant")localStorage.setItem("persona.currentManager",manager);
  },{theme,manager});
  let task={id:"job",title:"Review project files",prompt:"Summarize the project files.",repeat:"interval",every_minutes:30,enabled:true,model_revision:0};
  let defaultsMoved=false,holdSave=false,releaseSave,sawSave,failRun=false,runRequests=0;
  const viewTask=()=>({...task,execution:{machine_id:task.execution_machine_id||(defaultsMoved?"remote":"box"),launcher:task.launcher||(defaultsMoved?"codex":"assistant"),agent:task.launcher==="codex"?"codex":"claude",model:task.model||"main-model",effort:task.effort||"high"}});
  const writes=[], reads=[];
  let launcherCatalog="available", modelCatalogStatus=200;
  let holdRemoteModels=false, releaseModels, sawHeldModels;
  const heldModelRead=new Promise(resolve=>{sawHeldModels=resolve;});
  await context.route("**/api/v1/**",async(route)=>{
   const req=route.request(),url=new URL(req.url()),path=url.pathname.replace(/^\/api\/v1/,"");
   reads.push(`${req.method()} ${path}`);
   if(path==="/model-config/purposes/assistant"||/^\/machines\/[^/]+\/launchers$/.test(path)||path.startsWith("/model-config/providers/")||path.startsWith("/assistant/tasks")) {
    assert.equal(req.headers()["x-persona-manager"]||"assistant",manager,`wrong assistant for ${req.method()} ${path}`);
   }
   if(/^\/machines\/[^/]+\/launchers$/.test(path))assert.equal(url.searchParams.get("purpose"),"task");
   let body={};
   if(path==="/machines")body={machines:[{id:"box",name:"Example computer",online:true},{id:"remote",name:"Other computer",online:true}]};
   else if(path==="/sessions")body={sessions:[]};
   else if(path==="/assistant")body={machine_id:"box",main:"",conversations:[],tasks:[],memory:[]};
   else if(path==="/assistant/tasks")body={tasks:[viewTask()]};
   else if(path==="/assistant/runs")body={runs:[]};
   else if(path==="/model-config")body={providers:[{id:"launcher.assistant",name:"Assistant",agent:"claude"},{id:"launcher.codex",name:"agents",agent:"codex"}]};
   else if(path==="/model-config/purposes/assistant")body={purpose:{id:"assistant",machine_id:defaultsMoved?"remote":"box",effective:{provider:defaultsMoved?"launcher.codex":"launcher.assistant",model:"main-model",effort:"high"},override:{}},providers:[]};
   else if(path==="/machines/box/launchers") {
    if(launcherCatalog==="failed") {
     await route.fulfill({status:503,contentType:"application/json",body:JSON.stringify({error:"Launcher discovery unavailable"})});return;
    }
    body={launchers:launcherCatalog==="missing"?[]:[{name:"assistant",display_name:"Assistant",agent:"claude"},{name:"codex",display_name:"agents",agent:"codex"}],default:{provider:"assistant",model:"main-model",effort:"high"}};
   }
   else if(path==="/machines/remote/launchers")body={launchers:[{name:"codex",display_name:"Remote agent",agent:"codex"}],default:{provider:"codex"}};
   else if(path.startsWith("/model-config/providers/")) {
    if(modelCatalogStatus===404){
     await route.fulfill({status:404,contentType:"application/json",body:JSON.stringify({error:"No model catalog"})});return;
    }
    if(holdRemoteModels&&url.searchParams.get("machine")==="remote"&&path.includes("launcher.codex/")) {
     await new Promise(resolve=>{
      releaseModels=async()=>{await route.fulfill({status:503,contentType:"application/json",body:JSON.stringify({code:"internal",retryable:false,error:"Current catalog unavailable"})});resolve();};
      sawHeldModels();
     });return;
    }
    body={agent:path.includes("codex")?"codex":"claude",models:[{id:"test-model",name:"Example model",efforts:["low","high"],default_effort:"high"}]};
   }
   else if(path==="/assistant/tasks/job"&&req.method()==="PUT") {
    const patch=req.postDataJSON();writes.push({path,patch});task={...task,...patch,model_revision:1};
    if(holdSave){
     holdSave=false;task={...task,launcher:"assistant",execution_machine_id:"box"};
     await new Promise(resolve=>{releaseSave=resolve;sawSave();});
    }
    body={task:viewTask()};
   }
   else if(path==="/assistant/tasks/job/run"&&failRun){
    runRequests++;
    await route.fulfill({status:503,contentType:"application/json",body:JSON.stringify({error:"Run unavailable"})});return;
   }
   else if(path==="/extensions/ui")body={slots:[]};
   else if(path==="/extensions")body={extensions:[]};
   else if(path==="/managers")body={default:"assistant",managers:[{id:"assistant",name:"Default assistant",default:true,builtin:true,online:true},{id:"other",name:"Other assistant",online:true}]};
   if(!["GET","HEAD"].includes(req.method())&&!path.startsWith("/assistant/tasks/job"))throw new Error(`unexpected write ${req.method()} ${path}`);
   await route.fulfill({contentType:"application/json",body:JSON.stringify(body)});
  });
  await context.routeWebSocket("**/api/v1/ws**",ws=>{ws.send(JSON.stringify({type:"hello",server_version:"synthetic",features:["task_execution","task_cron"]}));ws.onMessage(raw=>{if(JSON.parse(String(raw)).type==="app_ping")ws.send(JSON.stringify({type:"pong"}));});});
  const page=await context.newPage();const errors=[];page.on("pageerror",e=>errors.push(String(e)));page.setDefaultTimeout(10_000);
  await page.goto(`${base}/routines`);
  await page.getByRole("button",{name:/Review project files/}).first().getByText("Assistant · claude · main-model · high",{exact:true}).waitFor().catch(async(error)=>{
   await page.screenshot({path:join(shots,`task-load-failed-${width}-${theme}.png`)});
   console.error(JSON.stringify({errors,reads,text:await page.locator("body").innerText()}));
   throw error;
  });
  await page.getByRole("button",{name:/Review project files/}).first().click();
  const disclosure=page.locator("summary").filter({hasText:"Execution options"});
  await page.getByRole("radio",{name:"Cron",exact:true}).waitFor();
  await disclosure.getByText("Assistant · claude · main-model · high",{exact:true}).waitFor();
  assert(await disclosure.evaluate((el)=>!!(el.compareDocumentPosition(document.querySelector('textarea'))&Node.DOCUMENT_POSITION_FOLLOWING)));
  await page.getByText("Execution options",{exact:true}).click();
  await page.getByRole("combobox",{name:"Agent",exact:true}).selectOption("codex");
  const dialog=page.getByRole("dialog");
  const closed=async()=>{
   await dialog.waitFor({state:"hidden"});
   await page.waitForFunction(()=>!history.state?.personaOverlay);
  };
  // Inspect the actual rendered model picker and choose the advertised model.
  await dialog.getByRole("button",{expanded:false}).filter({hasText:/Default|model/i}).first().click();
  await dialog.getByRole("option",{name:/Example model/}).click();
  await page.getByRole("combobox",{name:"Reasoning effort",exact:true}).selectOption("high");
  await page.getByLabel("Working directory",{exact:false}).fill("/work/project");
  await dialog.getByRole("button",{name:"Save",exact:true}).click();
  assert.equal(writes.length,1);assert.deepEqual({launcher:writes[0].patch.launcher,model:writes[0].patch.model,effort:writes[0].patch.effort,cwd:writes[0].patch.cwd},{launcher:"codex",model:"test-model",effort:"high",cwd:"/work/project"});
  await page.getByRole("button",{name:/Review project files/}).first().getByText("agents · codex · test-model · high",{exact:true}).waitFor();
  await page.getByText("Saved",{exact:true}).waitFor({state:"hidden"});
  await page.screenshot({path:join(shots,`task-list-${width}-${theme}.png`)});
  await page.getByRole("button",{name:/Review project files/}).first().click();
  await page.getByText("Execution options",{exact:true}).click();
  assert.equal(await page.getByRole("combobox",{name:"Agent",exact:true}).inputValue(),"codex");
  assert.equal(await page.getByLabel("Working directory",{exact:false}).inputValue(),"/work/project");
  await page.getByLabel("Working directory",{exact:false}).scrollIntoViewIfNeeded();
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>document.documentElement.clientWidth),false);
  await page.getByText("Saved",{exact:true}).waitFor({state:"hidden"});
  await page.getByText("Execution options",{exact:true}).click();
  await disclosure.getByText("agents · codex · test-model · high",{exact:true}).waitFor();
  const oneLine=await disclosure.evaluate(el=>{
   const [label,detail]=el.querySelectorAll('span');
   const a=label.getBoundingClientRect(),b=detail.getBoundingClientRect();
   return Math.abs((a.top+a.bottom-b.top-b.bottom)/2)<1 && el.scrollWidth<=el.clientWidth;
  });
  assert(oneLine,"execution title and summary share one line");
  await disclosure.scrollIntoViewIfNeeded();
  await page.screenshot({path:join(shots,`task-execution-${width}-${theme}.png`)});
  await page.getByText("Execution options",{exact:true}).click();
  // Stress the form at twice the standard text size; controls remain reachable in its own scroller.
  await page.evaluate(()=>document.documentElement.style.setProperty("--text-scale","2"));
  await page.getByLabel("Working directory",{exact:false}).scrollIntoViewIfNeeded();
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>document.documentElement.clientWidth),false);
  const overflow=await dialog.evaluate((el)=>{
   const box=el.getBoundingClientRect();
   return [...el.querySelectorAll('input,select,fieldset,button[aria-expanded]')].filter((n)=>{const r=n.getBoundingClientRect();return r.width>0&&(r.left<box.left-1||r.right>box.right+1);}).map(n=>n.outerHTML);
  });
  assert.deepEqual(overflow,[],"form controls stay inside the dialog at twice the text size");
  await page.screenshot({path:join(shots,`task-execution-${width}-${theme}-large.png`)});
  assert.equal(reads.some(s=>s.includes("launcher.codex/models")),true);
  // A historical source task may clear its saved computer pin, but cannot keep it while continuing that chat.
  task={...task,title:"Review project files",session_id:"origin",execution_machine_id:"box",launcher:"",model:"",effort:"",cwd:""};
  await page.goto(`${base}/routines`);
  await page.getByRole("button",{name:/Review project files/}).first().click();
  await page.getByText("Execution options",{exact:true}).click();
  await page.getByRole("textbox",{name:"Title",exact:true}).fill("Updated source task");
  const saveSource=page.getByRole("dialog").getByRole("button",{name:"Save",exact:true});
  assert(await saveSource.isDisabled(),"source chat and a computer pin cannot be saved together");
  const sourceSwitch=page.getByRole("switch",{name:"Continue the original chat",exact:true});
  await sourceSwitch.click();
  assert(await saveSource.isEnabled());
  await sourceSwitch.click();
  assert(await saveSource.isDisabled(),"restoring the source revalidates saved execution values");
  const originWarning=page.getByRole("alert").filter({hasText:"Turn off “Continue the original chat”"}).first();
  await originWarning.scrollIntoViewIfNeeded();
  await page.screenshot({path:join(shots,`task-origin-${width}-${theme}.png`)});
  await page.evaluate(()=>document.documentElement.style.setProperty("--text-scale","2"));
  await originWarning.scrollIntoViewIfNeeded();
  assert(await saveSource.isDisabled());
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>document.documentElement.clientWidth),false);
  await page.screenshot({path:join(shots,`task-origin-${width}-${theme}-large.png`)});
  await page.evaluate(()=>document.documentElement.style.setProperty("--text-scale","1"));
  assert.equal(writes.length,1,"invalid source edits must not send a save");
  await page.getByRole("combobox",{name:"Computer",exact:true}).selectOption("");
  assert(await saveSource.isEnabled());
  await saveSource.click();
  assert.equal(writes.length,2);
  assert.equal(writes[1].patch.session_id,"origin");
  assert.equal(writes[1].patch.execution_machine_id,"");
  await closed(); // a phone sheet removes its history entry asynchronously before another navigation
  for(const catalog of ["missing","failed"]) {
   launcherCatalog=catalog;
   task={...task,session_id:"origin",execution_machine_id:"",launcher:"gone-agent",model:"saved-model",effort:"high",cwd:""};
   const count=writes.length;
   await page.goto(`${base}/routines`);
   await page.getByRole("button",{name:/Updated source task/}).first().click();
   await page.getByText("Execution options",{exact:true}).click();
   const agent=page.getByRole("combobox",{name:"Agent",exact:true});
   assert(await agent.isEnabled(),"inheritance remains selectable when discovery is unavailable");
   assert.equal(await agent.inputValue(),"gone-agent");
   await agent.getByRole("option",{selected:true}).filter({hasText:/Unavailable/i}).waitFor({state:"attached"});
   const save=page.getByRole("dialog").getByRole("button",{name:"Save",exact:true});
   assert(await save.isDisabled(),"source execution validation remains active");
   if(catalog==="failed")await page.getByRole("alert").filter({hasText:"Launcher discovery unavailable"}).waitFor();
   assert.equal(writes.length,count,"discovery cannot clear saved choices");
   await agent.scrollIntoViewIfNeeded();
   await page.screenshot({path:join(shots,`task-launcher-${catalog}-${width}-${theme}.png`)});
   await page.evaluate(()=>document.documentElement.style.setProperty("--text-scale","2"));
   await agent.scrollIntoViewIfNeeded();
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>document.documentElement.clientWidth),false);
   await page.screenshot({path:join(shots,`task-launcher-${catalog}-${width}-${theme}-large.png`)});
   await agent.selectOption("");
   assert(await save.isEnabled());
   await save.click();
   await closed();
   assert.equal(writes.length,count+1);
   assert.deepEqual({launcher:writes[count].patch.launcher,model:writes[count].patch.model,effort:writes[count].patch.effort},{launcher:"",model:"",effort:""});
   await page.getByRole("button",{name:/Updated source task/}).first().click();
   await page.getByText("Execution options",{exact:true}).click();
   assert.equal(await agent.inputValue(),"");
   await page.getByRole("dialog").getByRole("button",{name:"Cancel",exact:true}).click();
   await closed();
  }
  launcherCatalog="available";
  task={...task,session_id:""};
  await page.goto(`${base}/routines`);
  await page.getByRole("button",{name:/Updated source task/}).first().click();
  await page.getByText("Execution options",{exact:true}).click();
  const modelField=page.getByRole("dialog").locator("fieldset").filter({has:page.locator("button[aria-expanded]")});
  await modelField.locator("button[aria-expanded]:enabled").waitFor();
  holdRemoteModels=true;
  const beforeSwitch=writes.length;
  await page.getByRole("combobox",{name:"Computer",exact:true}).selectOption("remote");
  await heldModelRead;
  assert(await modelField.locator("button[aria-expanded]").isDisabled(),"model picking waits for the current computer's catalog");
  assert.equal(await modelField.getByRole("option",{name:/Example model/}).count(),0);
  assert.equal(await page.locator("summary").filter({hasText:"Execution options"}).getByText(/main-model/).count(),0,"another computer's inherited model is not previewed");
  await modelField.scrollIntoViewIfNeeded();
  await page.screenshot({path:join(shots,`task-model-loading-${width}-${theme}.png`)});
  await page.evaluate(()=>document.documentElement.style.setProperty("--text-scale","2"));
  await modelField.scrollIntoViewIfNeeded();
  await page.screenshot({path:join(shots,`task-model-loading-${width}-${theme}-large.png`)});
  await releaseModels();
  await page.getByRole("alert").filter({hasText:"HTTP 503"}).first().waitFor().catch(async(error)=>{
   await page.screenshot({path:join(shots,`task-model-unsettled-${width}-${theme}.png`)});
   console.error(JSON.stringify({reads:reads.slice(-12),text:await page.locator("body").innerText()}));throw error;
  });
  assert(await page.getByRole("combobox",{name:"Agent",exact:true}).isEnabled(),"catalog failure retains the inheritance recovery control");
  assert.equal(await modelField.getByRole("option",{name:/Example model/}).count(),0);
  assert.equal(writes.length,beforeSwitch,"a discovery failure does not save another context's choice");
  await modelField.scrollIntoViewIfNeeded();
  await page.screenshot({path:join(shots,`task-model-failed-${width}-${theme}-large.png`)});
  await page.evaluate(()=>document.documentElement.style.setProperty("--text-scale","1"));
  await modelField.scrollIntoViewIfNeeded();
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>document.documentElement.clientWidth),false);
  await page.screenshot({path:join(shots,`task-model-failed-${width}-${theme}.png`)});
  await dialog.getByRole("button",{name:"Cancel",exact:true}).click();await closed();
  holdRemoteModels=false;
  modelCatalogStatus=404;
  task={...task,title:"Manual model task",session_id:"",launcher:"codex",model:"saved-unknown",effort:"low",execution_machine_id:"box"};
  await page.goto(`${base}/routines`);
  await page.getByRole("button",{name:/Manual model task/}).first().click();
  await page.getByText("Execution options",{exact:true}).click();
  const manualPicker=modelField.locator("button[aria-expanded]:enabled");
  await manualPicker.waitFor();
  assert((await manualPicker.innerText()).includes("saved-unknown"),"404 preserves the saved unknown model");
  assert.equal(await page.getByRole("combobox",{name:"Reasoning effort",exact:true}).inputValue(),"low");
  await manualPicker.click();
  await page.getByRole("textbox",{name:"Search models",exact:true}).fill("manual-model");
  await dialog.getByRole("option",{name:/manual-model/}).click();
  const manualCount=writes.length;
  await dialog.getByRole("button",{name:"Save",exact:true}).click();await closed();
  assert.equal(writes.length,manualCount+1);assert.equal(writes[manualCount].patch.model,"manual-model");
  for(const key of ["launcher","effort","execution_machine_id"])assert.equal(key in writes[manualCount].patch,false);
  modelCatalogStatus=200;
  task={id:"job",title:"Failed run draft",prompt:"Check the files.",repeat:"interval",every_minutes:30,enabled:true,model_revision:0};
  await page.goto(`${base}/routines`);
  await page.getByRole("button",{name:/Failed run draft/}).first().click();
  await page.getByText("Execution options",{exact:true}).click();
  await page.getByRole("combobox",{name:"Reasoning effort",exact:true}).selectOption("low");
  const title=page.getByRole("textbox",{name:"Title",exact:true});
  const count=writes.length;
  const pendingSave=new Promise(resolve=>{sawSave=resolve;});holdSave=true;failRun=true;
  await dialog.getByRole("button",{name:"Run now",exact:true}).click();await pendingSave;
  assert(await page.getByRole("combobox",{name:"Computer",exact:true}).isDisabled());
  await title.fill("Edited while saving");releaseSave();
  await page.getByText(/Run unavailable|HTTP 503/).first().waitFor();
  assert.equal(await title.inputValue(),"Edited while saving");
  assert.equal(await page.getByRole("combobox",{name:"Computer",exact:true}).inputValue(),"box");
  assert.equal(await page.getByRole("combobox",{name:"Agent",exact:true}).inputValue(),"assistant");
  assert.equal(runRequests,1);assert.equal(writes[count].patch.effort,"low");
  for(const key of ["launcher","execution_machine_id"])assert.equal(key in writes[count].patch,false);
  for(const scale of [1,2]){
   await page.evaluate(scale=>document.documentElement.style.setProperty("--text-scale",String(scale)),scale);
   await page.getByRole("combobox",{name:"Computer",exact:true}).scrollIntoViewIfNeeded();
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>document.documentElement.clientWidth),false);
   await page.screenshot({path:join(shots,`task-failed-run-pins-${width}-${theme}${scale===2?"-large":""}.png`)});
  }
  defaultsMoved=true;
  await title.fill("Edited after failed run");
  // On the phone, the recoverable error toast can cover Save; dismiss it through its visible control.
  const failureToast=page.locator('[aria-live="polite"] > div').filter({hasText:/Run unavailable|HTTP 503/});
  await failureToast.getByRole("button",{name:"Close",exact:true}).click();
  await failureToast.waitFor({state:"hidden"});
  await dialog.getByRole("button",{name:"Save",exact:true}).click();await closed();
  assert.equal(writes.length,count+2);
  for(const key of ["launcher","model","effort","cwd","execution_machine_id","model_revision"])assert.equal(key in writes[count+1].patch,false,`unrelated save must retain ${key}`);
  assert.equal(task.execution_machine_id,"box");assert.equal(task.launcher,"assistant");assert.equal(runRequests,1);
  // A legacy source task's inert launcher/effort metadata must not block an unrelated title save.
  task={id:"job",title:"Legacy source task",prompt:"Check the files.",repeat:"interval",every_minutes:30,enabled:true,model_revision:1,session_id:"origin",machine_id:"box",launcher:"assistant",effort:"high"};
  await page.goto(`${base}/routines`);
  await page.getByRole("button",{name:/Legacy source task/}).first().click();
  await page.getByText("Execution options",{exact:true}).click();
  await title.fill("Updated legacy source task");
  const legacyCount=writes.length;
  assert(await saveSource.isEnabled(),"unchanged legacy fields permit a title edit");
  assert.equal(await page.getByRole("alert").filter({hasText:"Turn off “Continue the original chat”"}).count(),0);
  for(const scale of [1,2]){
   await page.evaluate(scale=>document.documentElement.style.setProperty("--text-scale",String(scale)),scale);
   await page.getByRole("combobox",{name:"Agent",exact:true}).scrollIntoViewIfNeeded();
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>document.documentElement.clientWidth),false);
   await page.screenshot({path:join(shots,`task-legacy-source-${width}-${theme}${scale===2?"-large":""}.png`)});
  }
  const legacyEffort=page.getByRole("combobox",{name:"Reasoning effort",exact:true});
  await legacyEffort.selectOption("low");
  assert(await saveSource.isDisabled(),"a new source effort choice is still refused");
  await legacyEffort.selectOption("high");
  assert(await saveSource.isEnabled());
  await saveSource.click();await closed();
  assert.equal(writes.length,legacyCount+1);assert.equal(writes[legacyCount].patch.session_id,"origin");
  for(const key of ["launcher","model","effort","cwd","execution_machine_id","model_revision"])assert.equal(key in writes[legacyCount].patch,false,`legacy title save must preserve ${key}`);
  assert.equal(task.launcher,"assistant");assert.equal(task.effort,"high");assert.equal(runRequests,1);
  assert.deepEqual(errors,[]);
  console.log(`PASS ${width} ${theme} ${manager}: matching discovery/save scope, select/save/reopen, text scaling, source validation/toggle/clear, missing/failed launcher clear, computer catalog loading/failure, real 404 manual model save, failed Run now canonical pins with pending title edit and sparse resave without reopening, legacy source title save and changed-effort refusal`);
  await context.close();
 }
} finally {await browser.close();clearTimeout(watchdog);}
