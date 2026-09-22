// Real disposable-server preference forms, persistence, refresh, keyboard and layout.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path');
const {execFileSync,spawn}=require('node:child_process');const {chromium,webkit}=require('playwright');
const root=path.resolve(__dirname,'..'),tmp=fs.mkdtempSync(path.join(os.tmpdir(),'pellets-preferences-')),binary=path.join(tmp,'pl'),fixture=path.join(tmp,'fixture');
let server,browser,page;
async function until(fn){for(let i=0;i<100;i++){if(await fn())return;await new Promise(r=>setTimeout(r,100));}throw Error('condition timed out');}
(async()=>{
 execFileSync('go',['build','-o',binary,'./cmd/pl'],{cwd:root});fs.mkdirSync(fixture);execFileSync('git',['init','-q'],{cwd:fixture});
 const cli=(...args)=>JSON.parse(execFileSync(binary,['--json',...args],{cwd:fixture,encoding:'utf8'})).data;
 const original=cli('add','Fixture','--model','unlisted-model','--reasoning-effort','custom-effort');
 server=spawn(binary,['server','--no-open'],{cwd:fixture,env:{...process.env,PELLETS_CODEX_EXECUTABLE:path.join(tmp,'unavailable')}});
 const origin=await new Promise((resolve,reject)=>{let out='';server.stdout.on('data',d=>{out+=d;if(out.includes('\n'))resolve(out.trim())});server.on('error',reject)});
 const engine=process.env.PLAYWRIGHT_BROWSER==='webkit'?webkit:chromium;
 browser=await engine.launch({headless:true});page=await browser.newPage({viewport:{width:1280,height:900}});page.setDefaultTimeout(15000);
 await page.addInitScript(()=>{window.EventSource=undefined});
 const errors=[];page.on('pageerror',e=>errors.push(e.message));
 let snapshot={models:[{id:'model-a',name:'Model A',efforts:['medium','high']}],refreshing:false,stale:false,fetched_at:100};
 await page.route('**/models',route=>route.fulfill({json:snapshot}));
 await page.route('**/models/refresh',route=>route.fulfill({status:202,json:{refreshing:true}}));
 const tasks=origin+'/projects/'+original.project+'/tasks';await page.goto(tasks);
 await page.locator('.create-popover > summary').click();const create=page.locator('.create-popover form');
 await until(()=>create.locator('[name=model] option[value="model-a"]').count());
 const modelButton=create.getByRole('combobox',{name:'Execution model',exact:true});await modelButton.click();
 await page.getByRole('option',{name:'Model A',exact:true}).click();await create.getByRole('combobox',{name:'Reasoning effort',exact:true}).click();await page.getByRole('option',{name:'high',exact:true}).click();
 await create.locator('[name=title]').fill('Browser preference');await create.getByRole('button',{name:'Create pellet',exact:true}).click();
 await until(()=>cli('list').some(p=>p.title==='Browser preference'));const created=cli('list').find(p=>p.title==='Browser preference');assert.equal(created.model,'model-a');assert.equal(created.reasoning_effort,'high');
 await page.goto(tasks+'/'+created.id);const edit=page.locator('form.record-edit');await edit.waitFor();
 await until(()=>edit.locator('[name=model]').inputValue().then(v=>v==='model-a'));
 await edit.locator('[name=model]').selectOption('');await page.getByRole('button',{name:'Save changes',exact:true}).click();await until(()=>!cli('show',created.id).model);
 assert.equal(cli('show',created.id).reasoning_effort,'high');
 await page.goto(tasks+'/'+original.id);await edit.waitFor();assert.equal(await edit.locator('[name=model]').inputValue(),'unlisted-model');assert.equal(await edit.locator('[name=reasoning_effort]').inputValue(),'custom-effort');
 await until(()=>edit.locator('[name=model] option[value="model-a"]').count());
 snapshot={...snapshot,models:[],error:'Catalog unavailable',stale:true};await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));
 await until(()=>edit.locator('[name=model]').getAttribute('data-menu-status').then(v=>v==='Catalog unavailable'));
 assert.equal(await edit.locator('[name=model]').inputValue(),'unlisted-model');assert.equal(await edit.locator('[name=reasoning_effort]').inputValue(),'custom-effort');
 await edit.getByRole('combobox',{name:'Execution model',exact:true}).click();await page.keyboard.press('Escape');assert.equal(await edit.locator('[name=model]').inputValue(),'unlisted-model');
 snapshot={...snapshot,models:[{id:'model-a',name:'Model A',efforts:['medium','high']}],error:'',stale:false};
 for(const theme of ['light','dark','gruvbox-light','gruvbox-dark','icy']){
  for(const width of [1280,1092,390]){
   await page.goto(tasks+'/'+original.id);await page.evaluate(theme=>window.Workbench.applyTheme(theme),theme);await page.setViewportSize({width,height:900});await edit.waitFor();
   for(const label of ['Execution model','Reasoning effort']){
    const button=edit.getByRole('combobox',{name:label,exact:true});await button.click();const box=await button.boundingBox();assert.ok(box.x>=0&&box.x+box.width<=width+1,`${theme} ${width} ${label} clipped`);
    const menu=page.locator('.select-popover:visible');const b=await menu.boundingBox();assert.ok(b.x>=0&&b.x+b.width<=width+1);await page.screenshot({path:path.join(tmp,`${theme}-${width}-${label.replaceAll(' ','-')}.png`)});await page.keyboard.press('Escape');
   }
   await page.goto(tasks);await page.evaluate(theme=>window.Workbench.applyTheme(theme),theme);await page.locator('.create-popover > summary').click();await create.getByRole('combobox',{name:'Execution model',exact:true}).click();await page.keyboard.press('Escape');await page.screenshot({path:path.join(tmp,`${theme}-${width}-create.png`)});
   assert.equal(await create.evaluate(el=>el.scrollWidth<=el.clientWidth+1),true,'creation form overflows');
  }
 }
 await page.setViewportSize({width:1280,height:900});await page.goto(tasks);
 await page.locator('#row-menu-'+original.id+' > summary').click();
 await page.locator('#task-'+original.id+' [data-checkpoint-select]').check();await page.keyboard.press('Escape');const checkpoint=page.locator('[data-checkpoint-form]');await until(()=>checkpoint.locator('[name=model] option[value="model-a"]').count());
 await checkpoint.locator('[name=model]').selectOption('model-a');await checkpoint.locator('[name=reasoning_effort]').selectOption('medium');
 await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));await until(()=>checkpoint.locator('[name=model]').inputValue().then(v=>v==='model-a'));
 await checkpoint.getByRole('button',{name:'Insert review checkpoint',exact:true}).click();await until(()=>cli('list').some(p=>p.kind==='review_checkpoint'));
 const review=cli('list').find(p=>p.kind==='review_checkpoint');assert.equal(review.model,'model-a');assert.equal(review.reasoning_effort,'medium');
 assert.deepEqual(errors,[]);console.log(`PASS ${process.env.PLAYWRIGHT_BROWSER||'chromium'} execution preferences; artifacts ${tmp}`);
})().catch(async e=>{console.error(e);console.error('Artifacts:',tmp);if(page){await page.screenshot({path:path.join(tmp,'failure.png')});console.error(await page.locator('[data-execution-preference]').evaluateAll(nodes=>nodes.map(n=>({value:n.value,data:{...n.dataset}}))));console.error(await page.locator('body').innerText());}process.exitCode=1}).finally(async()=>{await browser?.close();if(server&&server.exitCode===null){const done=new Promise(r=>server.once('exit',r));server.kill('SIGINT');await done;}});
