// Initial-commit admission uses real HTTP/UI and an isolated fake Codex runtime.
// PELLETS_ADMISSION_BASELINE records the original failure for review evidence.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const root = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-admission-'));
const baseline = process.env.PELLETS_ADMISSION_BASELINE;
const binary = path.join(temporary, 'pl'), peer = path.join(temporary, 'codex');
const artifacts = process.env.PELLETS_BROWSER_ARTIFACTS || temporary;
const engine = process.env.PLAYWRIGHT_BROWSER || 'chromium';
const env = {...process.env, PATH:temporary + path.delimiter + process.env.PATH,
  PELLETS_CODEX_EXECUTABLE:peer, PELLETS_SUPERVISOR_PEER:'1'};
let server, browser, page;
async function stopServer() {
  if (server?.exitCode === null) {
    const done = new Promise(resolve => server.once('exit', resolve));
    server.kill('SIGINT'); await done;
  }
}
async function startServer(fixture) {
  server = spawn(binary, ['server', '--port', '0', '--no-open'], {cwd:fixture, env});
  return new Promise((resolve, reject) => {
    let out = '', errors = '';
    server.stdout.on('data', data => { out += data; if (out.includes('\n')) resolve(out.split('\n')[0].trim()); });
    server.stderr.on('data', data => { errors += data; });
    server.once('error', reject);
    server.once('exit', code => reject(Error(`Server ${code}: ${errors}`)));
  });
}
(async () => {
  if (baseline) fs.copyFileSync(baseline, binary);
  else execFileSync('go', ['build', '-o', binary, './cmd/pl'], {cwd:root});
  execFileSync('go', ['test', '-c', '-o', peer, './internal/app'], {cwd:root});
  browser = await (engine === 'webkit' ? webkit : chromium).launch({headless:true});
  for (const width of [1280, 390]) {
    const fixture = path.join(temporary, String(width), 'admission');
    fs.mkdirSync(fixture, {recursive:true});
    const git = (...args) => execFileSync('git', args, {cwd:fixture, encoding:'utf8'});
    const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd:fixture, env, encoding:'utf8'})).data;
    git('init', '-q'); cli('init-db');
    const pellet = cli('add', 'Bootstrap the application');
    const binding = JSON.parse(fs.readFileSync(path.join(fixture, '.git', 'pellets-database.json')));
    assert.ok(fs.realpathSync(path.resolve(fixture, '.git', binding.path)).startsWith(fs.realpathSync(fixture) + path.sep));
    fs.writeFileSync(path.join(fixture, 'README.md'), 'Planning notes\n');
    fs.writeFileSync(path.join(fixture, 'fake-mode'), 'schedule_success');
    fs.appendFileSync(path.join(fixture, '.git', 'info', 'exclude'), '\n/fake-*\n');
    let origin = await startServer(fixture);
    page = await browser.newPage({viewport:{width, height:900}, deviceScaleFactor:1, hasTouch:true});
    page.setDefaultTimeout(15000);
    const errors = []; page.on('pageerror', error => errors.push(error.message));
    const open = async () => {
      await page.goto(origin + '/projects/' + pellet.project + '/tasks?workspace=1');
      await page.locator('#theme-select').selectOption('light', {force:true});
      if (!await page.locator('#right-panel').isVisible()) await page.locator('#toggle-execution').click();
      await page.locator('#execution-tab').click();
    };
    await open();
    await page.getByRole('combobox', {name:'Execution intention'}).click();
    await page.getByRole('option', {name:/^Through matching queue/}).click();
    await page.getByRole('combobox', {name:'Execution access'}).click();
    await page.getByRole('option', {name:'Full access', exact:true}).click();
    await page.getByRole('button', {name:/Start next/}).click();
    if (baseline) {
      await page.getByText('the exact execution evidence is unavailable; do not substitute current work or treat this as success', {exact:false}).first().waitFor();
      assert.equal(cli('show', pellet.id).status, 'in_progress');
    } else {
      await page.getByRole('button', {name:'Check again', exact:true}).waitFor();
      const feedback = page.locator('#right-panel .request-failed');
      assert.match(await feedback.textContent(), /initial Git commit/);
      assert.equal(cli('show', pellet.id).status, 'open');
      assert.equal(await page.locator('#right-panel select[name=mode]').inputValue(), 'drain');
      assert.equal(await page.locator('#right-panel select[name=access_mode]').inputValue(), 'full');
      assert.equal(await page.locator('[data-no-run-resume]').count(), 0);
      const retry = page.getByRole('button', {name:'Check again', exact:true});
      if (width === 390) await retry.tap();
      else { await retry.focus(); await page.keyboard.press('Enter'); }
      await retry.waitFor();
      assert.equal(cli('show', pellet.id).status, 'open');
    }
    await page.mouse.move(0, 0);
    await page.locator('#right-panel .run-workspace').evaluate((el, scroll) => { el.scrollTop = scroll; }, width === 390 ? 160 : 0);
    const directory = path.join(artifacts, engine + '-' + width);
    fs.mkdirSync(directory, {recursive:true});
    const clip = width === 390 ? {x:0, y:500, width, height:376}
      : {x:width - 650, y:40, width:650, height:780};
    await page.screenshot({path:path.join(directory, baseline ? 'before.png' : 'after.png'), clip, animations:'disabled', caret:'hide'});
    if (!baseline) {
      // Reload and restart still show unclaimed work. No recovery is required.
      await open(); assert.equal(await page.locator('[data-no-run-resume]').count(), 0);
      await stopServer(); origin = await startServer(fixture); await open();
      assert.equal(cli('show', pellet.id).status, 'open');
      await page.getByRole('button', {name:/Start next/}).click();
      await page.getByRole('button', {name:'Check again', exact:true}).waitFor();
      assert.match(await page.locator('#right-panel .request-failed').textContent(), /initial Git commit/);
      assert.equal(cli('show', pellet.id).status, 'open');
      git('add', 'README.md');
      git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', '-c', 'commit.gpgSign=false', 'commit', '-qm', 'Initial commit');
      // A corrected repository passes the same button check and starts a run.
      const response = page.waitForResponse(response => response.request().method() === 'POST' && response.url().endsWith('/schedules'));
      await page.getByRole('button', {name:'Check again', exact:true}).click();
      assert.equal((await response).status(), 202);
    }
    assert.deepEqual(errors, []);
    console.log(`PASS ${engine}/${width}: ${baseline ? 'reproduced claimed pellet without a run' : 'unclaimed admission, actionable feedback, retry, reload/restart, initial-commit correction'}`);
    await page.close(); page = null; await stopServer();
  }
})().catch(error => { console.error(error); process.exitCode = 1; }).finally(async () => {
  await browser?.close(); await stopServer();
});
