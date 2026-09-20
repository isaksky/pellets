// Production execution details with a deterministic Codex peer and disposable data.
// Run with Playwright on NODE_PATH; PLAYWRIGHT_BROWSER=webkit selects WebKit.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-run-context-browser-'));
const binary = path.join(temporary, 'pl'), peer = path.join(temporary, 'codex');
const env = {...process.env, PATH: temporary + path.delimiter + process.env.PATH,
  PELLETS_CODEX_EXECUTABLE: peer, PELLETS_SUPERVISOR_PEER: '1', GORACE: 'atexit_sleep_ms=0'};
let server, browser;
async function stop() {
  if (server && server.exitCode === null && server.signalCode === null) {
    const done = new Promise(resolve => server.once('exit', resolve));
    server.kill('SIGINT'); await done;
  }
}
async function start(root) {
  server = spawn(binary, ['server','--port','0','--no-open'], {cwd:root, env});
  return new Promise((resolve,reject) => {
    let output='', error='';
    const timeout = setTimeout(()=>reject(Error('Server readiness: '+error)), 15000);
    server.stdout.on('data', data => {
      output += data;
      if (output.includes('\n')) { clearTimeout(timeout); resolve(output.split('\n')[0].trim()); }
    });
    server.stderr.on('data', data => error += data);
    server.once('error', reject);
  });
}
async function until(check, message) {
  const end = Date.now()+20000;
  while (Date.now()<end) { if (await check()) return; await new Promise(resolve=>setTimeout(resolve,70)); }
  throw Error(message);
}
const source = '# Original captured document\n\n```mermaid\nflowchart LR\n A --> B\n```\n\n' +
  '<script>window.contextInjected=true</script>\n<img src="https://example.invalid/tracker">\n' +
  Array.from({length:35}, (_,i)=>`## Step ${i}\n${'Raw Markdown and shared guidance. '.repeat(12)}`).join('\n') + '\n';
