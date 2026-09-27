// Checkpoint failures after real assignment/execution patches, in disposable data.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const engines = require('playwright');
const root = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-checkpoint-feedback-'));
const fixture = path.join(temporary, 'feedback');
const baseline = process.env.PELLETS_FEEDBACK_BASELINE;
const binary = baseline || path.join(temporary, 'pl');
const artifacts = process.env.PELLETS_BROWSER_ARTIFACTS;
const engine = process.env.PELLETS_BROWSER_ENGINE || 'chromium';
const env = {...process.env, PELLETS_CODEX_EXECUTABLE:path.join(temporary, 'missing-codex')};
const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd:fixture, env, encoding:'utf8'})).data;
let server, browser, page;
const errors = [];
async function capture(name) {
  if (!artifacts) return;
  const directory = path.join(artifacts, engine + '-' + name);
  fs.mkdirSync(directory, {recursive:true});
  const clip = name === 'remove' ? {x:270,y:60,width:740,height:610} : {x:270,y:560,width:740,height:310};
  const screenshot=await page.screenshot({path:path.join(directory, baseline?'before.png':'after.png'), clip, animations:'disabled', caret:'hide'});
  assert.ok(screenshot.length>8 && screenshot.subarray(1,4).toString()==='PNG','Capture must contain a readable PNG');
  fs.writeFileSync(path.join(directory, baseline?'before.json':'after.json'), JSON.stringify({clip,theme:'light',viewport:page.viewportSize(),scale:1,errors},null,2));
}
async function patchAwayFeedback(kind) {
  if (kind === 'assignment') {
    await page.locator('#assignment-popover > summary').click();
    await page.locator('.assignment-form [name=include_ungrouped]').check();
    await page.getByRole('button', {name:'Save assignments',exact:true}).click();
    await page.waitForFunction(() => !document.querySelector('#request-feedback'));
  } else {
    if(!await page.locator('#right-panel').isVisible())await page.locator('#toggle-execution').click();
    await page.locator('#execution-tab').click();
    await page.getByRole('button',{name:/Start next/}).click();
    await page.getByRole('button',{name:'Check again',exact:true}).waitFor();
    assert.equal(await page.locator('#request-feedback').evaluate(el=>!!el.closest('.run-controls')),true);
    await page.getByRole('button',{name:'Cancel',exact:true}).click();
    await page.waitForFunction(() => !document.querySelector('#request-feedback'));
  }
}
async function failAction(selector, kind) {
  const form = page.locator(selector), button = form.locator('button');
  const url = await form.getAttribute('action');
  let release, arrived, submissions=0, refreshes=0;
  const gate=new Promise(r=>release=r), ready=new Promise(r=>arrived=r);
  const waitReady=()=>Promise.race([ready,new Promise((_,reject)=>{const timer=setTimeout(()=>reject(Error('Checkpoint submission did not arrive')),15000);timer.unref();})]);
  await page.route('**'+url, async route => {submissions++;arrived();await gate;await route.fulfill({status:409,body:'Conflict'});});
  await page.evaluate(()=>{window.checkpointRefreshes=0;document.addEventListener('pellets-refresh',()=>window.checkpointRefreshes++);});
  await button.focus();await page.keyboard.press('Enter');await waitReady();
  assert.equal(await button.isDisabled(),true);
  await form.evaluate(el=>el.requestSubmit());
  release();
  await page.waitForFunction(selector=>!document.querySelector(selector)?.dataset.pending,selector);
  await page.evaluate(()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))));
  refreshes=await page.evaluate(()=>window.checkpointRefreshes);
  await capture(kind);
  if(!baseline){
    assert.equal(submissions,1,'Pending action must not submit twice');
    assert.ok(refreshes>0,'Failure retains authoritative refresh');
    const notice=page.locator('#request-feedback');
    await notice.waitFor();
    assert.match(await notice.innerText(),/refresh.*before trying again/i);
    assert.equal(await notice.evaluate((el,selector)=>!!el.closest(selector),selector),true,'Feedback stays beside the action');
    assert.equal(await button.isEnabled(),true,'Action can retry');
    for(const theme of ['light','dark','icy','gruvbox-light','gruvbox-dark']) for(const width of [1280,1092,390]) {
      await page.setViewportSize({width,height:900});
      await page.evaluate(theme=>window.Workbench.applyTheme(theme),theme);
      if(width===390 && await page.locator('#right-panel').isVisible())await page.locator('#toggle-execution').click();
      if(kind==='remove' && !await form.evaluate(el=>el.closest('.row-menu').open))await page.locator('.row-menu').filter({has:form}).locator(':scope > summary').click();
      await notice.scrollIntoViewIfNeeded();
      const box=await notice.boundingBox();
      assert.ok(box.x>=0 && box.x+box.width<=width+1 && box.y>=0 && box.y+box.height<=900,`${kind}/${theme}/${width}: feedback fits viewport`);
      assert.equal(await notice.evaluate(el=>el.scrollWidth<=el.clientWidth),true,`${kind}/${theme}/${width}: feedback wraps`);
    }
    await page.setViewportSize({width:1280,height:900});
    await page.evaluate(()=>window.Workbench.applyTheme('light'));

    assert.equal(await notice.evaluate(el=>{const b=el.getBoundingClientRect();return el.contains(document.elementFromPoint(b.x+b.width/2,b.y+b.height/2));}),true,'Feedback is visible and reachable');
  }
  await page.unroute('**'+url);
  return button;
}
(async()=>{
  if(!baseline)execFileSync('go',['build','-o',binary,'./cmd/pl'],{cwd:root});
  fs.mkdirSync(fixture);execFileSync('git',['init','-q'],{cwd:fixture});cli('init-db');
  const target=cli('add','Checkpoint feedback target');
  const checkpoint=cli('add','Review feedback target','--review-targets',target.id);
  const binding=JSON.parse(fs.readFileSync(path.join(fixture,'.git','pellets-database.json')));
  assert.ok(fs.realpathSync(path.resolve(fixture,'.git',binding.path)).startsWith(fs.realpathSync(fixture)+path.sep));
  server=spawn(binary,['server','--port','0','--no-open'],{cwd:fixture,env});
  const origin=await new Promise((resolve,reject)=>{let out='',error='';server.stdout.on('data',d=>{out+=d;if(out.includes('\n'))resolve(out.split('\n')[0].trim());});server.stderr.on('data',d=>error+=d);server.once('error',reject);server.once('exit',code=>reject(Error(`Server ${code}: ${error}`)));});
  browser=await engines[engine].launch({headless:true,...(process.env.PELLETS_BROWSER_EXECUTABLE?{executablePath:process.env.PELLETS_BROWSER_EXECUTABLE}:{})});page=await browser.newPage({viewport:{width:1280,height:900},deviceScaleFactor:1,reducedMotion:'reduce'});await page.bringToFront();page.setDefaultTimeout(30000);page.setDefaultNavigationTimeout(60000);
  page.on('pageerror',error=>errors.push(error.message));
  await page.addInitScript(()=>{window.EventSource=undefined;});
  await page.goto(origin+'/projects/'+target.project+'/tasks?workspace=1');
  await page.evaluate(()=>window.Workbench.applyTheme('light'));
  await patchAwayFeedback('assignment');console.log(engine+': assignment patched');
  await patchAwayFeedback('execution');console.log(engine+': execution patched');
  const row=page.locator('#task-'+checkpoint.id);
  await row.locator('.row-menu > summary').click();
  const remove=await failAction('[data-checkpoint-remove-inline]','remove');
  assert.equal(cli('show',checkpoint.id).status,'open');
  await remove.focus();await page.keyboard.press('Enter');
  await page.locator('[data-checkpoint-undo] button').waitFor();
  await row.waitFor({state:'detached'});
  await patchAwayFeedback('assignment');console.log(engine+': assignment patched');
  await patchAwayFeedback('execution');console.log(engine+': execution patched');
  const undo=await failAction('[data-checkpoint-undo]','undo');
  assert.equal(cli('show',checkpoint.id).status,'maybe_later');
  await undo.focus();await page.keyboard.press('Enter');
  await row.waitFor();await page.locator('#checkpoint-undo').waitFor({state:'detached'});
  await page.waitForFunction(id=>document.activeElement?.id===id,'review-actions-'+checkpoint.id);
  assert.equal(cli('show',checkpoint.id).status,'open');
  if(!baseline)assert.deepEqual(errors,[]);
  console.log(JSON.stringify({engine,baseline:!!baseline,errors,result:baseline?'Recorded original Remove/Undo failures':'PASS remove/undo after assignment and execution patches, retry, focus and duplicate protection'}));
})().catch(async error=>{await page?.screenshot({path:path.join(temporary,'failure.png'),timeout:5000}).catch(()=>{});console.error(error);console.error('Fixture:',temporary);process.exitCode=1;}).finally(async()=>{
  if(page)await page.unrouteAll({behavior:'ignoreErrors'});await browser?.close();
  if(server?.exitCode===null){const done=new Promise(r=>server.once('exit',r));server.kill('SIGINT');await done;}
});
