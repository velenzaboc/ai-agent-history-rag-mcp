"use strict";

const state = {dashboard:null, programs:new Map(), programLoading:new Set(), relatedTasks:new Map(), view:"recovery", query:"", findingKind:"all", selectedTask:"", selectedSession:"", detailEpoch:0, loading:false};
const $ = (selector, root=document) => root.querySelector(selector);
const $$ = (selector, root=document) => [...root.querySelectorAll(selector)];
const svgNS = "http://www.w3.org/2000/svg";

document.addEventListener("DOMContentLoaded", () => {
  bindNavigation();
  bindActions();
  loadDashboard(false);
});

function apiURL(path) { return new URL(path, window.location.href).toString(); }
function label(key, fallback) { return state.dashboard?.view?.labels?.[key] || fallback; }
function text(node, value) { node.textContent = value ?? ""; return node; }
function el(tag, className, value) { const node=document.createElement(tag); if(className) node.className=className; if(value!==undefined) text(node,value); return node; }
function svgEl(tag, attributes={}) { const node=document.createElementNS(svgNS,tag); Object.entries(attributes).forEach(([key,value])=>node.setAttribute(key,String(value))); return node; }

function bindNavigation() {
  $$(".nav-item").forEach(bindNavigationButton);
  $("#global-filter").addEventListener("input", event => { state.query=event.target.value.trim().toLowerCase(); renderActiveView(); });
}

function bindNavigationButton(button) { button.addEventListener("click", () => setView(button.dataset.view)); }

function bindActions() {
  $("#refresh-button").addEventListener("click", () => loadDashboard(true));
  $("#drawer-close").addEventListener("click", closeDrawer);
  $("#drawer-backdrop").addEventListener("click", closeDrawer);
  $("#history-form").addEventListener("submit", runHistorySearch);
  document.addEventListener("keydown", event => { if(event.key === "Escape") closeDrawer(); });
  $$(".metric-card").forEach(card => card.addEventListener("click", () => {
    const filter=card.dataset.metricFilter;
    if(filter === "tasks") setView("tasks");
    else if(filter === "sessions") setView("search");
    else if(filter === "links") setView("lanes");
    else { state.findingKind = filter === "high" ? "high" : "all"; setView("recovery"); }
  }));
}

async function loadDashboard(fresh) {
  if(state.loading) return;
  setLoading(true);
  hideError();
  try {
    const response=await fetch(apiURL(`api/dashboard${fresh?"?fresh=1":""}`),{headers:{Accept:"application/json"}});
    if(!response.ok) throw new Error(`Dashboard request returned ${response.status}`);
    state.dashboard=await response.json();
    if(fresh) { state.programs.clear(); state.relatedTasks.clear(); }
    configureShell();
    renderEverything();
  } catch(error) {
    showError(error.message || "Dashboard unavailable");
  } finally { setLoading(false); }
}

function configureShell() {
  const data=state.dashboard;
  document.title=data.view.title;
  text($("#brand-eyebrow"),data.view.eyebrow);
  text($("#brand-title"),data.view.title);
  text($("#view-subtitle"),data.view.subtitle);
  text($("#project-id"),data.project_id);
  text($("#revision"),data.revision || data.read_timestamp || "unversioned");
  $$('[data-label]').forEach(node => text(node,label(node.dataset.label,node.textContent)));
  renderProgramNavigation();
}

function renderProgramNavigation() {
  const host=$("#program-nav");host.replaceChildren();
  (state.dashboard?.programs||[]).forEach(program=>{
    const button=el("button",`nav-item ${state.view===`program:${program.id}`?"active":""}`);button.type="button";button.dataset.view=`program:${program.id}`;
    button.append(text(el("span","nav-glyph"),"◫"),text(el("span"),program.nav_label),text(el("span","nav-count"),number(program.expected_lane_count)));
    bindNavigationButton(button);host.append(button);
  });
}

function renderEverything() {
  renderSources(); renderMetrics(); renderCoverage(); renderFindingFilters(); renderActiveView();
}

function setView(view) {
  state.view=view;
  $$(".nav-item").forEach(item => item.classList.toggle("active",item.dataset.view===view));
  const panelView=view.startsWith("program:")?"program":view;
  $$(".view-panel").forEach(panel => panel.classList.toggle("active",panel.dataset.panel===panelView));
  const titles={recovery:label("inbox","Recovery inbox"),lanes:label("lanes","Lanes"),tasks:label("tasks","Tasks"),milestones:label("milestones","Milestones"),graph:label("graph","Graph"),search:label("history_search","History search")};
  const program=view.startsWith("program:")?(state.dashboard?.programs||[]).find(item=>item.id===view.slice(8)):null;
  text($("#view-title"),program?.title||titles[view]||view);
  text($("#view-eyebrow"),state.dashboard?.view?.eyebrow || "Overview");
  text($("#view-subtitle"),program?.description||state.dashboard?.view?.subtitle||"");
  renderActiveView();
}

