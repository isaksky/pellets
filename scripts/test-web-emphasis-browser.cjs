// Production emphasis/conditional-copy regression. Disposable data and protocol peer.
// PELLETS_EMPHASIS_BASELINE=/path/to/pl captures the original without new assertions.
// PELLETS_BROWSER_ARTIFACTS selects persistent evidence; repeat with PLAYWRIGHT_BROWSER=webkit.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-emphasis-'));
const fixture = path.join(temporary, 'emphasis');
const baseline = process.env.PELLETS_EMPHASIS_BASELINE;
const binary = baseline || path.join(temporary, 'pl'), peer = path.join(temporary, 'codex');
const engine = process.env.PLAYWRIGHT_BROWSER || 'chromium';
const artifacts = process.env.PELLETS_BROWSER_ARTIFACTS || path.join(temporary, 'screenshots');
const env = {...process.env, PATH:temporary+path.delimiter+process.env.PATH, PELLETS_CODEX_EXECUTABLE:peer, PELLETS_SUPERVISOR_PEER:'1'};
const check = (value, message) => { if (!baseline) assert.ok(value, message); };
let server, browser, page;
const captures = [];
const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd:fixture, env, encoding:'utf8'})).data;
const frame = () => page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
async function setTheme(choice) {
  await page.locator('#theme-select').selectOption(choice,{force:true});
  // Persist via the real settings flow: navigation must not restore the old
  // database theme after a presentation-only applyTheme call.
  const deadline=Date.now()+10000;
  while((await (await page.request.get(new URL('/settings',page.url()).href)).json()).settings.theme!==choice){
    if(Date.now()>deadline)throw Error('Theme was not saved: '+choice);
    await new Promise(r=>setTimeout(r,30));
  }
  await page.waitForFunction(choice=>document.documentElement.dataset.themeChoice===choice,choice);
}
async function capture(name, selector, height) {
  if(selector==='#main')await page.locator('#main').evaluate(e=>e.scrollTop=0);
  await page.mouse.move(0,0); await frame();
  const expectedTheme=name.match(/^(gruvbox-light|gruvbox-dark|light|dark|icy)-/)?.[1];
  if(expectedTheme)assert.equal(await page.evaluate(()=>document.documentElement.dataset.themeChoice),expectedTheme,'Capture theme');
  const directory = path.join(artifacts, engine+'-'+name); fs.mkdirSync(directory,{recursive:true});
  let clip = await page.locator(selector).boundingBox();
  if(height) clip.height = Math.min(height, page.viewportSize().height-clip.y);
  const metadata = path.join(directory,'before.json');
  if(!baseline && fs.existsSync(metadata)) clip = JSON.parse(fs.readFileSync(metadata)).clip;
  await page.screenshot({path:path.join(directory, baseline?'before.png':'after.png'),clip,animations:'disabled',caret:'hide'});
  const observation={name:engine+'-'+name,selector,clip,viewport:page.viewportSize(),scale:1,theme:await page.evaluate(()=>document.documentElement.dataset.themeChoice),scroll:await page.evaluate(()=>({window:[scrollX,scrollY],main:document.getElementById('main').scrollTop,editor:document.querySelector('.inspector-scroll,.plan-draft-dialog[open] .plan-row-editor')?.scrollTop||0}))};
  fs.writeFileSync(path.join(directory,baseline?'before.json':'after.json'),JSON.stringify(observation,null,2)); captures.push(observation);
}
async function quiet(selector) {
  const node=page.locator(selector).first(); await node.scrollIntoViewIfNeeded(); await page.mouse.move(0,0); await frame();
  const style=await node.evaluate(e=>{const s=getComputedStyle(e);return {background:s.backgroundColor,border:s.borderTopWidth,borderColor:s.borderTopColor,height:e.getBoundingClientRect().height};});
  check(style.background==='rgba(0, 0, 0, 0)' && (style.border==='0px' || style.borderColor==='rgba(0, 0, 0, 0)'),selector+' should reuse the quiet variant: '+JSON.stringify(style));
  await page.keyboard.press('Tab'); await node.focus();
  check(await node.evaluate(e=>e===document.activeElement && getComputedStyle(e).outlineStyle!=='none'),selector+' has visible keyboard focus');
  await node.hover();
  check(await node.evaluate(e=>getComputedStyle(e).backgroundColor!=='rgba(0, 0, 0, 0)'),selector+' has hover feedback');
  // New-chat confirmation disables its controls while the preference is saved.
  // Exercise the same native disabled state without sending another mutation.
  const disabledBackground=await node.evaluate(e=>{
    const previous=e.disabled;e.disabled=true;
    const background=getComputedStyle(e).backgroundColor;e.disabled=previous;return background;
  });
  check(disabledBackground==='rgba(0, 0, 0, 0)',selector+' disabled hover remains quiet: '+disabledBackground);
}
(async()=>{
  if(!baseline)execFileSync('go',['build','-o',binary,'./cmd/pl'],{cwd:repository});
  execFileSync('go',['test','-c','-o',peer,'./internal/app'],{cwd:repository});
  fs.mkdirSync(fixture);execFileSync('git',['init','-q'],{cwd:fixture});
  for(const args of [['--help'],['init-db','--help'],['add','--help'],['memory','--help']])execFileSync(binary,args,{cwd:fixture});
  cli('init-db'); const first=cli('add','Audit action hierarchy','--group','Interface');
  cli('add','Review interface changes','--review-targets',first.id);
  const bindingFile=path.join(fixture,'.git','pellets-database.json'),binding=JSON.parse(fs.readFileSync(bindingFile));
  assert.ok(fs.realpathSync(path.resolve(path.dirname(bindingFile),binding.path)).startsWith(fs.realpathSync(fixture)+path.sep));
  fs.writeFileSync(path.join(fixture,'fake-mode'),'planning_full');
  server=spawn(binary,['server','--port','0','--no-open'],{cwd:fixture,env});
  const origin=await new Promise((resolve,reject)=>{let out='',err='';server.stdout.on('data',d=>{out+=d;if(out.includes('\n'))resolve(out.split('\n')[0].trim());});server.stderr.on('data',d=>err+=d);server.once('error',reject);server.once('exit',code=>reject(Error(`Server ${code}: ${err}`)));});
  browser=await (engine==='webkit'?webkit:chromium).launch({headless:true});
  page=await browser.newPage({viewport:{width:1280,height:900},deviceScaleFactor:1,hasTouch:true});page.setDefaultTimeout(15000);
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  const url=origin+'/projects/'+first.project+'/tasks?workspace=1';
  await page.goto(url);await page.locator('#plan-tab').click();await page.locator('#plan-message').waitFor();
  assert.equal(await page.locator('[data-plan=new]').isVisible(),false,'Empty chat has no reset');
  await page.locator('#plan-message').fill('Propose action hierarchy checks');await page.locator('.plan-send').click();
  await page.locator('.plan-card').first().waitFor();await page.evaluate(()=>window.Planner.flush());
  for(const theme of (process.env.PELLETS_EMPHASIS_CASE==='descriptions'?[]:['gruvbox-light','gruvbox-dark','light','dark','icy']))for(const width of [1280,1092,390]){
    const scene=theme+'-'+width;await page.setViewportSize({width,height:900});
    await page.goto(url);await setTheme(theme);
    if(!await page.locator('#right-panel').isVisible())await page.locator('#toggle-execution').click();await page.locator('#execution-tab').click();if(width===390)await page.locator('#toggle-execution').click();
    await page.locator('#filter-summary').click();await page.locator('.filter-fields:popover-open').waitFor();
    await capture(scene+'-empty-filters','.filter-fields',410);
    check(!await page.getByRole('link',{name:'Clear filters',exact:true}).isVisible(),'No redundant filter reset');
    await page.keyboard.press('Escape');
    await page.locator('.pellet-row .row-menu > summary').click();await page.locator('[data-checkpoint-select]').first().check();
    await page.locator('.pellet-row .row-menu > summary').focus();await page.keyboard.press('Escape');await page.waitForFunction(()=>!document.querySelector('.pellet-row .row-menu').open);await page.locator('[data-checkpoint-composer]').waitFor();
    await capture(scene+'-review','#main',650);check(await page.locator('[data-checkpoint-composer]').evaluate(e=>e.scrollWidth<=e.clientWidth+1),'Review composer fits its pane');await quiet('[data-checkpoint-clear]');
    await page.locator('[data-checkpoint-clear]').press('Enter');await page.locator('[data-checkpoint-composer]').waitFor({state:'hidden'});
    if(process.env.PELLETS_EMPHASIS_CASE==='review'){console.log('PASS '+engine+'/'+scene+': review composer');continue;}
    await page.goto(origin+'/projects/'+first.project+'/memories?workspace=1');
    if(width===390 && await page.locator('#right-panel').isVisible())await page.locator('#toggle-execution').click();
    await capture(scene+'-empty-memories','#main',320);
    check(!await page.locator('[data-create-memory]').count(),'One memory creation entry point');
    check(await page.getByRole('heading',{name:'No memories yet',exact:true}).count(),'Operational empty copy');
    await page.getByText('New memory',{exact:true}).click();await page.locator('.create-popover textarea').fill('Unsubmitted memory draft');
    await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));await frame();
    assert.equal(await page.locator('.create-popover textarea').inputValue(),'Unsubmitted memory draft');
    await page.locator('.create-popover textarea').fill('');await page.getByText('New memory',{exact:true}).click();
    if(!await page.locator('#right-panel').isVisible())await page.locator('#toggle-execution').click();
    await page.locator('#plan-tab').click();await page.locator('.plan-card').first().waitFor();
    await capture(scene+'-proposals','#planning-panel');await quiet('[data-plan=dismiss-all]');
    await page.locator('.plan-open-draft').first().click();await page.locator('.plan-draft-dialog[open]').waitFor();
    await capture(scene+'-proposal-editor','.plan-draft-dialog[open]');
    await quiet('[data-plan=refine]');await quiet('[data-plan=split]');
    await page.locator('[data-plan=split]').first().click();await quiet('[data-plan=cancel-split]');
    await page.locator('[data-plan=cancel-split]').first().click();await page.keyboard.press('Escape');
    assert.equal(await page.locator('.plan-open-draft').first().evaluate(e=>e===document.activeElement),true);
    await page.locator('[data-plan=new]').click();await page.locator('#plan-new-dialog[open]').waitFor();
    await capture(scene+'-new-chat','#plan-new-dialog');await quiet('[data-plan=cancel-new]');
    if(width===390)await page.locator('[data-plan=cancel-new]').tap();else await page.locator('[data-plan=cancel-new]').press('Enter');assert.equal(await page.locator('.plan-card').count(),2);
    console.log('PASS '+engine+'/'+scene+': filters, review, memory creation, proposals, keyboard and draft preservation');
  }
  if(['review','actions'].includes(process.env.PELLETS_EMPHASIS_CASE))return;
  for(const theme of ['gruvbox-light','gruvbox-dark','light','dark','icy'])for(const width of [1280,1092,390]){
    await page.setViewportSize({width,height:900});await page.goto(url);
    await setTheme(theme);
    if(!await page.locator('#right-panel').isVisible())await page.locator('#toggle-execution').click();await page.locator('#execution-tab').click();
    if(width===390 && await page.locator('#right-panel').isVisible())await page.locator('#toggle-execution').click();
    for(const kind of ['creation','record','group']) {
      if(kind==='creation')await page.getByText('+ New pellet',{exact:true}).click();
      if(kind==='record'){await page.locator('.task-title').first().click();await page.locator('#record-dialog[open]').waitFor();await page.locator('#record-dialog [data-description-mode=edit]').click();}
      if(kind==='group'){await page.goto(origin+'/projects/'+first.project+'/groups?workspace=1');await page.locator('.group-card a').first().click();await page.locator('#record-dialog[open]').waitFor();}
      const host=page.locator(kind==='creation'?'.create-popover [data-description]':'#record-dialog [data-description]');
      const field=host.locator('textarea');
      assert.match(await field.getAttribute('name'),/description|context/);
      await field.fill('Preserve **source** while changing emphasis.');
      await capture(theme+'-'+width+'-'+kind+'-description',kind==='creation'?'.create-popover form':'#record-dialog');
      check(await host.locator('.description-source').evaluate(e=>[...e.childNodes].every(n=>n.nodeType!==Node.TEXT_NODE || !n.textContent.trim())),'Shared source label has no duplicate visible text');
      assert.equal(await host.getByRole('textbox',{name:kind==='group'?'Shared context (Markdown)':'Description',exact:true}).count(),1,'Native label remains accessible');
      await host.locator('[data-description-mode=view]').click();await host.locator('[data-description-mode=edit]').click();
      assert.equal(await field.inputValue(),'Preserve **source** while changing emphasis.');
      await field.fill('');
      if(kind==='creation')await page.getByText('+ New pellet',{exact:true}).click();else {page.once('dialog',d=>d.accept());await page.keyboard.press('Escape');await page.locator('#record-dialog').waitFor({state:'hidden'});}
    }
    console.log('PASS '+engine+'/'+theme+'/'+width+': creation, record and group description labels, modes and source');
  }
  await page.setViewportSize({width:1280,height:900});await page.goto(url);if(!await page.locator('#right-panel').isVisible())await page.locator('#toggle-execution').click();await page.locator('#execution-tab').click();
  // Server patches the clear action after search/status changes, without rebuilding controls.
  await page.locator('#search').fill('Audit');await page.waitForURL(/q=Audit/);
  await page.locator('#filter-summary').click();await page.getByRole('link',{name:'Clear filters',exact:true}).waitFor();
  await page.getByRole('link',{name:'Clear filters',exact:true}).click();await page.waitForURL(u=>!u.searchParams.has('q'));
  assert.equal(new URL(page.url()).searchParams.get('workspace'),'1');
  await page.locator('#filter-summary').click();check(!await page.getByRole('link',{name:'Clear filters',exact:true}).isVisible(),'Clear disappears after clearing');
  await page.keyboard.press('Escape');
  const memory=cli('memory','add','--text','Keep secondary actions quiet.','--created-by','agent');
  await page.goto(origin+'/projects/'+first.project+'/memories/'+memory.id+'?workspace=1');await page.locator('#record-dialog[open]').waitFor();
  await capture('memory-record','#record-dialog');
  check(!await page.locator('#record-dialog .eyebrow').count(),'Memory heading is not duplicated');
  check(await page.locator('#record-dialog details.metadata').count(),'Secondary memory metadata is disclosed');
  if(!baseline){await page.locator('#record-dialog details.metadata > summary').focus();await page.locator('#record-dialog details.metadata > summary').press('Enter');assert.equal(await page.locator('#record-dialog details.metadata').evaluate(e=>e.open),true);}
  assert.deepEqual(errors,[]);console.log('PASS filter live state, memory metadata and no browser errors');
  fs.writeFileSync(path.join(artifacts,engine+'-'+(baseline?'before':'after')+'-captures.json'),JSON.stringify(captures,null,2));
})().catch(e=>{console.error(e);process.exitCode=1;}).finally(async()=>{
  await browser?.close();if(server?.exitCode===null){const done=new Promise(r=>server.once('exit',r));server.kill('SIGINT');await done;}
  console.log('Fixture: '+fixture+'; evidence: '+artifacts);
});
