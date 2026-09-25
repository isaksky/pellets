// Review completed implementations after task text edits, using isolated data.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-review-readiness-'));
const baseline = process.env.PELLETS_REVIEW_BASELINE;
const binary = path.join(temporary, 'pl'), peer = path.join(temporary, 'codex');
const artifacts = process.env.PELLETS_BROWSER_ARTIFACTS || temporary;
const engine = process.env.PLAYWRIGHT_BROWSER || 'chromium';
const env = {...process.env, PATH: temporary + path.delimiter + process.env.PATH,
  PELLETS_CODEX_EXECUTABLE: peer, PELLETS_SUPERVISOR_PEER: '1'};
let browser, server;
let cliBinary = process.env.PELLETS_REVIEW_UPGRADE_FROM || binary;
const until = async (f, message) => {
  const end = Date.now() + 20000;
  while (Date.now() < end) { if (await f()) return; await new Promise(r => setTimeout(r, 50)); }
  throw Error(message);
};
(async () => {
  if (baseline) fs.copyFileSync(baseline, binary);
  else execFileSync('go', ['build', '-o', binary, './cmd/pl'], {cwd: repository});
  execFileSync('go', ['test', '-c', '-o', peer, './internal/app'], {cwd: repository});
  const fixture = path.join(temporary, 'review'); fs.mkdirSync(fixture);
  const git = (...args) => execFileSync('git', args, {cwd: fixture, encoding:'utf8'});
  const cli = (...args) => JSON.parse(execFileSync(cliBinary, ['--json', ...args], {cwd:fixture, env, encoding:'utf8'})).data;
  git('init', '-q'); cli('init-db');
  git('config', 'user.name', 'Test'); git('config', 'user.email', 'test@example.invalid'); git('config', 'commit.gpgSign', 'false');
  git('commit', '--allow-empty', '-qm', 'initial');
  fs.appendFileSync(path.join(fixture, '.git', 'info', 'exclude'), '\n/fake-*\n/.agents/\n');
  cli('skill', 'install', '--scope', 'repo', '--agent', 'codex', '--yes');
  const target = cli('add', 'Audit UI controls');
  const binding = JSON.parse(fs.readFileSync(path.join(fixture, '.git', 'pellets-database.json')));
  assert.ok(fs.realpathSync(path.resolve(fixture, '.git', binding.path)).startsWith(fs.realpathSync(fixture) + path.sep));
  const checkpoint = cli('add', 'Review selected changes', '--review-targets', target.id);
  cli('edit', target.id, '--description', 'Use isolated fixtures and preserve verification evidence.');
  fs.writeFileSync(path.join(fixture, 'fake-mode'), 'schedule_success');
  server = spawn(cliBinary, ['server', '--port', '0', '--no-open'], {cwd:fixture, env});
  const origin = await new Promise((resolve,reject) => {
    let out='', errors='';
    server.stdout.on('data', b => {out+=b; if(out.includes('\n')) resolve(out.split('\n')[0].trim());});
    server.stderr.on('data', b => errors+=b);
    server.once('error', reject); server.once('exit', c => reject(Error(`Server ${c}: ${errors}`)));
  });
  browser = await (engine === 'webkit' ? webkit : chromium).launch({headless:true});
  const page = await browser.newPage({viewport:{width:1280,height:900}, deviceScaleFactor:1});
  const errors=[]; page.on('pageerror', e=>errors.push(e.message));
  const queue = origin + `/projects/${target.project}/tasks?workspace=1`;
  await page.goto(queue);
  await page.locator('#execution-tab').click();
  let scheduleID=0;
  async function start() {
    const accepted = page.waitForResponse(r=>r.url()===origin+`/projects/${target.project}/schedules` && r.request().method()==='POST');
    await page.getByRole('button',{name:/Start next/}).click();
    assert.equal((await accepted).status(),202);
    let state;
    const id=++scheduleID;
    await until(async()=>{state=await (await page.request.get(origin+`/projects/${target.project}/schedules/${id}`)).json(); return ['completed','stopped','needs_attention'].includes(state.state);},'Schedule did not finish');
    return state;
  }
  assert.equal((await start()).completed,1);
  assert.equal(cli('show',target.id).status,'closed');
  if (process.env.PELLETS_REVIEW_UPGRADE_FROM) {
    const legacy=cli('show',checkpoint.id);
    assert.equal(legacy.checkpoint.ready,false);
    assert.equal(legacy.checkpoint.targets[0].reason,'scope_changed');
    const exited=new Promise(r=>server.once('exit',r)); server.kill('SIGINT'); await exited;
    cliBinary=binary;
    const upgraded=cli('show',checkpoint.id);
    assert.equal(upgraded.checkpoint.ready,true,'Migration did not unblock existing review');
    assert.equal(upgraded.updated_at,legacy.updated_at,'Migration edited the review');
    assert.deepEqual(upgraded.checkpoint.targets.map(t=>t.selected_reference),legacy.checkpoint.targets.map(t=>t.selected_reference));
    server=spawn(binary,['server','--port',new URL(origin).port,'--no-open'],{cwd:fixture,env});
    await new Promise((resolve,reject)=>{server.stdout.once('data',resolve);server.once('error',reject);server.once('exit',c=>reject(Error(`Upgrade server ${c}`)));});
    scheduleID=0;
    await page.goto(queue);
    await page.locator('#execution-tab').click();
  }
  const row=page.locator('#task-'+checkpoint.id);
  await until(async()=>await row.locator('.review-status').innerText()===(baseline?'Scope changed':'Ready'),'Review readiness did not refresh');
  const beforeEdit=cli('show',checkpoint.id);
  cli('edit',target.id,'--description','Later planning notes, added after implementation.');
  const afterEdit=cli('show',checkpoint.id);
  assert.equal(afterEdit.checkpoint.ready,!baseline);
  assert.equal(afterEdit.updated_at,beforeEdit.updated_at,'Description edit rewrote review generation');
  if (!baseline) {
    assert.equal(afterEdit.checkpoint.targets[0].description,'Use isolated fixtures and preserve verification evidence.');
    assert.deepEqual(afterEdit.checkpoint,beforeEdit.checkpoint,'Live notes replaced captured requirements');
    await page.reload();
    await until(async()=>await row.locator('.review-status').innerText()==='Ready','Ready status lost on reload');
    assert.equal(await row.getByRole('link',{name:'Update scope',exact:true}).count(),0);
    assert.equal(await page.locator('.review-blockers').count(),0);
  }
  for (const theme of ['light','dark','gruvbox-light','gruvbox-dark','icy']) {
    await page.locator('#theme-select').selectOption(theme,{force:true});
    for (const width of [1280,1092,390]) {
      await page.setViewportSize({width,height:900});
      const directory=path.join(artifacts,`${engine}-${theme}-${width}`); fs.mkdirSync(directory,{recursive:true});
      await page.mouse.move(0,0);
      if (width===390) { if(await page.locator('#right-panel').isVisible()) await page.locator('#toggle-execution').click(); }
      else if (!await page.locator('#right-panel').isVisible()) await page.locator('#toggle-execution').click();
      const box=await row.boundingBox();
      assert.ok(box.x>=0 && box.x+box.width<=width+1,'Review row overflows');
      await page.screenshot({path:path.join(directory,baseline?'before.png':'after.png'),clip:width===390?{x:0,y:90,width:390,height:400}:{x:170,y:90,width:width-170,height:660},animations:'disabled',caret:'hide'});
    }
  }
  await page.setViewportSize({width:1280,height:900});
  await page.locator('#theme-select').selectOption('icy',{force:true});
  if (!await page.locator('#right-panel').isVisible()) await page.locator('#toggle-execution').click();
  if (baseline) {
    const noWork=await start(); assert.equal(noWork.started,0);
  } else {
    fs.writeFileSync(path.join(fixture,'fake-mode'),'review_clean');
    assert.equal((await start()).completed,1);
    await until(()=>cli('show',checkpoint.id).status==='closed','Review did not complete without saving scope');
    await until(async()=>await page.locator('.execution-state-label').innerText()==='Finished','Review completion not displayed');
    assert.equal(cli('show',checkpoint.id).checkpoint.targets[0].evidence.run_id,afterEdit.checkpoint.targets[0].evidence.run_id,'Review replaced implementation evidence');
  }
  assert.deepEqual(errors,[]);
  console.log(`PASS ${engine}: ${baseline?'baseline reproduced':'description edits before/after implementation, captured requirements, unchanged scope, direct successful review'}; 5 themes × 3 widths`);
})().catch(e=>{console.error(e);process.exitCode=1;}).finally(async()=>{
  await browser?.close();
  if(server?.exitCode===null){const exited=new Promise(r=>server.once('exit',r));server.kill('SIGINT');await exited;}
});