function renderActiveView() {
  if(!state.dashboard) return;
  if(state.view.startsWith("program:")){renderProgramView(state.view.slice(8));return;}
  ({recovery:renderFindings,lanes:renderLanes,tasks:renderTasks,milestones:renderMilestones,graph:renderGraph,search:()=>{}}[state.view] || (()=>{}))();
}

function renderSources() {
  const list=$("#source-list"); list.replaceChildren();
  state.dashboard.sources.forEach(source => {
    const row=el("div","source-row"); const name=el("span","source-name");
    const dot=el("i",`source-dot ${source.healthy?"healthy":""}`); name.append(dot,text(el("span"),source.name));
    row.append(name,text(el("span"),source.healthy?"online":"degraded")); row.title=source.error || `Observed ${formatTime(source.observed_at)}`; list.append(row);
  });
}

function renderMetrics() {
  const counts=state.dashboard.counts;
  text($("#metric-high"),number(counts.high_severity)); text($("#metric-findings"),number(counts.findings));
  text($("#metric-tasks"),number(counts.tasks)); text($("#metric-sessions"),number(counts.sessions)); text($("#metric-links"),number(counts.relationships));
  text($("#inbox-count"),number(counts.findings));
  const taskCoverage=state.dashboard.coverage; text($("#metric-task-note"),taskCoverage.active_task_limit?`snapshot ${number(taskCoverage.fleet_snapshot_returned)} · active ${number(taskCoverage.active_task_returned)}`:`limit ${number(taskCoverage.fleet_limit)}`);
  text($("#metric-session-note"),`${number(state.dashboard.coverage.history_returned)} returned · ${number(state.dashboard.coverage.history_probed)} probed`);
}

function renderCoverage() {
  const coverage=state.dashboard.coverage; const strip=$("#coverage-strip"); strip.replaceChildren();
  strip.append(fragment("Fleet revision",state.dashboard.revision||"unversioned"),fragment("Fleet read",formatTime(state.dashboard.read_timestamp)),fragment("Snapshot",`${number(coverage.fleet_snapshot_returned)} / ${number(coverage.fleet_limit)}`),fragment("Active overlay",coverage.active_task_limit?`${number(coverage.active_task_returned)} / ${number(coverage.active_task_limit)}`:"disabled"),fragment("Relationship coverage",coverage.fleet_worklinks_capped?`targeted · ${number(coverage.session_links_hydrated)} sessions`:"snapshot complete"),fragment("History coverage",coverage.history_partial?"bounded":"complete response"),fragment("Generated",formatTime(state.dashboard.generated_at)));
}

function fragment(name,value) { const span=el("span"); span.append(text(el("strong"),`${name}: `),document.createTextNode(value)); return span; }

function renderFindingFilters() {
  const host=$("#finding-filters"); host.replaceChildren();
  const options=[{id:"all",label:"All"},{id:"high",label:"High signal"},...state.dashboard.classification.rules.map(rule=>({id:rule.id,label:rule.label}))];
  options.forEach(option=>{const button=text(el("button",`filter-chip ${state.findingKind===option.id?"active":""}`),option.label);button.type="button";button.addEventListener("click",()=>{state.findingKind=option.id;renderFindingFilters();renderFindings();});host.append(button);});
}

async function renderProgramView(programID) {
  const host=$("#program-view");
  const cached=state.programs.get(programID);
  if(cached){paintProgramView(cached);return;}
  if(state.programLoading.has(programID))return;
  state.programLoading.add(programID);host.replaceChildren(text(el("div","program-loading"),"Loading the live program subtree…"));
  try{
    const query=new URLSearchParams({id:programID});const response=await fetch(apiURL(`api/program?${query}`),{headers:{Accept:"application/json"}});
    if(!response.ok)throw new Error(`Program view returned ${response.status}`);
    const program=await response.json();state.programs.set(programID,program);
    if(state.view===`program:${programID}`)paintProgramView(program);
  }catch(error){if(state.view===`program:${programID}`)host.replaceChildren(text(el("div","empty-state"),error.message||"Program unavailable"));}
  finally{state.programLoading.delete(programID);}
}

