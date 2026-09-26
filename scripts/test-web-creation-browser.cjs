// Ordinary creation journey and recovery, on explicitly isolated disposable data.
// Run in Chromium and WebKit; screenshots include all themes and narrow panes.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const root = path.resolve(__dirname, '..');
const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-creation-'));
const fixture = path.join(temp, 'fixture'), binary = path.join(temp, 'pl');
const engine = process.env.PLAYWRIGHT_BROWSER || 'chromium';
const artifacts = process.env.PELLETS_BROWSER_ARTIFACTS || path.join(temp, 'screenshots');
let server, browser, page;
const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd:fixture, encoding:'utf8'})).data;
async function until(fn, message) {
  for(let i=0;i<150;i++) { if(await fn()) return; await new Promise(resolve=>setTimeout(resolve,50)); }
  throw Error(message || 'condition timed out');
}
async function reachable(locator) {
  return locator.evaluate(el => {
    const r=el.getBoundingClientRect();
    return r.width>0 && r.height>0 && r.top>=0 && r.bottom<=innerHeight &&
      el.contains(document.elementFromPoint(r.x+r.width/2,r.y+r.height/2));
  });
}
(async()=>{
  execFileSync('go',['build','-o',binary,'./cmd/pl'],{cwd:root});
  fs.mkdirSync(fixture); fs.mkdirSync(artifacts,{recursive:true});
  execFileSync('git',['init','-q'],{cwd:fixture});cli('init-db');cli('list');
  const project=cli('project','show');
  server=spawn(binary,['server','--port','0','--no-open'],{cwd:fixture,env:{...process.env,PELLETS_CODEX_EXECUTABLE:path.join(temp,'unavailable')}});
  const origin=await new Promise((resolve,reject)=>{
    let out='';server.stdout.on('data',d=>{out+=d;if(out.includes('\n'))resolve(out.split('\n')[0].trim());});server.once('error',reject);
  });
  browser=await (engine==='webkit'?webkit:chromium).launch({headless:true,...(engine==='webkit'&&process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE?{executablePath:process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE}:{})});
  page=await browser.newPage({viewport:{width:1280,height:720}});page.setDefaultTimeout(10000);
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.route('**/models',route=>route.fulfill({json:{models:[{id:'model-a',name:'Model A',efforts:['medium','high']}],refreshing:false,stale:false}}));
  const tasks=origin+'/projects/'+project.code+'/tasks';
  await page.goto(tasks);
  const disclosure=page.locator('.pellet-create'), opener=disclosure.locator(':scope > summary');
  const form=disclosure.locator('form'), title=form.locator('[name=title]'), description=form.locator('textarea');
  const options=form.locator('.pellet-create-options'), submit=form.getByRole('button',{name:'Create pellet',exact:true});
  const open=async()=>{await opener.click();await until(()=>title.evaluate(el=>document.activeElement===el),'opening must focus Title');};
  // Record transient feedback at response time, rather than racing a screenshot.
  await page.evaluate(()=>{
    window.creationReceipts=[];
    document.addEventListener('datastar-fetch',event=>{
      if(event.detail.type!=='datastar-patch-signals')return;
      const result=JSON.parse(event.detail.argsRaw.signals)._webResult;
      if(!result?.createdPellet)return;
      queueMicrotask(()=>{const row=document.getElementById('task-'+result.createdPellet);
        window.creationReceipts.push({reference:result.createdPellet,highlight:row?.classList.contains('pellet-created'),animation:row&&getComputedStyle(row).animationName,dialog:document.getElementById('record-dialog').open});
      });
    });
  });
  await open();
  await submit.click();assert.equal(await title.evaluate(el=>!el.validity.valid && document.activeElement===el),true);
  await title.fill('First browser pellet');await submit.click();
  await until(()=>disclosure.evaluate(el=>!el.open),'create must close');
  assert.equal(await page.locator('#record-dialog').evaluate(el=>el.open),false);
  await until(()=>opener.evaluate(el=>document.activeElement===el),'create must return focus');
  const first=cli('list')[0];assert.equal(first.title,'First browser pellet');
  assert.match(page.url(),/\/tasks(?:\?|$)/);
  const receipt=await page.evaluate(()=>window.creationReceipts[0]);
  assert.equal(receipt.reference,first.id);assert.equal(receipt.highlight,true);assert.equal(receipt.animation,'pellet-created');assert.equal(receipt.dialog,false);
  await open();assert.equal(await title.inputValue(),'');assert.equal(await description.inputValue(),'');
  assert.equal(await options.evaluate(el=>el.open),false);assert.equal(await form.locator('[data-description-mode=edit]').getAttribute('aria-pressed'),'true');
  const markdown='## Problem\nA useful description needs room to write.\n\n## Expected behavior\n'+Array.from({length:12},(_,i)=>'- Requirement '+(i+1)).join('\n');
  await title.fill('Second browser pellet');await description.fill(markdown);
  await options.locator('summary').click();await form.locator('[name=group]').fill('Creation audit');await form.locator('[name=external_id]').fill('audit:two');
  await form.getByRole('combobox',{name:'Execution model',exact:true}).click();await page.getByRole('option',{name:'Model A',exact:true}).click();
  await form.getByRole('combobox',{name:'Reasoning effort',exact:true}).click();await page.getByRole('option',{name:'high',exact:true}).click();
  await page.keyboard.press('Escape');assert.equal(await options.evaluate(el=>el.open),false);assert.equal(await disclosure.evaluate(el=>el.open),true);
  await page.keyboard.press('Escape');assert.equal(await disclosure.evaluate(el=>el.open),false);
  await open();assert.equal(await title.inputValue(),'Second browser pellet');assert.equal(await description.inputValue(),markdown);
  // Drafts survive live updates, including collapsed optional values.
  await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));
  await until(()=>title.evaluate(el=>document.activeElement===el));
  assert.equal(await form.locator('[name=group]').inputValue(),'Creation audit');
  await form.locator('[data-description-mode=view]').click();assert.equal(await reachable(submit),true);
  await submit.click();await until(()=>disclosure.evaluate(el=>!el.open));
  const second=cli('list')[1];assert.equal(second.title,'Second browser pellet');assert.equal(second.description,markdown);assert.equal(second.group,'Creation audit');assert.equal(second.external_id,'audit:two');assert.equal(second.model,'model-a');assert.equal(second.reasoning_effort,'high');
  await open();for(const name of ['title','description','group','external_id','model','reasoning_effort','request_id'])assert.equal(await form.locator('[name='+name+']').inputValue(),'','stale '+name);
  assert.equal(await form.locator('[name=status]').inputValue(),'open');assert.equal(await form.locator('[data-description-mode=edit]').getAttribute('aria-pressed'),'true');
  // A lost response must preserve both the draft and its idempotency key.
  await title.fill('Retry once');let dropped=false;
  await page.route('**/pellets?*',async route=>{await route.fetch();dropped=true;await route.abort('failed');},{times:1});
  await submit.click();await until(()=>dropped);await page.locator('#request-feedback.request-failed').waitFor();
  assert.equal(await title.inputValue(),'Retry once');const retryKey=await form.locator('[name=request_id]').inputValue();assert.ok(retryKey);
  await submit.click();await until(()=>disclosure.evaluate(el=>!el.open));assert.equal(cli('list').filter(p=>p.title==='Retry once').length,1);
  // Later typing while a save is pending belongs to a new draft.
  await open();await title.fill('Submitted draft');let release,received;
  const receivedPromise=new Promise(resolve=>received=resolve), gate=new Promise(resolve=>release=resolve);
  await page.route('**/pellets?*',async route=>{const response=await route.fetch();received();await gate;await route.fulfill({response});},{times:1});
  await submit.click();await receivedPromise;await title.fill('Newer unsent draft');release();
  await until(()=>form.getAttribute('aria-busy').then(v=>v===null));
  assert.equal(await disclosure.evaluate(el=>el.open),true);assert.equal(await title.inputValue(),'Newer unsent draft');assert.equal(await form.locator('[name=request_id]').inputValue(),'');
  assert.ok(cli('list').some(p=>p.title==='Submitted draft'));assert.ok(!cli('list').some(p=>p.title==='Newer unsent draft'));
  // Creating outside a filter remains in that scope and explains the missing row.
  await page.goto(tasks+'?q=unmatched&sort=title&direction=desc&workspace=1&execution=1');await open();await title.fill('Hidden created pellet');await submit.click();
  await until(()=>disclosure.evaluate(el=>!el.open));assert.match(await page.locator('#pellet-created').innerText(),/Hidden by the current filters/);
  for(const [k,v] of Object.entries({q:'unmatched',sort:'title',direction:'desc',workspace:'1',execution:'1'}))assert.equal(new URL(page.url()).searchParams.get(k),v);
  assert.equal(await page.locator('#record-dialog').evaluate(el=>el.open),false);
  await page.locator('#search').fill('');
  await until(()=>page.locator('#pellet-created').getAttribute('class').then(value=>value.includes('visually-hidden')),'filter notice should follow current row visibility');
  // Five palettes, narrow panes with sidebars, phone, and short-window layouts.
  for(const theme of ['gruvbox-light','gruvbox-dark','light','dark','icy']) {
    for(const [width,height] of [[1280,720],[1092,720],[800,720],[390,844],[1280,500],[390,500]]) {
      await page.setViewportSize({width,height});await page.goto(tasks);await page.evaluate(t=>window.Workbench.applyTheme(t),theme);await open();
      await title.fill('Layout check');await form.locator('[data-description-mode=edit]').click();await description.fill(markdown);
      const before=await submit.boundingBox();await form.locator('[data-description-mode=view]').click();const after=await submit.boundingBox();
      assert.ok(Math.abs(before.y-after.y)<1,'preview moved Create at '+width+' '+height);
      assert.equal(await reachable(submit),true,'submit obscured '+theme+' '+width+' '+height);
      assert.equal(await form.evaluate(el=>el.scrollWidth<=el.clientWidth+1),true,'horizontal form overflow');
      const bounds=await form.boundingBox();assert.ok(bounds.x>=0 && bounds.x+bounds.width<=width+1 && bounds.y+bounds.height<=height,'form outside viewport');
      await options.locator('summary').click();assert.equal(await reachable(submit),true,'options obscured submit');
      await form.getByRole('combobox',{name:'Execution model',exact:true}).click();await page.keyboard.press('Escape');
      assert.equal(await options.evaluate(el=>el.open),true,'nested select Escape collapsed options');
      await page.keyboard.press('Escape');assert.equal(await options.evaluate(el=>el.open),false);
      await page.screenshot({path:path.join(artifacts,`${engine}-${theme}-${width}x${height}.png`)});
      await page.keyboard.press('Escape');await open();assert.equal(await title.inputValue(),'Layout check');
    }
  }
  await page.setViewportSize({width:1280,height:720});await page.emulateMedia({reducedMotion:'reduce'});await page.goto(tasks);await open();await title.fill('Reduced motion creation');await submit.click();
  await until(()=>disclosure.evaluate(el=>!el.open));const last=cli('list').find(p=>p.title==='Reduced motion creation');
  const row=page.locator('#task-'+last.id);assert.equal(await row.evaluate(el=>getComputedStyle(el).animationName),'none');
  assert.equal(await row.evaluate(el=>el.classList.contains('pellet-created')),true);
  assert.deepEqual(errors,[]);console.log(`PASS ${engine}: repeated creation, exact draft reset, focus, first-row flash, recovery, concurrent typing, filters, reduced motion, 30 layouts. Artifacts: ${artifacts}`);
})().catch(async error=>{console.error(error);if(page){console.error(await page.evaluate(()=>({active:document.activeElement.tagName+'#'+document.activeElement.id,receipts:window.creationReceipts})));await page.screenshot({path:path.join(artifacts,engine+'-failure.png')});}process.exitCode=1;}).finally(async()=>{await browser?.close();if(server?.exitCode===null){const done=new Promise(resolve=>server.once('exit',resolve));server.kill('SIGINT');await done;}});
