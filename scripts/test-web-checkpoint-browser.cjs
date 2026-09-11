// Real browser, production server and durable review/triage writers. Only the
// external Codex protocol peer is deterministic; no HTTP outcomes are mocked.
// NODE_PATH=/path/to/node_modules PLAYWRIGHT_CHANNEL=chrome node scripts/test-web-checkpoint-browser.cjs
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');

const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-checkpoint-browser-'));
const binary = path.join(temporary, process.platform === 'win32' ? 'pl.exe' : 'pl');
const peer = path.join(temporary, process.platform === 'win32' ? 'codex.exe' : 'codex');
const environment = {...process.env, PATH: temporary + path.delimiter + process.env.PATH,
  PELLETS_CODEX_EXECUTABLE: peer, PELLETS_SUPERVISOR_PEER: '1', GORACE: 'atexit_sleep_ms=0'};
let browser, server;
const until = async (predicate, message) => {
  const deadline = Date.now() + 20000;
  while (Date.now() < deadline) {
    if (await predicate()) return;
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  throw new Error(message);
};
async function stopServer() {
  if (server && server.exitCode === null) {
    const exited = new Promise(resolve => server.once('exit', resolve));
    server.kill('SIGINT');
    await exited;
  }
}
async function startServer(root) {
  server = spawn(binary, ['server', '--port', '0', '--no-open'], {cwd: root, env: environment});
  return new Promise((resolve, reject) => {
    let output = '', errors = '';
    const timeout = setTimeout(() => reject(new Error('Server readiness timeout: ' + errors)), 10000);
    server.stderr.on('data', chunk => { errors += chunk; });
    server.stdout.on('data', chunk => {
      output += chunk;
      if (output.includes('\n')) { clearTimeout(timeout); resolve(output.split('\n')[0].trim()); }
    });
    server.once('error', reject);
    server.once('exit', code => { clearTimeout(timeout); reject(new Error(`Server exited ${code}: ${errors}`)); });
  });
}

(async () => {
  execFileSync('go', ['build', '-o', binary, './cmd/pl'], {cwd: repository});
  execFileSync('go', ['test', '-c', '-o', peer, './internal/app'], {cwd: repository});
  const engine = process.env.PLAYWRIGHT_BROWSER === 'webkit' ? webkit : chromium;
  browser = await engine.launch({headless: true, ...(engine === chromium && process.env.PLAYWRIGHT_CHANNEL ? {channel: process.env.PLAYWRIGHT_CHANNEL} : {})});
  for (const mode of ['review_clean', 'review_findings_partial', 'review_findings_invalid']) {
    const root = path.join(temporary, mode);
    fs.mkdirSync(root);
    const git = (...args) => execFileSync('git', args, {cwd: root, encoding: 'utf8'});
    const cli = (...args) => JSON.parse(execFileSync(binary, args, {cwd: root, env: environment, encoding: 'utf8'})).data;
    const setMode = value => fs.writeFileSync(path.join(root, 'fake-mode'), value);
    git('init', '-q');
    git('config', 'user.name', 'Test');
    git('config', 'user.email', 'test@example.invalid');
    git('config', 'commit.gpgSign', 'false');
    git('commit', '--allow-empty', '-m', 'initial');
    fs.appendFileSync(path.join(root, '.git', 'info', 'exclude'), '\n/fake-*\n/.agents/\n');
    const target = cli('add', 'selected implementation');
    cli('skill', 'install', '--scope', 'repo', '--agent', 'codex', '--yes');
    setMode('schedule_success');
    let origin = await startServer(root);
    const page = await browser.newPage();
    page.setDefaultTimeout(15000);
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(origin);
    const controls = await browser.newPage();
    await controls.goto(origin + `/projects/${target.project}/workspaces/1`);
    const endpoint = `/projects/${target.project}/schedules`;
    let scheduleID = 0;
    async function schedule(button) {
      const accepted = controls.waitForResponse(response => response.url() === origin + endpoint && response.request().method() === 'POST' && response.status() === 202);
      await button.click();
      await accepted;
      // The app intentionally consumes only response headers. Read the exact
      // disposable server's sequential receipt from its status endpoint.
      const id = ++scheduleID;
      let state;
      await until(async () => {
        state = await (await page.request.get(origin + endpoint + '/' + id)).json();
        return ['completed', 'needs_attention', 'interrupted'].includes(state.state);
      }, 'Schedule did not finish');
      return state;
    }
    assert.equal((await schedule(controls.getByRole('button', {name: 'Run one', exact: true}))).completed, 1);
    const checkpoint = cli('add', 'Review selected implementation', '--review-targets', target.id);
    const inspectorPath = `/projects/${target.project}/tasks/${checkpoint.id}`;
    await page.goto(origin + inspectorPath);
    await page.waitForFunction(() => !document.documentElement.hasAttribute('data-nonce'));
    const outcome = page.locator('[data-checkpoint-outcome]');
    assert.match(await outcome.textContent(), /Pending separate review/);
    await page.evaluate(() => { window.checkpointInspector = document.querySelector('[data-inspector]'); });
    setMode(mode);
    const reviewed = await schedule(controls.getByRole('button', {name: 'Run one', exact: true}));
    await until(async () => /Review outcome: (Clean|Findings)/.test(await outcome.textContent()), 'Live review completion retained a placeholder');
    assert.equal(await page.evaluate(() => window.checkpointInspector === document.querySelector('[data-inspector]')), true, 'Live outcome replaced the inspector instead of morphing it');
    // Review findings can reach the live inspector before the final triage
    // projection, even after the schedule endpoint reports completion.
    if (mode === 'review_findings_invalid') await until(async () => /Complete.*1 of 1/s.test(await outcome.textContent()), 'Completed invalid-finding triage stayed partial');
    let text = await outcome.textContent();
    if (mode === 'review_clean') {
      assert.match(text, /Clean/); assert.match(text, /0 of 0/); assert.match(text, /0 created/);
    } else if (mode === 'review_findings_invalid') {
      assert.match(text, /Findings/); assert.match(text, /1 of 1/); assert.match(text, /Invalid finding/); assert.match(text, /0 created/);
    } else {
      assert.equal(reviewed.state, 'needs_attention');
      await until(async () => /Needs attention/.test(await outcome.textContent()), 'Partial failure not shown');
      text = await outcome.textContent();
      assert.match(text, /Partial/); assert.match(text, /1 of 2/);
      assert.equal(await outcome.locator('a').count(), 1);
      const firstFollowup = await outcome.locator('a').textContent();
      // A restarted server and new event stream recover partial durable state.
      await stopServer(); origin = await startServer(root); scheduleID = 0;
      await controls.goto(origin + `/projects/${target.project}/workspaces/1`);
      await page.goto(origin + inspectorPath);
      assert.equal(await outcome.textContent(), text);
      setMode('review_findings');
      assert.equal((await schedule(controls.getByRole('button', {name: 'Resume', exact: true}))).completed, 1);
      await until(async () => /Complete.*2 of 2/s.test(await outcome.textContent()), 'Recovered triage stayed partial');
      assert.equal(await outcome.locator('a').count(), 2);
      assert.equal(await outcome.locator('a').first().textContent(), firstFollowup);
      // Actual follow-up navigation remains within the inspector and supports Back.
      await outcome.locator('a').first().click();
      await until(async () => await page.locator('#inspector-title').textContent() === firstFollowup, 'Follow-up link opened another Pellet');
      await page.goBack();
      await outcome.waitFor();
    }
    assert.equal(cli('show', checkpoint.id).status, 'closed');
    text = await outcome.textContent();
    await page.reload();
    assert.equal(await outcome.textContent(), text, 'Reconnect lost the closed result');
    // A later ordinary run changes the workspace dashboard, not this receipt.
    cli('add', 'later ordinary task');
    setMode('schedule_success');
    assert.equal((await schedule(controls.getByRole('button', {name: 'Run one', exact: true}))).completed, 1);
    await page.evaluate(() => document.dispatchEvent(new CustomEvent('pellets-refresh')));
    await page.waitForTimeout(500);
    assert.equal(await outcome.textContent(), text, 'Later workspace run replaced checkpoint evidence');
    await page.setViewportSize({width: 390, height: 844});
    assert.equal(await outcome.evaluate(el => el.scrollWidth <= el.clientWidth), true, 'Outcome overflows narrow inspector');
    assert.equal(await page.locator('[data-inspector]').getAttribute('aria-modal'), 'true');
    cli('reopen', checkpoint.id);
    await page.reload();
    assert.equal(await outcome.getAttribute('data-checkpoint-generation'), '2');
    assert.match(await outcome.textContent(), /Pending separate review/);
    assert.equal(await outcome.locator('a').count(), 0, 'New generation inherited old follow-ups');
    assert.deepEqual(errors, []);
    await controls.close();
    await page.close(); await stopServer();
    console.log(`Checkpoint browser passed: ${mode}, durable reconnect, later run, narrow layout, exact generation.`);
  }
})().catch(error => { console.error(error); process.exitCode = 1; }).finally(async () => {
  if (browser) await browser.close();
  await stopServer();
  fs.rmSync(temporary, {recursive: true, force: true});
});