function paintProgramView(data) {
  const host=$("#program-view");host.replaceChildren();const config=data.program;const taskMap=mapBy(data.tasks||[],"task_id");const byParent=groupBy((data.tasks||[]).filter(task=>task.parent_id),"parent_id");const laneRoots=(data.lane_task_ids||[]).map(id=>taskMap[id]).filter(Boolean);
  const hero=el("section","program-hero");const heading=el("div","program-hero-copy");heading.append(text(el("div","eyebrow"),config.nav_label),text(el("h2"),config.title),text(el("p"),config.description));
  const health=el("div",`program-health ${data.counts.lane_count_matches&&data.scope?.complete&&!data.scope?.capped?"complete":"partial"}`);health.append(text(el("strong"),`${number(data.counts.lanes)} / ${number(data.counts.expected_lanes)}`),text(el("span"),"configured lanes"));hero.append(heading,health);host.append(hero);
  const facts=el("section","program-facts");facts.append(programFact("Tasks",number(data.counts.tasks)),programFact("Dependencies",number(data.counts.dependencies)),programFact("Work links",number(data.counts.worklinks)),programFact("Snapshot",data.scope?.complete&&!data.scope?.capped?"complete":"bounded"),programFact("Read",formatTime(data.read_timestamp)));
  if(config.source_thread_title||config.source_thread_id||config.checkpoint_ref){const source=el("article","program-source");source.append(text(el("div","eyebrow"),"Source handover"),text(el("h3"),config.source_thread_title||"Configured source thread"));if(config.source_thread_id)source.append(text(el("div","work-id"),config.source_thread_id));if(config.source_note)source.append(text(el("p"),config.source_note));if(config.checkpoint_ref)source.append(text(el("div","program-checkpoint"),`Checkpoint: ${config.checkpoint_ref}`));facts.append(source);}
  host.append(facts);
  const filtered=laneRoots.filter(lane=>{const members=[lane,...descendantsOf(lane.task_id,byParent)];return !state.query||members.some(taskMatches);});
  const grid=el("section","program-lane-grid");filtered.forEach((lane,index)=>grid.append(programLaneCard(data,lane,index,byParent)));host.append(grid);
  if(!filtered.length)host.append(text(el("div","empty-state"),"No program lanes match this filter."));
}

function programFact(labelText,value){const fact=el("div","program-fact");fact.append(text(el("span"),labelText),text(el("strong"),value));return fact;}

function descendantsOf(taskID,byParent){const result=[];const stack=[...(byParent[taskID]||[])];const seen=new Set();while(stack.length){const task=stack.shift();if(!task||seen.has(task.task_id))continue;seen.add(task.task_id);result.push(task);stack.push(...(byParent[task.task_id]||[]));}return result;}

function programLaneCard(data,lane,index,byParent){
  const descendants=descendantsOf(lane.task_id,byParent),members=[lane,...descendants],terminal=new Set(state.dashboard.status.terminal_group_ids),complete=members.filter(task=>terminal.has(groupForStatus(task.status)?.id)).length,percent=members.length?Math.round(complete/members.length*100):0;
  const card=el("article","program-lane-card");card.style.setProperty("--lane-index",String(index));const head=el("div","program-lane-head");const identity=el("div");identity.append(text(el("div","task-card-id"),lane.task_id),text(el("h3"),lane.title));const status=text(el("span","pill"),lane.status);const group=groupForStatus(lane.status);if(group)status.style.color=group.color;head.append(identity,status);card.append(head);
  const meta=el("div","program-lane-meta");meta.append(text(el("span"),lane.owner||"unowned"),text(el("span"),`${number(members.length)} tasks · ${number(complete)} terminal`));card.append(meta);
  const track=el("div","progress-track"),fill=el("div","progress-fill");fill.style.width=`${percent}%`;track.append(fill);const caption=el("div","progress-caption");caption.append(text(el("span"),"subtree progress"),text(el("span"),`${percent}%`));card.append(track,caption);
  if((data.program.stages||[]).length){const stages=el("div","program-stages");data.program.stages.forEach(stage=>{const matches=descendants.filter(task=>task.task_id.startsWith(stage.task_id_prefix));const selected=matches.sort((a,b)=>String(a.task_id).localeCompare(String(b.task_id)))[0];const chip=el("div","program-stage");chip.append(text(el("span"),stage.label),text(el("strong"),selected?.status||"not present"));if(selected){chip.addEventListener("click",event=>{event.stopPropagation();openTask(selected.task_id);});const selectedGroup=groupForStatus(selected.status);if(selectedGroup)chip.style.setProperty("--stage-color",selectedGroup.color);}stages.append(chip);});card.append(stages);}
  const focus=descendants.filter(task=>!terminal.has(groupForStatus(task.status)?.id)).sort(programTaskOrder).slice(0,4);const list=el("div","program-focus-list");(focus.length?focus:descendants.sort(programTaskOrder).slice(0,4)).forEach(task=>{const row=el("button","program-focus-task");row.type="button";row.append(text(el("span"),task.title),text(el("small"),task.status));row.addEventListener("click",event=>{event.stopPropagation();openTask(task.task_id);});list.append(row);});card.append(list);card.addEventListener("click",()=>openTask(lane.task_id));return card;
}

function programTaskOrder(a,b){const rank={blocked:0,in_progress:1,in_review:2,not_started:3,done:4,complete:4,completed:4,resolved:4,cancelled:5};return (rank[a.status]??3)-(rank[b.status]??3)||String(a.task_id).localeCompare(String(b.task_id));}

