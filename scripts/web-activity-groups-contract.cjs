const assert = require('node:assert/strict');
const path = require('node:path');

// Controlled transport snapshots delivered to the real execution panel. The
// caller owns a gated disposable run; no model calls or durable state changes.
module.exports = async function activityGroups({page, send, temporary, until}) {
  const feed = page.locator('.activity-panel');
  const rows = page.locator('[data-activity-events] > *');
  const groups = page.locator('.activity-group');
  const event = id => page.locator(`[data-event-id="${id}"]`);
  let sequence = 500;
  const command = (id, status = 'completed') => ({id, sequence: ++sequence,
    kind: 'command', status, title: 'Command', command: 'go test ./internal/' + id, output: 'retained output ' + id});
  const file = (id, kind, filePath) => ({...command(id), kind, path: filePath,
    title: kind === 'file_read' ? 'Read file' : 'File change', diff: kind === 'file_change' ? '+ retained diff' : undefined});
  const publish = (items, extra = {}) => send({available: true, cursor: ++sequence, items, ...extra});
  const first = command('group-a');
  await publish([first], {reset: true});
  await event(first.id).locator('summary').focus();
  await page.keyboard.press('Enter');
  await event(first.id).locator('pre').last().evaluate(el => {
    const range = document.createRange(); range.selectNodeContents(el);
    getSelection().removeAllRanges(); getSelection().addRange(range);
  });
  const second = command('group-b', 'running');
  await publish([second]);
  assert.equal(await groups.count(), 1);
  assert.equal(await groups.first().evaluate(el => el.open), true, 'Promotion hid the open child');
  assert.equal(await event(first.id).locator('summary').evaluate(el => el === document.activeElement), true);
  assert.equal(await event(first.id).evaluate(el => el.open), true);
  assert.equal(await page.evaluate(() => getSelection().toString()), 'retained output group-a', 'Promotion lost selected child output');
  await page.evaluate(() => getSelection().removeAllRanges());
  const groupID = await groups.first().getAttribute('id');
  const group = page.locator('#' + groupID), summary = group.locator(':scope > summary');
  assert.match(await summary.textContent(), /2 commands.*1 in progress.*Current: go test .*group-b/);
  await summary.focus();
  await page.keyboard.press('Space');
  assert.equal(await group.evaluate(el => el.open), false);
  const failed = {...second, sequence: ++sequence, status: 'failed', exit_code: 1};
  await publish([failed, command('group-c', 'running')]);
  assert.equal(await group.evaluate(el => el.open), false);
  assert.equal(await summary.evaluate(el => el === document.activeElement), true);
  assert.match(await summary.textContent(), /3 commands.*1 in progress · 1 failed.*group-c/);
  assert.equal(await event(second.id).count(), 1, 'Same-ID replacement duplicated a member');
  await page.keyboard.press('Enter');
  await event(second.id).locator('summary').click();
  assert.match(await event(second.id).textContent(), /retained output group-b.*Exit 1/);

  const boundaryKinds = ['message', 'question', 'approval', 'turn', 'progress', 'review', 'tool', 'diff'];
  const items = [first, failed, command('group-c', 'running')];
  for (const [i, kind] of boundaryKinds.entries()) {
    items.push({...command('boundary-' + i), kind, title: kind, text: 'Individually readable ' + kind},
      command('after-' + i + '-a'), command('after-' + i + '-b'));
  }
  items.push(file('read-a', 'file_read', '/repo/a.go'), file('read-b', 'file_read', '/repo/a.go'),
    file('read-c', 'file_read', '/repo/b.go'), file('change-a', 'file_change', '/repo/a.go'),
    file('change-b', 'file_change', '/repo/a.go'), file('change-c', 'file_change', '/repo/b.go'),
    {...command('action-required'), status: 'awaiting_input'}, command('last-a'), command('last-b'));
  await publish(items, {reset: true});
  assert.equal(await groups.count(), 12);
  assert.equal(await rows.count(), 21);
  assert.deepEqual(await page.locator('.activity-event').evaluateAll(nodes => nodes.map(node => node.dataset.eventId)), items.map(item => item.id));
  assert.equal(await event('boundary-0').isVisible(), true, 'Commentary was collapsed');
  assert.equal(await event('action-required').isVisible(), true, 'Action-required tool was grouped');
  const reads = groups.filter({has: event('read-a')});
  const changes = groups.filter({has: event('change-a')});
  assert.match(await reads.locator(':scope > summary').textContent(), /3 file reads · 2 reported paths/);
  assert.match(await changes.locator(':scope > summary').textContent(), /3 file changes · 2 reported paths/);
  assert.equal(await group.evaluate(el => el.open), true);
  assert.equal(await event(second.id).evaluate(el => el.open), true);
  await summary.focus();
  await page.evaluate(() => window.activitySources.at(-1).dispatchEvent(new Event('error')));
  assert.equal(await page.locator('.activity-notice').isVisible(), true);
  await publish(items, {reset: true});
  assert.equal(await summary.evaluate(el => el === document.activeElement), true);
  assert.equal(await event(second.id).evaluate(el => el.open), true, 'Snapshot replay lost child expansion');
  // Visible child anchoring survives earlier output height changes and removal
  // of the group's original first member. Group identity is retained by overlap.
  await event(second.id).locator('summary').focus();
  await feed.evaluate(el => { el.scrollTop += el.querySelector('[data-event-id="group-b"]').getBoundingClientRect().top - el.getBoundingClientRect().top; });
  const before = await event(second.id).evaluate(el => el.getBoundingClientRect().top);
  await publish([{...first, output: 'Earlier output\n'.repeat(50)}]);
  const after = await event(second.id).evaluate(el => el.getBoundingClientRect().top);
  assert.ok(Math.abs(after - before) < 2, 'Earlier child output moved the reader: ' + before + ' → ' + after);
  await publish(items.slice(1), {reset: true, truncated: true});
  assert.equal(await group.evaluate(el => el.open), true);
  assert.equal(await event(second.id).evaluate(el => el.open), true);
  assert.equal(await event(second.id).locator('summary').evaluate(el => el === document.activeElement), true);
  assert.ok(Math.abs(await event(second.id).evaluate(el => el.getBoundingClientRect().top) - before) < 2, 'Eviction moved the reader inside a group');
  assert.match(await page.locator('.activity-notice').textContent(), /truncated/);
  // A late completion of an evicted starting item must not reuse its former
  // group's still-live disclosure ID or merge across intervening commentary.
  await publish([{id: 'late-boundary', kind: 'message', status: 'completed', text: 'A later turn update'},
    first, command('late-companion')]);
  const groupIDs = await groups.evaluateAll(nodes => nodes.map(node => node.id));
  assert.equal(new Set(groupIDs).size, groupIDs.length, 'Late completion reused a retained group identity');
  assert.equal(await group.evaluate(el => el.open), true);
  assert.equal(await groups.last().evaluate(el => el.open), false);

  // Long feeds in the actual panel, all themes and supported widths. Keep one
  // group open to verify nested controls and another collapsed with failures.
  const long = [];
  for (let i = 0; i < 8; i++) {
    long.push({id: 'long-message-' + i, sequence: ++sequence, kind: 'message', status: 'completed',
      title: 'Agent update', text: 'Checked the retained details. The next operations verify the changes.'});
    for (let j = 0; j < 8; j++) long.push({...command('long-command-' + i + '-' + j, j === 2 ? 'failed' : j === 7 ? 'running' : 'completed'),
      command: 'go test ./internal/' + 'long-path/'.repeat(30) + 'package'});
    for (let j = 0; j < 4; j++) long.push(file('long-file-' + i + '-' + j, 'file_change', '/repo/' + 'long-path/'.repeat(25) + (j % 2) + '.go'));
  }
  await publish(long, {reset: true});
  await groups.first().locator(':scope > summary').click();
  await event('long-command-0-0').locator('summary').click();
  for (const theme of ['gruvbox-light', 'gruvbox-dark', 'light', 'dark', 'icy']) {
    await page.evaluate(theme => window.Workbench.applyTheme(theme), theme);
    for (const width of [1280, 1092, 678, 390]) {
      await page.setViewportSize({width, height: 900});
      await page.locator('.run-workspace').evaluate(el => { el.scrollTop = 0; });
      await feed.evaluate(el => { el.scrollTop = 0; });
      assert.equal(await feed.evaluate(el => el.scrollWidth <= el.clientWidth + 1), true, 'Group overflow at ' + theme + '/' + width);
      assert.equal(await page.locator('body').evaluate(el => el.scrollWidth <= innerWidth), true);
      await groups.first().locator(':scope > summary').focus();
      await page.keyboard.press('Tab');
      await page.keyboard.press('Shift+Tab');
      await page.evaluate(() => new Promise(requestAnimationFrame));
      assert.notEqual(await groups.first().locator(':scope > summary').evaluate(el => getComputedStyle(el).outlineStyle), 'none');
      assert.equal(await groups.first().locator(':scope > summary').evaluate(el => {
        const box = el.getBoundingClientRect();
        return box.top >= 0 && box.bottom <= innerHeight &&
          el.contains(document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2));
      }), true, 'Focused group is obscured at ' + theme + '/' + width);
      await page.screenshot({path: path.join(temporary, `groups-${theme}-${width}.png`)});
      await page.keyboard.press('Enter');
      assert.equal(await groups.first().evaluate(el => el.open), false);
      await page.screenshot({path: path.join(temporary, `groups-collapsed-${theme}-${width}.png`)});
      await page.keyboard.press('Enter');
    }
  }
  await page.setViewportSize({width: 1280, height: 900});
  // Appends follow only when the reader is at the end; an open trailing group
  // grows in place, retaining focus and horizontal output scrolling.
  await groups.last().locator(':scope > summary').click();
  const last = event('long-file-7-3');
  await last.locator('summary').focus();
  const lastTop = await last.evaluate(el => el.getBoundingClientRect().top);
  await publish([file('live-file', 'file_change', '/repo/new.go')]);
  assert.equal(await last.locator('summary').evaluate(el => el === document.activeElement), true);
  const appendedTop = await last.evaluate(el => el.getBoundingClientRect().top);
  assert.ok(Math.abs(appendedTop - lastTop) < 2, 'Append moved focused child: ' + lastTop + ' → ' + appendedTop);
  assert.equal(await groups.last().evaluate(el => el.open), true);
  await page.locator('.run-follow-up textarea').focus();
  await feed.evaluate(el => { el.scrollTop = el.scrollHeight; });
  await publish([file('live-file-2', 'file_change', '/repo/new.go')]);
  assert.ok(await feed.evaluate(el => el.scrollHeight - el.clientHeight - el.scrollTop < 2));

  // The browser bound counts children, not groups. Reset/unavailable history
  // clears both levels and cannot resurrect stale members or their notices.
  await publish(Array.from({length: 140}, (_, i) => command('bounded-' + i)), {reset: true, truncated: true});
  assert.equal(await page.locator('.activity-event').count(), 128);
  assert.match(await groups.first().locator(':scope > summary').textContent(), /128 commands/);
  assert.equal(await page.locator('[data-activity-count]').textContent(), '128 events');
  assert.equal(await page.locator('.activity-notice').isVisible(), true);
  await publish([], {available: false, reset: true});
  assert.equal(await rows.count(), 0);
  assert.match(await page.locator('.activity-notice').textContent(), /unavailable/);
  await publish([command('fresh-a'), command('fresh-b')], {reset: true});
  assert.equal(await groups.first().evaluate(el => el.open), false);
  assert.equal(await page.locator('.activity-event').count(), 2);
  await groups.first().locator(':scope > summary').click();
  await event('fresh-a').locator('summary').click();
  const savedGroup = await groups.first().getAttribute('id');
  const originalURL = await feed.getAttribute('data-activity-url');
  await page.evaluate(() => { window.previousActivitySource = window.activitySources.at(-1); });
  await page.locator('[aria-label=Workspaces] .workspace-link').nth(1).click();
  await until(() => feed.getAttribute('data-activity-url').then(url => !url), 'Idle workspace activity');
  await page.evaluate(() => window.previousActivitySource.dispatchEvent(new MessageEvent('pellets-activity', {
    data: JSON.stringify({available: true, items: [{id: 'wrong-workspace', kind: 'command', status: 'running'}]})
  })));
  assert.equal(await rows.count(), 0, 'The prior workspace stream leaked into the selected workspace');
  // Supply the same retained snapshot on reconnect, exercising the real
  // workspace navigation/cache path without replacing authoritative run state.
  const replay = {available: true, reset: true, truncated: true, cursor: ++sequence,
    items: [command('fresh-a'), command('fresh-b')]};
  const routePattern = '**' + originalURL + '?*';
  await page.route(routePattern, async route => {
    await page.evaluate(() => {
      const source = window.activitySources.at(-1);
      source.addEventListener('pellets-activity', () => source.close(), {once: true});
    });
    await route.fulfill({status: 200, contentType: 'text/event-stream',
      body: 'event: pellets-activity\ndata: ' + JSON.stringify(replay) + '\n\n'});
  });
  await page.locator('[aria-label=Workspaces] .workspace-link').first().click();
  await until(() => groups.count().then(count => count === 1), 'Restored workspace group');
  await until(() => page.locator('.activity-notice').textContent().then(text => text.includes('truncated')), 'Reconnect truncation notice');
  await page.evaluate(() => window.activitySources.at(-1).close());
  await page.unroute(routePattern);
  assert.equal(await groups.first().getAttribute('id'), savedGroup);
  assert.equal(await groups.first().evaluate(el => el.open), true);
  assert.equal(await event('fresh-a').evaluate(el => el.open), true);
};
