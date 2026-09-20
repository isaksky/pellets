// Real run transitions plus controlled delivery of bounded activity snapshots.
// Uses disposable data and the deterministic Codex peer; no model calls.
// NODE_PATH=/path/to/node_modules node scripts/test-web-execution-state-browser.cjs
// Repeat with PLAYWRIGHT_BROWSER=webkit.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-execution-state-'));
const binary = path.join(temporary, 'pl'), peer = path.join(temporary, 'codex');
const repo = path.join(temporary, 'state');
const env = {...process.env, PATH: temporary + path.delimiter + process.env.PATH,
  PELLETS_CODEX_EXECUTABLE: peer, PELLETS_SUPERVISOR_PEER: '1'};
const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd: repo, env, encoding: 'utf8'})).data;
let server, browser, origin;
async function until(check, message) {
  const end = Date.now() + 25000;
  while (Date.now() < end) { if (await check()) return; await new Promise(resolve => setTimeout(resolve, 70)); }
  throw Error(message);
}
async function start() {
  server = spawn(binary, ['server', '--port', '0', '--no-open'], {cwd: repo, env});
  origin = await new Promise((resolve, reject) => {
    let output = '', error = '';
    server.stdout.on('data', data => { output += data; if (output.includes('\n')) resolve(output.split('\n')[0].trim()); });
    server.stderr.on('data', data => error += data);
    server.once('error', reject);
    server.once('exit', code => reject(Error('Server ' + code + ': ' + error)));
  });
}
async function stop() {
  if (server?.exitCode === null && server.signalCode === null) {
    const ended = new Promise(resolve => server.once('exit', resolve));
    server.kill('SIGINT'); await ended;
  }
}
(async () => {
  execFileSync('go', ['build', '-o', binary, './cmd/pl'], {cwd: repository});
  execFileSync('go', ['test', '-c', '-o', peer, './internal/app'], {cwd: repository});
  fs.mkdirSync(repo);
  const git = (...args) => execFileSync('git', args, {cwd: repo, stdio: 'pipe'});
  git('init', '-q'); git('config', 'user.name', 'Test'); git('config', 'user.email', 'test@example.invalid');
  git('config', 'commit.gpgSign', 'false'); git('commit', '--allow-empty', '-qm', 'initial');
  fs.appendFileSync(path.join(repo, '.git', 'info', 'exclude'), '\n/fake-*\n/.agents/\n');
  cli('add', 'Keep execution state visible');
  cli('skill', 'install', '--scope', 'repo', '--agent', 'codex', '--yes');
  fs.writeFileSync(path.join(repo, 'fake-mode'), 'schedule_activity_gate');
  await start();
  const engine = process.env.PLAYWRIGHT_BROWSER === 'webkit' ? webkit : chromium;
  browser = await engine.launch({headless: true});
  const page = await browser.newPage({viewport: {width: 1280, height: 900}});
  page.setDefaultTimeout(15000);
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  // Capture the real EventSource so transport-only edge cases exercise the
  // production renderer without altering durable state or application controls.
  await page.addInitScript(() => {
    const NativeSource = window.EventSource;
    window.activitySources = [];
    window.EventSource = class extends NativeSource {
      constructor(url, options) { super(url, options); if (url.includes('/activity?')) window.activitySources.push(this); }
    };
  });
  const status = page.locator('.execution-status'), label = status.locator('.execution-state-label');
  const operation = status.locator('.execution-operation');
  const expectState = async (text, working) => {
    await until(async () => await label.textContent() === text, 'Expected execution state ' + text);
    assert.equal(await status.getAttribute('data-working'), String(working));
    assert.equal(await label.getAttribute('role'), 'status');
    if (!working) {
      assert.equal(await status.locator('.execution-indicator').evaluate(el => getComputedStyle(el).animationName), 'none');
      assert.equal(await operation.isVisible(), false, 'Inactive state retained an active operation');
    }
  };
  const send = snapshot => page.evaluate(snapshot => window.activitySources.at(-1).dispatchEvent(
    new MessageEvent('pellets-activity', {data: JSON.stringify(snapshot)})), snapshot);
  const running = {id: 'operation', sequence: 100, kind: 'command', status: 'running', title: 'Command', command: 'go test ./internal/webui'};
  await page.goto(origin + '/projects/state/tasks?workspace=1');
  await page.getByRole('button', {name: '▷ Start next', exact: true}).click();
  await until(() => page.locator('.activity-event').count().then(count => count >= 5), 'Reported history');
  await expectState('Working', true);
  await until(() => status.locator('.execution-phase').textContent().then(text => text === 'Working on the pellet.'), 'Authoritative implementation phase');
  assert.equal(await operation.isVisible(), false, 'Completed historical events must use an honest phase fallback');
  assert.equal(await status.locator('.execution-phase').textContent(), 'Working on the pellet.');
  assert.equal(await status.locator('.execution-indicator').evaluate(el => getComputedStyle(el).animationName), 'execution-working');
  assert.equal(await page.locator('.activity-event summary').filter({hasText: 'Agent update'}).count(), 1);
  assert.equal(await page.locator('.activity-event summary').filter({hasText: /Codex completed|Agent update.*Completed/}).count(), 0);
  const snapshot = await (await page.request.get(origin + await page.locator('.activity-panel').getAttribute('data-activity-url'))).json();
  await send({available: true, cursor: 100, items: [running]});
  assert.equal(await operation.textContent(), 'Running command: go test ./internal/webui');
  const draft = page.locator('.run-follow-up textarea');
  await draft.fill('Keep my unfinished follow-up'); await draft.press('ArrowLeft');
  const caret = await draft.evaluate(el => el.selectionStart);
  cli('add', 'A queue update while working');
  await until(() => page.locator('.task-title').filter({hasText: 'A queue update'}).count().then(count => count === 1), 'Database patch');
  assert.equal(await operation.isVisible(), true, 'Authoritative refresh retains the active operation');
  assert.equal(await draft.inputValue(), 'Keep my unfinished follow-up');
  assert.equal(await draft.evaluate((el, caret) => el === document.activeElement && el.selectionStart === caret, caret), true);
  await send({available: true, cursor: 101, items: [{...running, sequence: 101, status: 'completed'}]});
  await expectState('Working', true);
  assert.equal(await operation.isVisible(), false, 'Operation completion ended the run');
  await send({available: true, cursor: 102, items: [{...running, sequence: 102}]});
  await send({available: true, cursor: 103, items: [{id: 'turn', sequence: 103, kind: 'turn', status: 'completed', title: 'Turn completed'}]});
  assert.equal(await operation.isVisible(), false, 'Turn completion left stale active work');
  await expectState('Working', true);
  await send({available: true, cursor: 104, items: [{...running, sequence: 104}]});
  await page.evaluate(() => window.activitySources.at(-1).dispatchEvent(new Event('error')));
  assert.match(await page.locator('.activity-availability').textContent(), /interrupted.*Reconnecting/);
  assert.equal(await operation.isVisible(), false);
  await expectState('Working', true);
  await send({available: false, reset: true, cursor: 0, items: [], message: 'Detailed activity is unavailable for this attempt.'});
  assert.equal(await page.locator('.activity-event').count(), 0);
  await expectState('Working', true);
  await send({...snapshot, reset: true, truncated: true, message: 'Earlier activity was truncated.'});
  assert.match(await page.locator('.activity-availability').textContent(), /truncated/);
  await expectState('Working', true);
  // Reconnected snapshots restore current details, never authoritative state.
  await send({...snapshot, reset: true});
  await send({available: true, cursor: 110, items: [{...running, sequence: 110, command: 'go test ./internal/webui ' + 'long-argument/'.repeat(30)}]});
  await page.evaluate(fs.readFileSync(require.resolve('axe-core/axe.min.js'), 'utf8'));
  for (const theme of ['gruvbox-light', 'gruvbox-dark', 'light', 'dark', 'icy']) {
    await page.evaluate(theme => window.Workbench.applyTheme(theme), theme);
    for (const width of [1280, 1092, 678, 390]) {
      await page.setViewportSize({width, height: 900});
      // Both the feed and its outer workspace can scroll; the status stays in view.
      await page.locator('.activity-event summary').first().click();
      await page.locator('.activity-panel').evaluate(el => { el.scrollTop = el.scrollHeight; });
      await page.locator('.run-workspace').evaluate(el => { el.scrollTop = el.scrollHeight; });
      const visible = await status.evaluate(el => {
        const box = el.getBoundingClientRect();
        return box.left >= 0 && box.right <= innerWidth + 1 && box.top >= 0 && box.bottom <= innerHeight &&
          el.contains(document.elementFromPoint(box.left + box.width / 2, box.top + 10));
      });
      assert.ok(visible, 'Execution state is obscured at ' + theme + '/' + width);
      assert.equal(await page.locator('body').evaluate(el => el.scrollWidth <= innerWidth), true);
      await page.screenshot({path: path.join(temporary, `${theme}-${width}.png`)});
    }
    const violations = await page.evaluate(async () => (await axe.run(document.querySelector('.execution-status'))).violations);
    assert.deepEqual(violations.map(v => v.id), [], 'Execution summary accessibility in ' + theme);
  }
  await page.emulateMedia({reducedMotion: 'reduce'});
  assert.equal(await status.locator('.execution-indicator').evaluate(el => getComputedStyle(el).animationName), 'none');
  await page.emulateMedia({reducedMotion: 'no-preference'});
  await page.setViewportSize({width: 1280, height: 900});
  await page.getByRole('button', {name: 'Stop after', exact: true}).click();
  await page.locator('.execution-stop-after').waitFor();
  await expectState('Working', true);
  assert.equal(await page.getByRole('button', {name: 'Stop after', exact: true}).isDisabled(), true);
  await send({...snapshot, reset: true});
  fs.writeFileSync(path.join(repo, 'fake-complete'), 'done');
  await expectState('Finished', false);
  await send({available: true, cursor: 200, items: [{...running, sequence: 200}]});
  await expectState('Finished', false);
  assert.equal(cli('show', 'state-1').status, 'closed');
  // Real stop-now intent is saved while the deterministic peer is gated.
  fs.unlinkSync(path.join(repo, 'fake-complete'));
  await page.getByRole('button', {name: '▷ Start next', exact: true}).click();
  await expectState('Working', true);
  await until(() => page.locator('.activity-event').count().then(count => count >= 5), 'Next run activity');
  await page.getByRole('button', {name: 'Stop now', exact: true}).click();
  await expectState('Stopping', false);
  await expectState('Interrupted', false);
  await stop(); await start();
  await page.goto(origin + '/projects/state/tasks?workspace=1');
  await expectState('Interrupted', false);
  await until(() => page.locator('.activity-availability').textContent().then(text => /unavailable|not retained/.test(text)), 'Restart history unavailable');
  await page.getByRole('button', {name: 'Resume', exact: true}).waitFor();
  assert.equal(await page.locator('.run-follow-up').count(), 0, 'Restart resumed work');
  cli('release', 'state-2');
  for (const mode of ['schedule_input_live', 'schedule_approval_live', 'schedule_wrong_report']) {
    fs.writeFileSync(path.join(repo, 'fake-mode'), mode);
    await page.goto(origin + '/projects/state/tasks?workspace=1');
    await page.getByRole('button', {name: '▷ Start next', exact: true}).click();
    if (mode === 'schedule_wrong_report') {
      await expectState('Needs attention', false);
      break;
    }
    await expectState(mode === 'schedule_input_live' ? 'Waiting for input' : 'Waiting for approval', false);
    await send({available: true, cursor: 300, items: [{...running, sequence: 300}]});
    assert.equal(await operation.isVisible(), false, 'Stale activity overrode a human wait');
    if (mode === 'schedule_input_live') {
      await page.locator('.run-interaction textarea[name="answer.note"]').fill('Preserve this answer');
      await page.locator('.run-interaction input[name="answer.choice"]').fill('Focused');
      await page.getByRole('button', {name: 'Send exact answers', exact: true}).click();
    } else await page.getByRole('button', {name: 'Approve once', exact: true}).click();
    await expectState('Finished', false);
    cli('add', 'Next execution state case');
  }
  assert.deepEqual(errors, []);
  console.log('PASS execution state, operations, message labels, waits, stops, outcomes, reconnect, history, drafts, themes, responsive layout and accessibility');
  console.log('Visual artifacts: ' + temporary);
})().catch(error => { console.error(error); process.exitCode = 1; }).finally(async () => {
  if (browser) await browser.close();
  await stop();
});