function renderFindings() {
  const tbody=$("#findings-body"); tbody.replaceChildren();
  const taskMap=mapBy(state.dashboard.tasks,"task_id"), sessionMap=mapBy(state.dashboard.sessions,"session_id");
  const findings=state.dashboard.findings.filter(f=>matchesQuery(f,taskMap[f.task_id],sessionMap[f.session_id]) && (state.findingKind==="all" || state.findingKind===f.kind || state.findingKind==="high"&&f.severity==="high"));
  findings.forEach(finding=>{
    const task=taskMap[finding.task_id], session=sessionMap[finding.session_id]; const row=document.createElement("tr");
    const signal=document.createElement("td");const wrap=el("div","signal-cell");const mark=el("i","signal-mark");mark.style.backgroundColor=finding.color;const copy=el("div");copy.append(text(el("div","signal-title"),finding.label),text(el("span","cell-subtitle"),finding.detail));wrap.append(mark,copy);signal.append(wrap);
    const work=document.createElement("td");work.append(text(el("span","work-id"),finding.task_id||finding.session_id||"—"),text(el("span","cell-subtitle"),task?.title||session?.project_name||session?.project_path||"Unbound evidence"));
    const evidence=document.createElement("td");evidence.append(text(el("span","pill"),session?.machine_id||task?.owner||"unknown"),text(el("span","cell-subtitle"),formatTime(session?.timestamp||task?.updated_at)));
    const age=text(el("td","age"),formatAge(finding.age_hours));const action=document.createElement("td");action.append(text(el("button","arrow-button"),"→"));
    row.append(signal,work,evidence,age,action);row.addEventListener("click",()=>openEvidence(finding.task_id,finding.session_id,finding));tbody.append(row);
  });
  $("#findings-empty").classList.toggle("hidden",findings.length!==0);
}

function renderLanes() {
  const board=$("#lane-board");board.replaceChildren();const links=groupBy(state.dashboard.worklinks,"task_id");const groups=[...state.dashboard.status.groups];
  const mapped=new Set(groups.flatMap(group=>group.statuses.map(status=>status.toLowerCase())));const unknown=state.dashboard.tasks.filter(task=>!mapped.has(task.status.toLowerCase()));
  if(unknown.length) groups.push({id:"unmapped",label:"Unmapped",color:state.dashboard.view.theme.muted,statuses:[]});
  groups.forEach(group=>{
    const tasks=state.dashboard.tasks.filter(task=>(group.id==="unmapped"?!mapped.has(task.status.toLowerCase()):group.statuses.some(status=>status.toLowerCase()===task.status.toLowerCase()))&&taskMatches(task)).slice(0,state.dashboard.view.max_lane_cards);
    const lane=el("section","lane");lane.style.setProperty("--lane-color",group.color);const head=el("div","lane-head");const title=el("div","lane-title");title.append(el("i","lane-dot"),text(el("span"),group.label));head.append(title,text(el("span","lane-count"),number(tasks.length)));const cards=el("div","lane-cards");
    tasks.forEach(task=>cards.append(taskCard(task,links[task.task_id]||[])));lane.append(head,cards);board.append(lane);
  });
}

function taskCard(task,links) { const card=el("article","task-card");card.append(text(el("div","task-card-id"),task.task_id),text(el("div","task-card-title"),task.title));const meta=el("div","task-card-meta");meta.append(text(el("span"),task.owner||task.pillar||"unowned"),dots(links.length));card.append(meta);card.addEventListener("click",()=>openTask(task.task_id));return card; }
function dots(count){const host=el("span","link-dots");for(let i=0;i<Math.min(count,6);i++)host.append(document.createElement("i"));if(!count)text(host,"0 links");return host;}

function renderTasks() {
  const body=$("#tasks-body");body.replaceChildren();const links=groupBy(state.dashboard.worklinks,"task_id");const tasks=state.dashboard.tasks.filter(taskMatches);
  tasks.forEach(task=>{const row=document.createElement("tr");const identity=document.createElement("td");identity.append(text(el("span","work-id"),task.task_id),text(el("span","cell-subtitle"),task.title));row.append(identity,text(el("td"),task.status),text(el("td"),task.owner||"—"),text(el("td"),task.pillar||"—"),text(el("td"),number((links[task.task_id]||[]).length)),text(el("td","age"),relativeTime(task.updated_at)));row.addEventListener("click",()=>openTask(task.task_id));body.append(row);});
  $("#tasks-empty").classList.toggle("hidden",tasks.length!==0);
}

