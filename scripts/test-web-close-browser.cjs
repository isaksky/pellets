// Closing a pellet returns to the same queue without selecting another record.
// NODE_PATH=/path/to/node_modules node scripts/test-web-close-browser.cjs
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium} = require('playwright');
const root = path.resolve(__dirname, '..');
const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-close-'));
const fixture = path.join(temp, 'fixture'), binary = path.join(temp, 'pl');
const engine = 'chromium';
const artifacts = process.env.PELLETS_BROWSER_ARTIFACTS;
let server, browser, page;
const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd:fixture, encoding:'utf8'})).data;
(async () => {
  execFileSync('go', ['build', '-o', binary, './cmd/pl'], {cwd:root});
  fs.mkdirSync(fixture);
  execFileSync('git', ['init', '-q'], {cwd:fixture});
  cli('init-db');
  const first = cli('add', 'First pellet', '--group', 'interface');
  const second = cli('add', 'Second pellet', '--group', 'interface');
  const third = cli('add', 'Third pellet', '--group', 'interface');
  const project = cli('project', 'show');
  server = spawn(binary, ['server', '--port', '0', '--no-open'], {cwd:fixture,
    env:{...process.env, PELLETS_CODEX_EXECUTABLE:path.join(temp, 'unavailable')}});
  const origin = await new Promise((resolve, reject) => {
    let out = ''; server.stdout.on('data', data => {out += data; if(out.includes('\n')) resolve(out.split('\n')[0].trim());});
    server.once('error', reject);
  });
  browser = await chromium.launch({headless:true});
  page = await browser.newPage({viewport:{width:1280, height:720}});
  page.setDefaultTimeout(10000);
  const errors = []; page.on('pageerror', error => errors.push(error.message));
  const tasks = origin + '/projects/' + project.code + '/tasks';
  const query = '?direction=desc&execution=1&group=v' + Buffer.from('interface').toString('base64url') + '&sort=title';
  await page.goto(tasks + query);
  const queueURL = page.url();
  const dialog = page.locator('#record-dialog');
  const open = async id => {
    await page.locator('#task-' + id + ' .task-title').click();
    await dialog.locator('details.record-actions > summary').click();
  };
  // Merely dismissing More actions / the dialog must not change any record.
  for (const dismiss of ['header', 'cancel', 'escape']) {
    await open(second.id);
    if (dismiss === 'header') await dialog.getByRole('link', {name:'Close inspector', exact:true}).click();
    else if (dismiss === 'cancel') await dialog.getByRole('button', {name:'Cancel', exact:true}).click();
    else {
      await page.keyboard.press('Escape');
      assert.equal(await dialog.evaluate(el => el.open), true);
      assert.equal(await dialog.locator('details.record-actions').evaluate(el => el.open), false);
      await page.keyboard.press('Escape');
    }
    await dialog.waitFor({state:'hidden'});
    assert.equal(page.url(), queueURL);
    assert.equal(cli('show', second.id).status, 'open');
  }
  // Actual lifecycle Close is the reported path: the middle row has neighbors
  // in both directions, and the returned table excludes the closed record.
  await open(second.id);
  const [response] = await Promise.all([
    page.waitForResponse(response => response.request().method() === 'POST' && response.url().includes('/transition')),
    dialog.getByRole('button', {name:'Close pellet', exact:true}).click(),
  ]);
  assert.equal(response.status(), 200);
  await response.finished();
  await page.waitForTimeout(250);
  if (artifacts) {
    const directory = path.join(artifacts, 'close'); fs.mkdirSync(directory, {recursive:true});
    await page.screenshot({path:path.join(directory, (process.env.PELLETS_EVIDENCE_PHASE || 'after') + '.png'), clip:{x:180,y:43,width:920,height:654}});
  }
  assert.equal(cli('show', second.id).status, 'closed');
  assert.equal(await dialog.evaluate(el => el.open), false, 'Close must dismiss the record dialog');
  assert.equal(await page.locator('[data-inspector]').count(), 0, 'Close must not select any record');
  assert.equal(page.url(), queueURL, 'Close must preserve queue scope and sorting');
  assert.equal(await page.locator('#task-' + second.id).count(), 0);
  for (const pellet of [first, third]) assert.equal(cli('show', pellet.id).status, 'open');
  // A later live refresh must not resurrect the inspector.
  const refreshed = page.waitForResponse(response => response.request().headers()['pellets-target'] === 'live');
  await page.evaluate(() => document.dispatchEvent(new CustomEvent('pellets-refresh')));
  await (await refreshed).finished();
  assert.equal(await dialog.evaluate(el => el.open), false);
  assert.equal(page.url(), queueURL);

  // Even when closed records remain visible, closing does not leave an editor.
  // Exercise keyboard activation and the existing unsaved-edit guard on phone.
  await page.setViewportSize({width:390, height:844});
  await page.goto(tasks + query + '&status=all');
  await open(first.id);
  const title = dialog.locator('input[name=title]');
  await title.fill('Unfinished edit');
  if (!await dialog.locator('details.record-actions').evaluate(el => el.open))
    await dialog.locator('details.record-actions > summary').click();
  page.once('dialog', prompt => prompt.dismiss());
  await dialog.getByRole('button', {name:'Close pellet', exact:true}).click();
  assert.equal(await title.inputValue(), 'Unfinished edit');
  assert.equal(cli('show', first.id).status, 'open');
  assert.equal(await dialog.evaluate(el => el.open), true);
  page.once('dialog', prompt => prompt.accept());
  await dialog.getByRole('button', {name:'Close pellet', exact:true}).focus();
  await page.keyboard.press('Enter');
  await dialog.waitFor({state:'hidden'});
  await page.locator('#task-' + first.id + '.status-closed').waitFor();
  assert.equal(new URL(page.url()).pathname, new URL(tasks).pathname);
  assert.equal(new URL(page.url()).searchParams.get('status'), 'all');
  assert.equal(cli('show', first.id).title, 'First pellet', 'Closing must discard, not save, the confirmed unsaved edit');
  assert.equal(await page.locator('[data-inspector]').count(), 0);
  assert.equal(cli('show', third.id).status, 'open');
  assert.deepEqual(errors, []);
  console.log('PASS ' + engine + ': desktop/phone dismissal, lifecycle Close, unchanged neighbors, preserved queue scope, live refresh, keyboard activation, and unsaved-edit guard');
})().catch(async error => {
  console.error(error);
  if (page && artifacts) {
    fs.mkdirSync(artifacts, {recursive:true});
    await page.screenshot({path:path.join(artifacts, 'failure.png')}).catch(() => {});
    console.error(await page.locator('body').innerText().catch(() => 'unavailable'));
  }
  process.exitCode = 1;
}).finally(async () => {
  await browser?.close();
  if (server && server.exitCode === null) {
    const exited = new Promise(resolve => server.once('exit', resolve)); server.kill('SIGINT'); await exited;
  }
  fs.rmSync(temp, {recursive:true, force:true});
});
