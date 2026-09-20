// Group workbench regression fixtures. Run with Playwright on NODE_PATH;
// PLAYWRIGHT_BROWSER=webkit repeats the workflow in the second engine.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-groups-browser-'));
const binary = path.join(temporary, 'pl');
const fixture = path.join(temporary, 'groups');
let server, browser, origin;
const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd: fixture, encoding: 'utf8', maxBuffer:8*1024*1024})).data;
const until = async (fn, message) => {
  const end = Date.now() + 15000;
  while (Date.now() < end) { if (await fn()) return; await new Promise(r => setTimeout(r, 70)); }
  throw Error(message);
};
const source = '# Shared plan\n\nContext for **every member**.\n\n```mermaid\ngraph TD; A[Plan]-->B[Verify];\n```\n\n' + Array.from({length:24}, (_,i) => `## Section ${i}\n\n${'Detailed guidance. '.repeat(12)}\n\n### Detail ${i}\n\nNested guidance.\n`).join('\n') + '\n<img src="https://example.invalid/tracker">\n\n[unsafe](javascript:alert(1))\n';
(async () => {
  execFileSync('go', ['build', '-o', binary, './cmd/pl'], {cwd:repository});
  fs.mkdirSync(fixture); execFileSync('git', ['init','-q'], {cwd:fixture});
  execFileSync(binary, ['--help'], {cwd:fixture});
  execFileSync(binary, ['group','--help'], {cwd:fixture});
  const member = cli('add','Group member','--group','Existing');
  const existing = cli('group','list')[0];
  cli('group','create','Empty from CLI');
  server = spawn(binary,['server','--port','0','--no-open'],{cwd:fixture});
  origin = await new Promise((resolve,reject) => {
    let output='', error='';
    server.stdout.on('data', d => { output+=d; if (output.includes('\n')) resolve(output.split('\n')[0].trim()); });
    server.stderr.on('data', d => error+=d); server.once('error',reject); server.once('exit', code => reject(Error(`server ${code}: ${error}`)));
  });
  const engine = process.env.PLAYWRIGHT_BROWSER === 'webkit' ? webkit : chromium;
  browser = await engine.launch({headless:true});
  const page = await browser.newPage({viewport:{width:1280,height:850},reducedMotion:"reduce"});
  const errors=[], external=[];
  page.on('pageerror', e => errors.push(e.message));
  page.on('request', r => { if (!r.url().startsWith(origin)) external.push(r.url()); });
  await page.addInitScript(() => document.addEventListener('datastar-fetch', e => {
    if (e.detail.type === 'datastar-patch-signals') window.statuses = [...(window.statuses || []),JSON.parse(e.detail.argsRaw.signals)._webResult?.status];
    if (e.detail.type === 'finished' && e.detail.el === document.body) window.refreshed=(window.refreshed||0)+1;
  }));
  const dialog=page.locator('#record-dialog'), field=dialog.locator('[name=context]'), host=dialog.locator('[data-description]'), view=host.locator('.markdown-body');
  const edit=()=>host.locator('[data-description-mode=edit]').click();
  const preview=()=>host.locator('[data-description-mode=view]').click();
  const refresh=async()=>{
    const n=await page.evaluate(()=>window.refreshed||0);
    await page.evaluate(()=>document.dispatchEvent(new CustomEvent('pellets-refresh')));
    await page.waitForFunction(n=>(window.refreshed||0)>n,n);
  };
  const close=async()=>{ await dialog.getByRole('link',{name:'Close inspector',exact:true}).click(); await page.waitForFunction(()=>!document.querySelector('#record-dialog').open); };
  const groupLink=name=>page.locator('.group-card').getByRole('link',{name,exact:true});
  const save=async()=>{ await dialog.getByRole('button',{name:'Save context',exact:true}).click(); await until(async()=>!(await dialog.locator('.is-dirty').count()),'Save remained dirty'); };
  await page.goto(origin);
  await page.locator("#plan-tab").click();
  await page.locator(`#task-${member.id} .group-name`).click();
  await dialog.locator('.group-members').waitFor();
  assert.equal(await page.locator('#plan-tab').getAttribute('aria-selected'),'true','Group navigation retains selected panel');
  assert.match(await dialog.innerText(), /Group member/);
  assert.match(await view.innerText(), /No shared context/);
  assert.equal(await field.inputValue(),'');
  await close();
  assert.equal(await groupLink('Empty from CLI').count(),1,'Empty groups are discoverable');
  await page.locator('[data-group-create]').focus(); await page.keyboard.press('Enter');
  await dialog.getByLabel('Group name',{exact:true}).fill('Created in browser');
  await dialog.getByRole('button',{name:'Create group',exact:true}).click();
  await view.waitFor({state:'visible'});
  const created=cli('group','list').find(g=>g.name==='Created in browser');
  assert.ok(created);
  assert.match(await dialog.innerText(), /No member pellets/);
  await host.locator('[data-description-mode=edit]').focus(); await page.keyboard.press('Enter');
  await field.fill(source);
  await field.evaluate(el=>{el.focus();el.setSelectionRange(15,29);el.scrollTop=100;});
  const revision=await dialog.locator('[name=revision]').inputValue();
  for(let i=0;i<3;i++) {
    await refresh();
    assert.equal(await field.inputValue(),source);
    assert.equal(await field.evaluate(el=>el===document.activeElement),true);
    assert.deepEqual(await field.evaluate(el=>[el.selectionStart,el.selectionEnd]),[15,29]);
    assert.equal(await dialog.locator('[name=revision]').inputValue(),revision);
  }
  page.once('dialog', d=>d.dismiss()); await dialog.getByRole('link',{name:'Rename group',exact:true}).click();
  assert.equal(await field.inputValue(),source,'Rejected navigation keeps draft');
  page.once('dialog', d=>d.dismiss()); await page.keyboard.press('Escape');
  assert.equal(await dialog.evaluate(el=>el.open),true);
  await page.context().setOffline(true); await preview();
  await view.locator('pl-diagram[data-state=ready]').waitFor();
  assert.equal(await view.getAttribute('aria-label'),'Shared context preview');
  assert.equal(await view.locator('img,script,a').count(),0,'Unsafe Markdown stays local');
  await page.context().setOffline(false);
  const contents=host.getByRole('navigation',{name:'Shared context contents'});
  await contents.waitFor({state:'visible'});
  assert.ok(await contents.locator('ol ol').count());
  const anchor=contents.getByRole('link',{name:'Section 4',exact:true});
  await anchor.focus(); await page.keyboard.press('Enter');
  await page.waitForFunction(()=>document.activeElement?.textContent==='Section 4');
  const before=await view.evaluate(el=>el.scrollTop);
  for(let i=0;i<3;i++) { await refresh(); assert.equal(await view.evaluate(el=>el.scrollTop),before); }
  await view.evaluate(el=>el.scrollTop=0);
  await view.locator('pl-diagram button').first().click();
  const zoom=page.locator('.diagram-viewer[open]');
  await zoom.waitFor();
  await zoom.getByRole('button',{name:'Zoom in',exact:true}).click();
  const canvas=zoom.getByRole('region',{name:'Diagram canvas',exact:true});
  await canvas.focus();
  for(let step=0;step<8;step++) await page.keyboard.press('+');
  const boxBeforePan=await zoom.locator('.diagram-viewer-viewport').getAttribute('viewBox');
  await page.keyboard.press('ArrowDown');
  assert.notEqual(await zoom.locator('.diagram-viewer-viewport').getAttribute('viewBox'),boxBeforePan,'Keyboard pans the enlarged group diagram');
  await page.keyboard.press('Escape');
  assert.equal(await dialog.evaluate(el=>el.open),true,'Nested Escape retains group editor');
  // Stale write retains source, preview, and a fresh revision for explicit retry.
  cli('group','edit',String(created.id),'--context','Concurrent saved text');
  await dialog.getByRole('button',{name:'Save context',exact:true}).click();
  await dialog.locator('.conflict-state').waitFor();
  assert.equal(cli('group','show',String(created.id)).context,'Concurrent saved text');
  assert.equal(await field.inputValue(),source);
  assert.equal(await view.isVisible(),true);
  assert.match(await dialog.locator('.conflict-state').innerText(),/draft is retained/);
  await save();
  assert.equal(cli('group','show',String(created.id)).context,source);
  await close(); await groupLink('Created in browser').click();
  await view.waitFor({state:'visible'});
  // Saved state, source and scroll survive theme changes and clean live updates.
  await view.evaluate(el=>{el.scrollTop=200;el.focus();window.groupPreview=el;});
  for(const theme of ['gruvbox-light','gruvbox-dark','light','dark','icy']) {
    await page.evaluate(theme=>window.Workbench.applyTheme(theme),theme);
    await refresh();
    assert.equal(await view.evaluate(el=>el===window.groupPreview),true);
    assert.equal(await view.evaluate(el=>el===document.activeElement),true);
    assert.equal(await field.inputValue(),source);
    for(const width of [1280,1092,678,390]) {
      await page.setViewportSize({width,height:850});
      await page.waitForTimeout(80);
      const box=await dialog.boundingBox(); assert.ok(box.x>=0 && box.x+box.width<=width+1);
      const submit=dialog.getByRole('button',{name:'Save context',exact:true});
      assert.equal(await submit.evaluate(el=>{const r=el.getBoundingClientRect();return el.contains(document.elementFromPoint(r.x+r.width/2,r.y+r.height/2));}),true,'Save remains reachable');
      await page.screenshot({path:path.join(temporary,`${theme}-${width}.png`)});
    }
  }
  await page.setViewportSize({width:1280,height:850});
  await dialog.getByRole('link',{name:'Rename group',exact:true}).click();
  await dialog.getByLabel('Group name',{exact:true}).fill('Renamed browser group');
  await refresh(); assert.equal(await dialog.getByLabel('Group name',{exact:true}).inputValue(),'Renamed browser group');
  await dialog.getByRole('button',{name:'Save name',exact:true}).click();
  await view.waitFor({state:'visible'});
  assert.equal(cli('group','show',String(created.id)).name,'Renamed browser group');
  assert.equal(cli('group','show',String(created.id)).context,source);
  await dialog.getByRole('link',{name:'Rename group',exact:true}).click();
  await dialog.getByLabel('Group name',{exact:true}).fill('My stale name');
  cli('group','rename',String(created.id),'Concurrent name');
  await dialog.getByRole('button',{name:'Save name',exact:true}).click();
  await dialog.locator('.conflict-state').waitFor();
  assert.equal(await dialog.getByLabel('Group name',{exact:true}).inputValue(),'My stale name');
  assert.equal(cli('group','show',String(created.id)).name,'Concurrent name');
  page.once('dialog',d=>d.accept());
  await dialog.getByRole('link',{name:'Close inspector',exact:true}).click();
  await view.waitFor({state:'visible'});
  assert.equal(await dialog.locator('[name=name]').count(),0,'Cancel rename returns to context, removing rename query');
  assert.match(await dialog.locator('#inspector-title').innerText(),/Concurrent name/);
  await edit(); await field.fill(''); await save();
  assert.equal(cli('group','show',String(created.id)).context,'');
  await preview(); assert.match(await view.innerText(),/No shared context/);
  await close();
  await page.waitForFunction(()=>document.activeElement?.closest('.group-card'));
  await groupLink('Existing').click();
  await dialog.locator('.group-members').waitFor();
  await dialog.getByRole('link',{name:'Filter queue by this group',exact:true}).click();
  await page.locator('.active-group-filter').waitFor();
  assert.match(await page.locator('.active-group-filter').innerText(),/Existing/);
  await page.locator(`#task-${member.id} .task-title`).click();
  await dialog.getByRole('link',{name:'Open group details · Existing',exact:true}).click();
  await dialog.locator('.group-members').waitFor();
  await edit(); await field.fill('# Survives last member'); await save();
  cli('edit',member.id,'--clear-group'); await refresh();
  assert.match(await dialog.innerText(),/No member pellets/);
  assert.equal(cli('group','show',String(existing.id)).context,'# Survives last member');
  // Maximum valid raw source survives preview/save exactly, including form expansion.
  const large='%'.repeat(1024*1024);
  await field.fill(large); await preview(); assert.equal((await view.innerText()).length,large.length);
  await save(); assert.equal(cli('group','show',String(existing.id)).context,large);
  await close();
  await page.locator('.workspace-link').first().click();
  await page.locator('#assignment-popover > summary').click();
  await page.getByLabel('Another group', {exact:true}).fill('Existing');
  await page.getByRole('button', {name:'Save assignments',exact:true}).click();
  await page.locator('#workspace-groups [data-edit-assignment]').first().click();
  assert.equal(await page.locator('#assignment-popover').evaluate(el=>el.open),true,'Routing chips still edit assignments');
  assert.deepEqual(errors,[]); assert.deepEqual(external,[]);
  console.log('PASS groups browser ' + (process.env.PLAYWRIGHT_BROWSER||'chromium') + ': ' + temporary);
})().catch(e=>{console.error(e);console.error('Artifacts: '+temporary);process.exitCode=1;}).finally(async()=>{
  await browser?.close();
  if(server?.exitCode===null) { const stopped=new Promise(r=>server.once('exit',r)); server.kill('SIGINT'); await stopped; }
});