function renderMilestones() {
  const grid=$("#milestone-grid");grid.replaceChildren();const levels=new Set(state.dashboard.status.milestone_levels.map(v=>v.toLowerCase()));const tasks=state.dashboard.tasks.filter(task=>levels.has(task.level.toLowerCase())&&taskMatches(task));const deps=state.dashboard.dependencies;
  tasks.forEach(task=>{const downstream=deps.filter(dep=>dep.task_id===task.task_id||dep.depends_on_task_id===task.task_id);const relatedIDs=new Set(downstream.flatMap(dep=>[dep.task_id,dep.depends_on_task_id]).filter(id=>id!==task.task_id));state.dashboard.tasks.filter(item=>item.parent_id===task.task_id).forEach(item=>relatedIDs.add(item.task_id));const related=state.dashboard.tasks.filter(item=>relatedIDs.has(item.task_id));const terminal=new Set(state.dashboard.status.terminal_group_ids);const complete=related.filter(item=>terminal.has(groupForStatus(item.status)?.id)).length;const percent=related.length?Math.round(complete/related.length*100):0;const card=el("article","milestone-card");const top=el("div","milestone-top");top.append(text(el("span","work-id"),task.task_id),text(el("span","pill"),task.status));card.append(top,text(el("h3"),task.title));const track=el("div","progress-track"),fill=el("div","progress-fill");fill.style.width=`${percent}%`;track.append(fill);const caption=el("div","progress-caption");caption.append(text(el("span"),`${complete}/${related.length} visible related tasks complete`),text(el("span"),`${percent}%`));card.append(track,caption);card.addEventListener("click",()=>openTask(task.task_id));grid.append(card);});
  if(!tasks.length) grid.append(text(el("div","empty-state"),"No configured milestone levels are present in this bounded snapshot."));
}

function renderGraph() {
  const svg=$("#task-graph"),legend=$("#graph-legend");svg.replaceChildren();legend.replaceChildren();const groups=state.dashboard.status.groups;groups.forEach(group=>{const item=el("span","legend-item");const swatch=el("i","legend-swatch");swatch.style.backgroundColor=group.color;item.append(swatch,text(el("span"),group.label));legend.append(item);});
  let tasks=state.dashboard.tasks.filter(taskMatches);if(tasks.length>state.dashboard.view.max_graph_nodes){const degrees=new Map();const bump=id=>degrees.set(id,(degrees.get(id)||0)+1);state.dashboard.dependencies.forEach(dep=>{bump(dep.task_id);bump(dep.depends_on_task_id);});tasks.forEach(task=>{if(task.parent_id){bump(task.task_id);bump(task.parent_id);}});tasks.sort((a,b)=>Number(b.is_critical_path)-Number(a.is_critical_path)||Number(b.is_gap)-Number(a.is_gap)||(degrees.get(b.task_id)||0)-(degrees.get(a.task_id)||0)||String(a.task_id).localeCompare(String(b.task_id)));tasks=tasks.slice(0,state.dashboard.view.max_graph_nodes);}
  const grouped=new Map();groups.forEach(group=>grouped.set(group.id,[]));grouped.set("unmapped",[]);tasks.forEach(task=>(grouped.get(groupForStatus(task.status)?.id)||grouped.get("unmapped")).push(task));const columnWidth=220,rowHeight=82,nodeWidth=180,nodeHeight=55,margin=32;const positions=new Map();let maxRows=1;[...grouped.entries()].forEach(([id,list],column)=>{maxRows=Math.max(maxRows,list.length);list.forEach((task,row)=>positions.set(task.task_id,{x:margin+column*columnWidth,y:margin+row*rowHeight,group:groups.find(g=>g.id===id)}));});svg.setAttribute("width",String(Math.max(900,margin*2+grouped.size*columnWidth)));svg.setAttribute("height",String(Math.max(540,margin*2+maxRows*rowHeight)));
  state.dashboard.dependencies.forEach(dep=>{const from=positions.get(dep.task_id),to=positions.get(dep.depends_on_task_id);if(!from||!to)return;svg.append(svgEl("line",{class:"graph-edge",x1:from.x+nodeWidth/2,y1:from.y+nodeHeight/2,x2:to.x+nodeWidth/2,y2:to.y+nodeHeight/2}));});
  tasks.forEach(task=>{if(!task.parent_id)return;const from=positions.get(task.parent_id),to=positions.get(task.task_id);if(!from||!to)return;svg.append(svgEl("line",{class:"graph-edge graph-parent-edge",x1:from.x+nodeWidth/2,y1:from.y+nodeHeight/2,x2:to.x+nodeWidth/2,y2:to.y+nodeHeight/2}));});
  tasks.forEach(task=>{const pos=positions.get(task.task_id),group=pos.group||{color:state.dashboard.view.theme.muted};const node=svgEl("g",{class:"graph-node",tabindex:"0",role:"button"});node.style.setProperty("--node-color",group.color);node.append(svgEl("rect",{x:pos.x,y:pos.y,width:nodeWidth,height:nodeHeight,rx:7}));const title=svgEl("text",{x:pos.x+10,y:pos.y+20});title.textContent=truncate(task.title,25);const meta=svgEl("text",{class:"node-meta",x:pos.x+10,y:pos.y+39});meta.textContent=truncate(`${task.task_id} · ${task.status}`,31);node.append(title,meta);node.addEventListener("click",()=>openTask(task.task_id));node.addEventListener("keydown",event=>{if(event.key==="Enter")openTask(task.task_id);});svg.append(node);});
}

