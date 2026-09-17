import { saveSetting } from "./settings.js";
// Workbench planning presentation. Conversations, drafts and created references
// come from the project planning API; only unsaved edits and panel preferences
// have a bounded browser handoff.
import * as uiVersion from './ui-version.js';

const root = document.documentElement;
const pendingKey = 'pellets-planner-pending';
let panel, data = null, state = emptyState(), chat = null, load = null;
let flight = null, timer, dirty = false, conflict = false, failed = null;
let splitting = null, confirming = false, feedback = '';
let savingNewChatPreference = false;
let skipNewChatConfirmation = JSON.parse(document.documentElement.dataset.settings || '{}').skip_new_chat_confirmation === 'true';
let trayCollapsed = false;
let modelsLoaded = false, refreshedAt = 0;
let activeOperation = null, handoffError = '', lastPlannerFocus = null, presentationToRestore = null;
let selectedTab = (document.documentElement.dataset.rightPanelTab || saved('pellets-right-panel-tab')) === 'plan' ? 'plan' : 'execution';
let pinned = null;
try { pinned = JSON.parse(saved('pellets-planner-chat') || 'null'); } catch {}
try { const handoff=readPending(); if(handoff?.operation) pinned={project:handoff.project,chat:handoff.chat_id}; } catch {}
const clone = value => JSON.parse(JSON.stringify(value));
const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
function saved(key) { try { return localStorage.getItem(key); } catch { return null; } }
function preference(key, value) { try { localStorage.setItem(key, value); } catch {} }
function emptyState() { return {workspace_id:0, access_mode:'automatic', input:'', model:'', effort:'', messages:[], drafts:[], refining_draft_id:null}; }
function normalize(value) { return {...emptyState(), ...value, messages:value?.messages || [], drafts:value?.drafts || []}; }
function projectCode() { return data?.project?.code || pinned?.project || document.querySelector('.app-shell')?.dataset.project; }
function initialWorkspace(code) {
  const shell = document.querySelector('.app-shell');
  if (shell?.dataset.project === code && Number(shell.dataset.workspace) > 0) return Number(shell.dataset.workspace);
  return data?.project?.code === code && data.routing?.length === 1 ? data.routing[0].id : 0;
}
function endpoint(code = projectCode()) { return '/projects/' + encodeURIComponent(code) + '/planning'; }
function csrf() { return document.querySelector('input[name="_csrf"]')?.value || ''; }
function created(draft) { return !!(draft.created_reference || draft.created_number); }
function selected() { return state.drafts.filter(draft => draft.selected && !created(draft)); }
function readPending() {
  const value=JSON.parse(sessionStorage.getItem(pendingKey)||'null');
  if(value&&(!value.created_at||Date.now()-value.created_at>30*60*1000)){clearPending();return null;}
  return value;
}
function presentationField(focus) {
  if(focus?.split)return panel?.querySelector('[data-draft-id="'+CSS.escape(focus.draft)+'"] [data-split-titles]');
  const form=focus&&document.getElementById(focus.form);
  return form&&Array.from(form.elements).find(el=>el.name===focus.name);
}
function capturePresentation() {
  if(!panel)return null;
  let focus=lastPlannerFocus;
  if(focus){const field=presentationField(focus);if(field)focus={...focus,start:field.selectionStart,end:field.selectionEnd,direction:field.selectionDirection};}
  const split=splitting?{id:splitting,text:panel.querySelector('[data-draft-id="'+CSS.escape(splitting)+'"] [data-split-titles]')?.value||''}:null;
  return {focus,split,open:Array.from(panel.querySelectorAll('dialog[id][open]'),el=>el.id),
    scrolls:Array.from(panel.querySelectorAll('.plan-transcript,.plan-draft-list'),el=>({className:el.className,top:el.scrollTop,left:el.scrollLeft}))};
}
function storePending(force = false) {
  if ((!dirty && !activeOperation && !failed && !force) || !projectCode()) return '';
  const operation=activeOperation||failed;
  const storedOperation=operation?{url:operation.url,action:operation.action,replace:operation.replace,
    payload:{...operation.payload,_csrf:undefined}}:null;
  const code=operation?.replace?decodeURIComponent(operation.url.split('/')[2]):projectCode();
  const value = JSON.stringify({created_at:Date.now(),project:code, chat_id:operation?.replace?null:chat?.id, version:chat?.version,
    state:(dirty||operation)?(operation?.replace?operation.payload.state:{workspace_id:state.workspace_id,access_mode:state.access_mode,input:state.input, model:state.model, effort:state.effort, refining_draft_id:state.refining_draft_id, drafts:state.drafts}):null,
    operation:storedOperation,presentation:capturePresentation()});
  try {
    if(new TextEncoder().encode(value).length>256*1024)throw Error('These planning edits are too large to keep through a reload. Save them or shorten them before reloading; they remain in this panel.');
    sessionStorage.setItem(pendingKey, value);handoffError='';
  } catch(error) {clearPending();handoffError=error.message||'Planning edits could not be stored for reload. Keep this page open until they are saved.';}
  return handoffError;
}
function clearPending() { try { sessionStorage.removeItem(pendingKey); } catch {} }
function markDirty() {
  if(failed?.safeToReplace){failed=null;feedback='';}
  dirty = true;
  storePending();
  clearTimeout(timer);
  timer = setTimeout(() => { if (!flight && !failed && !conflict) mutate(chat ? 'save' : 'new'); }, 500);
}
function pin() {
  if (chat) { pinned = {project:projectCode(), chat:chat.id}; preference('pellets-planner-chat', JSON.stringify(pinned)); }
}
async function json(url, options = {}) {
  if (uiVersion.isOutdated()) throw Error('Reload the updated interface before continuing. Your planning edits stay here.');
  uiVersion.beginRequest();
  try {
    const response = await fetch(url, {...options, credentials:'same-origin', cache:'no-store',
      headers:uiVersion.headers({'Accept':'application/json', ...(options.body ? {'Content-Type':'application/json'} : {})})});
    if (!uiVersion.inspectResponse(response)) throw Error('Reload the updated interface to continue planning.');
    const result = await response.json();
    if (!response.ok) {
      const error = Error(result.error?.message || result.message || result.error || 'Planning could not be saved. Your edits stay here.');
      error.status = response.status; error.code = result.code || result.error?.code; throw error;
    }
    return result;
  } finally { uiVersion.endRequest(); }
}
function useOnlyWorkspace() {
  if (!state.workspace_id && data?.routing?.length === 1) {
    state.workspace_id = data.routing[0].id;
    if (chat) dirty = true;
  }
}
function accept(value) {
  if (value.chat?.state?.drafts?.some(d=>!created(d)&&!state.drafts.some(existing=>existing.id===d.id))) trayCollapsed=false;
  if ((chat && value.chat?.id !== chat.id) || (data && value.project?.code !== data.project?.code)) {trayCollapsed=false;}
  if (value.models?.length) modelsLoaded = true;
  data = {...data, ...value, models:value.models?.length ? value.models : data?.models || []};
  chat = value.chat;
  const previousWorkspace = state.workspace_id;
  state = normalize(clone(chat?.state || {}));
  if (!chat) state.workspace_id = initialWorkspace(value.project.code);
  useOnlyWorkspace();
  if (previousWorkspace !== state.workspace_id) {modelsLoaded=false;data.models=[];}
  pin();
}
async function ensureLoaded() {
  if (data || load || !projectCode()) return load;
  const code = projectCode(), suffix = pinned?.project === code && pinned.chat ? '?chat=' + encodeURIComponent(pinned.chat) : '';
  load = (async () => {
    try {
      const result = await json(endpoint(code) + suffix), current = clone(state);
      accept(result);
      if (dirty) state = mergeEdits(state, emptyState(), current);
      let pending;
      try { pending = readPending(); } catch {}
      if (pending?.project === projectCode() && (!pending.chat_id || String(pending.chat_id) === String(chat?.id))) {
        presentationToRestore=pending.presentation;
        splitting=pending.presentation?.split?.id||null;
        if(pending.state){state = {...state, ...pending.state};dirty=true;}
        if (dirty && chat && pending.version != null && !pending.operation?.replace) chat.version = pending.version;
        if(pending.operation){failed={...pending.operation,before:clone(pending.operation.payload.state)};conflict=false;feedback='A planning request was interrupted. Retry the same request to confirm its result; your edits are kept.';}
        else if(dirty)feedback = 'Unfinished planning edits restored.';
      }
      useOnlyWorkspace();
      render();
      applyPresentation();
      if(!dirty&&!failed)clearPending();
      if (dirty&&!failed) markDirty();
    } catch (error) { feedback = error.message; render(); }
    finally { load = null; }
  })();
  return load;
}
function mergeEdits(server, before, current) {
  const result = normalize(clone(server));
  for (const name of ['workspace_id','access_mode','input','model','effort','refining_draft_id'])
    if (JSON.stringify(current[name]) !== JSON.stringify(before[name])) result[name] = current[name];
  const original = new Map(before.drafts.map(d => [d.id,d]));
  const local = new Map(current.drafts.map(d => [d.id,d]));
  result.drafts = result.drafts.filter(d => !original.has(d.id) || local.has(d.id));
  for (const draft of result.drafts) {
    const a = original.get(draft.id), b = local.get(draft.id);
    if (!a || !b || created(draft)) continue;
    for (const name of ['title','description','acceptance','group','reason','selected'])
      if (JSON.stringify(a[name]) !== JSON.stringify(b[name])) draft[name] = b[name];
  }
  for (const draft of current.drafts)
    if (!original.has(draft.id) && !result.drafts.some(d => d.id === draft.id)) result.drafts.push(draft);
  return result;
}
async function mutate(action, retry = false, replacement = null) {
  clearTimeout(timer);
  if (flight) { await flight; if (failed || conflict) return; }
  if (uiVersion.isOutdated() || conflict || (!retry && failed)) return;
  if (!data) await ensureLoaded();
  if (!data) return;
  if (action !== 'new' && !chat) { await mutate('new'); if (!chat || failed) return; }
  if ((action === 'create' || (action === 'send' && state.workspace_id !== chat?.state.workspace_id)) && dirty && !retry) { await mutate('save'); if (failed || conflict) return; }
  const before = clone(state);
  const operation = replacement || (retry ? failed : {url:endpoint(), before, action, payload:{_csrf:csrf(), action,
    chat_id:chat?.id, version:chat?.version, request_id:crypto.randomUUID(), state:before,
    ...(action === 'create' ? {draft_ids:selected().map(d => d.id)} : {})}});
  operation.payload._csrf=csrf();
  activeOperation=operation;
  if (action === 'send' && !retry) { state.input = ''; dirty = true; }
  feedback = action === 'send' ? 'Planning…' : action === 'create' ? 'Creating selected pellets…' : 'Saving…';
  storePending();
  flight = (async () => {
    try {
      const result = await json(operation.url, {method:'POST', body:JSON.stringify(operation.payload)});
      const current = clone(state);
      accept(result);
      if(!operation.replace)state = mergeEdits(state, operation.before, current);
      dirty = JSON.stringify(state) !== JSON.stringify(normalize(chat?.state));
      failed = null;
      feedback = '';
      if (!dirty) {clearPending();handoffError='';}
      if (action === 'create') document.dispatchEvent(new CustomEvent('pellets-refresh'));
    } catch (error) {
      failed = operation; conflict = ['planning_conflict','planning_chat_conflict','planning_request_id_conflict','planning_request_conflict','planning_draft_unavailable'].includes(error.code);
      failed.safeToReplace=!!error.status&&!conflict;
      feedback = error.message;
      if (action === 'send' && !state.input) state.input = operation.before.input;
      dirty = true;
    } finally {
      flight = null;
      activeOperation=null;
      if(dirty||failed)storePending();
      render();
      if (dirty && !failed && !conflict) markDirty();
    }
  })();
  render();
  return flight;
}
async function send() {
  if (!state.input.trim() || flight || failed || conflict) return;
  await mutate('send');
  panel?.querySelector('#plan-message')?.focus({preventScroll:true});
}
function makeDraft(title = '', description = '', acceptance = '', group = '') {
  return {id:crypto.randomUUID(),title,description,acceptance,group,reason:'',selected:true};
}
function routes(group) {
  if (!data) return '';
  const rows = data.routing || [];
  const matches = rows.filter(w => {
    if (data.assignments_enabled === false) return true;
    if (!group) return w.include_ungrouped;
    if (w.mode === 'explicit') return (w.groups || []).includes(group);
    return !rows.some(other => other.id !== w.id && other.mode === 'explicit' && (other.groups || []).includes(group));
  });
  return matches.length ? 'Routes to ' + matches.map(w => w.name).join(', ') : 'No workspace currently accepts this group.';
}
function scaffold() {
  if (panel.querySelector('#plan-content')) return;
  panel.innerHTML = `<dialog id="plan-new-dialog" aria-labelledby="plan-new-heading" aria-describedby="plan-new-description"><h2 id="plan-new-heading">Start a new chat?</h2><p id="plan-new-description"></p><label class="plan-confirm-preference"><input type="checkbox" id="plan-skip-new-confirmation"> Don’t ask me again</label><p class="plan-new-error" role="alert" hidden></p><div class="plan-new-actions"><button type="button" data-plan="cancel-new" autofocus>Cancel</button><button type="button" class="primary-button" data-plan="confirm-new">Start new chat</button></div></dialog><div id="plan-content"><div class="plan-context"><div data-plan-new><button type="button" class="quiet" data-plan="new">New chat</button></div></div>
    <div class="plan-transcript" aria-live="polite"><div class="plan-messages"></div><div class="plan-receipts" aria-label="Created pellets"></div><div class="plan-pending" hidden></div><div class="plan-chat-status"><div class="plan-status" role="status" aria-live="polite" aria-atomic="true" hidden></div></div></div><section class="plan-drafts" aria-label="Proposed pellets"><div class="plan-draft-heading"><button type="button" class="plan-tray-toggle" data-plan="toggle-tray" aria-expanded="true" aria-controls="plan-proposal-list"><svg class="plan-tray-chevron" viewBox="0 0 16 16" aria-hidden="true"><path d="m6 4 4 4-4 4"/></svg><span class="plan-total">0</span> <span data-proposal-label>proposed pellets</span></button><div class="plan-batch-tools" role="group" aria-label="Proposal actions"><button type="button" data-plan="select-all" aria-label="Select all" title="Select all"><svg viewBox="0 0 16 16" aria-hidden="true"><rect x="3" y="3" width="10" height="10" rx="2"/><path d="m5 8 2 2 4-4"/></svg></button><button type="button" data-plan="combine" aria-label="Combine selected" title="Combine selected"><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3 3v2c0 2 5 2 5 4v4M13 3v2c0 2-5 2-5 4m-3 1 3 3 3-3"/></svg></button><button type="button" data-plan="add-draft" aria-label="Add draft" title="Add draft"><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 3v10M3 8h10"/></svg></button></div></div><div class="plan-draft-list" id="plan-proposal-list"></div><div class="plan-create-row"><button type="button" data-plan="dismiss-all">Dismiss all</button><button type="button" class="primary-button" data-plan="create">Create pellets</button></div></section>
    <datalist id="plan-groups"></datalist><div class="plan-bottom"><form id="plan-form" method="post"><input type="hidden" name="version"><div class="plan-refining" hidden></div><div class="plan-composer-settings"><div class="plan-folder"><label class="plan-workspace-label"><select data-value-prefix="Folder: " id="plan-workspace" aria-label="Working folder" aria-describedby="plan-folder-error plan-folder-path"></select></label><span class="plan-folder-context"></span><p id="plan-folder-error" class="plan-folder-error" role="alert" hidden></p></div><label class="plan-access"><select data-value-prefix="Approval: " id="plan-access" name="access_mode" aria-label="Planning access"><option value="automatic">Automatic</option><option value="full">None (full access)</option></select></label></div><span id="plan-folder-path" class="plan-path visually-hidden"></span><label class="visually-hidden" for="plan-message">Message planner</label><textarea id="plan-message" name="input" rows="2" placeholder="What should we plan?" maxlength="65536"></textarea><div class="plan-composer-tools"><select id="plan-model" name="model" aria-label="Planning model"></select><span class="plan-tool-divider"></span><select id="plan-effort" name="effort" aria-label="Reasoning effort"></select><button type="submit" class="plan-send" aria-label="Send message">↑</button></div></form><button type="button" class="plan-select-models" data-plan="models">Choose a model…</button></div></div>`;
  const dialog=panel.querySelector('#plan-new-dialog');
  dialog.addEventListener('cancel',event=>{if(savingNewChatPreference)event.preventDefault();});
  dialog.addEventListener('close',()=>{confirming=false;panel.querySelector('[data-plan=new]').focus({preventScroll:true});});
}
function updateOptions(select, options, value) {
  const signature = JSON.stringify(options);
  if (select.dataset.options !== signature) {
    select.replaceChildren(...options.map(item => {const option = document.createElement('option'); option.value = item.id; option.textContent = item.name; return option;}));
    select.dataset.options = signature;
  }
  if (select.value !== value) select.value = value;
}
function draftNode(draft) {
  const node = document.createElement('article');
  node.dataset.draftId = draft.id;
  node.className = created(draft) ? 'plan-created' : 'plan-card';
  if (created(draft)) {
    const reference = draft.created_reference || `${projectCode()}-${draft.created_number}`;
    node.innerHTML = `<a href="/projects/${encodeURIComponent(projectCode())}/tasks/${encodeURIComponent(reference)}" data-plan-reference><span class="plan-row-check" aria-label="Created">Created</span><span class="plan-row-ref">${esc(reference)}</span><span class="plan-row-title"></span><span class="plan-row-group"></span><span aria-hidden="true">↗</span></a>`;
  } else {
    const id = 'plan-draft-' + draft.id;
    node.innerHTML = `<input class="plan-row-select" type="checkbox" name="selected" form="${esc(id)}" data-field="selected"><button type="button" class="plan-open-draft" data-plan="edit-draft" aria-haspopup="dialog"><span class="plan-row-title"></span><span class="plan-row-group"></span></button><dialog class="plan-draft-dialog" id="plan-details-${esc(draft.id)}" aria-labelledby="plan-editor-heading-${esc(draft.id)}"><header><h2 id="plan-editor-heading-${esc(draft.id)}">Edit proposed pellet</h2><button type="button" data-plan="close-editor" aria-label="Close draft editor">×</button></header><div class="plan-row-editor"><form id="${esc(id)}" method="post" class="plan-draft-form"><input type="hidden" name="version"><label class="visually-hidden" for="plan-title-${esc(draft.id)}">Pellet title</label><input class="plan-card-title" id="plan-title-${esc(draft.id)}" name="title" data-field="title" maxlength="4096"><label class="plan-field-label">Description<textarea name="description" data-field="description" rows="4" maxlength="65536"></textarea></label><label class="plan-field-label">Acceptance criteria<textarea class="plan-acceptance" name="acceptance" data-field="acceptance" rows="3" maxlength="65536"></textarea></label><div class="plan-group-row"><label>Group<input name="group" data-field="group" list="plan-groups" placeholder="Ungrouped" maxlength="4096"></label></div></form><p class="plan-route"></p><p class="plan-reason"></p><div class="plan-card-actions"><button type="button" data-plan="refine">Refine in chat</button><button type="button" data-plan="split">Split pellet</button></div><div class="plan-split" hidden><label>One title per new pellet<textarea rows="3" data-split-titles></textarea></label><p>Each draft keeps the description and acceptance criteria for refinement.</p><button type="button" data-plan="apply-split">Split into drafts</button><button type="button" data-plan="cancel-split">Cancel</button><span data-split-error role="alert"></span></div></div><footer><span class="plan-editor-status" role="status"></span><button type="button" data-plan="close-editor">Done</button></footer></dialog>`;
  }
  if (!created(draft)) node.insertAdjacentHTML('beforeend', '<button type="button" class="plan-dismiss" data-plan="remove" title="Dismiss proposal">×</button>');
  const dialog=node.querySelector('dialog');
  if(dialog)dialog.addEventListener('close',()=>{
    if(node.isConnected&&(document.activeElement===document.body||dialog.contains(document.activeElement)))node.querySelector('.plan-open-draft').focus({preventScroll:true});
  });
  return node;
}
function deleteDrafts(drafts) {
  if (!drafts.length || flight || failed || conflict) return;
  const ids = new Set(drafts.filter(d=>!created(d)).map(d=>d.id));
  if (!ids.size) return;
  state.drafts = state.drafts.filter(d=>!ids.has(d.id));
  if (ids.has(state.refining_draft_id)) state.refining_draft_id=null;
  if (ids.has(splitting)) splitting=null;
  markDirty();render();
  panel.querySelector('#plan-message').focus();
}
function render() {
  if (!panel) return;
  scaffold();
  const p = data?.project;
  panel.dataset.planningProject = p?.code || projectCode() || '';
  panel.querySelector('.plan-context').hidden = !state.messages.length && !state.drafts.length && !state.input.trim();
  const workspace = (data?.routing || []).find(w => w.id === state.workspace_id);
  const workspaceProblem = !workspace ? (state.workspace_id ? 'The chat’s checkout is unavailable. Restore it or start a new chat.' : data?.routing?.length ? 'Choose a working folder to send this message.' : 'This project has no registered checkout available for planning.') : '';
  const path = workspace?.path || '';
  const folderError = panel.querySelector('.plan-folder-error');
  folderError.hidden = !workspaceProblem;
  if (folderError.textContent !== workspaceProblem) folderError.textContent = workspaceProblem;
  panel.querySelector('.plan-folder').classList.toggle('invalid', !!workspaceProblem);
  panel.querySelector('#plan-workspace').setAttribute('aria-invalid', String(!!workspaceProblem));
  const folderContext = panel.querySelector('.plan-folder-context');
  folderContext.hidden = (data?.routing?.length || 0) > 1 || !workspace;
  folderContext.textContent = workspace ? 'Folder: ' + workspace.name : '';
  folderContext.title = workspace ? workspace.path || workspace.name : '';
  panel.querySelector('.plan-workspace-label').hidden = (data?.routing?.length || 0) <= 1;
  panel.querySelector('.plan-path').textContent = path;
  panel.querySelector('.plan-path').title = path;
  panel.querySelector('#plan-workspace').title = path;
  const workspaceChoices = (data?.routing || []).map(w => ({id:String(w.id),name:chat?.state.workspace_id ? w.name : w.path || w.name}));
  if (!workspace) workspaceChoices.unshift({id:String(state.workspace_id || 0),name:state.workspace_id ? 'Unavailable workspace' : 'Choose folder…'});
  updateOptions(panel.querySelector('#plan-workspace'), workspaceChoices, String(state.workspace_id || 0));
  const newDialog=panel.querySelector('#plan-new-dialog');
  const draftCount=state.drafts.filter(d=>!created(d)).length;
  panel.querySelector('#plan-new-description').textContent='Your current conversation'+(draftCount?' and '+draftCount+' draft proposal'+(draftCount===1?'':'s'):'')+' will be replaced. Created pellets will remain.';
  if(confirming&&!newDialog.open)newDialog.showModal();
  if(!confirming&&newDialog.open)newDialog.close();
  const transcript = panel.querySelector('.plan-transcript');
  const messages=panel.querySelector('.plan-messages');
  const following = transcript.scrollHeight - transcript.clientHeight - transcript.scrollTop < 32;
  if (!state.messages.length) {
    messages.replaceChildren();
  } else {
    const ids = new Set();
    state.messages.forEach((message, index) => {
      const id = String(message.id || index); ids.add(id);
      let node = Array.from(messages.children).find(item => item.dataset.messageId === id);
      if (!node) { node = document.createElement('div'); node.dataset.messageId = id; node.className = 'plan-message'; node.innerHTML = '<span class="plan-speaker"></span><p></p>'; messages.append(node); }
      node.className = 'plan-message ' + (message.role === 'user' ? 'user' : 'assistant');
      node.querySelector('.plan-speaker').textContent = (message.role === 'user' ? 'You' : 'Planner') + (message.model ? ' · ' + message.model + (message.effort ? ' / ' + message.effort : '') : '');
      if (node.querySelector('p').textContent !== message.text) node.querySelector('p').textContent = message.text;
    });
    for (const node of Array.from(messages.children)) if (!ids.has(node.dataset.messageId)) node.remove();
  }
  if (following) transcript.scrollTop = transcript.scrollHeight;
  const proposals=state.drafts.filter(d=>!created(d));
  const list=panel.querySelector('.plan-draft-list'), receipts=panel.querySelector('.plan-receipts');
  for (const host of [list,receipts]) for (const node of Array.from(host.children)) {
    if (!state.drafts.some(d=>String(d.id)===node.dataset.draftId && created(d)===(host===receipts))) node.remove();
  }
  state.drafts.forEach((draft,index) => {
    const host=created(draft)?receipts:list;
    let node = Array.from(host.children).find(item => item.dataset.draftId === String(draft.id));
    if (node && node.classList.contains('plan-created') !== created(draft)) { node.remove(); node = null; }
    if (!node) { node = draftNode(draft); host.append(node); }
    node.querySelector('.plan-row-title').textContent = draft.title || 'Untitled pellet';
    node.querySelector('.plan-row-title').title = draft.title || 'Untitled pellet';
    node.querySelector('.plan-row-group').textContent = draft.group || '—';
    if (created(draft)) {
      const target = document.querySelector('.app-shell')?.dataset.project === projectCode() ? 'inspector-host' : 'app-content';
      node.querySelector('a').setAttribute('data-on:click', `@navigate('${target}')`);
      return;
    }
    node.querySelector('[data-plan=remove]').setAttribute('aria-label','Dismiss proposal: '+(draft.title || 'Untitled pellet'));
    node.querySelector('[data-plan=remove]').disabled=!!flight || !!failed || conflict;
    node.querySelector('[data-field=selected]').setAttribute('aria-label', 'Select draft ' + (index+1) + ': ' + (draft.title || 'Untitled pellet'));
    for (const field of node.querySelectorAll('[data-field]')) {
      const value = draft[field.dataset.field] ?? '';
      if (field.type === 'checkbox') field.checked = !!value;
      else if (field.value !== String(value)) field.value = value;
    }
    node.querySelector('form').action = endpoint();
    node.querySelector('[name=version]').value = chat?.version || '';
    node.querySelector('.plan-route').textContent = routes(draft.group);
    node.querySelector('.plan-reason').textContent = draft.reason || '';
    node.querySelector('.plan-split').hidden = splitting !== draft.id;
  });
  panel.querySelector('.plan-drafts').hidden = !proposals.length;
  panel.querySelector('.plan-total').textContent = proposals.length;
  panel.querySelector('[data-proposal-label]').textContent=proposals.length===1?'proposed pellet':'proposed pellets';
  panel.querySelector('.plan-tray-toggle').setAttribute('aria-expanded',String(!trayCollapsed));
  for (const selector of ['.plan-batch-tools','.plan-draft-list','.plan-create-row']) panel.querySelector(selector).hidden=trayCollapsed;
  panel.querySelector('[data-plan=combine]').disabled = selected().length < 2 || !!flight;
  panel.querySelector('[data-plan=dismiss-all]').disabled = !!flight || !!failed || conflict;
  const create = panel.querySelector('[data-plan=create]');
  create.textContent = `Create ${selected().length} pellet${selected().length === 1 ? '' : 's'}`;
  create.disabled = !selected().length || selected().some(d => !d.title.trim() || (!d.description.trim() && !(d.acceptance || '').trim())) || !!flight || !!failed || conflict || uiVersion.isOutdated();
  panel.querySelector('.plan-create-row').hidden = trayCollapsed || !proposals.length;
  const composer = panel.querySelector('#plan-form'); composer.action = endpoint();
  composer.elements.version.value = chat?.version || '';
  if (composer.elements.input.value !== state.input) composer.elements.input.value = state.input;
  const models = (data?.models || []).map(m => ({id:m.id, name:m.name || m.id}));
  if (!models.some(m => m.id === (state.model || ''))) models.unshift({id:state.model || '', name:state.model || 'Configured model'});
  updateOptions(composer.elements.model, models, state.model || '');
  composer.elements.access_mode.value = state.access_mode || 'automatic';
  const efforts = (data?.models || []).find(m => m.id === state.model)?.efforts || [];
  const effortOptions = efforts.map(value => ({id:value,name:value}));
  if (!effortOptions.some(e => e.id === (state.effort || ''))) effortOptions.unshift({id:state.effort || '',name:state.effort || 'Configured effort'});
  updateOptions(composer.elements.effort, effortOptions, state.effort || '');
  composer.querySelector('[type=submit]').disabled = !workspace || !state.input.trim() || !!flight || !!failed || conflict || uiVersion.isOutdated();
  for (const button of panel.querySelectorAll('[data-plan=new],[data-plan=confirm-new]')) button.disabled = !!flight;
  for(const field of panel.querySelectorAll('input,textarea,select'))field.disabled=!!activeOperation?.replace;
  panel.querySelector('#plan-workspace').disabled = !!chat?.state.workspace_id || !!flight;
  composer.elements.model.disabled = !workspace || !!activeOperation?.replace;
  composer.elements.effort.disabled = !workspace || !!activeOperation?.replace;
  composer.elements.access_mode.disabled = !workspace || !!activeOperation || (!!failed && !failed.safeToReplace);
  panel.querySelector('[data-plan=models]').hidden = true;
  const refining = state.drafts.find(d => d.id === state.refining_draft_id && !created(d));
  const refine = panel.querySelector('.plan-refining'); refine.hidden = !refining;
  const refiningHTML = refining ? `Refining ${esc(refining.title || 'Untitled pellet')}<button type="button" data-plan="end-refine" aria-label="Stop refining">×</button>` : '';
  if (refine.innerHTML !== refiningHTML) refine.innerHTML = refiningHTML;
  const groups = panel.querySelector('#plan-groups');
  const groupHTML = (data?.groups || []).map(g => `<option value="${esc(g)}"></option>`).join('');
  if (groups.innerHTML !== groupHTML) groups.innerHTML = groupHTML;
  const status = panel.querySelector('.plan-status');
  status.classList.toggle('busy', !!activeOperation);
  const submissionError = !!failed || conflict || !!handoffError;
  status.classList.toggle('error', submissionError);
  if (submissionError) {
    if (status.nextElementSibling !== composer) composer.before(status);
  } else if (status.parentElement !== panel.querySelector('.plan-chat-status')) {
    panel.querySelector('.plan-chat-status').append(status);
  }
  composer.querySelector('[type=submit]').title = workspaceProblem || '';
  const text=handoffError||(activeOperation?.action==='save'||activeOperation?.action==='new'?'':feedback);
  status.hidden=!text;
  const pending=panel.querySelector('.plan-pending');
  pending.hidden=activeOperation?.action!=='send';
  const pendingText=activeOperation?.action==='send'?activeOperation.before?.input || activeOperation.payload.state.input:'';
  if(pending.textContent!==pendingText)pending.textContent=pendingText;
  if(following)transcript.scrollTop=transcript.scrollHeight;
  const statusSignature=JSON.stringify([text,!!failed,conflict]);
  if(status.dataset.signature!==statusSignature){
    status.dataset.signature=statusSignature;status.replaceChildren(document.createTextNode(text));
    if (failed && !conflict) {const retry = document.createElement('button');retry.type='button';retry.dataset.plan='retry';retry.textContent='Retry request';status.append(retry);}
    if (conflict) {const reload = document.createElement('button');reload.type='button';reload.dataset.plan='reload';reload.textContent='Reload saved chat';status.append(reload);}
  }
  for(const editorStatus of panel.querySelectorAll('.plan-editor-status')){
    const signature=JSON.stringify([submissionError,text,dirty,activeOperation?.action,!!failed,conflict]);
    if(editorStatus.dataset.signature===signature)continue;
    editorStatus.dataset.signature=signature;
    editorStatus.classList.toggle('error',submissionError);
    editorStatus.replaceChildren(document.createTextNode(submissionError ? text || 'Changes could not be saved.' : dirty || activeOperation?.action==='save' ? 'Saving changes…' : 'Changes saved automatically'));
    if(failed&&!conflict){const retry=document.createElement('button');retry.type='button';retry.dataset.plan='retry';retry.textContent='Retry request';editorStatus.append(retry);}
    if(conflict){const reload=document.createElement('button');reload.type='button';reload.dataset.plan='reload';reload.textContent='Reload saved chat';editorStatus.append(reload);}
  }
  window.Dropdowns?.enhance(panel);
  const folderTrigger = panel.querySelector('#plan-workspace-trigger');
  if (folderTrigger) {folderTrigger.setAttribute('aria-invalid', String(!!workspaceProblem));folderTrigger.setAttribute('aria-describedby', 'plan-folder-error plan-folder-path');}
  if(following)transcript.scrollTop=transcript.scrollHeight;
}
function chooseTab(value, focus = false) {
  selectedTab = value; saveSetting('right_panel_tab', value); preference('pellets-right-panel-tab', value); sync();
  if (focus) document.getElementById(value === 'plan' ? 'plan-tab' : 'execution-tab')?.focus();
}
function sync() {
  const host = document.getElementById('right-panel');
  if (!host) return;
  if (!panel) panel = document.getElementById('planning-panel');
  if (panel.parentElement !== host) { document.getElementById('planning-panel')?.remove(); host.append(panel); }
  const collapsed = root.classList.contains('execution-collapsed');
  host.hidden = collapsed; host.inert = collapsed;
  // The phone Plan panel covers the queue and rail. They stay outside the tab
  // sequence until the user changes tabs, collapses the panel, or widens it.
  const covering = selectedTab === 'plan' && !collapsed && matchMedia('(max-width:600px)').matches;
  const main = document.getElementById('main'), rail = document.getElementById('project-drawer');
  if (main) main.inert = covering;
  if (rail) rail.inert = covering || root.classList.contains('navigation-collapsed');
  root.classList.toggle('planning-active', selectedTab === 'plan');
  panel.hidden = selectedTab !== 'plan'; panel.inert = selectedTab !== 'plan';
  const execution = document.getElementById('execution');
  if (execution) {execution.hidden = collapsed || selectedTab === 'plan'; execution.inert = collapsed || selectedTab === 'plan';}
  for (const name of ['plan','execution']) {
    const tab = document.getElementById(name + '-tab');
    if (tab) {tab.setAttribute('aria-selected',String(name === selectedTab));tab.tabIndex=name === selectedTab ? 0 : -1;}
  }
  if (selectedTab === 'plan') { scaffold(); ensureLoaded(); }
  if (data) render();
  applyPresentation();
}
function status(value, attention) {
  const tab = document.getElementById('execution-tab'), dot = tab?.querySelector('.execution-tab-dot');
  if (tab) tab.title = 'Execution · ' + value;
  if (dot) {dot.hidden = !attention;dot.dataset.state = attention ? 'attention' : 'idle';}
  const toggle = document.getElementById('toggle-execution');
  if (toggle) {toggle.setAttribute('aria-controls','right-panel');toggle.title = `${root.classList.contains('execution-collapsed') ? 'Show' : 'Hide'} right panel · Execution: ${value}`;toggle.setAttribute('aria-label',toggle.title);}
}
async function loadModels(openMenu = false, triggerID = 'plan-model-trigger') {
  if (!data) await ensureLoaded();
  if (!data) return;
  try {
    feedback='Loading available models…';render();
    const code=projectCode(), workspaceID=state.workspace_id, accessMode=state.access_mode || 'automatic';
    const result=await json(endpoint(code)+'/models?workspace='+encodeURIComponent(workspaceID)+'&access_mode='+encodeURIComponent(accessMode));
    if (projectCode()!==code || state.workspace_id!==workspaceID || accessMode!==(state.access_mode || 'automatic')) return;
    data.models=result.models || [];modelsLoaded=true;feedback='';render();
    const trigger=document.getElementById(triggerID);
    if(openMenu)trigger?.click();else trigger?.focus();
  }catch(error){feedback=error.message;render();}
}
// Window capture precedes the shared selector's document listener. Opening the
// model or effort menu loads the shared runtime catalog.
for (const type of ['click','keydown']) window.addEventListener(type,event=>{
  const trigger=event.target.closest?.('#plan-model-trigger, #plan-effort-trigger');
  if(modelsLoaded || !trigger)return;
  if(type==='keydown'&&!['Enter',' ','ArrowDown','ArrowUp','Home','End'].includes(event.key))return;
  event.preventDefault();event.stopImmediatePropagation();loadModels(true,trigger.id);
},true);
async function fresh() {
  if (flight) return;
  clearTimeout(timer);
  confirming = false;
  const code = document.querySelector('.app-shell')?.dataset.project;
  const initial={...emptyState(),workspace_id:initialWorkspace(code),access_mode:state.access_mode,model:state.model,effort:state.effort};
  failed=null;conflict=false;
  await mutate('new',false,{url:endpoint(code),action:'new',replace:true,before:clone(initial),payload:{_csrf:csrf(),action:'new',request_id:crypto.randomUUID(),state:initial}});
  if(!failed){splitting=null;panel.querySelector('#plan-message').focus();}
}
function applyPresentation(){
  if(!presentationToRestore||!panel||!data||selectedTab!=='plan'||panel.hidden)return;
  const value=presentationToRestore;presentationToRestore=null;
  for(const id of value.open||[]){const details=document.getElementById(id);if(details?.tagName==='DIALOG'&&!details.open)details.showModal();}
  if(value.split){const field=presentationField({split:true,draft:value.split.id});if(field)field.value=value.split.text;}
  for(const scroll of value.scrolls||[]){const el=panel.querySelector('.'+scroll.className.split(' ').join('.'));if(el){el.scrollTop=scroll.top;el.scrollLeft=scroll.left;}}
  if(value.focus){lastPlannerFocus=value.focus;const field=presentationField(value.focus);const target=value.focus.trigger&&field?document.getElementById(field.id+'-trigger'):field;if(target?.getClientRects().length){target.focus({preventScroll:true});try{field.setSelectionRange(value.focus.start,value.focus.end,value.focus.direction);}catch{}}}
}
document.addEventListener('focusin',event=>{
  if(!event.target.closest?.('#planning-panel'))return;
  const trigger=event.target.closest('.select-trigger'),field=trigger?document.getElementById(trigger.dataset.selectId):event.target;
  if(field?.matches('[data-split-titles]')){lastPlannerFocus={split:true,draft:field.closest('[data-draft-id]').dataset.draftId,start:field.selectionStart,end:field.selectionEnd,direction:field.selectionDirection};return;}
  if(field?.form&&field.name)lastPlannerFocus={form:field.form.id,name:field.name,trigger:!!trigger,start:field.selectionStart,end:field.selectionEnd,direction:field.selectionDirection};
});
window.addEventListener('beforeunload',event=>{
  if(uiVersion.isReloading())return;
  const error=storePending(true);if(error){event.preventDefault();event.returnValue='';}
});
document.addEventListener('input', event => {
  if (!event.target.closest('#planning-panel')) return;
  const field = event.target, draft = state.drafts.find(d => String(d.id) === field.closest('[data-draft-id]')?.dataset.draftId);
  if (field.dataset.field && draft && !created(draft)) {draft[field.dataset.field] = field.type === 'checkbox' ? field.checked : field.value;markDirty();render();}
  else if (field.id === 'plan-message') {state.input=field.value;markDirty();render();}
});
document.addEventListener('change', event => {
  if (event.target.id === 'plan-workspace') {
    if (chat?.state.workspace_id || flight) return;
    state.workspace_id=Number(event.target.value);modelsLoaded=false;data.models=[];
    markDirty();render();return;
  }
  if (event.target.id === 'plan-access') {state.access_mode=event.target.value;modelsLoaded=false;markDirty();render();return;}
  if (!['plan-model','plan-effort'].includes(event.target.id)) return;
  if (event.target.id === 'plan-model') {state.model=event.target.value;const choices=(data?.models || []).find(m=>m.id===state.model)?.efforts || [];if(!choices.includes(state.effort))state.effort=choices.includes('medium')?'medium':choices[0] || '';}
  else state.effort=event.target.value;
  markDirty();render();
});
document.addEventListener('submit', event => {
  if (!event.target.closest('#planning-panel')) return;
  event.preventDefault();event.stopImmediatePropagation();
  if (event.target.id === 'plan-form') send();else if(!flight)mutate(chat?'save':'new');
}, true);
document.addEventListener('click', async event => {
  const button=event.target.closest('[data-plan]');if(!button || button.disabled)return;
  const action=button.dataset.plan, draft=state.drafts.find(d=>String(d.id)===button.closest('[data-draft-id]')?.dataset.draftId);
  if(action==='open'||action==='execution'){chooseTab(action==='open'?'plan':'execution',true);return;}
  if(action==='close-editor'){button.closest('dialog').close();return;}
  if(action==='edit-draft'){button.closest('[data-draft-id]').querySelector('dialog').showModal();return;}
  if(uiVersion.isOutdated())return;
  if(activeOperation?.replace)return;
  if(action==='toggle-tray'){trayCollapsed=!trayCollapsed;render();return;}
  if(action==='remove'&&draft){deleteDrafts([draft]);return;}
  if(action==='dismiss-all'){deleteDrafts(state.drafts.filter(d=>!created(d)));return;}
  if(action==='models') {loadModels(true);return;}
  if(action==='new'){if(!skipNewChatConfirmation&&(state.messages.length||state.drafts.length||state.input)){panel.querySelector('#plan-skip-new-confirmation').checked=false;panel.querySelector('.plan-new-error').hidden=true;confirming=true;render();}else fresh();return;}
  if(action==='confirm-new'){
    const dialog=panel.querySelector('#plan-new-dialog'),error=dialog.querySelector('.plan-new-error');
    savingNewChatPreference=true;
    dialog.querySelectorAll('button,input').forEach(control=>control.disabled=true);
    try{
      if(panel.querySelector('#plan-skip-new-confirmation').checked){
        await json('/settings',{method:'POST',body:JSON.stringify({_csrf:csrf(),key:'skip_new_chat_confirmation',value:'true'})});
        skipNewChatConfirmation=true;
      }
      confirming=false;dialog.close();await fresh();
    }catch(problem){error.textContent='Could not save your preference. '+problem.message;error.hidden=false;}
    finally{savingNewChatPreference=false;dialog.querySelectorAll('button,input').forEach(control=>control.disabled=false);}
    return;
  }
  if(action==='cancel-new'){if(savingNewChatPreference)return;confirming=false;render();return;}
  if(action==='retry'){mutate(failed.action,true);return;}
  if(action==='reload'){if(!confirm('Reload the saved chat? Copy any unsaved planning edits you want to keep first.'))return;try{accept(await json(endpoint()+'?chat='+chat.id));failed=null;conflict=false;dirty=false;feedback='';clearPending();render();}catch(error){feedback=error.message;render();}return;}
  if(action==='create'){if(selected().length)await mutate('create');return;}
  if(action==='refine'&&draft){button.closest('dialog').close();state.refining_draft_id=draft.id;markDirty();render();panel.querySelector('#plan-message').focus();return;}
  if(action==='end-refine'){state.refining_draft_id=null;markDirty();render();panel.querySelector('#plan-message').focus();return;}
  if(action==='split'&&draft){splitting=draft.id;render();const node=button.closest('[data-draft-id]');node.querySelector('[data-split-titles]').focus();return;}
  if(action==='cancel-split'){splitting=null;render();return;}
  if(action==='apply-split'&&draft){const node=button.closest('[data-draft-id]'),titles=node.querySelector('[data-split-titles]').value.split('\n').map(x=>x.trim()).filter(Boolean);if(titles.length<2){node.querySelector('[data-split-error]').textContent='Enter at least two titles.';return;}node.querySelector('dialog').close();state.drafts.splice(state.drafts.indexOf(draft),1,...titles.map(title=>makeDraft(title,draft.description,draft.acceptance,draft.group)));splitting=null;state.refining_draft_id=null;}
  else if(action==='combine'){const drafts=selected();if(drafts.length<2)return;const group=drafts.every(d=>d.group===drafts[0].group)?drafts[0].group:'';const combined=makeDraft(drafts.map(d=>d.title).join(' / '),drafts.map(d=>d.title+'\n'+d.description).join('\n\n'),drafts.map(d=>d.acceptance).filter(Boolean).join('\n\n'),group);const index=state.drafts.indexOf(drafts[0]);state.drafts=state.drafts.filter(d=>!drafts.includes(d));state.drafts.splice(index,0,combined);state.refining_draft_id=null;}
  else if(action==='select-all'){state.drafts.forEach(d=>{if(!created(d))d.selected=true;});}
  else if(action==='add-draft'){trayCollapsed=false;state.drafts.push(makeDraft());}
  else return;
  markDirty();render();
  if(action==='add-draft'){const last=panel.querySelector('.plan-draft-list').lastElementChild;last.querySelector('dialog').showModal();last.querySelector('[name=title]').focus();}
});
document.addEventListener('keydown', event => {
  if(event.target.id==='plan-message'&&event.key==='Enter'&&!event.shiftKey&&!event.isComposing){event.preventDefault();send();}
  if(['plan-tab','execution-tab'].includes(event.target.id)&&['ArrowLeft','ArrowRight','Home','End'].includes(event.key)){event.preventDefault();chooseTab(event.key==='Home'?'plan':event.key==='End'?'execution':selectedTab==='plan'?'execution':'plan',true);}
});
document.addEventListener('pellets-refresh', async () => {
  if(!chat||selectedTab!=='plan'||flight||dirty||conflict||uiVersion.isOutdated()||Date.now()-refreshedAt<1000)return;
  refreshedAt=Date.now();
  try{accept(await json(endpoint()+'?chat='+chat.id));render();}catch(error){feedback=error.message;render();}
});
window.addEventListener('resize', sync);
window.Planner={sync,status,selectExecution:()=>chooseTab('execution'),flush:()=>dirty?mutate(chat?'save':'new'):Promise.resolve(),prepareReload:()=>{const error=storePending(true);render();return error;}};
