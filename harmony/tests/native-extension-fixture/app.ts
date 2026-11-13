import {main, componentPage, type Program, type View} from "../../../sdk/ts/src/index.ts";
import {createElement as h, View as NativeView, Section, Text, Button, Field, callback, useState} from "../../../sdk/ts/src/ui.ts";
import manifest from "./persona-extension.json";
function StreamRoot() {
  const [revision,setRevision]=useState(0), [saved,setSaved]=useState("");
  return h(NativeView,{key:"stream-root",title:"Native Stream Check",children:h(Section,{key:"stream-section",children:[
    h(Text,{key:"stream-count",text:"Stream revision: "+revision}),
    h(Field,{key:"stream-note",name:"note",kind:"text",label:"Stream note",value:saved}),
    h(Button,{key:"stream-step",title:"Advance "+revision,action:callback(()=>setRevision(n=>n+1))}),
    h(Button,{key:"stream-save",title:"Save note",action:callback(values=>{setSaved(values.note||"");setRevision(n=>n+1);},{submit:true,confirm:"Save this synthetic stream note?"})}),
    h(Text,{key:"stream-saved",text:"Saved: "+saved})
  ]})});
}
export function program(): Program {
  let count=0, level=20, enabled=true, note="";
  const home=():View=>({title:"Native UI Check",sections:[{key:"controls",items:[
    {type:"row",key:"level",title:"Level",control:{kind:"slider",name:"level",value:level,min:0,max:100,step:1,action:{id:"level"}}},
    {type:"row",key:"enabled",title:"Enabled",control:{kind:"switch",name:"enabled",on:enabled,action:{id:"enabled"}}},
    {type:"field",key:"note",kind:"text",name:"note",label:"Note",value:note},
    {type:"button",key:"apply",title:"Apply "+count,action:{id:"apply",submit:true,confirm:"Apply this synthetic change?"}},
    {type:"text",key:"receipt",text:"Native actions: "+count},
    {type:"row",title:"Unkeyed "+count,value:String(count)},
    {type:"code",key:"code",path:"proof.ts",language:"ts",text:"const nativeRevision = "+count+";"},
    {type:"table",key:"table",columns:[{title:"Metric"},{title:"Value"}],rows:[["Revision",String(count)]]},
    {type:"chart",key:"chart",kind:"line",series:[{name:"Load",points:[{label:"Night",value:-2.5-count},{label:"Day",value:3+count}]}]},
    {type:"diff",key:"file-a",path:"fixture-a.ts",status:"modified",context:{id:"context"},hunks:[{old_start:1000,new_start:1000,lines:[" old line","+new line"]}]},
    {type:"diff",key:"file-b",path:"fixture-b.ts",status:"modified",context:{id:"context"},hunks:[{old_start:10,new_start:10,lines:[" old line","+new line"]}]}
  ]}]});
  return {id:manifest.id,version:manifest.version,manifest,ui:{"harmony-native-stream":componentPage(request=>{if(request.transport!=="stream")throw new Error("native_stream_required");return h(StreamRoot,{key:"root"});}),"harmony-native-check":{render:home,action(r){
    if(r.action==="context")return {lines:{path:r.values?.path||"fixture-a.ts",side:r.values?.side==="old"?"old":"new",start:1,lines:Array.from({length:1500},(_,i)=>"Synthetic line "+(i+1)),total:1500}};
    count++; if(r.action==="level")level=Number(r.values?.level)||0;
    if(r.action==="enabled")enabled=r.values?.enabled==="true";
    if(r.action==="apply")note=r.values?.note||"";
    return {view:home()};
  }},"harmony-native-check-today":{render:()=>({sections:[{items:[{type:"row",key:"entry",title:"Native UI Check",detail:""}]}]})}}};
}

main(program());
