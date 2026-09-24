// Production hit targets and feedback in disposable fixtures. No real model calls.
// Set PLAYWRIGHT_BROWSER=webkit for WebKit; PELLETS_INTERACTION_BASELINE captures
// the original executable without enforcing the corrected contracts.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-interaction-'));
const baseline = process.env.PELLETS_INTERACTION_BASELINE;
const binary = baseline || path.join(temporary, 'pl'), peer = path.join(temporary, 'codex');
const fixture = path.join(temporary, 'interaction');
const engineName = process.env.PLAYWRIGHT_BROWSER || 'chromium';
const artifacts = process.env.PELLETS_BROWSER_ARTIFACTS || path.join(temporary, 'screenshots');
const env = {...process.env, PATH: temporary + path.delimiter + process.env.PATH, PELLETS_CODEX_EXECUTABLE: peer, PELLETS_SUPERVISOR_PEER: '1'};
const check = (value, message) => { if (!baseline) assert.ok(value, message); };
let server, browser, page;
const captures = [];
async function frame() { await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))); }
async function capture(name, selector) {
  const directory = path.join(artifacts, engineName + '-' + name); fs.mkdirSync(directory, {recursive:true});
  await frame();
  const box = await page.locator(selector).boundingBox();
  const clip = selector === '#main' ? {...box, height:350} : selector === '#planning-panel' ? {...box, y:box.y+box.height-370, height:370} : box;
  await page.screenshot({path:path.join(directory, baseline ? 'before.png' : 'after.png'), clip, animations:'disabled', caret:'hide'});
  captures.push({name:engineName+'-'+name, selector, clip, viewport:page.viewportSize(), scale:1,
    state:await page.evaluate(()=>({theme:document.documentElement.dataset.themeChoice, scroll:[scrollX,scrollY], mainScroll:document.querySelector('#main').scrollTop, focused:document.activeElement?.outerHTML.slice(0,300)}))});
}
async function feedback(selector) {
  const node = page.locator(selector).first();
  await node.scrollIntoViewIfNeeded();
  await page.mouse.move(0,0); await frame();
  const before = await node.evaluate(e => ({color:getComputedStyle(e).backgroundColor, box:e.getBoundingClientRect().toJSON()}));
  await node.hover(); await frame();
  const after = await node.evaluate(e => ({color:getComputedStyle(e).backgroundColor, box:e.getBoundingClientRect().toJSON()}));
  check(before.color !== after.color, selector + ': missing hover background');
  check(JSON.stringify(before.box) === JSON.stringify(after.box), selector + ': hover moves control: '+JSON.stringify({before,after}));
}
(async () => {
  if (!baseline) execFileSync('go',['build','-o',binary,'./cmd/pl'],{cwd:repository});
  execFileSync('go',['test','-c','-o',peer,'./internal/app'],{cwd:repository});
  fs.mkdirSync(fixture);
  execFileSync('git',['init','-q'],{cwd:fixture});
  execFileSync(binary,['init-db','--help'],{cwd:fixture});
  // Always initialize a fresh OS-temporary repository, before discovery can run.
  execFileSync(binary,['--json','init-db'],{cwd:fixture});
  const cli = (...args) => JSON.parse(execFileSync(binary,['--json',...args],{cwd:fixture,env,encoding:'utf8'})).data;
  for (const args of [['--help'],['add','--help'],['group','--help']]) execFileSync(binary,args,{cwd:fixture});
  const first = cli('add','Inspect complete queue targets','--group','Interface');
  cli('add','Review interaction changes','--review-targets',first.id);
  execFileSync(binary,['memory','--help'],{cwd:fixture});
  cli('memory','add','--text','Keep complete controls reachable with mouse, keyboard and touch.','--created-by','agent');
  const bindingFile=path.join(fixture,'.git','pellets-database.json');
  const binding=JSON.parse(fs.readFileSync(bindingFile,'utf8'));
  assert.ok(fs.realpathSync(path.resolve(path.dirname(bindingFile),binding.path)).startsWith(fs.realpathSync(fixture)+path.sep),'Fixture database must stay inside its own repository');
  fs.writeFileSync(path.join(fixture,'fake-mode'),'planning_full');
  server = spawn(binary,['server','--port','0','--no-open'],{cwd:fixture,env});
  const origin = await new Promise((resolve,reject) => {
    let out='',err=''; server.stdout.on('data',d=>{out+=d;if(out.includes('\n'))resolve(out.split('\n')[0].trim());});
    server.stderr.on('data',d=>err+=d);server.once('error',reject);server.once('exit',code=>reject(Error(`Server ${code}: ${err}`)));
  });
  browser = await (engineName === 'webkit' ? webkit : chromium).launch({headless:true});
  page = await browser.newPage({viewport:{width:1280,height:900},deviceScaleFactor:1});
  page.setDefaultTimeout(15000);
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  const url=origin+'/projects/'+first.project+'/tasks?workspace=1';
  await page.goto(url);
  if(!await page.locator('#right-panel').isVisible())await page.locator('#toggle-execution').click();
  await page.locator('#plan-tab').click();await page.locator('#plan-message').waitFor();
  await page.locator('#plan-message').fill('Propose interaction checks');
  await page.waitForFunction(()=>!document.querySelector('.plan-send')?.disabled);
  await page.locator('.plan-send').click();await page.locator('.plan-card').first().waitFor();
  await page.evaluate(()=>window.Planner.flush());
  const observations=[];
  for (const theme of process.env.PELLETS_INTERACTION_CASE === 'memory' ? [] : ['gruvbox-light','gruvbox-dark','light','dark','icy']) {
    for (const width of [1280,1092,390]) {
      await page.setViewportSize({width,height:900});
      await page.evaluate(theme=>window.Workbench.applyTheme(theme),theme);
      await page.locator('#execution-tab').click();
      if(width===390) await page.locator('#toggle-execution').click();
      await page.mouse.move(0,0);
      const row=page.locator('.pellet-row').first(), menu=row.locator('.row-menu > summary');
      const opacity=await menu.evaluate(e=>getComputedStyle(e).opacity);
      check(Number(opacity)>0,'Queue actions must be discoverable without hover');
      const rowTitle=row.locator('.task-title');
      await page.keyboard.press('Tab');await rowTitle.focus();await page.mouse.move(0,0);
      check(await row.evaluate(e=>getComputedStyle(e).outlineStyle!=='none'),'Keyboard title focus identifies the complete row');
      await capture(`${theme}-${width}-queue-focus`, '#main');
      await rowTitle.press('Enter');await page.locator('#record-dialog[open]').waitFor();await page.keyboard.press('Escape');
      await page.locator('#record-dialog').waitFor({state:'hidden'});
      await page.locator('#search').focus();await page.locator('#search').evaluate(e=>e.blur());
      if(process.env.PELLETS_INTERACTION_CASE==='focus')continue;
      await capture(`${theme}-${width}-queue`, '#main');
      await feedback('.pellet-row .row-menu > summary');
      await menu.focus(); await page.keyboard.press('Space');
      const spaceOpens=await menu.evaluate(e=>e.parentElement.open);
      check(spaceOpens,'Space opens the native row menu');
      if (!spaceOpens) await menu.click();
      await page.keyboard.press('Escape');
      // Native Space on a menu action must activate once, not be swallowed by typeahead.
      await menu.click();
      const insert=row.locator('[data-insert-checkpoint=before]');await insert.focus();await page.keyboard.press('Space');
      const actionSpace=await page.locator('#insert-dialog').evaluate(e=>e.open);
      check(actionSpace,'Space activates the focused native menu action');
      if(actionSpace) await page.keyboard.press('Escape'); else await menu.focus().then(()=>page.keyboard.press('Escape'));
      await menu.click();
      await capture(`${theme}-${width}-queue-menu`, '#main');
      const label=row.locator('.row-popover label');
      const checked=await row.locator('[data-checkpoint-select]').isChecked();
      // Click the label text, outside the input: default label forwarding is
      // independent from the queue's background-click navigation.
      await label.click({position:{x:100,y:12}});
      await frame();
      check(await row.locator('[data-checkpoint-select]').isChecked()!==checked,'Whole checkbox label toggles selection');
      if(baseline)await page.locator('#record-dialog[open]').waitFor();
      const labelNavigates=await page.locator('#record-dialog').evaluate(e=>e.open);
      check(!labelNavigates,'Checkbox label must not navigate the row');
      if(labelNavigates){await page.keyboard.press('Escape');await page.locator('#record-dialog').waitFor({state:'hidden'});}
      await menu.click();
      if(!await menu.evaluate(e=>e.parentElement.open))await menu.click();
      if(!baseline)await row.locator('.row-popover').click({position:{x:2,y:2}});
      check(await page.locator('#record-dialog').evaluate(e=>!e.open),'Menu surface padding must not navigate the row');
      // Restore selection so the next screenshot measures the same queue,
      // rather than the checkpoint composer introduced by the label check.
      if(await page.locator('#record-dialog').evaluate(e=>e.open)){await page.keyboard.press('Escape');await page.locator('#record-dialog').waitFor({state:'hidden'});}
      if(!await menu.evaluate(e=>e.parentElement.open))await menu.click();
      await row.locator('[data-checkpoint-select]').uncheck();
      await page.locator('[data-checkpoint-composer]').waitFor({state:'hidden'});
      await menu.focus();await page.keyboard.press('Escape');
      await feedback('.review-disclosure > summary');
      await capture(`${theme}-${width}-review-hover`, '#main');
      const disclosure=page.locator('.review-disclosure > summary');await disclosure.focus();await page.keyboard.press('Enter');
      check(await disclosure.evaluate(e=>e.parentElement.open),'Review scope expands independently');
      await disclosure.press('Space');
      // Background row padding navigates, while group links and menus stay independent.
      await row.click({position:{x:3,y:3}});await page.locator('#record-dialog[open]').waitFor();
      await page.keyboard.press('Escape');
      if(width===390) await page.locator('#toggle-execution').click();
      await page.locator('#plan-tab').click();
      await page.locator('#plan-message').fill('Unsubmitted interaction draft');
      await page.mouse.move(0,0);await page.locator('#plan-message').focus();await frame();
      const card=page.locator('.plan-card').first(), title=card.locator('.plan-open-draft'), dismiss=card.locator('.plan-dismiss');
      const idle=await title.boundingBox(), visible=await dismiss.isVisible();
      await capture(`${theme}-${width}-proposal-idle`, '#planning-panel');
      check(visible,'Proposal dismissal is discoverable without hover');
      check(await dismiss.evaluate(e=>{const r=e.getBoundingClientRect(),p=e.parentElement.getBoundingClientRect();return r.width>=28&&r.height>=28&&r.right<=p.right&&e.contains(document.elementFromPoint(r.x+r.width/2,r.y+r.height/2));}),'Proposal dismissal retains an unobscured 28px hit target');
      await title.hover();await frame();const hovered=await title.boundingBox();
      check(JSON.stringify(idle)===JSON.stringify(hovered),'Proposal title must not move or truncate differently on hover: '+JSON.stringify({idle,hovered}));
      await capture(`${theme}-${width}-proposal-hover`, '#planning-panel');
      await feedback('.plan-tray-toggle');
      await feedback('.plan-draft-heading [data-plan=add-draft]');
      await capture(`${theme}-${width}-proposal-action`, '#planning-panel');
      await title.focus();await page.keyboard.press('Enter');await card.locator('dialog[open]').waitFor();
      await card.locator('[name=title]').fill('Retained proposal title');await page.keyboard.press('Escape');
      check(await title.evaluate(e=>e===document.activeElement),'Proposal editor returns focus');
      const selection=card.locator('.plan-row-select'), selected=await selection.isChecked();
      await selection.press('Space');check(await selection.isChecked()!==selected,'Independent checkbox keyboard activation');
      check(await card.locator('dialog').evaluate(e=>!e.open),'Checkbox must not open editor');
      await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));await frame();
      check(await page.locator('#plan-message').inputValue()==='Unsubmitted interaction draft','Refresh preserves composer draft');
      observations.push({theme,width,opacity,spaceOpens,actionSpace,labelNavigates,visible,idle,hovered});
      console.log(`${baseline ? 'OBSERVED baseline' : 'PASS'} ${engineName}/${theme}/${width}: target geometry, feedback, keyboard isolation, drafts, disabled state`);
    }
  }
  if (!process.env.PELLETS_INTERACTION_CASE) {
    // Hold an actual planning request: application renders cannot re-enable
    // these controls until the deterministic peer is explicitly released.
    fs.writeFileSync(path.join(fixture,'fake-mode'),'planning_gate');
    fs.rmSync(path.join(fixture,'fake-planning-release'),{force:true});
    await page.locator('#plan-message').fill('Check busy planning feedback');
    await page.evaluate(()=>window.Planner.flush());
    await page.waitForFunction(()=>!document.querySelector('.plan-send')?.disabled);
    await page.locator('.plan-send').click();
    await page.locator('.plan-status.busy').waitFor();
    await page.locator('#plan-message').fill('Retain this draft while busy');
    for (const theme of ['gruvbox-light','gruvbox-dark','light','dark','icy']) {
      for (const width of [1280,1092,390]) {
        await page.setViewportSize({width,height:900});
        await page.evaluate(theme=>window.Workbench.applyTheme(theme),theme);
        const trigger=page.locator('#plan-access-trigger');await page.mouse.move(0,0);await frame();
        const color=await trigger.evaluate(e=>getComputedStyle(e).backgroundColor);
        assert.ok(await trigger.isDisabled(),'Busy selector fixture must remain disabled');
        await trigger.hover();
        check(await trigger.evaluate(e=>getComputedStyle(e).backgroundColor)===color,'Disabled selector must not gain enabled hover feedback');
        check(await trigger.evaluate(e=>getComputedStyle(e).cursor)==='not-allowed','Disabled selector cursor');
        await capture(`${theme}-${width}-disabled-selector`, '#plan-form');
      }
    }
    fs.writeFileSync(path.join(fixture,'fake-planning-release'),'continue');
    await page.locator('.plan-status.busy').waitFor({state:'hidden'});
    check(await page.locator('#plan-message').inputValue()==='Retain this draft while busy','Busy response retains newer input');
  }
  for (const theme of process.env.PELLETS_INTERACTION_CASE==='focus'?[]:['gruvbox-light','gruvbox-dark','light','dark','icy']) {
    for (const width of [1280,1092,390]) {
      await page.setViewportSize({width,height:900});
      await page.goto(origin+'/projects/'+first.project+'/memories?workspace=1');
      if(!await page.locator('#right-panel').isVisible()) await page.locator('#toggle-execution').click();
      await page.locator('#execution-tab').click();
      if(width===390 && await page.locator('#right-panel').isVisible()) await page.locator('#toggle-execution').click();
      await page.evaluate(theme=>window.Workbench.applyTheme(theme),theme);
      const card=page.locator('.memory-card').first(), link=card.locator(':scope > a');
      await card.hover({position:{x:5,y:5}});await capture(`${theme}-${width}-memory`, '#main');
      const whole=await card.evaluate(e=>{const a=e.querySelector('a'),r=e.getBoundingClientRect(),b=a.getBoundingClientRect();return a.contains(e.querySelector('footer'))&&b.y===r.y&&b.bottom>=r.bottom-1;});
      check(whole,'Memory metadata and padding must belong to the native link');
      console.log(`${baseline?'OBSERVED baseline':'PASS'} ${engineName}/${theme}/${width}: memory whole-card target=${whole}`);
      if(!baseline){
        await card.click({position:{x:5,y:5}});await page.locator('#record-dialog[open]').waitFor();
        await page.keyboard.press('Escape');await page.locator('#record-dialog').waitFor({state:'hidden'});
        // Traverse using real keyboard navigation after the dismissal patch.
        // Programmatic focus can retain pointer modality from the opening click.
        await link.focus();
        // macOS WebKit uses Option+Tab to include native links in traversal.
        await page.keyboard.press(engineName==='webkit'?'Alt+Shift+Tab':'Shift+Tab');
        await page.keyboard.press(engineName==='webkit'?'Alt+Tab':'Tab');
        check(await link.evaluate(e=>e===document.activeElement && e.matches(':focus-visible') && getComputedStyle(e).outlineStyle!=='none'),'Memory card is keyboard reachable with visible focus');
      }
    }
  }
  // Touch does not require a synthetic hover to discover or activate actions.
  const touch=await browser.newPage({viewport:{width:390,height:900},hasTouch:true});
  await touch.goto(url);if(!await touch.locator('#right-panel').isVisible())await touch.locator('#toggle-execution').tap();await touch.locator('#execution-tab').tap();await touch.locator('#toggle-execution').tap();
  await touch.locator('.pellet-row .row-menu > summary').tap();
  check(await touch.locator('.pellet-row .row-menu').evaluate(e=>e.open),'Touch opens row actions');
  await touch.locator('.pellet-row [data-checkpoint-select]').tap();
  check(await touch.locator('.pellet-row [data-checkpoint-select]').isChecked(),'Touch checkbox stays independent');
  check(await touch.locator('#record-dialog').evaluate(e=>!e.open),'Touch checkbox does not navigate');
  await touch.close();
  fs.mkdirSync(artifacts,{recursive:true});fs.writeFileSync(path.join(artifacts,engineName+(baseline?'-before':'-after')+(process.env.PELLETS_INTERACTION_CASE?'-'+process.env.PELLETS_INTERACTION_CASE:'')+'-observations.json'),JSON.stringify(observations,null,2));
  fs.writeFileSync(path.join(artifacts,engineName+(baseline?'-before':'-after')+(process.env.PELLETS_INTERACTION_CASE?'-'+process.env.PELLETS_INTERACTION_CASE:'')+'-captures.json'),JSON.stringify(captures,null,2));
  assert.deepEqual(errors,[]);console.log('Artifacts: '+artifacts);
})().catch(async error=>{console.error(error);if(page)await page.screenshot({path:path.join(temporary,'failure.png')}).catch(()=>{});console.error('Fixture: '+temporary);process.exitCode=1;}).finally(async()=>{
  await browser?.close();if(server?.exitCode===null){const done=new Promise(r=>server.once('exit',r));server.kill('SIGINT');await done;}
});
