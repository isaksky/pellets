// Production planning UI + SQLite + deterministic Codex protocol peer.
// NODE_PATH=/path/to/node_modules PLAYWRIGHT_CHANNEL=chrome node scripts/test-web-planning-browser.cjs
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const repository = path.resolve(__dirname, '..'), temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-planning-browser-'));
const binary = path.join(temporary, 'pl'), peer = path.join(temporary, 'codex'), fixture = path.join(temporary, 'planner');
const env = {...process.env, PATH:temporary+path.delimiter+process.env.PATH, PELLETS_CODEX_EXECUTABLE:peer, PELLETS_SUPERVISOR_PEER:'1'};
let browser, server;
const cli = (...args) => JSON.parse(execFileSync(binary,args,{cwd:fixture,env,encoding:'utf8'})).data;
const until = async (predicate,message) => {const end=Date.now()+25000;while(Date.now()<end){if(await predicate())return;await new Promise(r=>setTimeout(r,60));}throw Error(message);};
async function stop(){if(server&&server.exitCode===null){const done=new Promise(r=>server.once('exit',r));server.kill('SIGINT');await done;}}
(async()=>{
  execFileSync('go',['build','-o',binary,'./cmd/pl'],{cwd:repository});
  execFileSync('go',['test','-c','-o',peer,'./internal/app'],{cwd:repository});
  fs.mkdirSync(fixture);
  const init=directory=>{fs.mkdirSync(directory,{recursive:true});execFileSync('git',['init','-q'],{cwd:directory});execFileSync('git',['config','user.name','Test'],{cwd:directory});execFileSync('git',['config','user.email','test@example.invalid'],{cwd:directory});execFileSync('git',['-c','commit.gpgSign=false','commit','--allow-empty','-qm','initial'],{cwd:directory});};
  init(fixture);
  const original=cli('add','Existing queue context','--group','web-ui');
  const otherRoot=path.join(fixture,'other');init(otherRoot);
  const other=JSON.parse(execFileSync(binary,['add','Other project work'],{cwd:otherRoot,env,encoding:'utf8'})).data;
  fs.appendFileSync(path.join(fixture,'.git/info/exclude'),'\n/fake-*\n/other/\n');
  fs.writeFileSync(path.join(fixture,'fake-mode'),'planning_gate');
  server=spawn(binary,['server','--port','0','--no-open'],{cwd:fixture,env});
  const origin=await new Promise((resolve,reject)=>{let out='',err='';server.stdout.on('data',d=>{out+=d;if(out.includes('\n'))resolve(out.split('\n')[0].trim());});server.stderr.on('data',d=>err+=d);server.once('exit',code=>reject(Error('Server '+code+': '+err)));server.once('error',reject);});
  const engine=process.env.PLAYWRIGHT_BROWSER==='webkit'?webkit:chromium;
  browser=await engine.launch({headless:true,...(engine===chromium&&process.env.PLAYWRIGHT_CHANNEL?{channel:process.env.PLAYWRIGHT_CHANNEL}:{}),...(engine===webkit&&process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE?{executablePath:process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE}:{})});
  const page=await browser.newPage({viewport:{width:1280,height:850}});page.setDefaultTimeout(15000);
  const errors=[];page.on('pageerror',error=>errors.push(error.message));
  const posts=[];page.on('request',request=>{if(request.method()==='POST'&&/\/planning$/.test(request.url()))posts.push(request.postDataJSON());});
  await page.goto(origin+'/projects/'+original.project+'/tasks');
  await page.locator('#plan-tab').click();
  await page.getByRole('heading',{name:'What should we work on?'}).waitFor();
  assert.equal(await page.locator('.plan-drafts').isVisible(),false);
  assert.equal(await page.locator('.plan-create-row').isVisible(),false);
  assert.equal(fs.existsSync(path.join(fixture,'fake-events.jsonl')),false,'Opening Plan contacted Codex');
  await page.locator('#plan-model-trigger').click();
  await page.getByRole('option',{name:'Planning Test',exact:true}).click();
  await page.locator('#plan-effort').selectOption('high',{force:true});
  await page.locator('#plan-message').fill('Plan the input and persistence work');
  await until(async()=>await page.getByRole('button',{name:'Send message',exact:true}).isEnabled(),'Composer did not finish autosaving');
  await page.getByRole('button',{name:'Send message',exact:true}).click();
  await until(()=>fs.existsSync(path.join(fixture,'fake-planning-started')),'Actual planner never reached turn/start');
  await page.locator('#plan-message').fill('Next idea kept while planning');
  await page.locator('#plan-message').press('ArrowLeft');
  await page.evaluate(()=>{window.plannerNode=document.getElementById('planning-panel');window.plannerInput=document.getElementById('plan-message');});
  await page.locator('#execution-tab').click();
  assert.equal(await page.locator('#execution').isVisible(),true);
  await page.locator('#plan-tab').click();
  assert.equal(await page.locator('#plan-message').inputValue(),'Next idea kept while planning');
  await page.locator('#theme-select').selectOption('dark',{force:true});
  await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));
  assert.equal(await page.evaluate(()=>window.plannerNode===document.getElementById('planning-panel')&&window.plannerInput===document.getElementById('plan-message')),true,'Live update rebuilt planning inputs');
  fs.writeFileSync(path.join(fixture,'fake-planning-release'),'continue');
  await until(async()=>await page.locator('.plan-card').count()===2,'Reported planner drafts did not appear');
  assert.equal(await page.locator('#plan-message').inputValue(),'Next idea kept while planning','Reply discarded a newer composer draft');
  assert.equal(cli('list').length,1,'Planning reply created queue records automatically');
  const cards=page.locator('.plan-card');
  await cards.nth(0).locator('summary').click();
  await cards.nth(0).locator('[name=title]').fill('Create only this reviewed draft');
  await cards.nth(0).locator('[name=description]').fill('');
  await cards.nth(0).locator('[name=acceptance]').fill('Acceptance-only scope survives immediate creation.');
  await cards.nth(1).locator('[name=selected]').uncheck();
  // Click before autosave can run: Create must persist the latest edits first.
  await page.getByRole('button',{name:'Create 1 pellet',exact:true}).click();
  await until(async()=>await page.locator('.plan-created').count()===1,'Explicit selected creation failed');
  const created=cli('list').find(p=>p.title==='Create only this reviewed draft');
  assert.ok(created,'Immediate Create lost the latest title');
  assert.match(created.description,/Acceptance-only scope/);
  assert.equal(cli('list').length,2,'Creation included an unselected draft');
  const remaining=page.locator('.plan-card').first();
  await remaining.locator('summary').click();
  await remaining.getByRole('button',{name:'Split pellet',exact:true}).click();
  await remaining.locator('[data-split-titles]').fill('First split\nSecond split');
  await remaining.getByRole('button',{name:'Split into drafts',exact:true}).click();
  assert.equal(await page.locator('.plan-card').count(),2);
  await until(async()=>await page.getByRole('button',{name:'Combine selected',exact:true}).isEnabled(),'Split did not save');
  await page.getByRole('button',{name:'Combine selected',exact:true}).click();
  assert.equal(await page.locator('.plan-card').count(),1);
  await page.locator('.plan-card summary').click();
  await page.getByRole('button',{name:'Refine in chat',exact:true}).click();
  await page.locator('#plan-message').fill('Include the important edge cases');
  await until(async()=>await page.getByRole('button',{name:'Send message',exact:true}).isEnabled(),'Refinement did not save');
  await page.getByRole('button',{name:'Send message',exact:true}).click();
  await until(async()=>await page.locator('.plan-card [name=description]').inputValue()==='Refined with the requested edge cases.','Refinement did not update the exact draft');
  assert.equal(await page.locator('.plan-card').count(),1);
  assert.equal(await page.locator('.plan-created').count(),1);
  for(const width of [390,1280]){
    await page.setViewportSize({width,height:850});
    assert.equal(await page.locator('.plan-draft-list').evaluate(el=>el.scrollWidth<=el.clientWidth),true,'Draft rows overflow horizontally');
    assert.equal(await page.locator('[data-plan-new]').evaluate(el=>el.scrollWidth<=el.clientWidth),true,'New chat control is clipped');
  }
  // An unfinished split is local presentation input, not a changed proposal.
  await page.getByRole('button',{name:'Split pellet',exact:true}).click();
  await page.locator('[data-split-titles]').fill('First unfinished split\nSecond unfinished split');
  await page.locator('[data-split-titles]').press('ArrowLeft');
  const splitSelection=await page.locator('[data-split-titles]').evaluate(el=>el.selectionStart);
  await page.reload();
  await page.locator('[data-split-titles]').waitFor();
  assert.equal(await page.locator('[data-split-titles]').inputValue(),'First unfinished split\nSecond unfinished split');
  assert.deepEqual(await page.locator('[data-split-titles]').evaluate(el=>({focused:el===document.activeElement,selection:el.selectionStart})),{focused:true,selection:splitSelection});
  await page.getByRole('button',{name:'Cancel',exact:true}).click();
  // The server commits a real Send, but its response is lost. A reload must
  // retain the original operation and explicitly confirm its receipt.
  let lostSend, committedSend;
  await page.route('**/projects/'+original.project+'/planning',async route=>{
    const request=route.request(),body=request.method()==='POST'?request.postDataJSON():null;
    if(body?.action==='send'&&!lostSend){lostSend=body;committedSend=(await(await route.fetch()).json()).chat;await route.abort('failed');}
    else await route.continue();
  });
  await page.locator('#plan-message').fill('Confirm this reply exactly once');
  await until(async()=>await page.getByRole('button',{name:'Send message',exact:true}).isEnabled(),'Recovery message did not save');
  await page.getByRole('button',{name:'Send message',exact:true}).click();
  await page.getByRole('button',{name:'Retry request',exact:true}).waitFor();
  assert.ok(committedSend,'Lost-response scenario did not commit a real server reply');
  const savedSend=await page.evaluate(()=>JSON.parse(sessionStorage.getItem('pellets-planner-pending')).operation.payload);
  assert.equal(savedSend.request_id,lostSend.request_id);
  const beforeSendReload=posts.length;
  await page.reload();
  await page.getByRole('button',{name:'Retry request',exact:true}).waitFor();
  await page.waitForTimeout(750);
  assert.equal(posts.length,beforeSendReload,'Reload automatically retried a planning mutation');
  await page.getByRole('button',{name:'Retry request',exact:true}).focus();
  await page.evaluate(()=>{window.retryNode=document.querySelector('[data-plan=retry]');window.Planner.sync();});
  assert.equal(await page.evaluate(()=>window.retryNode===document.activeElement),true,'A live render replaced the focused Retry button');
  await page.getByRole('button',{name:'Retry request',exact:true}).click();
  await until(async()=>!(await page.getByRole('button',{name:'Retry request',exact:true}).count()),'Lost Send receipt did not recover');
  const retriedSend=posts.filter(post=>post.action==='send').at(-1);
  assert.equal(retriedSend.request_id,lostSend.request_id);
  assert.deepEqual(retriedSend.state,lostSend.state);
  const recoveredSend=(await(await page.request.get(origin+'/projects/'+original.project+'/planning?chat='+committedSend.id)).json()).chat;
  assert.equal(recoveredSend.state.messages.length,committedSend.state.messages.length,'Retry duplicated the reported reply');
  await page.unroute('**/projects/'+original.project+'/planning');
  const originalChat=(await (await page.request.get(origin+'/projects/'+original.project+'/planning')).json()).chat;
  await until(async()=>await page.locator('.plan-status').innerText()!=='Saving…','Planning save pending');
  await page.reload();
  await page.locator('.plan-created').waitFor();
  assert.equal(await page.locator('#plan-tab').getAttribute('aria-selected'),'true');
  assert.match(await page.locator('.plan-message.assistant').last().innerText(),/refined the selected draft/i);
  await page.locator('#project-switcher > summary').click();
  await page.locator('#project-switcher a').filter({hasText:other.project}).click();
  await until(()=>page.url().includes('/projects/'+other.project+'/'),'Project switch did not finish');
  assert.match(await page.locator('[data-plan-project]').innerText(),new RegExp(original.project),'Main navigation repinned the conversation');
  assert.equal(await page.locator('.plan-created').count(),1);
  await page.locator('.plan-created a').click();
  await page.locator('[data-inspector]').waitFor();
  assert.equal(await page.locator('#inspector-title').innerText(),created.id,'Created reference opened another pellet');
  await page.getByRole('link',{name:'Close inspector',exact:true}).click();
  await page.locator('#project-switcher > summary').click();
  await page.locator('#project-switcher a').filter({hasText:other.project}).click();
  await page.getByRole('button',{name:'New chat',exact:true}).click();
  await page.getByRole('button',{name:'Cancel',exact:true}).click();
  assert.equal(await page.locator('.plan-created').count(),1,'Cancel new chat lost the conversation');
  await page.getByRole('button',{name:'New chat',exact:true}).click();
  let lostNew,committedNew;
  await page.route('**/projects/'+other.project+'/planning',async route=>{
    const request=route.request(),body=request.method()==='POST'?request.postDataJSON():null;
    if(body?.action==='new'&&!lostNew){lostNew=body;committedNew=(await(await route.fetch()).json()).chat;await route.abort('failed');}
    else await route.continue();
  });
  await page.getByRole('button',{name:'Start new',exact:true}).click();
  await page.getByRole('button',{name:'Retry request',exact:true}).waitFor();
  assert.ok(committedNew,'Lost New chat scenario did not commit a real chat');
  const beforeNewReload=posts.length;
  await page.reload();
  await page.getByRole('button',{name:'Retry request',exact:true}).waitFor();
  await page.waitForTimeout(750);
  assert.equal(posts.length,beforeNewReload,'Reload automatically created another chat');
  await page.getByRole('button',{name:'Retry request',exact:true}).click();
  await until(async()=>!(await page.getByRole('button',{name:'Retry request',exact:true}).count()),'Lost New chat receipt did not recover');
  const retriedNew=posts.filter(post=>post.action==='new').at(-1);
  assert.equal(retriedNew.request_id,lostNew.request_id);
  assert.deepEqual(retriedNew.state,lostNew.state);
  const recoveredNew=(await(await page.request.get(origin+'/projects/'+other.project+'/planning')).json()).chat;
  assert.equal(recoveredNew.id,committedNew.id,'Retry created a duplicate chat');
  await page.unroute('**/projects/'+other.project+'/planning');
  await until(async()=>(await page.locator('[data-plan-project]').innerText()).includes(other.project),'New chat did not pin the selected project');
  assert.equal(await page.locator('.plan-created').count(),0);
  const retained=(await (await page.request.get(origin+'/projects/'+original.project+'/planning?chat='+originalChat.id)).json()).chat;
  assert.ok(retained.state.drafts.some(d=>d.created_reference===created.id),'New chat erased completed planning history');
  for(const width of [390,1280]){
    await page.setViewportSize({width,height:800});
    const bounds=await page.locator('#right-panel').boundingBox();
    assert.ok(bounds&&bounds.x>=0&&bounds.x+bounds.width<=width+1,'Planning exceeds viewport');
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
    assert.equal(await page.locator('#main').evaluate(el=>el.inert),width===390,'Phone Plan overlay leaves the covered queue interactive');
    if(width===390){
      await page.locator('#plan-tab').focus();
      for(let i=0;i<18;i++){
        await page.keyboard.press('Tab');
        assert.equal(await page.evaluate(()=>!!document.activeElement.closest('#main,#project-drawer')),false,'Tab reached content hidden behind the Plan panel');
      }
    }
    await page.locator('#toggle-execution').click();assert.equal(await page.locator('#right-panel').isVisible(),false);
    assert.equal(await page.locator('#main').evaluate(el=>el.inert),false,'Collapsing Plan left the queue inert');
    await page.locator('#toggle-execution').click();assert.equal(await page.locator('#planning-panel').isVisible(),true);
  }
  assert.deepEqual(errors,[]);
  console.log('PASS real planning: lazy model catalog, gated reply, retained composer/DOM, explicit selected creation, immediate edit save, split/combine/refine, persistent project pin, created links, confirmed new chat, lost Send/New receipts with exact explicit retry, responsive tabs and keyboard');
})().catch(error=>{console.error(error);process.exitCode=1;}).finally(async()=>{if(browser)await browser.close();await stop();fs.rmSync(temporary,{recursive:true,force:true});});
