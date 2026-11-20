// Sessions active in a time window (docs/integrations/runtimes.md "History"): the window, the order and the cursor
// every runtime's listHistory shares. Kept compact: this file is embedded in personad's runtime packages.
import{RpcError}from"./wire.js";
export const HISTORY_LIMIT=200,HISTORY_MAX=500;
// A row's time: when it was last active (active_at), else updated_at, else started_at. Deleted ones are in no window.
export const historyAt=r=>Date.parse(r?.active_at)||Date.parse(r?.updated_at)||Date.parse(r?.started_at)||0;
const deleted=r=>r?.status==="deleted"||r?.deleted===!0;
const bad=m=>new RpcError(m,"invalid");
/** sessions.history's params checked: since / until in ms (until defaults to now), limit within 1…500. */
export function historyParams(p={}){const since=Date.parse(p.since);if(typeof p.since!="string"||Number.isNaN(since))throw bad("since must be an RFC 3339 time");
let until=Date.now();if(p.until!=null&&p.until!==""){until=Date.parse(p.until);if(typeof p.until!="string"||Number.isNaN(until))throw bad("until must be an RFC 3339 time")}
let limit=HISTORY_LIMIT;if(p.limit!=null){limit=Math.trunc(Number(p.limit));if(!(limit>=1))throw bad("limit must be a positive number");limit=Math.min(limit,HISTORY_MAX)}
const cursor=typeof p.cursor=="string"?p.cursor:"";return{since,until,limit,cursor}}
function decode(c){if(!c)return;try{const v=JSON.parse(Buffer.from(c,"base64url").toString());if(typeof v.t=="number"&&typeof v.k=="string")return v}catch{}throw bad("malformed cursor")}
/**
 * One page of rows in [since, until) by active_at (else updated_at, else started_at), newest first (ties by
 * session_id), after cursor; deleted rows (status "deleted") left out. complete: whether the rows given are all the runtime has for the window (default true).
 * @param {object[]} rows
 * @param {{since: string, until?: string, cursor?: string, limit?: number}} params
 * @param {{complete?: boolean, id?: (row: object) => string, updatedAt?: (row: object) => number}} [o]
 * @returns {{rows: object[], next_cursor: string, complete: boolean}}
 */
export function historyPage(rows,params,o={}){const{since,until,limit,cursor}=historyParams(params),after=decode(cursor),id=o.id??(r=>String(r?.session_id??"")),t=o.updatedAt??historyAt;
const list=[];for(const r of rows??[]){const k=id(r),u=t(r);if(!k||!u||u<since||u>=until||deleted(r))continue;if(after&&(u>after.t||u===after.t&&k<=after.k))continue;list.push({r,u,k})}
list.sort((a,b)=>b.u-a.u||(a.k<b.k?-1:a.k>b.k?1:0));const page=list.slice(0,limit),last=page[page.length-1];
return{rows:page.map(x=>x.r),next_cursor:list.length>limit?Buffer.from(JSON.stringify({t:last.u,k:last.k})).toString("base64url"):"",complete:o.complete!==!1}}
/** What a driver's listHistory answered, as sessions.history returns it. */
export function historyResult(res){return{rows:Array.isArray(res?.rows)?res.rows.filter(r=>r&&typeof r.session_id=="string"&&r.session_id&&!deleted(r)):[],next_cursor:typeof res?.next_cursor=="string"?res.next_cursor:"",complete:res?.complete===!0}}
