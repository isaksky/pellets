// Held real API responses exercise draft merging and refresh races.
// PELLETS_FEEDBACK_BASELINE records the original build; repeat in both engines.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {spawn, execFileSync} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const root = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-planning-feedback-'));
const fixture = path.join(temporary, 'planner');
const baseline = process.env.PELLETS_FEEDBACK_BASELINE;
const binary = baseline || path.join(temporary, 'pl'), peer = path.join(temporary, 'codex');
const artifacts = process.env.PELLETS_BROWSER_ARTIFACTS || temporary;
const engine = process.env.PLAYWRIGHT_BROWSER || 'chromium';
const env = {...process.env, PATH:temporary + path.delimiter + process.env.PATH,
  PELLETS_CODEX_EXECUTABLE:peer, PELLETS_SUPERVISOR_PEER:'1'};
let server, browser, page;
async function capture(name) {
  const directory = path.join(artifacts, engine + '-' + name);
  fs.mkdirSync(directory, {recursive:true});
  const clip = name==='planning-refresh-race' ? {x:280, y:330, width:720, height:550} : {x:280, y:450, width:720, height:430};
  await page.screenshot({path:path.join(directory, baseline?'before.png':'after.png'), clip, animations:'disabled', caret:'hide'});
  fs.writeFileSync(path.join(directory, baseline?'before.json':'after.json'), JSON.stringify({clip,
    theme:await page.locator('html').getAttribute('data-theme'), viewport:page.viewportSize(), scale:1},null,2));
}
async function holdResponse(matches) {
  let release, arrived, held = false;
  const gate = new Promise(resolve => { release=resolve; });
  const ready = new Promise(resolve => { arrived=resolve; });
  await page.route('**/planning*', async route => {
    if (held || !matches(route.request())) return route.continue();
    held = true;
    const response = await route.fetch();
    arrived(); await gate; await route.fulfill({response});
  });
  return {release, ready:() => Promise.race([ready, new Promise((_,reject) => {
    const timer=setTimeout(() => reject(Error('Expected planning request did not arrive')),15000);timer.unref();
  })])};
}
(async () => {
  if (!baseline) execFileSync('go',['build','-o',binary,'./cmd/pl'],{cwd:root});
  execFileSync('go',['test','-c','-o',peer,'./internal/app'],{cwd:root});
  fs.mkdirSync(fixture);execFileSync('git',['init','-q'],{cwd:fixture});
  const cli=(...args)=>JSON.parse(execFileSync(binary,['--json',...args],{cwd:fixture,env,encoding:'utf8'})).data;
  cli('init-db'); const first=cli('add','Planner feedback fixture');
  const binding=JSON.parse(fs.readFileSync(path.join(fixture,'.git','pellets-database.json')));
  assert.ok(fs.realpathSync(path.resolve(fixture,'.git',binding.path)).startsWith(fs.realpathSync(fixture)+path.sep));
  fs.writeFileSync(path.join(fixture,'fake-mode'),'planning_full');
  server=spawn(binary,['server','--port','0','--no-open'],{cwd:fixture,env});
  const origin=await new Promise((resolve,reject)=>{let out='',errors='';server.stdout.on('data',d=>{out+=d;if(out.includes('\n'))resolve(out.split('\n')[0].trim());});server.stderr.on('data',d=>errors+=d);server.once('error',reject);server.once('exit',code=>reject(Error(`Server ${code}: ${errors}`)));});
  browser=await(engine==='webkit'?webkit:chromium).launch({headless:true});
  page=await browser.newPage({viewport:{width:1280,height:900},deviceScaleFactor:1});page.setDefaultTimeout(15000);
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.addInitScript(()=>{window.EventSource=undefined;});
  // Background invalidations are controlled explicitly in this race fixture.
  for(let i=0;i<200;i++){
    if((await(await page.request.get(origin+'/models')).json()).fetched_at)break;
    if(i===199)throw Error('Fixture catalog did not load');
    await page.waitForTimeout(25);
  }
  const endpoint=origin+'/projects/'+first.project+'/planning';
  const savedDraft=async()=>(await(await page.request.get(endpoint)).json()).chat.state.drafts[0];
  await page.goto(origin+'/projects/'+first.project+'/tasks');await page.locator('#plan-tab').click();
  await page.locator('#plan-message').fill('Propose feedback checks');await page.locator('.plan-send').click();
  await page.locator('.plan-card').first().waitFor();await page.evaluate(()=>window.Planner.flush());
  await page.locator('.plan-open-draft').first().click();const editor=page.locator('.plan-draft-dialog[open]');
  const model=editor.locator('[name=model]'),effort=editor.locator('[name=reasoning_effort]');
  const saved=()=>page.waitForFunction(()=>document.querySelector('.plan-editor-status').textContent==='Changes saved automatically');
  const saving=request=>request.method()==='POST'&&request.postDataJSON().action==='save';
  const gate=await holdResponse(saving);
  await editor.locator('[name=title]').fill('Keep newer preferences while saving');await gate.ready();
  await model.selectOption('test-model');await effort.selectOption('high');
  gate.release();await saved();
  console.log('Preferences after old save:',JSON.stringify([await model.inputValue(),await effort.inputValue()]));
  await capture('planning-preference-race');
  if(!baseline){
    assert.equal(await model.inputValue(),'test-model');assert.equal(await effort.inputValue(),'high');
    const value=await savedDraft();assert.equal(value.model,'test-model');assert.equal(value.reasoning_effort,'high');
  }
  await page.unrouteAll({behavior:'wait'});
  if(!baseline){
    const gate=await holdResponse(saving);
    await editor.locator('[name=title]').fill('Keep explicitly cleared preferences');await gate.ready();
    await model.selectOption('');await effort.selectOption('');gate.release();await saved();
    assert.equal(await model.inputValue(),'');assert.equal(await effort.inputValue(),'');
    const value=await savedDraft();assert.equal(value.model??null,null);assert.equal(value.reasoning_effort??null,null);
    await page.unrouteAll({behavior:'wait'});
  }
  // A clean read can be stale by the time its response arrives. Hold the next
  // autosave too, so the read demonstrably finishes before a newer save response.
  await editor.locator('[name=title]').fill('Refresh response fixture');
  await page.evaluate(()=>window.Planner.flush());
  await page.waitForTimeout(1100); // The production read-invalidation throttle.
  const read=await holdResponse(request=>request.method()==='GET');
  await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));await read.ready();console.log('Held clean read');
  const save=await holdResponse(saving);
  const field=editor.locator('[name=acceptance]');await field.fill('Keep these newer acceptance criteria');
  await field.evaluate(el=>el.setSelectionRange(5,10));
  await save.ready();console.log('Held newer save');
  const response=page.waitForResponse(response=>response.request().method()==='GET'&&response.url().includes('/planning?'));
  read.release();await(await response).finished();
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
  console.log('Draft after old read:',JSON.stringify(await field.inputValue()));
  await capture('planning-refresh-race');
  if(!baseline){
    assert.equal(await field.inputValue(),'Keep these newer acceptance criteria');
    assert.deepEqual(await field.evaluate(el=>[document.activeElement===el,el.selectionStart,el.selectionEnd]),[true,5,10]);
  }
  save.release();await saved();await page.unrouteAll({behavior:'wait'});
  if(!baseline)assert.equal((await savedDraft()).acceptance,'Keep these newer acceptance criteria');
  assert.deepEqual(errors,[]);
  console.log('PASS planning response races: newer selection, clearing, draft, focus/caret and persisted values');
})().catch(error=>{console.error(error);process.exitCode=1;}).finally(async()=>{
  if(page)await page.unrouteAll({behavior:'ignoreErrors'});await browser?.close();
  if(server?.exitCode===null){const done=new Promise(resolve=>server.once('exit',resolve));server.kill('SIGINT');await done;}
});
