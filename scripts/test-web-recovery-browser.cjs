// Real browser + compiled production server, with the existing deterministic
// external Codex protocol peer. No account, model service, or live DB is used.
// NODE_PATH=/path/to/node_modules PLAYWRIGHT_CHANNEL=chrome node scripts/test-web-recovery-browser.cjs
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');

const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-recovery-browser-'));
const binary = path.join(temporary, process.platform === 'win32' ? 'pl.exe' : 'pl');
const peer = path.join(temporary, process.platform === 'win32' ? 'codex.exe' : 'codex');
const environment = {...process.env, PATH: temporary + path.delimiter + process.env.PATH,
  PELLETS_SUPERVISOR_PEER: '1', GORACE: 'atexit_sleep_ms=0'};
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
  for (const scenario of [
    {name: 'auth', failure: 'unauthenticated', mode: 'run_one'},
    {name: 'config', failure: 'config_disallows_review', mode: 'drain', restart: true},
    {name: 'cli', mode: 'watch'}
  ]) {
    const root = path.join(temporary, scenario.name);
    fs.mkdirSync(root);
    const git = (...args) => execFileSync('git', args, {cwd: root, encoding: 'utf8'});
    const cli = (...args) => JSON.parse(execFileSync(binary, args, {cwd: root, env: environment, encoding: 'utf8'})).data;
    git('init', '-q');
    git('config', 'user.name', 'Test');
    git('config', 'user.email', 'test@example.invalid');
    git('config', 'commit.gpgSign', 'false');
    git('commit', '--allow-empty', '-m', 'initial');
    fs.appendFileSync(path.join(root, '.git', 'info', 'exclude'), '\n/fake-*\n/.agents/\n');
    const group = ' Exact Group ', external = 'Exact:ID';
    const target = cli('add', 'recover this exact target', '--group', group, '--external-id', external);
    const queued = cli('add', 'leave matching target queued', '--group', group, '--external-id', external);
    const unrelated = cli('add', 'leave unrelated target queued');
    cli('skill', 'install', '--scope', 'repo', '--agent', 'codex', '--yes');
    const initialHead = git('rev-parse', 'HEAD').trim();
    const modeFile = path.join(root, 'fake-mode');
    fs.writeFileSync(modeFile, scenario.failure || 'schedule_success');
    if (!scenario.failure) cli('start-next', '--group', group, '--external-id', external);
    let origin = await startServer(root);
    const page = await browser.newPage();
    page.setDefaultTimeout(15000);
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(origin);
    await page.waitForFunction(() => !document.documentElement.hasAttribute('data-nonce'));
    const resume = page.locator('form[data-no-run-resume]');
    const events = () => {
      const file = path.join(root, 'fake-events.jsonl');
      return fs.existsSync(file) ? fs.readFileSync(file, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse) : [];
    };
    if (scenario.failure) {
      const start = page.locator('form[data-schedule]').filter({has: page.locator('button[name=mode]')});
      await start.locator('input[name=group]').fill(group);
      await start.locator('input[name=external_id]').fill(external);
      await start.locator(`button[value=${scenario.mode}]`).click();
      // This disposable server's first schedule is #1. Preflight may finish
      // after the ownership invalidation; reconnect to its authoritative state
      // without depending on the dashboard's 35-second fallback refresh.
      await until(async () => {
        const response = await page.request.get(origin + `/projects/${target.project}/schedules/1`);
        return response.status() === 200 && (await response.json()).state === 'needs_attention';
      }, 'Initial preflight did not fail');
      await page.reload();
      await resume.waitFor();
      assert.equal(cli('show', target.id).status, 'in_progress');
      assert.equal(await page.locator('.run-facts').count(), 0, 'Preflight invented a run');
      assert.equal(events().filter(event => event.method === 'thread/start' || event.method === 'turn/start').length, 0);
      fs.writeFileSync(modeFile, 'schedule_gate');
      if (scenario.restart) {
        await stopServer();
        origin = await startServer(root);
      }
      await page.goto(origin + `/projects/${target.project}/tasks?group=n&external_id=unrelated-page-filter`);
    } else {
      fs.writeFileSync(modeFile, 'schedule_gate');
    }
    await resume.waitFor();
    assert.match(await resume.textContent(), /No run or saved schedule intent exists/);
    assert.match(await resume.textContent(), /new conversation/);
    assert.equal(await resume.locator('select[name=mode]').inputValue(), '', 'Recovery silently chose a mode');
    assert.equal(await resume.locator('input[name=group]').inputValue(), group);
    assert.equal(await resume.locator('input[name=external_id]').inputValue(), external);
    assert.equal(await resume.locator('input[name=resume_from]').count(), 0);
    assert.equal(await resume.locator('input[name=resume_pellet]').inputValue(), String(target.number));
    await page.setViewportSize({width: 390, height: 844});
    assert.equal(await resume.evaluate(form => {
      const workspace = form.closest('.run-workspace').getBoundingClientRect();
      return Array.from(form.querySelectorAll('select, input:not([type=hidden]), button')).every(control => {
        const box = control.getBoundingClientRect();
        return box.left >= workspace.left && box.right <= workspace.right;
      });
    }), true, 'Recovery controls overflow the workspace on a narrow screen');
    await page.setViewportSize({width: 1280, height: 900});
    await resume.getByRole('button', {name: 'Resume', exact: true}).click();
    assert.equal(await resume.locator('select[name=mode]').evaluate(el => el.validity.valueMissing), true);
    await resume.locator('select[name=mode]').selectOption(scenario.mode);
    await resume.locator('input[name=limit]').fill('1');
    // A live invalidation must not reset the user's reconstructed intent.
    await page.evaluate(() => document.dispatchEvent(new CustomEvent('pellets-refresh')));
    await page.waitForTimeout(400);
    assert.equal(await resume.locator('select[name=mode]').inputValue(), scenario.mode);
    assert.equal(await resume.locator('input[name=limit]').inputValue(), '1');
    const fields = await resume.evaluate(form => Object.fromEntries(new FormData(form)));
    const endpoint = origin + `/projects/${target.project}/schedules`;
    const forbidden = await page.request.post(endpoint, {headers: {Origin: 'https://example.invalid'}, form: fields});
    assert.equal(forbidden.status(), 403);
    const noCSRF = await page.request.post(endpoint, {headers: {Origin: origin}, form: {...fields, _csrf: 'invalid'}});
    assert.equal(noCSRF.status(), 403);
    const accepted = page.waitForResponse(response => response.url() === endpoint && response.request().method() === 'POST' && response.status() === 202);
    await resume.getByRole('button', {name: 'Resume', exact: true}).click();
    await accepted;
    // Read the receipt through the server-rendered live schedule identity.
    // The browser application intentionally consumes only POST status headers.
    const stop = page.locator('form[data-schedule][action$="/stop-now"]');
    await stop.waitFor();
    const receiptURL = origin + (await stop.getAttribute('action')).replace(/\/stop-now$/, '');
    const receipt = await (await page.request.get(receiptURL)).json();
    assert.equal(receipt.mode, scenario.mode);
    assert.equal(receipt.group, group);
    assert.equal(receipt.external_id, external);
    assert.equal(receipt.limit, 1);
    await until(() => events().some(event => event.method === 'turn/start'), 'Resumed target never started');
    const overlap = await page.request.post(endpoint, {headers: {Origin: origin}, form: fields, timeout: 5000});
    assert.equal(overlap.status(), 409, 'Duplicate Resume overlapped execution');
    assert.equal(events().filter(event => event.method === 'turn/start').length, 1);
    fs.writeFileSync(path.join(root, 'fake-complete'), 'complete');
    await until(() => cli('show', target.id).status === 'closed', 'Resumed target did not close');
    await until(async () => {
      const state = await (await page.request.get(endpoint + '/' + receipt.id)).json();
      return state.completed === 1 && (state.state === 'completed' || state.reason === 'limit_reached');
    }, 'Schedule did not finish within its confirmed mode/limit');
    assert.equal(cli('show', queued.id).status, 'open');
    assert.equal(cli('show', unrelated.id).status, 'open');
    assert.equal(git('rev-list', '--count', `${initialHead}..HEAD`).trim(), '1');
    assert.equal(events().filter(event => event.method === 'thread/start').length, 1);
    assert.equal(events().filter(event => event.method === 'thread/resume').length, 0);
    if (scenario.name === 'auth') {
      // Reopening the same number creates a new implementation generation.
      // Its previous completed run cannot suppress recovery or supply history.
      cli('reopen', target.id);
      cli('start', target.id);
      assert.equal(cli('show', target.id).status, 'in_progress');
      fs.writeFileSync(modeFile, 'unauthenticated');
      await page.reload();
      await resume.waitFor();
      assert.match(await resume.textContent(), /implementation revision 2/);
      assert.equal(await resume.locator('input[name=resume_from]').count(), 0);
      await resume.locator('select[name=mode]').selectOption('run_one');
      await resume.locator('input[name=limit]').fill('1');
      await resume.getByRole('button', {name: 'Resume', exact: true}).click();
      await until(async () => {
        const response = await page.request.get(endpoint + '/3');
        return response.status() === 200 && (await response.json()).state === 'needs_attention';
      }, 'Reopened generation did not reach authentication preflight');
      assert.equal(events().filter(event => event.method === 'thread/start').length, 1);
      fs.writeFileSync(modeFile, 'schedule_reimplementation');
      await page.reload();
      await resume.waitFor();
      await resume.locator('select[name=mode]').selectOption('run_one');
      await resume.locator('input[name=limit]').fill('1');
      await resume.getByRole('button', {name: 'Resume', exact: true}).click();
      await until(() => cli('show', target.id).status === 'closed', 'Reopened generation did not recover');
      await until(async () => {
        const response = await page.request.get(endpoint + '/4');
        return response.status() === 200 && (await response.json()).completed === 1;
      }, 'Reopened generation did not complete its new run');
      assert.equal(events().filter(event => event.method === 'thread/start').length, 2);
      assert.equal(events().filter(event => event.method === 'thread/resume').length, 0);
      assert.equal(git('rev-list', '--count', `${initialHead}..HEAD`).trim(), '2');
      assert.equal(cli('show', queued.id).status, 'open');
      assert.equal(cli('show', unrelated.id).status, 'open');
      console.log('Recovery browser check passed: completed target reopened at revision 2, preflight repaired, fresh conversation and one additional commit.');
    }
    // The previous completed pellet's run must not hide new CLI ownership.
    cli('start', queued.id);
    await page.reload();
    await resume.waitFor();
    assert.equal(await resume.locator('input[name=resume_pellet]').inputValue(), String(queued.number));
    assert.equal(await page.locator('button[name=mode]:not([disabled])').count(), 0);
    assert.deepEqual(errors, []);
    await page.close();
    await stopServer();
    console.log(`Recovery browser check passed: ${scenario.name} → explicit ${scenario.mode}, exact filters, one target/commit, duplicate and CSRF rejection.`);
  }
})().catch(error => { console.error(error); process.exitCode = 1; }).finally(async () => {
  if (browser) await browser.close();
  await stopServer();
  fs.rmSync(temporary, {recursive: true, force: true});
});