async function runHistorySearch(event) {
  event.preventDefault();const button=$("#history-form button");button.disabled=true;text(button,"Searching…");const body={query:$("#history-query").value.trim(),project_filter:$("#history-project").value.trim(),date_from:$("#history-from").value,date_to:$("#history-to").value,limit:state.dashboard?.coverage?.history_limit||20,files:$("#history-files").checked};
  try{const response=await fetch(apiURL("api/history/search"),{method:"POST",headers:{"Content-Type":"application/json",Accept:"application/json"},body:JSON.stringify(body)});if(!response.ok)throw new Error(`History search returned ${response.status}`);const result=await response.json();renderSearchResults(result.sessions||[]);}catch(error){showToast(error.message||"Search unavailable");}finally{button.disabled=false;text(button,"Search history");}
}

function renderSearchResults(sessions) {const host=$("#search-results");host.replaceChildren();sessions.forEach(session=>{const card=el("article","session-card");const head=el("div","session-card-head");head.append(text(el("span","work-id"),session.session_id||session.chunk_id),text(el("span","pill"),session.chunk_type||"evidence"));card.append(head,text(el("p"),truncate(session.summary,state.dashboard.view.summary_preview_characters)));const foot=el("div","session-card-foot");foot.append(text(el("span"),session.project_name||session.project_path||"unscoped"),text(el("span"),`${session.machine_id||"unknown"} · ${relativeTime(session.timestamp)}`));card.append(foot);card.addEventListener("click",()=>openSession(session));host.append(card);});if(!sessions.length)host.append(text(el("div","empty-state"),"No indexed evidence matched the query."));}

function openEvidence(taskID,sessionID,finding){if(taskID)openTask(taskID,sessionID,finding);else{const session=state.dashboard.sessions.find(item=>item.session_id===sessionID);openSession(session,finding);}}

function openTask(taskID,sessionID="",finding=null) {
  const dataset=taskDataset(taskID),task=dataset?.tasks?.find(item=>item.task_id===taskID);if(!task)return;const epoch=++state.detailEpoch;state.selectedTask=taskID;state.selectedSession=sessionID;const links=(dataset.worklinks||[]).filter(item=>item.task_id===taskID),deps=(dataset.dependencies||[]).filter(item=>item.task_id===taskID||item.depends_on_task_id===taskID),relationships=state.dashboard.relationships.filter(item=>item.task_id===taskID),relatedSessions=relationships.map(rel=>state.dashboard.sessions.find(item=>item.session_id===rel.session_id)).filter(Boolean);const chosen=sessionID||relationships.find(rel=>rel.kind!=="suggested")?.session_id||"";
  text($("#drawer-eyebrow"),finding?.label||task.status);text($("#drawer-title"),task.title);const body=$("#drawer-body");body.replaceChildren(detailSection("Task",detailGrid({ID:task.task_id,Status:task.status,Owner:task.owner||"—",Pillar:task.pillar||"—",Level:task.level,Parent:task.parent_id||"—",Projection:task.projection||"snapshot",Updated:formatTime(task.updated_at)})));if(task.note)body.append(detailSection("Note",text(el("div","summary-block"),task.note)));const worklinksSection=detailSection("Durable work links (snapshot)",relatedList(links.map(formatWorklink))),relatedWorkSection=detailSection(label("related_work","Related work"),text(el("div","related-loading"),"Running exact, structural, task, artifact, and history passes…"));body.append(worklinksSection,relatedWorkSection,detailSection("Dependencies",relatedList(deps.map(dep=>`${dep.task_id} → ${dep.depends_on_task_id} · ${dep.kind}`))),detailSection("Related history",relatedList(relatedSessions.map(session=>`${session.session_id} · ${session.machine_id||"unknown"} · ${relativeTime(session.timestamp)}`))));setDrawerActions(task.task_id,chosen);openDrawer();refreshExactWorklinks(taskID,worklinksSection,epoch);refreshRelatedWork(taskID,relatedWorkSection,epoch);
}

function taskDataset(taskID){const activeID=state.view.startsWith("program:")?state.view.slice(8):"";const active=activeID?state.programs.get(activeID):null;if(active?.tasks?.some(task=>task.task_id===taskID))return active;for(const program of state.programs.values())if(program.tasks?.some(task=>task.task_id===taskID))return program;const related=state.relatedTasks.get(taskID);if(related)return{tasks:[related.task],worklinks:related.worklinks||[],dependencies:[]};return state.dashboard;}

async function refreshExactWorklinks(taskID,section,epoch){try{const query=new URLSearchParams({task_id:taskID});const response=await fetch(apiURL(`api/task/worklinks?${query}`),{headers:{Accept:"application/json"}});if(!response.ok)throw new Error(`Exact work links returned ${response.status}`);const result=await response.json();if(state.detailEpoch!==epoch||state.selectedTask!==taskID)return;text($("h3",section),"Durable work links (exact)");$(".related-list",section).replaceWith(relatedList((result.worklinks||[]).map(formatWorklink)));}catch(error){if(state.detailEpoch===epoch)showToast(error.message||"Exact work links unavailable");}}

