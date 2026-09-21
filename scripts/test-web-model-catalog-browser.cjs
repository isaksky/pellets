// Model menu latency and live reconciliation, independent of runtime/network latency.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path');
const {execFileSync,spawn}=require('node:child_process');const {chromium,webkit}=require('playwright');
const root=path.resolve(__dirname,'..'),tmp=fs.mkdtempSync(path.join(os.tmpdir(),'pellets-model-catalog-')),binary=path.join(tmp,'pl'),fixture=path.join(tmp,'fixture');
let server,browser,page;
(async()=>{
 execFileSync('go',['build','-o',binary,'./cmd/pl'],{cwd:root});fs.mkdirSync(fixture);execFileSync('git',['init','-q'],{cwd:fixture});
 const cli=(...args)=>JSON.parse(execFileSync(binary,['--json',...args],{cwd:fixture,encoding:'utf8'})).data;
 cli('add','Fixture');
 server=spawn(binary,['server','--no-open'],{cwd:fixture,env:{...process.env,PELLETS_CODEX_EXECUTABLE:path.join(tmp,'unavailable')}});
 const origin=await new Promise((resolve,reject)=>{let out='';server.stdout.on('data',d=>{out+=d;if(out.includes('\n'))resolve(out.trim())});server.on('error',reject)});
 browser=await(process.env.PLAYWRIGHT_BROWSER==='webkit'?webkit:chromium).launch({headless:true});
 page=await browser.newPage({viewport:{width:1280,height:850}});const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.addInitScript(()=>{window.EventSource=undefined});
 let snapshot={models:[],refreshing:true,stale:true,fetched_at:0},reads=0,refreshes=0,holdRead=null;
 await page.route('**/models',async route=>{reads++;const captured=snapshot;const hold=holdRead;holdRead=null;if(hold)await hold;await route.fulfill({json:captured})});
 await page.route('**/models/refresh',async route=>{refreshes++;await route.fulfill({status:202,json:{refreshing:true}})});
 await page.goto(origin);await page.locator('#plan-tab').click();await page.locator('#plan-model-trigger').waitFor();
 await page.waitForFunction(()=>document.querySelector('#plan-model')?.dataset.menuStatus?.includes('Loading'));
 // Measure actual synchronous DOM response, excluding automation round-trip latency.
 const elapsed=await page.evaluate(()=>{const t=performance.now();document.querySelector('#plan-model-trigger').click();if(!document.querySelector('#plan-model-listbox'))throw Error('Menu did not open synchronously');return performance.now()-t});
 assert.ok(elapsed<100,`Menu took ${elapsed}ms`);await page.getByRole('status').filter({hasText:'Loading available models'}).waitFor();
 const before=reads;await page.waitForTimeout(1200);assert.equal(reads,before,'No 500ms polling');
 await page.evaluate(()=>{window.originalMenu=document.querySelector('#plan-model-popover');window.originalFocus=document.activeElement;});
 snapshot={models:[{id:'fast',name:'Fast Model',efforts:['medium','high']}],refreshing:false,stale:false,fetched_at:100};
 await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));
 await page.getByRole('option',{name:'Fast Model',exact:true}).waitFor();
 assert.equal(await page.evaluate(()=>document.querySelector('#plan-model-popover')===window.originalMenu&&document.activeElement===window.originalFocus),true,'Update must retain menu and focus');
 await page.getByRole('option',{name:'Fast Model',exact:true}).click();
 await page.locator('#plan-effort-trigger').click();await page.getByRole('option',{name:'high',exact:true}).click();
 await page.locator('#plan-model-trigger').click();await page.keyboard.press('Tab');
 assert.equal(await page.evaluate(()=>document.activeElement.textContent),'Refresh models');
 await page.keyboard.press('Enter');await page.waitForFunction(()=>document.querySelector('#plan-model')?.dataset.menuBusy==='true');assert.equal(refreshes,1);
 snapshot={...snapshot,stale:true,error:'Refresh failed. Try again.'};
 await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));await page.getByRole('button',{name:'Retry refresh'}).waitFor();
 assert.equal(await page.locator('#plan-model').inputValue(),'fast');
 await page.getByRole('button',{name:'Retry refresh'}).click();
 await page.waitForFunction(()=>document.querySelector('#plan-model').dataset.menuBusy==='true');assert.equal(refreshes,2);
 assert.equal(await page.evaluate(()=>document.activeElement?.textContent),'Refresh models');
 await page.keyboard.press('Escape');
 await page.locator('#plan-model-popover').waitFor({state:'detached'});
 // An invalidation during an outstanding read must not lose the final state.
 let release;holdRead=new Promise(resolve=>{release=resolve});
 const readBefore=reads;
 await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));
 while(reads===readBefore)await new Promise(resolve=>setTimeout(resolve,20));
 snapshot={...snapshot,error:'',stale:false,refreshing:false};
 await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));
 release();
 await page.waitForFunction(()=>document.querySelector('#plan-model').dataset.menuStatus==='');
 // Keep a selected model/effort even when the refreshed catalog omits it.
 snapshot={models:[{id:'new',name:'New Model',efforts:['low']}],refreshing:false,stale:false,fetched_at:100};
 await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));
 await page.waitForFunction(()=>document.querySelector('#plan-model option[value=new]'));
 assert.equal(await page.locator('#plan-model').inputValue(),'fast');
 assert.equal(await page.locator('#plan-effort').inputValue(),'high');
 snapshot={models:[{id:'fast',name:'Fast Model',efforts:['medium','high']},{id:'new',name:'New Model',efforts:['low']}],refreshing:false,stale:false,fetched_at:100};
 await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));
 for(const theme of ['light','dark','icy','gruvbox-light','gruvbox-dark']){
  await page.evaluate(theme=>window.Workbench.applyTheme(theme),theme);
  for(const width of [1280,800,390]){
   await page.setViewportSize({width,height:850});
   for(const id of ['plan-model','plan-effort']){
    await page.locator('#'+id+'-trigger').click();const menu=page.locator('#'+id+'-popover');await menu.waitFor();const box=await menu.boundingBox();assert.ok(box.x>=0&&box.x+box.width<=width+1&&box.y>=0&&box.y+box.height<=850);
    await page.screenshot({path:path.join(tmp,`${theme}-${width}-${id}.png`)});await page.keyboard.press('Escape');
   }
  }
 }
 assert.deepEqual(errors,[]);console.log(`PASS ${process.env.PLAYWRIGHT_BROWSER||'chromium'} click-to-menu ${elapsed.toFixed(2)}ms; artifacts ${tmp}`);
})().catch(async e=>{console.error(e);console.error('Artifacts:',tmp);if(page){await page.screenshot({path:path.join(tmp,'failure.png')});console.error(await page.locator('body').innerText());}process.exitCode=1}).finally(async()=>{await browser?.close();if(server&&server.exitCode===null){const done=new Promise(r=>server.once('exit',r));server.kill('SIGINT');await done;}});
