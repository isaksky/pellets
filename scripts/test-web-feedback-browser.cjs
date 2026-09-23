// Production feedback audit. All writes use an explicitly isolated database.
// PELLETS_FEEDBACK_BASELINE=/path/to/pl records the original behavior.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const root = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-feedback-'));
const fixture = path.join(temporary, 'feedback');
const baseline = process.env.PELLETS_FEEDBACK_BASELINE;
const binary = baseline || path.join(temporary, 'pl');
const artifacts = process.env.PELLETS_BROWSER_ARTIFACTS || path.join(temporary, 'screenshots');
const engine = process.env.PLAYWRIGHT_BROWSER || 'chromium';
const env = {...process.env, PELLETS_CODEX_EXECUTABLE:path.join(os.tmpdir(), 'pellets-feedback-missing-codex')};
const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd:fixture, env, encoding:'utf8'})).data;
let server, browser, page;
const observations = [];
const check = (value, message) => { if (!baseline) assert.ok(value, message); };
async function capture(name, selector) {
  await page.mouse.move(0, 0);
  const directory = path.join(artifacts, engine + '-' + name);
  fs.mkdirSync(directory, {recursive:true});
  let clip = await page.locator(selector).boundingBox();
  clip = {x:Math.max(0, clip.x - 8), y:Math.max(0, clip.y - 8),
    width:Math.min(page.viewportSize().width - Math.max(0, clip.x - 8), clip.width + 16),
    height:Math.min(page.viewportSize().height - Math.max(0, clip.y - 8), clip.height + 16)};
  if (selector === '#record-dialog') {
    clip.y = Math.max(0, clip.y - 100);
    clip.height = Math.min(page.viewportSize().height - clip.y, clip.height + 200);
  } else if (name.endsWith('execution-preflight')) {
    clip.x = Math.max(0, Math.min(clip.x, page.viewportSize().width / 2 - 228));
    clip.width = page.viewportSize().width - clip.x;
    clip.height = Math.min(620, clip.height);
  }
  const before = path.join(directory, 'before.json');
  if (!baseline && fs.existsSync(before)) clip = JSON.parse(fs.readFileSync(before)).clip;
  await page.screenshot({path:path.join(directory, baseline ? 'before.png' : 'after.png'), clip, animations:'disabled', caret:'hide'});
  const feedback = page.locator('.request-feedback:not([hidden]), #request-feedback:not([hidden]), .plan-status:not([hidden])').first();
  const observation = {name, clip, viewport:page.viewportSize(), scale:1,
    theme:await page.locator('html').getAttribute('data-theme'),
    feedback:await feedback.count() ? await feedback.evaluate(el => {
      const box=el.getBoundingClientRect();
      return {text:el.textContent, height:box.height, inDialog:!!el.closest('dialog'),
        reachable:el.contains(document.elementFromPoint(box.x+box.width/2, box.y+box.height/2))};
    }) : null};
  fs.writeFileSync(path.join(directory, baseline ? 'before.json' : 'after.json'), JSON.stringify(observation,null,2));
  observations.push(observation);
}
async function theme(value) {
  await page.locator('#theme-select').selectOption(value, {force:true});
  await page.waitForFunction(value => document.documentElement.dataset.theme === value, value);
  for (let i=0;i<200;i++) {
    if ((await (await page.request.get(new URL('/settings',page.url()).href)).json()).settings.theme === value) return;
    await page.waitForTimeout(25);
  }
  throw Error('Theme did not persist');
}
(async () => {
  if (!baseline) execFileSync('go', ['build','-o',binary,'./cmd/pl'], {cwd:root});
  fs.mkdirSync(fixture); execFileSync('git',['init','-q'],{cwd:fixture});
  cli('init-db');
  const pellet = cli('add','Feedback audit record');
  const binding = JSON.parse(fs.readFileSync(path.join(fixture,'.git','pellets-database.json')));
  assert.ok(fs.realpathSync(path.resolve(fixture,'.git',binding.path)).startsWith(fs.realpathSync(fixture)+path.sep));
  server=spawn(binary,['server','--port','0','--no-open'],{cwd:fixture,env});
  const origin=await new Promise((resolve,reject)=>{let out='',error='';server.stdout.on('data',d=>{out+=d;if(out.includes('\n'))resolve(out.split('\n')[0].trim());});server.stderr.on('data',d=>error+=d);server.once('error',reject);server.once('exit',code=>reject(Error(`Server ${code}: ${error}`)));});
  browser=await (engine==='webkit'?webkit:chromium).launch({headless:true});
  page=await browser.newPage({viewport:{width:1280,height:900},deviceScaleFactor:1,hasTouch:true});
  page.setDefaultTimeout(15000);
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  const url=origin+'/projects/'+pellet.project+'/tasks?workspace=1';
  // Both revisions start with the same empty saved chat. Retry checks below
  // restore this input so subsequent screenshots also have identical data.
  await page.goto(url);await page.locator('#plan-tab').click();
  await page.locator('.plan-folder-context').waitFor();
  await page.locator('#plan-message').fill('Initialize fixture chat');await page.evaluate(()=>window.Planner.flush());
  await page.locator('#plan-message').fill('');await page.evaluate(()=>window.Planner.flush());
  await page.locator('#execution-tab').click();
  for (const choice of (process.env.PELLETS_FEEDBACK_CASE==='refresh'?[]:['light','dark','icy','gruvbox-light','gruvbox-dark'])) for (const width of [1280,1092,390]) {
    await page.setViewportSize({width,height:900}); await page.goto(url); await theme(choice);
    if (width===390 && await page.locator('#right-panel').isVisible()) await page.locator('#toggle-execution').click();
    // A failed Datastar mutation must keep its feedback inside the active dialog.
    await page.locator('.task-title').first().click(); await page.locator('#record-dialog[open]').waitFor();
    const title=page.locator('#record-dialog [name=title]'); await title.fill('Keep this unsaved title');
    await page.route('**/projects/*/pellets/*/edit**',route=>route.request().method()==='POST'?route.abort():route.continue());
    await page.getByRole('button',{name:'Save changes',exact:true}).click();
    const notice=page.locator('.request-feedback, #request-feedback').filter({hasText:'Save could not be confirmed'});
    await notice.waitFor();
    await capture(`${choice}-${width}-record-save`, '#record-dialog');
    check(await notice.evaluate(el=>!!el.closest('#record-dialog')), 'Save feedback belongs to the dialog');
    check((await notice.boundingBox()).height<180,'Short feedback is compact');
    assert.equal(await title.inputValue(),'Keep this unsaved title');
    await title.focus(); await title.evaluate(el=>el.setSelectionRange(5,12));
    await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));
    assert.equal(await title.inputValue(),'Keep this unsaved title');
    assert.deepEqual(await title.evaluate(el=>[document.activeElement===el,el.selectionStart,el.selectionEnd]),[true,5,12]);
    await page.unroute('**/projects/*/pellets/*/edit**');
    page.once('dialog',d=>d.accept()); await page.keyboard.press('Escape');
    await page.locator('#record-dialog').waitFor({state:'hidden'});
    // A rejected start is checked at invocation and remains beside its control.
    await page.goto(url); if(!await page.locator('#right-panel').isVisible())await page.locator('#toggle-execution').click();
    await page.locator('#execution-tab').click();
    await page.getByRole('button',{name:/Start next/}).click();
    await page.getByRole('button',{name:'Check again',exact:true}).waitFor();
    await capture(`${choice}-${width}-execution-preflight`, '#right-panel');
    const failure=page.locator('.request-feedback, #request-feedback').filter({has:page.getByRole('button',{name:'Check again',exact:true})});
    check(await failure.evaluate(el=>!!el.closest('.run-controls')), 'Preflight feedback stays with execution controls');
    check((await failure.boundingBox()).height<300,'Preflight message wraps without filling the viewport');
    assert.equal(cli('show',pellet.id).status,'open','Failed preflight must not claim work');
    if(width===390){
      const retry=page.getByRole('button',{name:'Check again',exact:true});
      await retry.scrollIntoViewIfNeeded();await retry.tap();await retry.waitFor();
      assert.equal(cli('show',pellet.id).status,'open','Touch preflight retry must not claim work');
    }
    await page.getByRole('button',{name:'Cancel',exact:true}).click();
    // Failed initial planning reads must explain retry, not report a missing checkout.
    await page.route('**/planning**',route=>route.request().method()==='GET'?route.abort():route.continue()); await page.goto(url);
    await page.locator('#plan-tab').click(); await page.locator('.plan-status').waitFor();
    await capture(`${choice}-${width}-planning-load`, '#right-panel');
    check(await page.getByRole('button',{name:'Retry loading',exact:true}).count(), 'Failed planning load has an explicit read-only retry');
    check(!await page.locator('#plan-folder-error').isVisible(), 'Unknown folders are not reported as absent');
    if(!baseline) {
      await page.locator('#plan-message').fill('Preserve my planning input');
      await page.unroute('**/planning**');
      const retry=page.getByRole('button',{name:'Retry loading',exact:true});
      if(width===390)await retry.tap();else {await retry.focus();await page.keyboard.press('Enter');}
      await page.waitForFunction(()=>!document.querySelector('.plan-status.error'));
      assert.equal(await page.locator('#plan-message').inputValue(),'Preserve my planning input');
      await page.evaluate(()=>window.Planner.flush());
      await page.locator('#plan-message').fill('');await page.evaluate(()=>window.Planner.flush());
    } else await page.unroute('**/planning**');
    console.log(`PASS ${engine}/${choice}/${width}: record draft, preflight, planning load feedback`);
  }
  // A second invalidation arriving during an old read must not be dropped.
  await page.close();page=await browser.newPage({viewport:{width:1280,height:900},deviceScaleFactor:1});
  await page.addInitScript(()=>{window.EventSource=undefined;});
  await page.goto(url);await theme('light');
  await page.locator('.task-title').first().waitFor();
  let release,arrived,reads=0;
  const gate=new Promise(resolve=>{release=resolve;}),ready=new Promise(resolve=>{arrived=resolve;});
  await page.route('**/*',async route=>{
    if(route.request().headers()['pellets-target']!=='live')return route.continue();
    reads++;
    if(reads!==1)return route.continue();
    const response=await route.fetch();arrived();await gate;await route.fulfill({response});
  });
  await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));await ready;
  const added=cli('add','Arrived during the previous refresh');
  await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));
  const returned=page.waitForResponse(response=>response.request().headers()['pellets-target']==='live');
  release();await(await returned).finished();
  let current=true;
  try{await page.locator('#task-'+added.id).waitFor({timeout:3000});}catch{current=false;}
  await capture('live-refresh-race','#main');
  console.log(`Live refresh: ${reads} reads; new row visible=${current}`);
  check(current,'A queued invalidation must refresh the newer authoritative state');
  await page.unrouteAll({behavior:'wait'});
  assert.deepEqual(errors,[]);
  fs.writeFileSync(path.join(artifacts,`${engine}-${baseline?'before':'after'}-observations.json`),JSON.stringify(observations,null,2));
})().catch(async error=>{console.error(error);if(page)await page.screenshot({path:path.join(temporary,'failure.png')});console.error('Fixture:',temporary);process.exitCode=1;}).finally(async()=>{
  await browser?.close();
  if(server?.exitCode===null){const done=new Promise(r=>server.once('exit',r));server.kill('SIGINT');await done;}
});
