// Native production editing/dismissal audit. Baseline records defects without
// enforcing fixes; all writes use a fresh explicitly bound disposable database.
// PLAYWRIGHT_BROWSER=webkit selects WebKit. PELLETS_EDITING_CASE=headers runs
// all clean/dirty header consumers; details skips the theme/width popup matrix.
// PELLETS_EDITING_BASELINE=/path/to/pl records before images in
// PELLETS_BROWSER_ARTIFACTS (the default artifact directory is temporary).
const assert = require('node:assert/strict');
const {createHash} = require('node:crypto');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-editing-'));
const fixture = path.join(temporary, 'editing');
const baseline = process.env.PELLETS_EDITING_BASELINE;
const binary = baseline || path.join(temporary, 'pl');
const engine = process.env.PLAYWRIGHT_BROWSER || 'chromium';
const artifacts = process.env.PELLETS_BROWSER_ARTIFACTS || path.join(temporary, 'screenshots');
const phase = baseline ? 'before' : 'after', observations = [], captures = [];
let server, browser, page;
const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd:fixture,encoding:'utf8'})).data;
const check = (value, message) => { observations.push({message, passed:!!value}); if (!baseline) assert.ok(value, message); };
async function settle() { await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))); }
async function capture(name, clip) {
  const directory = path.join(artifacts, engine + '-' + name); fs.mkdirSync(directory, {recursive:true});
  await settle();
  await page.screenshot({path:path.join(directory, phase+'.png'),clip,animations:'disabled',caret:'hide'});
  captures.push({name:engine+'-'+name,clip,viewport:page.viewportSize(),scale:1});
}
async function backdrop(selector) {
  const box = await page.locator(selector).boundingBox();
  await page.mouse.click(Math.max(1,box.x/2), Math.max(1,box.y/2));
  await settle();
}
async function refresh() {
  const n=await page.evaluate(()=>window.refreshes||0);
  await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));
  await page.waitForFunction(n=>(window.refreshes||0)>n,n);
}
(async()=>{
  if (!baseline) execFileSync('go',['build','-o',binary,'./cmd/pl'],{cwd:repository});
  fs.mkdirSync(fixture); execFileSync('git',['init','-q'],{cwd:fixture});
  for (const args of [['--help'],['init-db','--help'],['add','--help'],['memory','--help'],['group','--help']]) execFileSync(binary,args,{cwd:fixture});
  cli('init-db');
  const record = cli('add','Editable record','--description','# Reading mode\n\n```mermaid\nflowchart LR\n A[Edit] --> B[Review]\n```','--group','Editing');
  const memory = cli('memory','add','--text','Original memory','--created-by','agent');
  const bindingFile=path.join(fixture,'.git','pellets-database.json');
  const binding=JSON.parse(fs.readFileSync(bindingFile,'utf8'));
  assert.ok(fs.realpathSync(path.resolve(path.dirname(bindingFile),binding.path)).startsWith(fs.realpathSync(fixture)+path.sep));
  server=spawn(binary,['server','--port','0','--no-open'],{cwd:fixture});
  const origin=await new Promise((resolve,reject)=>{let out='',err='';server.stdout.on('data',d=>{out+=d;if(out.includes('\n'))resolve(out.split('\n')[0].trim());});server.stderr.on('data',d=>err+=d);server.once('error',reject);server.once('exit',code=>reject(Error(`Server ${code}: ${err}`)));});
  browser=await (engine==='webkit'?webkit:chromium).launch({headless:true});
  page=await browser.newPage({viewport:{width:1280,height:900},deviceScaleFactor:1,reducedMotion:'reduce'});
  page.setDefaultTimeout(12000);
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.addInitScript(()=>document.addEventListener('datastar-fetch',e=>{if(e.detail.type==='finished'&&e.detail.el===document.body)window.refreshes=(window.refreshes||0)+1;}));
  const url=origin+'/projects/'+record.project+'/tasks?workspace=1';
  await page.goto(url);
  if(process.env.PELLETS_EDITING_CASE==='headers') {
    for(const kind of ['pellet','memory','group']) for(const theme of ['gruvbox-light','gruvbox-dark','light','dark','icy']) for(const width of [1280,1092,390]) {
      await page.setViewportSize({width,height:900});await page.evaluate(theme=>window.Workbench.applyTheme(theme),theme);
      if(kind==='pellet')await page.locator('.task-title').first().click();
      else if(kind==='memory'){
        await page.locator('.area-tabs a').filter({hasText:'Memories'}).click();await page.locator('.memory-card > a').click();
      }else {
        await page.locator('.area-tabs a').filter({hasText:'Groups'}).click();await page.locator('.group-card a').first().click();
      }
      const dialog=page.locator('#record-dialog'), header=dialog.locator('[data-inspector] > header');
      const field=dialog.locator(kind==='pellet'?'[name=title]':kind==='memory'?'[name=text]':'.group-title-input');
      const geometry=()=>header.evaluate(e=>[e,e.firstElementChild,e.querySelector('.header-actions')].map(n=>n.getBoundingClientRect().toJSON()));
      if(kind==='pellet')await dialog.locator('pl-diagram[data-state=ready]').waitFor();
      await field.waitFor();await settle();
      const clean=await geometry();await field.fill(kind==='group'?'Editing renamed':'Unfinished '+kind);
      await settle();const dirty=await geometry();
      check(JSON.stringify(clean)===JSON.stringify(dirty),`${kind} header, title region and close control stay fixed when dirty at ${theme}/${width}: ${JSON.stringify({clean,dirty})}`);
      const box=await header.boundingBox();
      await capture(`${theme}-${width}-${kind}-header`,{x:Math.max(0,box.x-8),y:Math.max(0,box.y-8),width:Math.min(width,box.width+16),height:Math.min(180,900-box.y)});
      if(kind==='group'){await page.keyboard.press('Escape');await dialog.getByRole('button',{name:'Cancel',exact:true}).click();}
      else {page.once('dialog',d=>d.accept());await dialog.getByRole('button',{name:'Cancel',exact:true}).click();}
      await dialog.waitFor({state:'hidden'});
      await page.locator('.area-tabs a').filter({hasText:'Queue'}).click();
    }
    assert.deepEqual(errors,[]);console.log(`${observations.filter(x=>x.passed).length}/${observations.length} stable-header assertions (${phase})`);return;
  }
  for(const theme of process.env.PELLETS_EDITING_CASE ? [] : ['gruvbox-light','gruvbox-dark','light','dark','icy']) for(const width of [1280,1092,390]) {
    await page.setViewportSize({width,height:900});
    await page.evaluate(theme=>window.Workbench.applyTheme(theme),theme);
    const create=page.locator('.create-popover'), title=create.locator('[name=title]');
    await create.locator('summary').click();
    await title.fill('Unfinished creation'); await title.focus();
    await title.evaluate(e=>e.setSelectionRange(2,7));
    await page.keyboard.press('Escape');
    check(!await create.evaluate(e=>e.open),'Creation Escape collapses without discarding');
    await capture(`${theme}-${width}-creation-escape`,{x:0,y:35,width,height:Math.min(710,865)});
    if (!await create.evaluate(e=>e.open)) await create.locator('summary').click();
    assert.equal(await title.inputValue(),'Unfinished creation');
    await page.mouse.click(2,2);await settle();
    check(!await create.evaluate(e=>e.open),'Creation outside click collapses without discarding');
    if(await create.evaluate(e=>e.open)) await create.locator('summary').click();
    const settings=page.locator('#project-record');
    await settings.locator('summary').click();await settings.locator('summary').focus();
    await page.keyboard.press('Escape');
    check(!await settings.evaluate(e=>e.open),'Settings Escape closes the overlay');
    await capture(`${theme}-${width}-settings-escape`,{x:Math.max(0,width-570),y:0,width:Math.min(width,570),height:280});
    if(!await settings.evaluate(e=>e.open))await settings.locator('summary').click();
    await page.mouse.click(2,850);await settle();
    check(!await settings.evaluate(e=>e.open),'Settings outside click closes the overlay');
    if(await settings.evaluate(e=>e.open))await settings.locator('summary').click();
  }
  await page.setViewportSize({width:1280,height:900});await page.evaluate(()=>window.Workbench.applyTheme('gruvbox-light'));
  // Record title is directly editable; modes and dirty guards remain intentional.
  if(!baseline){
    const settings=page.locator('#project-record');
    await settings.locator('summary').click();
    const enabled=settings.locator('[name=enabled]');await enabled.uncheck();
    const version=await settings.locator('[name=version]').inputValue();
    await page.keyboard.press('Escape');await refresh();
    await settings.locator('summary').click();
    assert.equal(await enabled.isChecked(),false,'Collapsed settings retain unsaved routing through live updates');
    assert.equal(await settings.locator('[name=version]').inputValue(),version);
    await enabled.check();await settings.locator('summary').click();
    // Memory creation uses the same disclosure behavior and retains required validation.
    await page.locator('.area-tabs a').filter({hasText:'Memories'}).click();
    const creation=page.locator('.create-popover');await creation.locator('summary').click();
    await creation.locator('[name=text]').fill('Unfinished new memory');
    await page.keyboard.press('Escape');assert.equal(await creation.evaluate(e=>e.open),false);
    await creation.locator('summary').click();assert.equal(await creation.locator('[name=text]').inputValue(),'Unfinished new memory');
    await creation.locator('[name=text]').fill('');await creation.locator('button[type=submit]').click();
    assert.equal(await creation.locator('[name=text]').evaluate(e=>e===document.activeElement&&!e.validity.valid),true);
    await page.keyboard.press('Escape');await page.locator('.area-tabs a').filter({hasText:'Queue'}).click();
  }
  const row=page.locator('.pellet-row').first();
  await row.locator('.row-menu > summary').click();await row.locator('[data-insert-checkpoint=after]').click();
  const insertion=page.locator('#insert-dialog');await insertion.locator('[name=title]').fill('Unfinished review title');
  page.once('dialog',d=>d.dismiss());await page.keyboard.press('Escape');
  page.removeAllListeners('dialog');
  check(await insertion.evaluate(e=>e.open),'Insertion Escape guards an unfinished title and scope');
  await capture('insertion-draft',{x:250,y:100,width:780,height:700});
  if(await insertion.evaluate(e=>e.open)){page.once('dialog',d=>d.accept());await insertion.getByRole('button',{name:'Cancel',exact:true}).click();}
  const dialog=page.locator('#record-dialog');
  await page.locator('.task-title').first().click();await dialog.waitFor();
  const title=dialog.locator('[name=title]');
  assert.equal(await title.isEditable(),true);
  assert.equal(await dialog.locator('.markdown-body').isVisible(),true);
  await title.fill('Unsaved record title');
  await title.evaluate(e=>e.setSelectionRange(2,7));
  page.once('dialog',d=>d.dismiss()); await backdrop('#record-dialog');
  assert.equal(await dialog.evaluate(e=>e.open),true);
  await refresh();assert.equal(await title.inputValue(),'Unsaved record title');
  assert.deepEqual(await title.evaluate(e=>[e.selectionStart,e.selectionEnd]),[2,7]);
  const diagram=dialog.locator('.mermaid-canvas').first();await diagram.waitFor();await diagram.click();
  await page.locator('.diagram-viewer[open]').waitFor();
  await backdrop('.diagram-viewer');
  check(!await page.locator('.diagram-viewer[open]').count(),'Diagram backdrop dismisses only the nested viewer');
  await capture('diagram-backdrop',{x:100,y:20,width:1080,height:860});
  if(await page.locator('.diagram-viewer[open]').count())await page.keyboard.press('Escape');
  assert.equal(await dialog.evaluate(e=>e.open),true);
  assert.equal(await title.inputValue(),'Unsaved record title');
  assert.equal(await diagram.evaluate(e=>e===document.activeElement),true);
  page.once('dialog',d=>d.accept());await dialog.getByRole('button',{name:'Cancel',exact:true}).click();await dialog.waitFor({state:'hidden'});
  // Memory edits must survive optimistic conflicts in the editable field.
  await page.locator('.area-tabs a').filter({hasText:'Memories'}).click();
  await page.locator('.memory-card > a').click();
  const text=dialog.locator('[name=text]');await text.fill('My unfinished memory');
  const form=await text.evaluate(e=>({action:e.form.action,data:Object.fromEntries(new FormData(e.form))}));
  const concurrent=await page.request.post(form.action,{headers:{Origin:origin},form:{...form.data,text:'Concurrent saved memory'}});
  assert.equal(concurrent.status(),200);
  await dialog.getByRole('button',{name:'Save text',exact:true}).click();
  await dialog.locator('.conflict-state').waitFor();
  check(await text.inputValue()==='My unfinished memory','Memory conflict retains an editable draft');
  check(await dialog.locator('[data-inspector]').evaluate(e=>e.classList.contains('is-dirty')),'Memory conflict retains the discard guard');
  await capture('memory-conflict',{x:250,y:390,width:780,height:490});
  if(!baseline){
    page.once('dialog',d=>d.dismiss());await page.keyboard.press('Escape');assert.equal(await dialog.evaluate(e=>e.open),true);
    await dialog.getByRole('button',{name:'Save text',exact:true}).click();
    await page.waitForFunction(()=>!document.querySelector('.conflict-state'));
    assert.equal(cli('memory','show',String(memory.id)).text,'My unfinished memory');
  }
  await dialog.getByRole('button',{name:'Cancel',exact:true}).click();await dialog.waitFor({state:'hidden'});
  // A pending new-chat preference must not hide or replace the current chat.
  await page.locator('.area-tabs a').filter({hasText:'Queue'}).click();
  await page.locator('#plan-tab').click();await page.locator('#plan-message').fill('Keep this chat');
  await page.evaluate(()=>window.Planner.flush());
  await page.locator('[data-plan=new]').click();await page.locator('#plan-new-dialog[open]').waitFor();
  await page.locator('#plan-skip-new-confirmation').check();
  let releaseSetting, settingStarted;
  const started=new Promise(r=>settingStarted=r), release=new Promise(r=>releaseSetting=r);
  await page.route('**/settings',async route=>{
    if(route.request().method()==='POST'&&route.request().postDataJSON()?.key==='skip_new_chat_confirmation'){
      settingStarted();await release;await route.fulfill({status:503,contentType:'application/json',body:JSON.stringify({error:'Fixture preference unavailable'})});
    }else await route.continue();
  });
  await page.locator('[data-plan=confirm-new]').click();await started;
  await refresh();
  check(await page.locator('[data-plan=confirm-new]').isDisabled(),'Pending new-chat confirmation stays disabled through live refresh');
  await backdrop('#plan-new-dialog');
  check(await page.locator('#plan-new-dialog').evaluate(e=>e.open),'Pending new-chat confirmation rejects backdrop dismissal');
  await capture('new-chat-pending',{x:250,y:220,width:780,height:460});
  releaseSetting();await page.waitForFunction(()=>!document.querySelector('[data-plan=cancel-new]').disabled);
  await page.unroute('**/settings');
  if(!await page.locator('#plan-new-dialog').evaluate(e=>e.open))await page.locator('[data-plan=new]').click();
  await page.keyboard.press('Escape');await page.locator('#plan-new-dialog').waitFor({state:'hidden'});
  assert.equal(await page.locator('#plan-message').inputValue(),'Keep this chat');
  if(!baseline){
    await page.locator('#execution-tab').click();
    await page.locator('.area-tabs a').filter({hasText:'Queue'}).click();
    const row=page.locator('.pellet-row').first();
    await row.locator('.row-menu > summary').click();await row.locator('[data-insert-checkpoint=after]').click();
    const insertion=page.locator('#insert-dialog');
    await insertion.locator('[name=title]').fill('Pending insertion');
    const action=await insertion.locator('form').getAttribute('action');
    let allowSave;
    const allowed=new Promise(r=>allowSave=r);
    const matchesAction=url=>url.pathname===new URL(action,origin).pathname;
    const saving=page.waitForRequest(r=>r.method()==='POST'&&matchesAction(new URL(r.url())));
    await page.route(matchesAction,async route=>{await allowed;await route.continue();});
    await insertion.locator('button[type=submit]').click();await saving;
    await page.keyboard.press('Escape');await backdrop('#insert-dialog');
    await insertion.getByRole('button',{name:'Cancel',exact:true}).click();
    assert.equal(await insertion.evaluate(e=>e.open),true,'Pending insertion rejects all dismissal paths');
    assert.equal(await insertion.locator('[name=title]').inputValue(),'Pending insertion');
    allowSave();await insertion.waitFor({state:'hidden'});await page.unroute(matchesAction);
    assert.equal(cli('list').filter(p=>p.title==='Pending insertion').length,1,'Explicit submission creates exactly one checkpoint');
  }
  assert.deepEqual(errors,[]);
  console.log(`${observations.filter(x=>x.passed).length}/${observations.length} editing assertions passed (${phase})`);
})().catch(error=>{console.error(error);process.exitCode=1;}).finally(async()=>{
  fs.mkdirSync(artifacts,{recursive:true});fs.writeFileSync(path.join(artifacts,engine+'-'+phase+(process.env.PELLETS_EDITING_CASE ? '-'+process.env.PELLETS_EDITING_CASE : '')+'.json'),JSON.stringify({source:{head:execFileSync('git',['rev-parse','HEAD'],{cwd:repository,encoding:'utf8'}).trim(),baseline:!!baseline,binarySHA256:fs.existsSync(binary)?createHash('sha256').update(fs.readFileSync(binary)).digest('hex'):null,uncommitted:baseline?null:execFileSync('git',['diff','--stat'],{cwd:repository,encoding:'utf8'})},observations,captures},null,2));
  await browser?.close(); if(server && server.exitCode===null){const stopped=new Promise(r=>server.once('exit',r));server.kill('SIGINT');await stopped;}
  console.log(`Editing audit ${engine} ${phase}: ${artifacts}`);
});
