// Feature-owned controls share the planner's single global catalog read/refresh.
export function executionPreferenceFields() {
  return `<div class="form-grid" data-execution-preferences>${[['model','Execution model'],['reasoning_effort','Reasoning effort']].map(([name,label])=>`<label>${label}<pl-select><select name="${name}" data-field="${name}" data-execution-preference="${name}" data-live-options><option value="">Use execution default</option></select></pl-select></label>`).join('')}</div>`;
}
function options(select, choices, value) {
  if(select.dataset.preferenceDefault===undefined)select.dataset.preferenceDefault=value;
  const original=select.dataset.preferenceDefault;
  const entries=[{id:'',name:'Use execution default'},...choices.filter(c=>c.id)];
  if(original && !entries.some(c=>c.id===original))entries.push({id:original,name:original});
  if(value && !entries.some(c=>c.id===value))entries.push({id:value,name:value});
  const signature=JSON.stringify([entries,value]);
  if(select.dataset.preferenceOptions===signature)return;
  select.dataset.preferenceOptions=signature;
  select.replaceChildren(...entries.map(entry=>new Option(entry.name,entry.id,entry.id===original,entry.id===value)));
  select.value=value;
  select.closest('pl-select')?.refresh();
}
export function updateExecutionPreferences(root,catalog,values) {
  for(const group of root.querySelectorAll('[data-execution-preferences]')) {
    const model=group.querySelector('[name=model]'),effort=group.querySelector('[name=reasoning_effort]');
    if(!model||!effort)continue;
    const selectedModel=values ? values.model || '' : model.value;
    const selectedEffort=values ? values.reasoning_effort || '' : effort.value;
    options(model,(catalog.models||[]).map(m=>({id:m.id,name:m.name||m.id})),selectedModel);
    // With an inherited model, offer the catalog's union; runtime preflight
    // validates the final pair against the selected workspace's actual model.
    const models=selectedModel ? (catalog.models||[]).filter(m=>m.id===selectedModel) : catalog.models||[];
    options(effort,[...new Set(models.flatMap(m=>m.efforts||[]))].map(id=>({id,name:id})),selectedEffort);
  }
}
export function syncExecutionPreferenceMenus(catalog,status) {
  updateExecutionPreferences(document,catalog);
  for(const select of document.querySelectorAll('[data-execution-preference]')) {
    select.dataset.menuStatus=status;
    select.dataset.menuAction=catalog.error?'Retry refresh':'Refresh models';
    select.dataset.menuBusy=String(!!catalog.refreshing);
    select.closest('pl-select')?.refresh();
  }
}
export function observeExecutionPreferences(refresh,load) {
  function connect(){
    if(document.querySelector('[data-execution-preferences]')){refresh();load();}
  }
  // A streamed patch may retain the group node while replacing its select or
  // removing enhancement attributes. Reconcile those patches as well as newly
  // inserted forms; option signatures make our own DOM updates converge.
  new MutationObserver(records=>{
    if(records.some(r=>r.target.nodeType===1&&r.target.closest('[data-execution-preferences]') || [...r.addedNodes].some(n=>n.nodeType===1&&(n.matches?.('[data-execution-preferences]')||n.querySelector?.('[data-execution-preferences]')))))connect();
  }).observe(document.documentElement,{childList:true,subtree:true,attributes:true,attributeFilter:['data-execution-preference','data-preference-options']});
  document.addEventListener('change',event=>{if(event.target.matches('[data-execution-preference]'))refresh();});
  document.addEventListener('reset',event=>{if(event.target.querySelector('[data-execution-preferences]'))queueMicrotask(refresh);});
  connect();
}