async function refreshRelatedWork(taskID,section,epoch){try{const query=new URLSearchParams({task_id:taskID});const response=await fetch(apiURL(`api/task/related?${query}`),{headers:{Accept:"application/json"}});if(!response.ok)throw new Error(`Related work returned ${response.status}`);const packet=await response.json();if(state.detailEpoch!==epoch||state.selectedTask!==taskID)return;(packet.candidates||[]).forEach(candidate=>state.relatedTasks.set(candidate.task.task_id,{task:candidate.task,worklinks:candidate.worklinks||[]}));section.replaceChildren(text(el("h3"),label("related_work","Related work")),relatedWorkView(packet));}catch(error){if(state.detailEpoch===epoch)section.replaceChildren(text(el("h3"),label("related_work","Related work")),text(el("div","related-error"),error.message||"Related work unavailable"));}}

function relatedWorkView(packet){const host=el("div","related-work");const summary=el("div","related-summary");const verdict=text(el("span","pill"),packet.decision?.label||packet.decision?.id||"Unknown");if(packet.decision?.color)verdict.style.color=packet.decision.color;summary.append(verdict,text(el("span",packet.complete?"coverage-complete":"coverage-partial"),packet.complete?"5 passes complete":"partial evidence"));host.append(summary);if(packet.decision?.detail)host.append(text(el("p","related-detail"),packet.decision.detail));host.append(text(el("div","related-query"),`Query: ${packet.query||"—"}`));const passes=el("div","discovery-passes");(packet.passes||[]).forEach(pass=>{const row=el("div",`discovery-pass ${pass.complete?"complete":"partial"}`);row.append(text(el("span"),pass.label||pass.id),text(el("strong"),`${number(pass.count)} · ${pass.complete?"complete":"partial"}`));if(pass.error)row.title=pass.error;passes.append(row);});host.append(passes);if((packet.candidates||[]).length){host.append(text(el("h4"),"Task candidates"));const list=el("div","related-list");packet.candidates.forEach(candidate=>{const button=el("button","related-item related-task");button.type="button";const head=el("span","related-item-head");const badge=text(el("span","pill"),candidate.verdict?.label||candidate.verdict?.id);if(candidate.verdict?.color)badge.style.color=candidate.verdict.color;head.append(text(el("strong"),candidate.task.task_id),badge);button.append(head,text(el("span","related-item-title"),candidate.task.title),text(el("small"),`${candidate.task.status} · score ${candidate.score} · ${(candidate.reasons||[]).join(", ")}`));button.addEventListener("click",()=>openTask(candidate.task.task_id));list.append(button);});host.append(list);}if((packet.history||[]).length){host.append(text(el("h4"),"History evidence"));const list=el("div","related-list");packet.history.forEach(session=>{const button=el("button","related-item related-history");button.type="button";button.append(text(el("strong"),session.project_name||session.session_id||session.chunk_id),text(el("span","related-item-title"),truncate(session.summary,state.dashboard.view.summary_preview_characters)),text(el("small"),`${session.machine_id||"unknown"} · ${relativeTime(session.timestamp)}`));button.addEventListener("click",()=>openSession(session));list.append(button);});host.append(list);}if(!(packet.candidates||[]).length&&!(packet.history||[]).length)host.append(text(el("div","related-empty"),"No related task or history evidence met the configured thresholds."));return host;}

function formatWorklink(link){return `${link.artifact_type}: ${link.artifact_ref}${link.branch?` · ${link.branch}`:""}${link.thread?` · ${link.thread}`:""}`;}

function openSession(session,finding=null){if(!session)return;++state.detailEpoch;state.selectedTask="";state.selectedSession=session.session_id;text($("#drawer-eyebrow"),finding?.label||session.chunk_type||"History evidence");text($("#drawer-title"),session.project_name||session.session_id);const relation=state.dashboard?.relationships?.find(item=>item.session_id===session.session_id&&item.kind!=="suggested");const body=$("#drawer-body");body.replaceChildren(detailSection("Session",detailGrid({ID:session.session_id,Project:session.project_name||"—",Path:session.project_path||"—",Machine:session.machine_id||"—",Recorded:formatTime(session.timestamp),Match:relation?`${relation.kind} · ${relation.score}`:"unbound"})),detailSection("History evidence",text(el("div","summary-block"),session.summary||"No content returned.")));setDrawerActions(relation?.task_id||"",session.session_id);openDrawer();}

function setDrawerActions(taskID,sessionID){const actions=$("#drawer-actions");actions.replaceChildren();if(taskID){const copyTask=text(el("button",`button ${sessionID?"secondary":"primary"}`),label("copy_task_prompt","Copy task prompt"));copyTask.addEventListener("click",()=>copyTaskPrompt(taskID,copyTask));actions.append(copyTask);}if(sessionID){const copy=text(el("button","button primary"),label("copy_resume","Copy resume packet"));copy.addEventListener("click",()=>copyResume(taskID,sessionID,copy));actions.append(copy);}const close=text(el("button","button secondary"),"Close");close.addEventListener("click",closeDrawer);actions.append(close);}

