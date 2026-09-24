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
  cli('init-db');
  cli('add', 'Keep execution state visible');
  cli('skill', 'install', '--scope', 'repo', '--agent', 'codex', '--yes');
  const otherWorkspace = path.join(temporary, 'other-workspace');
  git('worktree', 'add', '-qb', 'other-workspace', otherWorkspace);
  execFileSync(binary, ['project', 'show'], {cwd: otherWorkspace, env});
  fs.writeFileSync(path.join(repo, 'fake-mode'), 'schedule_activity_gate');
  await start();
  const engine = process.env.PLAYWRIGHT_BROWSER === 'webkit' ? webkit : chromium;
  browser = await engine.launch({headless: true});
  const page = await browser.newPage({viewport: {width: 1280, height: 900}});
  page.setDefaultTimeout(15000);
  const errors = [], remoteRequests = [];
  page.on('request', request => { if (!request.url().startsWith(origin + '/')) remoteRequests.push(request.url()); });
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
  assert.equal(await page.locator('.activity-message .event-heading').filter({hasText: 'Agent update'}).count(), 1);
  assert.equal(await page.locator('.activity-event .event-heading').filter({hasText: /Codex completed|Agent update.*Completed/}).count(), 0);
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
  await require('./web-activity-groups-contract.cjs')({page, send, temporary, until});
  await require('./web-activity-summary-contract.cjs')({page, send, temporary, repo});
  // Mixed feed: completed commentary is primary reading content; tool rows
  // remain compact disclosures. All text still crosses the shared safe renderer.
  const prose = 'Stable `group_id` values keep **context** attached to the *same group*.\n\n' +
    '- Use `--context-file` to load a document.\n- Use `-` to read stdin.\n\n' +
    '1. Read the [guide](https://example.invalid/guide).\n2. Verify the result.\n\n' +
    '```go\nfunc main() {\n  return // ' + 'long/path/'.repeat(35) + '\n}\n```\n\n' +
    'Path: `' + 'workspace/subdirectory/'.repeat(25) + 'context.md`\n\n' +
    '<img src="https://example.invalid/image" onerror="alert(1)">\n\n' +
    '[unsafe](javascript:alert%281%29) [encoded](javascript&#58;alert%281%29) ' +
    '[local](file:///etc/passwd) ![image alternative](https://example.invalid/image)';
  const mixed = Array.from({length: 24}, (_, i) => i % 3 === 0 ?
    {id: 'mixed-' + i, sequence: 200 + i, kind: 'message', title: 'Agent update', status: 'completed',
      text: i === 0 ? prose : 'I checked `group_id` and the context flags.\n\nThe next check covers stdin and long paths.'} :
    {id: 'mixed-' + i, sequence: 200 + i, kind: i % 3 === 1 ? 'file_read' : 'command',
      title: i % 3 === 1 ? 'Read file' : 'Command', status: 'completed',
      path: i % 3 === 1 ? 'internal/' + 'long-path/'.repeat(20) + 'context.go' : undefined,
      output: 'package context\nfunc read() { return }', exit_code: 0});
  mixed[3] = {...mixed[3], text: '', status: 'running'};
  mixed[6] = {...mixed[6], text: '   '};
  mixed[9] = {...mixed[9], title: 'Agent response'};
  const sendMixed = items => send({available: true, cursor: 250, items});
  await send({available: true, reset: true, cursor: 250, items: mixed});
  const message = page.locator('[data-event-id="mixed-0"]');
  assert.equal(await message.locator('summary').count(), 0, 'Commentary must not require disclosure');
  assert.equal(await message.locator('.markdown-body > p code').first().textContent(), 'group_id');
  assert.equal(await message.locator('strong').textContent(), 'context');
  assert.equal(await message.locator('em').textContent(), 'same group');
  assert.equal(await message.locator('ul li').count(), 2);
  assert.equal(await message.locator('ol li').count(), 2);
  assert.ok(await message.locator('pre .code-keyword').count() > 0);
  assert.ok(await message.locator('pre code').evaluate(el => parseFloat(getComputedStyle(el).fontSize) >= 12.5), 'Fenced commentary code must retain readable Markdown typography');
  assert.equal(await message.locator('a').count(), 1, 'Unsafe link schemes must remain inert');
  assert.equal(await message.locator('a').getAttribute('rel'), 'noopener noreferrer');
  assert.equal(await message.locator('img,script,iframe').count(), 0);
  assert.match(await message.textContent(), /<img src=/, 'HTML remains literal text');
  assert.match(await page.locator('[data-event-id="mixed-3"]').textContent(), /Waiting for the complete message/);
  assert.match(await page.locator('[data-event-id="mixed-6"]').textContent(), /No message text was reported/);
  assert.equal(await page.locator('[data-event-id="mixed-9"] h4').textContent(), 'Agent response');
  assert.equal(await message.locator('.markdown-body').evaluate(el => getComputedStyle(el).fontSize), '14px');
  assert.equal(await page.locator('.run-details').evaluate(el => el.open), false);
  await page.locator('.run-details > summary').focus();
  await page.keyboard.press('Enter');
  assert.equal(await page.locator('.run-details').evaluate(el => el.open), true);
  assert.match(await page.locator('.run-details').textContent(), /Frozen filters/);
  await page.keyboard.press('Enter');

  const feed = page.locator('.activity-panel');
  await feed.evaluate(el => { el.scrollTop = 0; });
  await message.locator('a').focus();
  // A completion updates the same item in its first-observed position, even
  // when its revision sequence is newer than all subsequent entries.
  mixed[3] = {...mixed[3], sequence: 300, status: 'completed', text: 'The complete `stdin` check passed.'};
  await sendMixed([mixed[3]]);
  assert.equal(await message.locator('a').evaluate(el => el === document.activeElement), true);
  assert.equal(await page.locator('.activity-event').nth(3).getAttribute('data-event-id'), 'mixed-3');
  await message.locator('code').first().evaluate(el => {
    const selection = getSelection(), range = document.createRange();
    range.selectNodeContents(el); selection.removeAllRanges(); selection.addRange(range);
    window.selectedActivityText = el.firstChild;
  });
  await send({available: true, reset: true, cursor: 301, items: mixed});
  assert.equal(await page.evaluate(() => getSelection().toString()), 'group_id');
  assert.equal(await message.locator('code').first().evaluate(el => el.firstChild === window.selectedActivityText), true);
  cli('add', 'Refresh while reading commentary');
  await until(() => page.locator('.task-title').filter({hasText: 'Refresh while reading'}).count().then(count => count === 1), 'Database patch while selecting commentary');
  assert.equal(await page.evaluate(() => getSelection().toString()), 'group_id');
  assert.equal(await message.locator('a').evaluate(el => el === document.activeElement), true);
  await page.evaluate(() => getSelection().removeAllRanges());
  const code = message.locator('pre');
  await code.focus();
  await code.evaluate(el => { el.scrollLeft = 200; });
  const codeLeft = await code.evaluate(el => el.scrollLeft);
  assert.ok(codeLeft > 0, 'Long code must scroll inside its own block');
  await sendMixed([mixed[0]]);
  assert.equal(await code.evaluate(el => el === document.activeElement), true);
  assert.equal(await code.evaluate(el => el.scrollLeft), codeLeft);

  const tool = page.locator('[data-event-id="mixed-1"]');
  await tool.locator('summary').click();
  await tool.locator('summary').focus();
  await sendMixed([{...mixed[1], sequence: 302, status: 'failed'}]);
  assert.equal(await tool.evaluate(el => el.open), true);
  assert.equal(await tool.locator('summary').evaluate(el => el === document.activeElement), true);
  // Preserve the visible event's position when an earlier, expanded output
  // changes height or retention evicts content above the reader.
  const anchor = page.locator('[data-event-id="mixed-9"]');
  await feed.evaluate(el => { el.scrollTop += el.querySelector('[data-event-id="mixed-9"]').getBoundingClientRect().top - el.getBoundingClientRect().top; });
  const anchorTop = await anchor.evaluate(el => el.getBoundingClientRect().top);
  mixed[1] = {...mixed[1], output: 'long output\n'.repeat(70)};
  await sendMixed([mixed[1]]);
  assert.ok(Math.abs(await anchor.evaluate(el => el.getBoundingClientRect().top) - anchorTop) < 2, 'Earlier output moved the reader');
  await send({available: true, truncated: true, reset: true, cursor: 303, items: mixed.slice(6), message: 'Earlier activity was truncated.'});
  assert.ok(Math.abs(await anchor.evaluate(el => el.getBoundingClientRect().top) - anchorTop) < 2, 'Retention moved the reader');
  assert.equal(await page.locator('.activity-notice').isVisible(), true);
  await send({available: true, reset: true, cursor: 304, items: mixed});
  await draft.fill('Keep my mixed-feed follow-up'); await draft.press('ArrowLeft');
  const mixedCaret = await draft.evaluate(el => el.selectionStart);
  await feed.evaluate(el => { el.scrollTop = 75; });
  const beforeAppend = await feed.evaluate(el => el.scrollTop);
  await sendMixed(Array.from({length: 9}, (_, i) => ({...mixed[0], id: 'added-' + i, sequence: 310 + i, text: 'Another progress update.'})));
  assert.equal(await page.locator('.activity-event').count(), 33);
  assert.equal(await feed.evaluate(el => el.scrollTop), beforeAppend, 'New events pulled the reader to the bottom');
  assert.equal(await draft.inputValue(), 'Keep my mixed-feed follow-up');
  assert.equal(await draft.evaluate((el, caret) => el === document.activeElement && el.selectionStart === caret, mixedCaret), true);
  await feed.evaluate(el => { el.scrollTop = el.scrollHeight; });
  await sendMixed([{...mixed[0], id: 'following', sequence: 320, text: 'Following the latest update.'}]);
  assert.ok(await feed.evaluate(el => el.scrollHeight - el.clientHeight - el.scrollTop < 2), 'A reader at the end should follow new activity');
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
      await page.locator('.run-workspace').evaluate(el => { el.scrollTop = 0; });
      await feed.evaluate(el => { el.scrollTop = 0; });
      assert.equal(await message.locator('.markdown-body').evaluate(el => el.scrollWidth <= el.clientWidth + 1), true, 'Long prose escapes its reading area');
      await page.screenshot({path: path.join(temporary, `commentary-${theme}-${width}.png`)});
    }
    const violations = await page.evaluate(async () => (await axe.run(document.querySelector('.execution-status'))).violations);
    assert.deepEqual(violations.map(v => v.id), [], 'Execution summary accessibility in ' + theme);
  }
  for (const width of [1280, 1092, 678, 390]) {
    await page.setViewportSize({width, height: 480});
    assert.equal(await status.evaluate(el => getComputedStyle(el).position), 'static');
    assert.equal(await draft.evaluate(el => getComputedStyle(el.closest('form')).position), 'static');
    for (const control of [page.getByRole('button', {name: 'Stop now', exact: true}), message.locator('a'), draft]) {
      await control.focus();
      await until(() => control.evaluate(el => {
        const box = el.getBoundingClientRect();
        return box.top >= 0 && box.bottom <= innerHeight && box.left >= 0 && box.right <= innerWidth &&
          el.contains(document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2));
      }), 'Short layout clipped or obscured a focused ' + await control.evaluate(el => el.tagName) + ' at ' + width);
      await page.screenshot({path: path.join(temporary, `short-focus-${width}-${await control.evaluate(el => el.tagName)}.png`)});
    }
    await page.screenshot({path: path.join(temporary, `short-${width}.png`)});
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
  assert.equal(await page.locator('[data-event-id="fresh-a"], [data-event-id="mixed-0"]').count(), 0, 'Another execution attempt reused prior activity');
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
  assert.deepEqual(remoteRequests, [], 'Activity must never request remote rendering or image assets');
  console.log('PASS execution state, safe Markdown, mixed feeds, selection, focus, scroll anchoring, metadata, waits, stops, recovery, drafts, five themes, responsive/short layouts and accessibility');
  console.log('Visual artifacts: ' + temporary);
})().catch(error => { console.error(error); process.exitCode = 1; }).finally(async () => {
  if (browser) await browser.close();
  await stop();
});
