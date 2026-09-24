// Production native-control audit with isolated data and the deterministic peer.
// PELLETS_NATIVE_BASELINE=/path/to/pl captures the original without fix assertions.
// PELLETS_BROWSER_ARTIFACTS=/path retains matched crops and computed observations.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-native-controls-'));
const fixture = path.join(temporary, 'native-controls');
const baseline = process.env.PELLETS_NATIVE_BASELINE;
const binary = baseline || path.join(temporary, 'pl'), peer = path.join(temporary, 'codex');
const engine = process.env.PLAYWRIGHT_BROWSER || 'chromium';
const artifacts = process.env.PELLETS_BROWSER_ARTIFACTS || path.join(temporary, 'screenshots');
const env = {...process.env, PATH:temporary+path.delimiter+process.env.PATH, PELLETS_CODEX_EXECUTABLE:peer, PELLETS_SUPERVISOR_PEER:'1'};
const themes = ['gruvbox-light','gruvbox-dark','light','dark','icy'];
const widths = [1280,1092,800,390];
const check = (condition, message) => { if (!baseline) assert.ok(condition, message); };
const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd:fixture, env, encoding:'utf8'})).data;
let server, browser, page;
const captures = [];
const frame = () => page.evaluate(() => new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))));
async function theme(value) {
  await page.locator('#theme-select').selectOption(value,{force:true});
  const end=Date.now()+10000;
  while((await (await page.request.get(new URL('/settings',page.url()).href)).json()).settings.theme!==value) {
    if(Date.now()>end)throw Error('Theme did not persist');
    await new Promise(r=>setTimeout(r,30));
  }
  await page.waitForFunction(value=>document.documentElement.dataset.themeChoice===value,value);
}
async function panel(visible) {
  if(await page.locator('#right-panel').isVisible()!==visible)await page.locator('#toggle-execution').click();
}
async function capture(name, selector) {
  await page.mouse.move(0,0);await frame();
  const directory=path.join(artifacts,engine+'-'+name);fs.mkdirSync(directory,{recursive:true});
  const box=await page.locator(selector).boundingBox();assert.ok(box,selector+' is visible');
  // Include surrounding space and the extra 6px restored by the primary button.
  let clip={x:Math.max(0,box.x-6),y:Math.max(0,box.y-6),width:Math.min(box.width+12,page.viewportSize().width-Math.max(0,box.x-6)),height:Math.min(box.height+24,page.viewportSize().height-Math.max(0,box.y-6))};
  const before=path.join(directory,'before.json');
  if(!baseline&&fs.existsSync(before))clip=JSON.parse(fs.readFileSync(before)).clip;
  const observation=await page.locator(selector).evaluate(region=>({
    theme:document.documentElement.dataset.themeChoice,
    scroll:{window:[scrollX,scrollY],main:document.querySelector('#main').scrollTop,region:region.scrollTop},
    controls:[...region.querySelectorAll('input:not([type=hidden]),textarea,select,button,a')].filter(e=>e.checkVisibility()).map(e=>{
      const s=getComputedStyle(e),r=e.getBoundingClientRect();return {tag:e.localName,type:e.type,name:e.name,label:e.getAttribute('aria-label')||e.textContent.trim().slice(0,80),value:e.value,checked:e.checked,disabled:e.disabled,
        bounds:[r.x,r.y,r.width,r.height],font:s.fontFamily,fontSize:s.fontSize,color:s.color,background:s.backgroundColor,padding:s.padding,appearance:s.appearance};
    })
  }));
  const state={name:engine+'-'+name,selector,clip,viewport:page.viewportSize(),scale:1,...observation};
  await page.screenshot({path:path.join(directory,baseline?'before.png':'after.png'),clip,animations:'disabled',caret:'hide'});
  fs.writeFileSync(path.join(directory,baseline?'before.json':'after.json'),JSON.stringify(state,null,2));captures.push(state);
}
async function primary(locator) {
  const style=await locator.evaluate(e=>{
    const s=getComputedStyle(e),probe=document.createElement('span');probe.style.color='var(--theme-primary)';e.after(probe);
    const primary=getComputedStyle(probe).color;probe.remove();
    return {background:s.backgroundColor,primary,border:s.borderTopWidth,padding:s.padding};
  });
  check(style.background===style.primary&&style.border==='1px'&&style.padding==='5px 9px','Creation action must retain the primary button: '+JSON.stringify(style));
}
async function nativeCheckboxes(selector) {
  const states=await page.locator(selector+' input[type=checkbox]').evaluateAll(es=>es.filter(e=>e.checkVisibility()).map(e=>{const s=getComputedStyle(e),r=e.getBoundingClientRect();return [r.width,r.height,s.padding,s.appearance];}));
  assert.ok(states.length,selector+' has checkboxes');
  assert.ok(states.every(s=>s[0]===13&&s[1]===13&&s[2]==='0px'&&s[3]==='auto'),selector+': '+JSON.stringify(states));
}
(async()=>{
  if(!baseline)execFileSync('go',['build','-o',binary,'./cmd/pl'],{cwd:repository});
  execFileSync('go',['test','-c','-o',peer,'./internal/app'],{cwd:repository});
  fs.mkdirSync(fixture);execFileSync('git',['init','-q'],{cwd:fixture});
  for(const args of [['--help'],['init-db','--help'],['add','--help'],['start','--help']])execFileSync(binary,args,{cwd:fixture});
  cli('init-db');
  const first=cli('add','Audit native controls','--group','Interface');cli('add','Second control audit target');
  const bindingFile=path.join(fixture,'.git','pellets-database.json');
  const binding=JSON.parse(fs.readFileSync(bindingFile));
  assert.ok(fs.realpathSync(path.resolve(path.dirname(bindingFile),binding.path)).startsWith(fs.realpathSync(fixture)+path.sep));
  fs.writeFileSync(path.join(fixture,'fake-mode'),'planning_full');
  server=spawn(binary,['server','--port','0','--no-open'],{cwd:fixture,env});
  const origin=await new Promise((resolve,reject)=>{let out='',error='';server.stdout.on('data',d=>{out+=d;if(out.includes('\n'))resolve(out.split('\n')[0].trim());});server.stderr.on('data',d=>error+=d);server.once('error',reject);server.once('exit',code=>reject(Error(`Server ${code}: ${error}`)));});
  browser=await (engine==='webkit'?webkit:chromium).launch({headless:true});
  page=await browser.newPage({viewport:{width:1280,height:900},deviceScaleFactor:1,hasTouch:true});page.setDefaultTimeout(15000);
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  const url=origin+'/projects/'+first.project+'/tasks?workspace=1';
  await page.goto(url);await page.locator('#plan-tab').click();await page.locator('#plan-message').fill('Propose native control checks');await page.locator('.plan-send').click();
  await page.locator('.plan-card').first().waitFor();await page.evaluate(()=>window.Planner.flush());
  for(const value of (process.env.PELLETS_NATIVE_CASE==='behavior'?[]:themes))for(const width of widths) {
    const scene=value+'-'+width;await page.setViewportSize({width,height:900});await page.goto(url);await theme(value);await panel(true);await page.locator('#execution-tab').click();if(width===390)await panel(false);
    await page.getByText('+ New pellet',{exact:true}).click();
    const form=page.locator('.create-popover form'),title=form.locator('[name=title]'),status=form.locator('[name=status]');
    await title.fill('Unsubmitted native control draft');await status.selectOption('maybe_later');
    await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));await frame();
    assert.equal(await title.inputValue(),'Unsubmitted native control draft');assert.equal(await status.inputValue(),'maybe_later');
    assert.equal(await status.evaluate(e=>e.closest('pl-select').hasAttribute('native')&&!e.classList.contains('select-native')),true,'Retain native picker');
    const nativeStyle=await status.evaluate(e=>{const s=getComputedStyle(e);return [s.appearance,s.paddingLeft,s.paddingRight,e.getBoundingClientRect().height];});assert.deepEqual(nativeStyle,['none','12px','34px',31]);
    await primary(form.getByRole('button',{name:'Create pellet',exact:true}));await capture(scene+'-create-pellet','.create-popover form');
    await form.evaluate(e=>e.reset());await frame();assert.equal(await status.inputValue(),'open');assert.equal(await title.inputValue(),'');
    await form.getByRole('button',{name:'Create pellet',exact:true}).click();assert.equal(await title.evaluate(e=>e===document.activeElement&&!e.validity.valid),true,'Native required validation');
    await page.keyboard.press('Escape');await page.getByText('+ New pellet',{exact:true}).click();
    await page.goto(origin+'/projects/'+first.project+'/memories?workspace=1');await page.getByText('New memory',{exact:true}).click();
    await page.locator('.create-popover textarea').fill('Unsubmitted memory draft');await primary(page.locator('.create-popover button[type=submit]'));
    await capture(scene+'-create-memory','.create-popover form');await page.locator('.create-popover textarea').fill('');await page.getByText('New memory',{exact:true}).click();
    await panel(true);await page.locator('#plan-tab').click();await page.locator('.plan-card').first().waitFor();
    await page.locator('[data-plan=new]').click();await page.locator('#plan-new-dialog[open]').waitFor();
    const checkbox=page.getByRole('checkbox',{name:'Don’t ask me again'});
    const geometry=await checkbox.evaluate(e=>{const r=e.getBoundingClientRect(),s=getComputedStyle(e);return [r.width,r.height,s.padding];});check(JSON.stringify(geometry)===JSON.stringify([16,16,'0px']),'Confirmation checkbox is square: '+JSON.stringify(geometry));
    await capture(scene+'-new-chat','#plan-new-dialog');
    await checkbox.focus();await page.keyboard.press('Space');assert.equal(await checkbox.isChecked(),true);assert.equal(await checkbox.evaluate(e=>getComputedStyle(e).outlineStyle!=='none'),true);
    await capture(scene+'-new-chat-checked','#plan-new-dialog');
    if(width===390)await checkbox.tap();else await page.keyboard.press('Space');assert.equal(await checkbox.isChecked(),false);
    await page.locator('[data-plan=cancel-new]').click();assert.equal(await page.locator('.plan-card').count(),2,'Cancel retains proposals');
    await page.goto(url);await panel(true);await page.locator('#execution-tab').click();if(width===390)await panel(false);
    await page.locator('#assignment-popover > summary').click();await nativeCheckboxes('.assignment-form');await capture(scene+'-assignment','.assignment-form');
    const group=page.locator('.assignment-form [name=new_group]');await group.fill('Retain unsaved assignment');await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));await frame();assert.equal(await group.inputValue(),'Retain unsaved assignment');
    await page.locator('.assignment-form .select-trigger').click();await page.keyboard.press('Escape');assert.equal(await page.locator('#assignment-popover').evaluate(e=>e.open),true);await page.keyboard.press('Escape');
    assert.equal(await page.locator('#assignment-popover > summary').evaluate(e=>e===document.activeElement),true);
    await page.locator('#assignment-remaining > summary').click();await nativeCheckboxes('.recipient-form');await page.keyboard.press('Escape');
    await panel(true);await page.locator('#project-record > summary').click();await nativeCheckboxes('.routing-settings');await page.locator('#project-record > summary').click();
    console.log(`${baseline?'CAPTURE':'PASS'} ${engine}/${scene}: primary creation, native picker/reset/validation, confirmation keyboard/touch, assignments/settings and live drafts`);
  }
  // Exercise actual submission after the presentation-only matrix.
  await page.setViewportSize({width:1280,height:900});await page.goto(url);await theme('gruvbox-light');await panel(true);await page.locator('#execution-tab').click();
  await page.getByText('+ New pellet',{exact:true}).click();await page.locator('.create-popover [name=title]').fill('Created through native controls');await page.locator('.create-popover [name=status]').selectOption('maybe_later');
  const saved=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname.endsWith('/pellets')&&r.ok());
  await page.locator('.create-popover button[type=submit]').click();await saved;
  assert.ok(cli('list','--all').some(p=>p.title==='Created through native controls'&&p.status==='maybe_later'));
  await page.goto(origin+'/projects/'+first.project+'/memories?workspace=1');await page.getByText('New memory',{exact:true}).click();await page.locator('.create-popover textarea').fill('Native memory submission');await page.locator('.create-popover button[type=submit]').click();await page.locator('.memory-card p').filter({hasText:'Native memory submission'}).waitFor();
  // Plain ownership exposes the existing production recovery stepper; no runtime is started.
  cli('start',first.id);await page.goto(url);await panel(true);await page.locator('#execution-tab').click();
  const limit=page.locator('[data-no-run-resume] input[name=limit]');await limit.waitFor();
  assert.equal(await limit.evaluate(e=>getComputedStyle(e).appearance),'textfield');await limit.fill('9999');await page.getByRole('button',{name:'Increase pellet limit'}).click();assert.equal(await limit.inputValue(),'10000');await page.getByRole('button',{name:'Increase pellet limit'}).click();assert.equal(await limit.inputValue(),'10000');
  await limit.fill('1');await page.getByRole('button',{name:'Decrease pellet limit'}).click();assert.equal(await limit.inputValue(),'1');await limit.fill('0');assert.equal(await limit.evaluate(e=>e.validity.rangeUnderflow),true);
  await limit.evaluate(e=>{e.value='27';e.readOnly=true;});assert.equal(await page.getByRole('button',{name:'Increase pellet limit'}).isVisible(),false);assert.equal(await limit.inputValue(),'27');
  await limit.evaluate(e=>{e.readOnly=false;e.disabled=true;});assert.equal(await page.getByRole('button',{name:'Increase pellet limit'}).isVisible(),false);await limit.evaluate(e=>{e.disabled=false;e.form.reset();});await frame();assert.equal(await limit.inputValue(),'100');
  await capture('recovery-stepper','[data-no-run-resume]');
  // Forced colors keep OS checkbox glyphs and the original native select arrow.
  await page.emulateMedia({forcedColors:'active'});await page.getByText('+ New pellet',{exact:true}).click();
  const forced=await page.locator('.create-popover [name=status]').evaluate(e=>({supported:matchMedia('(forced-colors: active)').matches,appearance:getComputedStyle(e).appearance,arrow:getComputedStyle(e.parentElement,'::after').display}));
  if(forced.supported)assert.deepEqual(forced,{supported:true,appearance:'auto',arrow:'none'});
  await page.getByText('+ New pellet',{exact:true}).click();await page.locator('#plan-tab').click();await page.locator('[data-plan=new]').click();
  if(forced.supported)assert.equal(await page.locator('.plan-confirm-preference input').evaluate(e=>getComputedStyle(e).appearance),'auto');
  console.log(`PASS ${engine}: native submissions, recovery min/max/read-only/disabled/reset, forced-colors ${forced.supported?'verified':'not supported by engine emulation'}`);
  assert.deepEqual(errors,[]);
})().catch(e=>{console.error(e);process.exitCode=1;}).finally(async()=>{
  fs.mkdirSync(artifacts,{recursive:true});fs.writeFileSync(path.join(artifacts,engine+'-'+(baseline?'before':'after')+(process.env.PELLETS_NATIVE_CASE==='behavior'?'-behavior':'')+'-observations.json'),JSON.stringify(captures,null,2));
  await browser?.close();if(server?.exitCode===null){const stopped=new Promise(r=>server.once('exit',r));server.kill('SIGINT');await stopped;}
  console.log('Visual artifacts: '+artifacts);
});