(async()=>{
  execFileSync('go',['build','-o',binary,'./cmd/pl'],{cwd:repository});
  execFileSync('go',['test','-c','-o',peer,'./internal/app'],{cwd:repository});
  const engine = process.env.PLAYWRIGHT_BROWSER === 'webkit' ? webkit : chromium;
  browser = await engine.launch({headless:true});
  for (const kind of ['captured','empty','ungrouped']) {
    const root=path.join(temporary,kind); fs.mkdirSync(root);
    const git=(...args)=>execFileSync('git',args,{cwd:root,stdio:'pipe'});
    const cli=(...args)=>JSON.parse(execFileSync(binary,['--json',...args],{cwd:root,env,encoding:'utf8'})).data;
    git('init','-q'); git('config','user.name','Test'); git('config','user.email','test@example.invalid');
    git('config','commit.gpgSign','false'); git('commit','--allow-empty','-m','initial');
    fs.appendFileSync(path.join(root,'.git','info','exclude'),'\n/fake-*\n/.agents/\n');
    const pellet=cli('add','Inspect captured group context',...(kind==='ungrouped'?[]:['--group','Original <group>']));
    let group;
    if (kind !== 'ungrouped') {
      group=cli('group','list')[0];
      if (kind==='captured') group=cli('group','edit',String(group.id),'--context',source);
    }
    cli('skill','install','--scope','repo','--agent','codex','--yes');
    fs.writeFileSync(path.join(root,'fake-mode'),'schedule_unfinished');
    let origin=await start(root);
    const page=await browser.newPage({viewport:{width:1280,height:900}, reducedMotion:'reduce'});
    const errors=[], external=[];
    page.on('pageerror',e=>errors.push(e.message));
    page.on('request',r=>{if (!r.url().startsWith(origin)) external.push(r.url());});
    const route=`/projects/${pellet.project}/workspaces/1`;
    await page.goto(origin+route);
    await page.getByRole('button',{name:/Start next/}).click();
    await page.getByRole('button',{name:'Resume',exact:true}).waitFor();
    const panel=page.getByRole('region',{name:'Captured group context',exact:true});
    const openDetails=async()=>{
      const details=page.locator('.run-details');
      if (!await details.evaluate(el=>el.open)) { await details.locator(':scope > summary').focus(); await page.keyboard.press('Enter'); }
    };
    const openSource=async()=>{
      const disclosure=panel.locator('details');
      if (!await disclosure.evaluate(el=>el.open)) { await disclosure.locator('summary').focus(); await page.keyboard.press('Enter'); }
    };
    await openDetails();
    assert.match(await panel.innerText(),/Historical snapshot.*current editable context/);
    if (kind==='captured') {
      await openSource();
      const raw=panel.getByLabel('Captured Markdown source',{exact:true});
      assert.equal(await raw.textContent(),source);
      assert.match(await panel.innerText(),new RegExp(`${group.id} / ${group.revision}`));
      assert.equal(await panel.locator('script,img,pl-diagram').count(),0);
      assert.equal(await page.evaluate(()=>window.contextInjected),undefined);
      await raw.focus(); await page.keyboard.press('End');
      await until(()=>raw.evaluate(el=>el.scrollTop>0),'Source is keyboard scrollable');
      await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));
      await page.waitForTimeout(300);
      assert.equal(await panel.locator('details').evaluate(el=>el.open),true,'Live refresh retains disclosure');
      for (const theme of ['gruvbox-light','gruvbox-dark','light','dark','icy']) {
        await page.evaluate(theme=>window.Workbench.applyTheme(theme),theme);
        for (const width of [1280,1092,390]) {
          await page.setViewportSize({width,height:900});
          await raw.scrollIntoViewIfNeeded();
          const box=await raw.boundingBox();
          assert.ok(box.width>100 && box.x>=0 && box.x+box.width<=width+1,'Source fits visible pane');
          assert.ok(box.height<=281,'Long document has bounded scrolling');
          await page.screenshot({path:path.join(temporary,`${theme}-${width}.png`)});
        }
      }
      await page.setViewportSize({width:1280,height:900});
      cli('group','edit',String(group.id),'--context','');
      await stop(); origin=await start(root); await page.goto(origin+route);
      await openDetails(); await openSource();
      assert.equal(await raw.textContent(),source,'Restart fetched current empty context');
      // Explicit missing-history recovery must carry the original snapshot to a new thread.
      fs.writeFileSync(path.join(root,'fake-mode'),'schedule_missing_history');
      await page.getByRole('button',{name:'Resume',exact:true}).click();
      await page.getByRole('button',{name:'Start a fresh conversation',exact:true}).waitFor();
      fs.writeFileSync(path.join(root,'fake-mode'),'schedule_success');
      await page.getByRole('button',{name:'Start a fresh conversation',exact:true}).click();
      await until(()=>cli('show',pellet.id).status==='closed','Fresh recovery did not complete');
      await page.reload(); await openDetails(); await openSource();
      assert.equal(await raw.textContent(),source,'Fresh recovery lost the captured source');
      const events=fs.readFileSync(path.join(root,'fake-events.jsonl'),'utf8').trim().split('\n').map(JSON.parse);
      const prompts=events.filter(e=>e.method==='turn/start').map(e=>e.params.input[0].text);
      assert.equal(prompts.length,2);
      for (const prompt of prompts) {
        assert.ok(prompt.includes(source));
        assert.ok(prompt.indexOf('CAPTURED GROUP CONTEXT')>prompt.indexOf('STABLE PELLETS WORKFLOW'));
        assert.ok(prompt.indexOf('END GROUP MARKDOWN')<prompt.indexOf('The foreground Pellets server'));
      }
      cli('group','rename',String(group.id),'Current renamed group');
      await page.reload(); await openDetails(); await openSource();
      assert.match(await panel.innerText(),/Original <group>/,'Historical group name followed rename');
      assert.equal(await raw.textContent(),source);
    } else if (kind==='empty') {
      assert.match(await panel.innerText(),/Original <group>/);
      assert.match(await panel.innerText(),/captured group document was empty/);
      assert.equal(await panel.locator('pre').count(),0);
    } else {
      assert.match(await panel.innerText(),/Ungrouped at admission/);
      assert.equal(await panel.locator('pre').count(),0);
    }
    assert.deepEqual(errors,[]); assert.deepEqual(external,[]);
    await page.close(); await stop();
  }
  console.log(`PASS execution group context ${process.env.PLAYWRIGHT_BROWSER||'chromium'}: ${temporary}`);
})().catch(error=>{console.error(error);console.error('Artifacts: '+temporary);process.exitCode=1;}).finally(async()=>{
  await stop(); if (browser) await browser.close();
});
