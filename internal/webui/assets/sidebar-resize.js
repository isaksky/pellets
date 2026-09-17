import { saveSetting } from './settings.js';

const root = document.documentElement;
const settings = JSON.parse(root.dataset.settings || '{}');
const sides = [
  {name:'navigation', label:'Navigation sidebar width', selector:'#project-drawer', variable:'--rail', min:140, max:360},
  {name:'execution', label:'Planning and execution sidebar width', selector:'#right-panel', variable:'--execution-width', min:280, max:720},
];
let drag = null, frame = 0;
function limit(side) {
  const other = sides.find(item=>item!==side);
  const panel = document.querySelector(other.selector);
  const width = panel?.getClientRects().length ? panel.getBoundingClientRect().width : 0;
  return Math.max(side.min, Math.min(side.max, innerWidth-width-240));
}
function setWidth(side, width) {
  const value = Math.round(Math.max(side.min, Math.min(limit(side), width)));
  root.style.setProperty(side.variable, value+'px');
  return value;
}
function layout() {
  frame = 0;
  for (const side of sides) {
    const panel = document.querySelector(side.selector), handle = side.handle;
    const visible = innerWidth>600 && panel?.getClientRects().length;
    handle.hidden = !visible;
    if (!visible) continue;
    const rect = panel.getBoundingClientRect();
    handle.style.left = (side.name==='navigation'?rect.right:rect.left)-3+'px';
    handle.style.top = rect.top+'px';
    handle.style.height = rect.height+'px';
    handle.setAttribute('aria-valuenow', Math.round(rect.width));
    handle.setAttribute('aria-valuemax', limit(side));
  }
}
function schedule() { if(!frame) frame=requestAnimationFrame(layout); }
function finish(event) {
  if (!drag || event.pointerId!==drag.pointer) return;
  const {side, startWidth}=drag;
  drag=null;
  root.classList.remove('sidebar-resizing');
  if(event.type==='pointercancel')setWidth(side,startWidth);
  else saveSetting(side.name+'_width',String(Math.round(document.querySelector(side.selector).getBoundingClientRect().width)));
  schedule();
}
for (const side of sides) {
  const saved=Number(settings[side.name+'_width']);
  if(Number.isInteger(saved)&&saved>=side.min&&saved<=side.max)root.style.setProperty(side.variable,saved+'px');
  const handle=document.createElement('div');
  side.handle=handle;
  handle.className='sidebar-resize-handle';handle.dataset.sidebar=side.name;
  handle.tabIndex=0;handle.setAttribute('role','separator');
  handle.setAttribute('aria-label',side.label);handle.setAttribute('aria-orientation','vertical');
  handle.setAttribute('aria-controls',side.selector.slice(1));handle.setAttribute('aria-valuemin',side.min);
  handle.title='Drag to resize. Double-click to reset.';
  handle.addEventListener('pointerdown',event=>{
    if(event.button!==0)return;
    event.preventDefault();handle.focus();
    drag={side,pointer:event.pointerId,startX:event.clientX,startWidth:document.querySelector(side.selector).getBoundingClientRect().width};
    handle.setPointerCapture(event.pointerId);root.classList.add('sidebar-resizing');
  });
  handle.addEventListener('pointermove',event=>{
    if(!drag||drag.pointer!==event.pointerId)return;
    setWidth(side,drag.startWidth+(event.clientX-drag.startX)*(side.name==='navigation'?1:-1));schedule();
  });
  handle.addEventListener('pointerup',finish);handle.addEventListener('pointercancel',finish);
  handle.addEventListener('keydown',event=>{
    if(!['ArrowLeft','ArrowRight','Home','End'].includes(event.key))return;
    event.preventDefault();
    const current=document.querySelector(side.selector).getBoundingClientRect().width;
    const delta=(event.key==='ArrowRight'?1:-1)*(side.name==='navigation'?1:-1)*(event.shiftKey?40:10);
    const value=setWidth(side,event.key==='Home'?side.min:event.key==='End'?limit(side):current+delta);
    saveSetting(side.name+'_width',String(value));schedule();
  });
  handle.addEventListener('dblclick',()=>{
    root.style.removeProperty(side.variable);
    const value=setWidth(side,document.querySelector(side.selector).getBoundingClientRect().width);
    saveSetting(side.name+'_width',String(value));schedule();
  });
  document.body.append(handle);
}
new MutationObserver(schedule).observe(document.body,{childList:true,subtree:true});
new MutationObserver(schedule).observe(root,{attributes:true,attributeFilter:['class']});
new ResizeObserver(schedule).observe(document.body);
window.addEventListener('resize',()=>{
  if(innerWidth>600)for(const side of sides){const value=parseFloat(root.style.getPropertyValue(side.variable));if(value)setWidth(side,value);}
  schedule();
});
if(innerWidth>600)for(const side of sides){const value=parseFloat(root.style.getPropertyValue(side.variable));if(value)setWidth(side,value);}
schedule();