async function copyTaskPrompt(taskID,button){button.disabled=true;text(button,"Building prompt…");try{const query=new URLSearchParams({task_id:taskID});const response=await fetch(apiURL(`api/task/prompt?${query}`),{headers:{Accept:"application/json"}});if(!response.ok)throw new Error(`Task prompt returned ${response.status}`);const prompt=await response.json();await navigator.clipboard.writeText(prompt.text);showToast("Task prompt copied");text(button,"Copied");setTimeout(()=>text(button,label("copy_task_prompt","Copy task prompt")),1200);}catch(error){showToast(error.message||"Copy unavailable");text(button,label("copy_task_prompt","Copy task prompt"));}finally{button.disabled=false;}}

async function copyResume(taskID,sessionID,button){button.disabled=true;text(button,"Building packet…");try{const query=new URLSearchParams({task_id:taskID,session_id:sessionID});const response=await fetch(apiURL(`api/resume?${query}`),{headers:{Accept:"application/json"}});if(!response.ok)throw new Error(`Resume packet returned ${response.status}`);const packet=await response.json();await navigator.clipboard.writeText(packet.text);showToast("Resume packet copied");text(button,"Copied");setTimeout(()=>text(button,label("copy_resume","Copy resume packet")),1200);}catch(error){showToast(error.message||"Copy unavailable");text(button,label("copy_resume","Copy resume packet"));}finally{button.disabled=false;}}

function detailSection(title,content){const section=el("section","detail-section");section.append(text(el("h3"),title),content);return section;}
function detailGrid(values){const list=el("dl","detail-grid");Object.entries(values).forEach(([key,value])=>{list.append(text(document.createElement("dt"),key),text(document.createElement("dd"),String(value??"—")));});return list;}
function relatedList(values){const list=el("div","related-list");if(!values.length)return text(list,"None in this bounded snapshot.");values.forEach(value=>list.append(text(el("div","related-item"),value)));return list;}
function openDrawer(){$("#detail-drawer").classList.add("open");$("#detail-drawer").setAttribute("aria-hidden","false");$("#drawer-backdrop").classList.add("open");}
function closeDrawer(){++state.detailEpoch;state.selectedTask="";state.selectedSession="";$("#detail-drawer").classList.remove("open");$("#detail-drawer").setAttribute("aria-hidden","true");$("#drawer-backdrop").classList.remove("open");}

function groupForStatus(status){return state.dashboard.status.groups.find(group=>group.statuses.some(value=>value.toLowerCase()===String(status).toLowerCase()));}
function mapBy(items,key){return Object.fromEntries(items.map(item=>[item[key],item]));}
function groupBy(items,key){return items.reduce((result,item)=>{(result[item[key]]??=[]).push(item);return result;},{});}
function taskMatches(task){if(!state.query)return true;return [task.task_id,task.title,task.status,task.owner,task.pillar,task.level].some(value=>String(value||"").toLowerCase().includes(state.query));}
function matchesQuery(finding,task,session){if(!state.query)return true;return [finding.kind,finding.label,finding.detail,finding.task_id,finding.session_id,task?.title,task?.owner,session?.summary,session?.project_name,session?.machine_id].some(value=>String(value||"").toLowerCase().includes(state.query));}
function formatTime(value){if(!value)return "unknown";const date=new Date(value);return Number.isNaN(date.valueOf())?String(value):date.toLocaleString();}
function relativeTime(value){if(!value)return "unknown";const elapsed=Date.now()-new Date(value).valueOf();if(!Number.isFinite(elapsed))return String(value);const hours=Math.max(0,Math.floor(elapsed/3600000));return formatAge(hours);}
function formatAge(hours){if(!hours)return "now";if(hours<24)return `${hours}h`;const days=Math.floor(hours/24);if(days<60)return `${days}d`;return `${Math.floor(days/30)}mo`;}
function number(value){return new Intl.NumberFormat().format(value||0);}
function truncate(value,limit){const text=String(value||"").trim();return text.length<=limit?text:`${text.slice(0,limit).trim()}…`;}
function setLoading(active){state.loading=active;$("#loading-bar").classList.toggle("active",active);$("#refresh-button").classList.toggle("loading",active);$("#refresh-button").disabled=active;}
function showError(message){text($("#error-banner"),message);$("#error-banner").classList.remove("hidden");}
function hideError(){$("#error-banner").classList.add("hidden");}
let toastTimer;function showToast(message){const toast=$("#toast");text(toast,message);toast.classList.add("show");clearTimeout(toastTimer);toastTimer=setTimeout(()=>toast.classList.remove("show"),2200);}
