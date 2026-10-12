(function () {
'use strict';
const app=document.getElementById('monitoring-app');if(!app)return;
const $=id=>document.getElementById(id);
const el=(tag,cls,text)=>{const n=document.createElement(tag);if(cls)n.className=cls;if(text!==undefined)n.textContent=text;return n;};
const link=(text,url,cls)=>{const a=el('a',cls,text);a.href=url;return a;};
const labels={fail:'Missing footage',error:'Check error',review:'Review frames',pending:'Incomplete',unknown:'Not checked',pass:'Passed'};
const colors={pass:'#168467',fail:'#d73a49',error:'#daa33b',pending:'#bec8d6'};
let overview=window.monitoringInitial||{},kind='retention',run=window.monitoringInitialRun||{},runTimer,historyLimit=10;
function stamp(v,zone){if(!v||v.startsWith('0001'))return 'Not checked';try{return new Intl.DateTimeFormat(undefined,{dateStyle:'medium',timeStyle:'short',...(zone?{timeZone:zone}:{})}).format(new Date(v));}catch(e){return v;}}
function type(k){return k==='retention'?'28-day recordings':'Camera screenshots';}
function pill(status,k){return el('span','mon-pill '+status,status==='pass'?(k==='snapshots'?'Exported':'Target met'):(labels[status]||status.replaceAll('_',' ')));}
function counts(sites){const c={pass:0,fail:0,error:0,review:0,pending:0};for(const s of sites)c[s.Status in c?s.Status:'pending']++;return c;}
function donut(container,sites,k){
 const c=counts(sites),total=sites.length,passed=c.pass;
 container.replaceChildren();
 const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');svg.setAttribute('viewBox','0 0 160 160');svg.classList.add('mon-donut');svg.setAttribute('role','img');
 svg.setAttribute('aria-label',type(k)+': '+passed+' of '+total+' sites passed, '+c.fail+' missing footage, '+c.error+' errors, '+c.review+' review, '+c.pending+' unverified.');
 const make=(tag,attrs,text)=>{const n=document.createElementNS(svg.namespaceURI,tag);for(const [key,v]of Object.entries(attrs))n.setAttribute(key,String(v));if(text!==undefined)n.textContent=text;svg.append(n);return n;};
 make('circle',{cx:80,cy:80,r:59,fill:'none',stroke:'#edf1f5','stroke-width':15});
 const parts=k==='retention'?[[c.pass,colors.pass],[c.fail,colors.fail],[c.error+c.review,colors.error],[c.pending,colors.pending]]:[[c.pass,colors.pass],[c.review,colors.error],[c.error+c.fail,colors.fail],[c.pending,colors.pending]];
 let offset=0;const length=2*Math.PI*59;
 for(const [n,color]of parts){if(!n||!total)continue;const amount=length*n/total;make('circle',{cx:80,cy:80,r:59,fill:'none',stroke:color,'stroke-width':15,'stroke-dasharray':Math.max(0,amount-2)+' '+(length-Math.max(0,amount-2)),'stroke-dashoffset':-offset,transform:'rotate(-90 80 80)'});offset+=amount;}
 make('text',{x:80,y:79,'text-anchor':'middle',class:'mon-donut-value'},total?Math.round(passed/total*100)+'%':'—');
 make('text',{x:80,y:99,'text-anchor':'middle',class:'mon-donut-label'},k==='retention'?'sites meet target':'sites exported');
 const legend=el('div','mon-chart-legend');
 const entries=k==='retention'?[['Target met',c.pass,colors.pass],['Missing footage',c.fail,colors.fail],['Check errors',c.error+c.review,colors.error],['Not verified',c.pending,colors.pending]]:[['Exported',c.pass,colors.pass],['Frames to review',c.review,colors.error],['Check errors',c.error+c.fail,colors.fail],['Not verified',c.pending,colors.pending]];
 for(const [name,n,color]of entries){const row=el('div'),dot=el('i');dot.style.background=color;row.append(dot,el('span','',name),el('b','',String(n)));legend.append(row);}
 container.append(svg,legend);
}
function stack(h){
 const parts=[['pass',h.Passed||0],['fail',h.Failed||0],['error',(h.Errors||0)+(h.Review||0)],['pending',h.Pending||0]];
 const total=parts.reduce((a,p)=>a+p[1],0),wrap=el('div'),bar=el('div','mon-stack');
 bar.setAttribute('role','img');bar.setAttribute('aria-label',parts.map(p=>p[1]+' '+p[0]).join(', '));
 for(const [status,n]of parts){const seg=el('span',status);seg.style.width=(total?n/total*100:0)+'%';bar.append(seg);}
 wrap.append(bar,el('div','mon-stack-label',(h.Passed||0)+' passed · '+(h.Failed||0)+' missing · '+((h.Errors||0)+(h.Review||0))+' errors / review · '+(h.Pending||0)+' incomplete'));
 return wrap;
}
function recheck(s){
 const form=el('form');form.method='post';form.action='/monitoring';
 for(const [name,value]of Object.entries({kind:s.Kind,reference:'Recheck '+s.Name,scope:'selected',site:String(s.SiteID)})){const n=el('input');n.type='hidden';n.name=name;n.value=name==='reference'?value.slice(0,120):value;form.append(n);}
 const button=el('button','mon-button secondary','Recheck site');button.type='submit';form.append(button);return form;
}
function recorderCaption(n){
 if(n.Scope==='entire_nvr')return 'All '+n.Failed+' enabled cameras lack footage at the 28-day target';
 const out=[];
 if(n.Failed)out.push(n.Failed+' camera'+(n.Failed===1?'':'s')+' missing 28-day footage');
 if(n.Errors)out.push(n.Scope==='recorder'?'NVR could not be checked':n.Errors+' camera check errors');
 if(n.Skipped)out.push(n.Skipped+' dark / placeholder frames');
 if(n.Pending)out.push(n.Pending+' checks incomplete');
 return out.join(' · ')||((n.Passed||n.Exported)+' cameras passed');
}
function findings(s){
 const details=el('details','mon-findings');details.dataset.key=s.Kind+':'+s.SiteID;
 const ns=s.Recorders||[],bad=ns.filter(n=>n.Status!=='pass');
 const headline=bad.length?bad.map(n=>n.Name||'Recorder').join(', '):(ns.length?'All recorders passed':'No completed check');
 details.append(el('summary','',headline));
 for(const n of ns){
  const box=el('div','mon-recorder');box.append(el('strong','',n.Name||'Site configuration'),el('p','',recorderCaption(n)));
  const list=el('ul');
  const affected=(n.Results||[]).filter(v=>v.Status!=='pass'&&v.Status!=='exported');
  for(const v of affected){const li=el('li');li.append(el('strong','',(v.Camera||'Recorder')+(v.Channel?' · channel '+v.Channel:'')),el('div','',v.Detail||v.Status));if(v.Target)li.append(el('div','',v.Target));list.append(li);}
  if(affected.length)box.append(list);
  if(n.NVRID)box.append(link('Open recorder settings →','/nvrs/'+n.NVRID));
  details.append(box);
 }
 if(!ns.length)details.append(el('p','mon-caption',s.Action));
 return details;
}
function matches(s,filter){if(filter==='all')return true;if(filter==='attention')return s.Status!=='pass';if(filter==='unverified')return s.Status==='pending'||s.Status==='unknown';return s.Status===filter;}
function renderSites(){
 const rows=$('mon-sites'),filter=$('mon-filter').value,search=$('mon-search').value.toLowerCase();
 const open=new Set([...rows.querySelectorAll('details[open]')].map(n=>n.dataset.key));
 rows.replaceChildren();
 const selected=(overview.Sites||[]).filter(s=>s.Kind===kind&&matches(s,filter)&&JSON.stringify([s.Name,s.Recorders]).toLowerCase().includes(search));
 selected.sort((a,b)=>({fail:0,error:1,review:2,pending:3,unknown:4,pass:5}[a.Status]-{fail:0,error:1,review:2,pending:3,unknown:4,pass:5}[b.Status])||a.Name.localeCompare(b.Name));
 $('mon-site-count').textContent=selected.length+' shown';
 for(const s of selected){
  const tr=el('tr'),name=el('td'),status=el('td'),coverage=el('td'),affected=el('td'),checked=el('td'),action=el('td');
  name.append(link(s.Name,'/sites/'+s.SiteID),el('small','',s.Action));status.append(pill(s.Status,s.Kind));
  coverage.append(el('strong','',(s.Passed+s.Exported)+' passed'),el('small','',s.Failed+' missing · '+s.Errors+' errors'+(s.Skipped?' · '+s.Skipped+' skipped':'')+(s.Pending?' · '+s.Pending+' pending':'')));
  const d=findings(s);if(open.has(d.dataset.key))d.open=true;affected.append(d);
  checked.append(el('span','',stamp(s.CheckedAt)));if(s.RunID)checked.append(link('Run #'+s.RunID,'/monitoring?run='+s.RunID+'#check-detail'));
  action.append(recheck(s));tr.append(name,status,coverage,affected,checked,action);rows.append(tr);
 }
 const empty=$('mon-sites-empty');empty.hidden=selected.length>0;empty.replaceChildren();
 if(!selected.length)empty.append(el('strong','',filter==='attention'?'No sites need attention in this view':'No matching sites'),el('span','',filter==='attention'?'Switch to All sites to review passed checks.':'Adjust the result filter or search.'));
}
function renderHistory(){
 const history=overview.History||[];const table=$('mon-history');table.replaceChildren();
 for(const h of history.slice(0,historyLimit)){
  const tr=el('tr'),name=el('td'),date=el('td'),status=el('td'),chart=el('td'),action=el('td');
  name.append(link(h.Reference||'Check #'+h.ID,'/monitoring?run='+h.ID+'#check-detail'),el('small','',type(h.Kind)));
  date.textContent=stamp(h.Created);status.append(el('span','mon-pill '+(h.Status==='completed'?'pass':h.Status==='completed_with_issues'?'error':'pending'),h.Status.replaceAll('_',' ')));
  chart.append(stack(h));action.append(link('Open →','/monitoring?run='+h.ID+'#check-detail'));tr.append(name,date,status,chart,action);table.append(tr);
 }
 $('mon-history-more').hidden=history.length<=historyLimit;$('mon-history-more').textContent='Show older checks ('+Math.max(0,history.length-historyLimit)+' remaining)';
 const trend=$('mon-trend');trend.replaceChildren();
 if(!history.length){trend.append(el('div','mon-empty','No checks yet. Run a check or create a schedule to begin tracking coverage.'));return;}
 for(const h of history.slice(0,12).reverse()){
  const a=link('','/monitoring?run='+h.ID+'#check-detail','mon-trend-column'),bar=el('div','mon-trend-bar');
  const c={pass:h.Passed||0,fail:h.Failed||0,error:(h.Errors||0)+(h.Review||0),pending:h.Pending||0},total=Object.values(c).reduce((a,b)=>a+b,0);
  a.title=type(h.Kind)+' #'+h.ID+' · '+Object.entries(c).map(([k,v])=>v+' '+k).join(', ');
  a.setAttribute('aria-label',a.title);
  for(const [status,n]of Object.entries(c)){const seg=el('span');seg.style.height=(total?n/total*100:0)+'%';seg.style.background=colors[status];bar.append(seg);}
  a.append(bar,el('small','',(h.Kind==='retention'?'28d':'Cam')+' #'+h.ID));trend.append(a);
 }
}
function renderSchedules(){
 const list=$('mon-schedules');list.replaceChildren();
 const days=['Sun','Mon','Tue','Wed','Thu','Fri','Sat'];
 for(const s of overview.Schedules||[]){
  const card=el('article','mon-schedule'),head=el('div','mon-schedule-head'),foot=el('div','mon-schedule-footer');
  head.append(el('h3','',s.Name),el('span','mon-pill '+(s.Active?'pass':'pending'),s.Active?'Enabled':'Paused'));
  const cadence=s.Mode==='weekly'?'Every '+s.Interval+' week'+(s.Interval===1?'':'s')+' · '+(s.Weekdays||[]).map(d=>days[d]).join(', '):'Every '+s.Interval+' day'+(s.Interval===1?'':'s');
  card.append(head,el('p','',type(s.Kind)+' · '+cadence+' at '+s.StartTime+' ('+s.Timezone+')'),el('p','',(s.AllSites?'All installations, including new sites':(s.SiteIDs||[]).length+' selected installations')+' · '+(s.Active?'Next: '+stamp(s.NextDue,s.Timezone):'Paused')));
  if(s.LastError)card.append(el('p','mon-schedule-error',s.LastError));
  foot.append(link('Edit schedule','/monitoring?schedule='+s.ID+'#schedule-editor'));
  if(s.LastRunID)foot.append(link('Last run #'+s.LastRunID,'/monitoring?run='+s.LastRunID+'#check-detail'));
  const f=el('form');f.method='post';f.action='/monitoring/schedules/'+s.ID+'/toggle';
  const input=el('input');input.type='hidden';input.name='active';input.value=s.Active?'false':'true';
  const button=el('button','mon-button secondary',s.Active?'Pause':'Resume');button.type='submit';f.append(input,button);foot.append(f);card.append(foot);list.append(card);
 }
 if(!(overview.Schedules||[]).length)list.append(el('p','mon-caption','No schedules yet. Create one to automate your checks.'));
}
function renderActive(){
 const holder=$('mon-active');holder.replaceChildren();
 for(const r of overview.Active||[]){
  const card=el('div','mon-active-check'),left=el('div');left.append(link((r.Reference||type(r.Kind))+' · '+r.Status,'/monitoring?run='+r.ID+'#check-detail'),el('small','',r.Current||'Waiting for a worker'));
  const progress=el('progress');progress.max=r.Total||1;progress.value=r.Done;
  card.append(left,progress,el('strong','',r.Done+' / '+r.Total));holder.append(card);
 }
}
function renderOverview(){
 const retention=(overview.Sites||[]).filter(s=>s.Kind==='retention'),snapshots=(overview.Sites||[]).filter(s=>s.Kind==='snapshots'),c=counts(retention);
 $('mon-kpi-action').textContent=c.fail+c.error;$('mon-kpi-pass').textContent=c.pass;$('mon-kpi-pending').textContent=c.pending+c.review;
 const schedules=(overview.Schedules||[]).filter(s=>s.Active);$('mon-kpi-schedules').textContent=schedules.length;
 const due=schedules.filter(s=>s.NextDue).sort((a,b)=>a.NextDue.localeCompare(b.NextDue))[0];
 $('mon-next-due').textContent=due?'Next: '+stamp(due.NextDue,due.Timezone):'Set a recurring check below';
 donut($('mon-retention-chart'),retention,'retention');donut($('mon-snapshots-chart'),snapshots,'snapshots');
 renderSites();renderHistory();renderSchedules();renderActive();$('mon-freshness').textContent='Updated '+new Date().toLocaleTimeString(undefined,{hour:'2-digit',minute:'2-digit'});
}
function renderRun(){
 if(!run.ID)return;const section=$('check-detail');section.hidden=false;
 $('mon-run-title').textContent=(run.Reference||type(run.Kind))+' · #'+run.ID;
 $('mon-run-status').textContent=type(run.Kind)+' · '+run.Status.replaceAll('_',' ')+' · '+stamp(run.Created);
 $('mon-run-progress').max=run.Total||1;$('mon-run-progress').value=run.Done;$('mon-run-count').textContent=run.Done+' / '+run.Total;
 $('mon-run-current').textContent=run.Current?'Working on: '+run.Current:'';
 $('mon-run-error').hidden=!run.Error;$('mon-run-error').textContent=run.Error||'';
 $('mon-run-root').textContent=run.Root;
 $('mon-site-csv').href='/api/monitoring/'+run.ID+'?format=sites-csv';$('mon-camera-csv').href='/api/monitoring/'+run.ID+'?format=csv';$('mon-run-json').href='/api/monitoring/'+run.ID;
 $('mon-repeat').action='/monitoring/checks/'+run.ID+'/repeat';
 donut($('mon-run-chart'),run.Sites||[],run.Kind);
 const holder=$('mon-run-sites'),open=new Set([...holder.querySelectorAll('details[open]')].map(n=>n.dataset.key));holder.replaceChildren();
 const filtered=(run.Sites||[]).filter(s=>matches(s,$('mon-run-filter').value));
 for(const s of filtered){
  const card=el('article','mon-run-site '+s.Status),head=el('div','mon-run-site-head');head.append(el('h3','',s.Name||'Site #'+s.SiteID),pill(s.Status,s.Kind));
  const detail=findings(s);if(open.has(detail.dataset.key))detail.open=true;
  card.append(head,el('p','',s.Passed+s.Exported+' passed · '+s.Failed+' missing · '+s.Errors+' errors · '+s.Skipped+' skipped'),detail);holder.append(card);
 }
 if(!filtered.length)holder.append(el('p','mon-empty','No results need attention. Select All sites to review passed checks.'));
}
async function refreshRun(){
 if(!run.ID)return;
 try{const response=await fetch('/api/monitoring/'+run.ID);if(!response.ok)throw new Error('Check detail unavailable ('+response.status+')');run=await response.json();renderRun();if(run.Status==='queued'||run.Status==='running')runTimer=setTimeout(refreshRun,2500);}
 catch(e){$('mon-run-error').hidden=false;$('mon-run-error').textContent=e.message;runTimer=setTimeout(refreshRun,5000);}
}
async function refreshOverview(){
 try{const response=await fetch('/api/monitoring');if(!response.ok)throw new Error('Monitoring refresh failed ('+response.status+')');overview=await response.json();$('mon-api-error').hidden=true;renderOverview();}
 catch(e){$('mon-api-error').hidden=false;$('mon-api-error').textContent=e.message+'. The last displayed results may be outdated.';}
 setTimeout(refreshOverview,(overview.Active||[]).length?2500:15000);
}
for(const b of app.querySelectorAll('[data-kind]'))b.addEventListener('click',()=>{kind=b.dataset.kind;for(const n of app.querySelectorAll('[data-kind]')){n.classList.toggle('selected',n===b);n.setAttribute('aria-pressed',String(n===b));}renderSites();});
$('mon-history-more').addEventListener('click',()=>{historyLimit+=10;renderHistory();});
$('mon-filter').addEventListener('change',renderSites);$('mon-search').addEventListener('input',renderSites);$('mon-run-filter').addEventListener('change',renderRun);
for(const picker of app.querySelectorAll('.mon-site-picker')){
 const scope=picker.querySelector('.mon-scope'),options=picker.querySelector('.mon-site-options');
 const sync=()=>{options.hidden=scope.value!=='selected';for(const n of options.querySelectorAll('input[name="site"]'))n.disabled=options.hidden;};scope.addEventListener('change',sync);sync();
 picker.querySelector('.mon-site-search').addEventListener('input',function(){for(const row of options.querySelectorAll('.mon-site-option'))row.hidden=!row.textContent.toLowerCase().includes(this.value.toLowerCase());});
}
const mode=$('mon-schedule-mode'),scheduleForm=$('mon-schedule-form');
function syncMode(){const weekly=mode.value==='weekly';$('mon-weekdays').hidden=!weekly;scheduleForm.elements.interval.max=weekly?52:365;$('mon-interval-unit').textContent=weekly?'Weeks between checks':'Days between checks';}
mode.addEventListener('change',syncMode);syncMode();
scheduleForm.addEventListener('submit',e=>{
 const picked=[...scheduleForm.querySelectorAll('input[name="weekday"]:checked')];
 if(mode.value==='weekly'&&!picked.length){e.preventDefault();$('mon-weekdays').scrollIntoView({behavior:'smooth',block:'center'});alert('Select at least one day of the week.');}
});
renderOverview();renderRun();if(run.ID)refreshRun();setTimeout(refreshOverview,5000);
})();
