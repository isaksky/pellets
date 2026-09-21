// Real protocol notifications, server projection and browser stream; no
// synthetic activity IDs or renderer calls can mask projection collisions.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');

module.exports = async function ({page, root, origin, until}) {
  const artifacts = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-runtime-errors-'));
  console.log('Runtime error visual artifacts: ' + artifacts);
  const rows = page.locator('[data-activity-events] > .activity-event').filter({
    has: page.locator('.event-title').filter({hasText: /^(Runtime error|Command)$/})
  });
  const groups = page.locator('.activity-group');
  const ids = () => rows.evaluateAll(nodes => nodes.map(node => node.dataset.eventId));
  const advance = name => fs.writeFileSync(path.join(root, 'fake-activity-' + name), 'continue');
  await until(() => rows.count().then(count => count === 2), 'Initial error and command');
  const url = origin + await page.locator('.activity-panel').getAttribute('data-activity-url');
  const read = async (after = 0) => (await page.request.get(url + '?after=' + after)).json();
  const reported = snapshot => snapshot.items.filter(item => ['error', 'command'].includes(item.kind));
  const first = await read();
  assert.deepEqual(reported(first).map(item => item.kind), ['error', 'command']);
  assert.match(await rows.first().locator('.event-status').textContent(), /Retry reported/);
  assert.match(await rows.first().locator('.event-error').textContent(), /Connection lost; \[redacted\]/);
  const firstIDs = await ids();
  // Keep a disclosure open while later reports arrive and reconnect.
  await rows.first().locator('summary').click();
  advance('next');
  await until(async () => (await read()).cursor >= first.cursor + 2, 'Second error and command publication');
  const second = await read();
  await until(() => page.locator('[data-activity-count]').textContent().then(text => text === second.items.length + ' events'), 'Second live update');
  await page.screenshot({path: path.join(artifacts, 'live.png')});
  assert.deepEqual(reported(second).map(item => item.kind), ['error', 'command', 'error', 'command']);
  const expectedIDs = reported(second).map(item => item.id);
  assert.equal(new Set(expectedIDs).size, 4);
  assert.deepEqual(expectedIDs.slice(0, 2), firstIDs);
  const check = async () => {
    assert.deepEqual(await ids(), expectedIDs, 'Chronology changed or commands grouped across an error');
    assert.equal(await groups.count(), 0);
    assert.match(await rows.nth(0).locator('.event-status').textContent(), /Retry reported/);
    assert.match(await rows.nth(2).locator('.event-status').textContent(), /No retry planned/);
    assert.match(await rows.nth(2).locator('.event-error').textContent(), /Retry exhausted/);
    assert.ok(!(await page.content()).includes('private-activity-value'));
  };
  await check();
  assert.equal(await rows.first().evaluate(el => el.open), true);
  const delta = await read(first.cursor);
  assert.equal(delta.reset, false);
  assert.deepEqual(delta.items, reported(second).slice(2));

  // End one transport response after a real retained snapshot. Native
  // EventSource must reconnect with its cursor, exercising the server's
  // Last-Event-ID path and the production renderer's retained Map.
  await page.addInitScript(() => {
    const NativeSource = window.EventSource;
    window.replayedActivity = [];
    window.EventSource = class extends NativeSource {
      constructor(url, options) {
        super(url, options);
        if (url.includes('/activity?')) this.addEventListener('pellets-activity', event => {
          window.replayedActivity.push(JSON.parse(event.data));
        });
      }
    };
  });
  let connections = 0;
  const streamPattern = '**' + new URL(url).pathname + '?stream=1*';
  await page.route(streamPattern, async route => {
    connections++;
    if (connections === 1) {
      await route.fulfill({status: 200, contentType: 'text/event-stream',
        body: 'retry: 100\nid: ' + second.cursor + '\nevent: pellets-activity\ndata: ' + JSON.stringify(second) + '\n\n'});
    } else {
      await route.continue();
    }
  });
  await page.reload();
  await until(() => rows.count().then(count => count === 4), 'Retained snapshot replay');
  await until(() => page.evaluate(() => window.replayedActivity.length >= 2), 'Native EventSource reconnect');
  const resumed = await page.evaluate(() => window.replayedActivity[1]);
  assert.equal(resumed.cursor, second.cursor);
  assert.equal(resumed.reset, false, 'Reconnect must resume from Last-Event-ID');
  assert.deepEqual(resumed.items, [], 'Caught-up reconnect must not repeat history');
  await until(() => page.locator('.activity-notice').isVisible().then(visible => !visible), 'Reconnected activity');
  await check();
  assert.deepEqual((await read()).items, second.items, 'Snapshot replay changed retained identities');
  await rows.first().locator('summary').click();

  advance('complete-commands');
  await until(() => rows.nth(3).locator('.event-status').textContent().then(text => /Completed/.test(text)), 'Live command completions after reconnect');
  await check();
  assert.match(await rows.nth(1).locator('.event-status').textContent(), /Completed/);
  assert.equal(await rows.first().evaluate(el => el.open), true);
  assert.deepEqual(reported(await read()).map(item => item.id), expectedIDs);
  await page.screenshot({path: path.join(artifacts, 'reconnected.png')});

  advance('repeat-error');
  await until(() => rows.count().then(count => count === 5), 'Identical repeated error');
  const repeated = {items: reported(await read())};
  assert.deepEqual(repeated.items.map(item => item.kind), ['error', 'command', 'error', 'command', 'error']);
  assert.deepEqual((await ids()).slice(0, 4), expectedIDs);
  assert.equal(new Set(await ids()).size, 5);
  assert.equal(repeated.items[4].error, repeated.items[2].error);
  assert.equal(repeated.items[4].status, repeated.items[2].status);
  assert.equal(await groups.count(), 0);
  await page.reload();
  await until(() => rows.count().then(count => count === 5), 'Reloaded repeated error');
  assert.deepEqual(await ids(), repeated.items.map(item => item.id));
  assert.equal(await groups.count(), 0);
  await page.screenshot({path: path.join(artifacts, 'repeated.png')});
  await page.unroute(streamPattern);
};
