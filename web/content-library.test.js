const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
function load(){const sandbox={URLSearchParams};vm.runInNewContext(fs.readFileSync(__dirname+'/content-library.js','utf8'),sandbox);return sandbox.ThoughtFlowLibrary;}
test('fixed content links preserve exact kind, version, member and section',()=>{
 const ui=load();const url=ui.route('set-1',{kind:'archive',id:'run-2'},'module:订单','错误处理');
 const q=new URLSearchParams(url.split('?')[1]);
 assert.equal(q.get('set'),'set-1');assert.equal(q.get('kind'),'archive');assert.equal(q.get('version'),'run-2');assert.equal(q.get('member'),'module:订单');assert.equal(q.get('section'),'错误处理');
});
test('version selection preserves existing member and sources; absent member chooses first body only',()=>{
 const ui=load();const d={members:[{key:'architecture'},{key:'verification'}],sources:[{key:'archive-source'}]};
 assert.equal(ui.versionMember(d,'verification'),'verification');assert.equal(ui.versionMember(d,'storage'),'architecture');assert.equal(ui.versionMember(d,'archive-source'),'archive-source');assert.equal(ui.targetMember(d,'missing'),null);
});
test('unrelated stale list responses cannot overwrite the latest authoritative inventory',async()=>{
 const pending=[];const nodes={'[data-layout]':{},'[data-sort]':{}};
 const root={innerHTML:'',querySelector:s=>nodes[s]||null,querySelectorAll:()=>[]};
 const sandbox={URLSearchParams,document:{querySelector:()=>null}};
 vm.runInNewContext(fs.readFileSync(__dirname+'/content-library.js','utf8'),sandbox);
 const ui=sandbox.ThoughtFlowLibrary.create({api:()=>new Promise(resolve=>pending.push(resolve)),t:(key,p)=>key+(p?JSON.stringify(p):''),escapeHTML:x=>String(x),date:x=>x});
 const old=ui.list(root,{kind:'all'});const latest=ui.list(root,{kind:'thought'});
 const response=title=>({total:1,page:1,page_size:24,counts:{sets:0,notes:1,documents:1},items:[{id:title,kind:'thought',title,state:'note'}]});
 pending[1](response('LATEST'));await latest;const accepted=root.innerHTML;
 pending[0](response('STALE'));await old;
 assert.equal(root.innerHTML,accepted);assert.match(root.innerHTML,/LATEST/);assert.doesNotMatch(root.innerHTML,/STALE/);
});
test('invalidated reader cannot render a response after navigating away',async()=>{
 const ui=load();let resolve;
 const root={isConnected:true,innerHTML:''};
 const readings=[];const view=ui.create({reading:item=>readings.push(item),api:()=>new Promise(r=>resolve=r),t:x=>x,escapeHTML:x=>x,isSetCurrent:()=>true});
 const pending=view.show(root,'set-1');view.invalidate();resolve({selection:{kind:'archive',id:'run-1'}});await pending;
 assert.doesNotMatch(root.innerHTML,/library-shell/);assert.equal(readings.length,0);
});
test('home ignores a late inventory after leaving overview and preserves the next home response',async()=>{
 const pending=[];let onHome=true;
 const root={innerHTML:''};
 const ui=load().create({api:()=>new Promise(resolve=>pending.push(resolve)),t:x=>x,escapeHTML:x=>String(x),date:x=>x,isHomeCurrent:()=>onHome});
 const old=ui.home(root);onHome=false;ui.invalidate();onHome=true;
 const next=ui.home(root);
 const result=title=>({counts:{content:1,sets:0,notes:1,drafts:0},items:[{id:title,title,kind:'thought',state:'note'}]});
 pending[1](result('CURRENT'));await next;
 pending[0](result('STALE'));await old;
 assert.match(root.innerHTML,/CURRENT/);assert.doesNotMatch(root.innerHTML,/STALE/);
});
test('empty overview has conversation and import paths, with no generated sample content',async()=>{
 const root={innerHTML:''};
 const ui=load().create({api:async()=>({counts:{content:0,sets:0,notes:0,drafts:0},items:[]}),t:x=>x,escapeHTML:x=>String(x),date:x=>x,isHomeCurrent:()=>true});
 await ui.home(root);assert.match(root.innerHTML,/#\/capture/);assert.match(root.innerHTML,/#\/notes\?import=1/);assert.doesNotMatch(root.innerHTML,/library-card/);
});
test('overview separates full-inventory counts and concerns from recent result cards',async()=>{
 const root={innerHTML:''};const stats={innerHTML:''},count={textContent:''},attention={innerHTML:'',querySelectorAll:()=>[]};let request;
 const ui=load().create({api:async path=>{request=path;return {counts:{content:22,sets:9,notes:13,drafts:4},items:[{id:'saved',kind:'document_set',title:'Saved design',state:'finalized',selection:{kind:'revision',id:'approved-v1'},members:[{key:'architecture',role:'architecture',title:'Architecture'}],source_count:1}],attention:[{kind:'questions',title:'Another design beyond this page',set_id:'older',question_count:3,selection:{kind:'archive',id:'frozen-draft'}},{kind:'task_failed',title:'No result yet',run_id:'failed-run',session_id:'continue-session'}],attention_total:2,archived_sessions:['saved-conversation']};},t:(k,v)=>k+(v?JSON.stringify(v):''),escapeHTML:x=>String(x??''),date:()=>'',isHomeCurrent:()=>true,homeRegions:()=>({stats,count,attention})});
 await ui.home(root);
 assert.match(request,/overview=1/);assert.match(stats.innerHTML,/>22</);assert.match(stats.innerHTML,/kind=document_set&state=draft/);
 assert.doesNotMatch(root.innerHTML,/workspace-summary-band/);assert.match(root.innerHTML,/workspace-result-card/);assert.match(root.innerHTML,/<footer>/);assert.match(root.innerHTML,/approved-v1/);
 assert.match(attention.innerHTML,/Another design beyond this page/);assert.match(attention.innerHTML,/version=frozen-draft/);assert.match(attention.innerHTML,/data-home-session="continue-session"/);assert.match(attention.innerHTML,/data-home-task="1"/);
});
test('overview load failure clears misleading statistics and provides local retries',async()=>{
 const retry={};const root={innerHTML:'',querySelector:()=>retry};const stats={innerHTML:'old counts'},attention={innerHTML:'old concerns',querySelector:()=>({})},count={textContent:'old count'};
 const ui=load().create({api:async()=>{throw new Error('Cannot read inventory');},t:x=>x,escapeHTML:x=>String(x),isHomeCurrent:()=>true,homeRegions:()=>({stats,attention,count})});
 await ui.home(root);
 assert.match(root.innerHTML,/role="alert"/);assert.match(root.innerHTML,/data-reload-home/);assert.match(attention.innerHTML,/Cannot read inventory/);assert.match(stats.innerHTML,/overview.counts_unavailable/);assert.doesNotMatch(stats.innerHTML,/old counts|>0</);assert.equal(count.textContent,'');assert.equal(typeof retry.onclick,'function');
});
test('draft statistic filters remain in inventory requests and can be cleared',async()=>{
 const nodes={'[data-layout]':{},'[data-sort]':{},'[data-clear-state]':{}};const root={innerHTML:'',querySelector:s=>nodes[s]||null,querySelectorAll:()=>[]};const paths=[];
 const sandbox={URLSearchParams,document:{querySelector:()=>null}};vm.runInNewContext(fs.readFileSync(__dirname+'/content-library.js','utf8'),sandbox);
 const ui=sandbox.ThoughtFlowLibrary.create({api:async path=>{paths.push(path);return {items:[],counts:{},page:1,page_size:24,total:0};},t:x=>x,escapeHTML:x=>String(x),date:x=>x});
 await ui.list(root,{kind:'document_set',state:'draft'});assert.match(paths[0],/state=draft/);nodes['[data-clear-state]'].onclick();await new Promise(resolve=>setImmediate(resolve));assert.match(paths[1],/state=&/);
});
test('fixed reader passes the exact member and version into attachment rendering',async()=>{
 const body={innerHTML:'',querySelectorAll:()=>[]};const nodes=new Map();
 const root={isConnected:true,innerHTML:'',querySelector(selector){
  if(selector==='[data-management]')return null;
  if(selector==='[data-library-body]')return body;
  if(!nodes.has(selector))nodes.set(selector,{addEventListener(){}});
  return nodes.get(selector);
 }};
 const sandbox={URLSearchParams,localStorage:{setItem(){}}};
 vm.runInNewContext(fs.readFileSync(__dirname+'/content-library.js','utf8'),sandbox);
 let rendering;const readings=[];
 const selection={kind:'revision',id:'retained-v1'};
 const member={key:'architecture',thought_id:'note-copy',title:'Architecture',role:'architecture',body:'![old](resource://old-image)',resources:[{resource_id:'old-image'}]};
 const ui=sandbox.ThoughtFlowLibrary.create({reading:item=>readings.push(item),api:async()=>({id:'set',title:'Design',state:'working',selection,versions:[selection],members:[member],sources:[]}),t:x=>x,escapeHTML:x=>String(x||''),date:x=>x,isSetCurrent:()=>true,markdown:(...args)=>{rendering=args;return '<p>Fixed</p>';}});
 await ui.show(root,'set',{kind:'revision',version:'retained-v1',member:'architecture'});
 assert.ok(rendering,'member body was rendered');
 assert.equal(rendering[3].member,'architecture');
 assert.deepEqual(JSON.parse(JSON.stringify(rendering[3].selection)),selection);
 assert.equal(rendering[2][0].resource_id,'old-image');
 assert.equal(readings.length,1);assert.equal(readings[0].title,'Design · Architecture');
 assert.match(readings[0].href,/kind=revision&version=retained-v1&member=architecture/);
 await ui.show(root,'set',{kind:'revision',version:'retained-v1',member:'missing'});
 assert.equal(readings.length,1,'failed reading must not enter recent shortcuts');
});
