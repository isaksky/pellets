const assert = require('node:assert/strict');
const path = require('node:path');
const fs = require('node:fs');

module.exports = async function activitySummaries({page, send, temporary, repo}) {
  const panel = page.locator('.activity-panel'), groups = page.locator('.activity-group');
  const event = id => page.locator(`[data-event-id="${id}"]`);
  const heading = id => event(id).locator(':scope > summary');
  let sequence = 900;
  const command = (id, status, fields = {}) => ({id, sequence: ++sequence, kind: 'command',
    title: 'Command', status, command: 'go test ./internal/' + id, ...fields});
  const publish = (items, reset = false) => send({available: true, cursor: ++sequence, items, reset});
  // Resolve roots from production markup. Do not infer roots from event paths.
  repo = fs.realpathSync(repo);
  assert.equal(await panel.getAttribute('data-workspace-root'), repo);
  const files = [
    ['read', repo + '/internal/webui/config.go'],
    ['duplicate', repo + '/internal/app/config.go'],
    ['long-a', repo + '/' + 'shared-directory/'.repeat(20) + 'client/settings.go'],
    ['long-b', repo + '/' + 'shared-directory/'.repeat(20) + 'server/settings.go'],
    ['outside', repo + '-other/internal/config.go'],
    ['escape', repo + '/../other/config.go'],
    ['redacted-path', repo + '/[redacted]/config.go'],
  ].map(([id, filePath]) => ({id, sequence: ++sequence, kind: 'file_read', title: 'Read file',
    status: 'completed', path: filePath, source: 'package config\n// safe source'}));
  const failure = command('failed-check', 'failed', {exit_code: 2, output: 'reported diagnostic output'});
  const items = [failure, command('success', 'completed', {exit_code: 0, output: 'ok'}),
    command('active', 'running', {command: 'go test\n  ./internal/webui --token [redacted]'}),
    ...files,
    {id: 'change', sequence: ++sequence, kind: 'file_change', title: 'File change', status: 'completed',
      path: repo + '/internal/webui/workbench.js', diff: '@@ -1 +1 @@\n-old\n+new'},
    {id: 'diff', sequence: ++sequence, kind: 'diff', title: 'Reported turn diff', status: 'reported', diff: '@@ -1 +1 @@\n-old\n+new'},
    command('missing', 'failed', {command: ''}),
    {id: 'retry', sequence: ++sequence, kind: 'error', title: 'Runtime error', status: 'retrying', error: 'Connection lost; [redacted]'},
    {id: 'recovery-report', sequence: ++sequence, kind: 'message', title: 'Agent update', status: 'completed',
      text: 'The connection recovered after the reported retry. The command failure still needs investigation.'},
    {id: 'tool-error', sequence: ++sequence, kind: 'tool', title: 'Tool: inspect', status: 'failed',
      error: 'Connection refused; [redacted]', text: 'Retained tool context'},
    {id: 'no-retry', sequence: ++sequence, kind: 'error', status: 'not_retrying', error: 'Connection lost'},
    {id: 'turn-error', sequence: ++sequence, kind: 'turn', status: 'failed', title: 'Turn failed', text: 'Reported turn error'},
    command('input', 'awaiting_input'), command('tail-a', 'completed'), command('tail-b', 'completed')];
  await publish(items, true);
  assert.equal(await page.locator('.execution-state-label').textContent(), 'Working', 'Reported failure ended the run');
  const groupSummary = groups.first().locator(':scope > summary');
  assert.match(await groupSummary.textContent(), /1 in progress · 1 failed/);
  assert.match(await groupSummary.textContent(), /Last failure: go test .*failed-check.*exit 2.*Impact unknown/);
  assert.doesNotMatch(await groupSummary.textContent(), /recovered|retrying|blocking/i);
  await groupSummary.click();
  assert.match(await heading('failed-check').textContent(), /go test .*failed-check.*Failed · exit 2.*Impact unknown/);
  assert.match(await heading('success').textContent(), /go test .*success.*Completed · exit 0/);
  assert.match(await heading('active').textContent(), /go test .\/internal\/webui --token \[redacted\].*In progress/);
  await heading('active').click();
  assert.match(await event('active').locator('.event-body').textContent(), /Output not reported.*Exit code not reported/);
  assert.match(await event('active').locator('pre').textContent(), /go test\n  /, 'Expanded command lost reported formatting');
  await heading('failed-check').click();
  assert.match(await event('failed-check').locator('.event-body').textContent(), /reported diagnostic output.*Exit 2/);
  await publish([command('active', 'completed', {exit_code: 0, output: 'ok'})]);
  assert.match(await groupSummary.textContent(), /1 failed.*failed-check.*Impact unknown/);
  assert.doesNotMatch(await groupSummary.textContent(), /recovered|retrying|blocking/i);
  const reads = groups.filter({has: event('read')});
  await reads.locator(':scope > summary').click();
  assert.equal(await event('read').locator('.event-path').textContent(), 'internal/webui/config.go');
  assert.equal(await event('duplicate').locator('.event-path').textContent(), 'internal/app/config.go');
  assert.match(await heading('long-a').textContent(), /…\/client\/settings.go/);
  assert.match(await heading('long-b').textContent(), /…\/server\/settings.go/);
  for (const id of ['outside', 'escape', 'redacted-path']) {
    assert.match(await event(id).locator('.event-path').textContent(), /^Absolute path:/);
  }
  for (const file of files) {
    await heading(file.id).click();
    assert.equal(await event(file.id).locator('.event-full-path').textContent(), 'Reported path: ' + file.path);
    assert.match(await event(file.id).locator('pre').textContent(), /package config/);
    await heading(file.id).click();
  }
  assert.equal(await event('change').locator('.event-path').textContent(), 'internal/webui/workbench.js');
  await heading('change').click();
  assert.deepEqual(await event('change').locator('.source-line').allTextContents(), ['@@ -1 +1 @@', '-old', '+new']);
  assert.equal(await heading('diff').textContent(), 'Turn changesReported diff');
  await heading('diff').click();
  assert.deepEqual(await event('diff').locator('.source-line').allTextContents(), ['@@ -1 +1 @@', '-old', '+new']);
  assert.match(await heading('missing').textContent(), /Command not reported.*Failed · exit not reported.*Impact unknown/);
  await heading('missing').click();
  assert.match(await event('missing').locator('.event-body').textContent(), /Output not reported.*Exit code not reported/);
  assert.match(await heading('retry').textContent(), /Retry reported.*Connection lost; \[redacted\]/);
  assert.equal(await event('recovery-report').isVisible(), true);
  assert.match(await event('recovery-report').textContent(), /connection recovered after the reported retry/);
  assert.match(await heading('tool-error').textContent(), /Tool: inspect.*Failed.*Connection refused; \[redacted\].*Impact unknown/);
  await heading('tool-error').click();
  assert.match(await event('tool-error').locator('.event-body').textContent(), /Retained tool context.*Connection refused; \[redacted\]/);
  assert.match(await heading('no-retry').textContent(), /No retry planned \(reported\).*See current execution/);
  assert.match(await heading('turn-error').textContent(), /Turn.*Failed.*Reported turn error.*Turn ended with an error/);
  assert.equal(await event('input').locator('xpath=..').getAttribute('class'), 'activity-events', 'Pending action was grouped');
  assert.match(await heading('input').textContent(), /Input requested.*run’s input and approval controls/);
  // Cross-platform and missing-root path behavior through the production module.
  await page.evaluate(async () => {
    const source = document.querySelector('script[src*="/app.js"]').src;
    const {activityPath} = await import(new URL('activity-summary.js', source).href);
    const cases = [
      ['C:\\repo\\src\\a.go', 'C:\\repo', 'src/a.go'],
      ['C:\\repo-other\\a.go', 'C:\\repo', 'C:\\repo-other\\a.go'],
      ['/repo/src/a.go', '', '/repo/src/a.go'],
      ['src/a.go', '/repo', 'src/a.go'],
      ['/repo/../outside/a.go', '/repo', '/repo/../outside/a.go'],
    ];
    for (const [file, root, expected] of cases) {
      if (activityPath(file, root) !== expected) throw Error('Unexpected path summary: ' + file);
    }
  });
  // Keep failure counts and impact visible when collapsed, and confirm the
  // expanded evidence fits alongside both app sidebars in each theme/width.
  await publish([failure, command('success', 'completed', {exit_code: 0, output: 'ok'}),
    ...files.slice(0, 4), items.find(item => item.id === 'tool-error')], true);
  for (const theme of ['gruvbox-light', 'gruvbox-dark', 'light', 'dark', 'icy']) {
    await page.evaluate(theme => window.Workbench.applyTheme(theme), theme);
    for (const width of [1280, 1092, 678, 390]) {
      await page.setViewportSize({width, height: 900});
      await page.locator('.run-workspace').evaluate(el => { el.scrollTop = 0; });
      await panel.evaluate(el => { el.scrollTop = 0; });
      await groups.first().evaluate(el => { el.open = false; });
      await groups.nth(1).evaluate(el => { el.open = true; });
      // Refocus after a viewport change even when this summary already had
      // focus. This exercises keyboard scrolling and captures the actual feed
      // on phones instead of the queue above it.
      await groupSummary.focus();
      await page.keyboard.press('Tab'); await page.keyboard.press('Shift+Tab');
      await page.evaluate(() => new Promise(requestAnimationFrame));
      assert.equal(await groupSummary.evaluate(el => {
        const box = el.getBoundingClientRect();
        return box.top >= 0 && box.bottom <= innerHeight &&
          el.contains(document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2));
      }), true, 'Failure summary obscured at ' + theme + '/' + width);
      assert.equal(await panel.evaluate(el => el.scrollWidth <= el.clientWidth + 1), true, 'Summary overflow at ' + theme + '/' + width);
      assert.equal(await page.locator('body').evaluate(el => el.scrollWidth <= innerWidth), true);
      assert.match(await groupSummary.textContent(), /1 failed.*exit 2.*Impact unknown/);
      await page.screenshot({path: path.join(temporary, `summaries-${theme}-${width}.png`)});
      await groupSummary.focus(); await page.keyboard.press('Enter');
      await heading('failed-check').evaluate(el => { el.parentElement.open = true; });
      assert.equal(await event('failed-check').locator('.event-body').isVisible(), true);
      await event('failed-check').locator('pre').last().scrollIntoViewIfNeeded();
      await page.screenshot({path: path.join(temporary, `summary-evidence-${theme}-${width}.png`)});
    }
  }
  await page.setViewportSize({width: 1280, height: 900});
};
