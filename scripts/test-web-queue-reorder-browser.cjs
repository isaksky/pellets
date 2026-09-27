// Real queue gestures against a disposable, explicitly bound project database.
// Run with Playwright on NODE_PATH.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-queue-reorder-'));
const fixture = path.join(temporary, 'project'), binary = path.join(temporary, 'pl');
const artifacts = process.env.PELLETS_BROWSER_ARTIFACTS || path.join(temporary, 'screenshots');
fs.mkdirSync(fixture); fs.mkdirSync(artifacts, {recursive: true});
execFileSync('go', ['build', '-o', binary, './cmd/pl'], {cwd: repository});
execFileSync('git', ['init', '-q'], {cwd: fixture});
const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd: fixture, encoding: 'utf8'})).data;
cli('init-db');
const a = cli('add', 'Visible alpha', '--group', 'visible');
const hidden = cli('add', 'Hidden record', '--group', 'other');
const b = cli('add', 'Visible beta', '--group', 'visible');
const c = cli('add', 'Visible gamma', '--group', 'visible');
const review = cli('add', 'Review visible work', '--review-targets', `${a.id},${b.id}`, '--group', 'visible');
cli('move', review.id, '--after', c.id);
const deferred = cli('add', 'Deferred record', '--maybe-later');
const fillers = Array.from({length: 24}, (_, i) => cli('add', `Other work ${i + 1}`, '--group', 'other'));
const group = 'v' + Buffer.from('visible').toString('base64url');
const originPath = `/projects/${a.project}/tasks`;
let server, browser;
const wait = async (test, message) => {
  const deadline = Date.now() + 12000;
  while (Date.now() < deadline) { if (await test()) return; await new Promise(resolve => setTimeout(resolve, 70)); }
  throw Error(message);
};
const order = page => page.locator('#queue-rows > [data-row-id]').evaluateAll(rows => rows.map(row => row.dataset.rowId));
const activeOrder = () => cli('list').map(p => p.id);
const handle = (page, id) => page.locator(`#task-${id} [data-queue-handle]`);
async function drag(page, source, target, position = 'before') {
  await handle(page, source).scrollIntoViewIfNeeded();
  await page.locator(`#task-${target}`).scrollIntoViewIfNeeded();
  const from = await handle(page, source).boundingBox(), to = await page.locator(`#task-${target}`).boundingBox();
  assert.ok(from && to, 'drag geometry is visible');
  await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
  await page.mouse.down();
  await page.mouse.move(from.x + from.width / 2 + 8, from.y + from.height / 2 + 8, {steps: 3});
  await page.mouse.move(to.x + 16, position === 'before' ? to.y + 3 : to.y + to.height - 3, {steps: 6});
  if (process.env.PELLETS_REORDER_DEBUG) console.log('drag state', {from,to,drop:{x:to.x+16,y:position==='before'?to.y+3:to.y+to.height-3},dom:await page.evaluate(() => ({preview: !!document.querySelector('.queue-drag-preview'), source: !!document.querySelector('.queue-drag-source'), line: !!document.querySelector('.queue-insertion'), queue: document.getElementById('queue-rows')?.getBoundingClientRect().toJSON(), pane: document.getElementById('main')?.getBoundingClientRect().toJSON()}))});
  await page.mouse.up();
  await wait(async () => (await page.locator('#queue-move-feedback').textContent()).includes(`Moved ${source} ${position} ${target}`), `move ${source} did not finish`).catch(async error => {
    await page.screenshot({path: path.join(artifacts, 'drag-failure.png')});
    throw Error(`${error.message}; feedback=${await page.locator('#queue-move-feedback').textContent()}; order=${(await order(page)).join(',')}`);
  });
  assert.equal(await page.locator('#record-dialog').evaluate(el => el.open), false, 'drag opened the inspector');
}
(async () => {
  console.log('Queue reorder artifacts: ' + artifacts);
  server = spawn(binary, ['server', '--port', '0', '--no-open'], {cwd: fixture});
  const origin = await new Promise((resolve, reject) => {
    let output = '', error = '';
    server.stdout.on('data', data => { output += data; if (output.includes('\n')) resolve(output.split('\n')[0].trim()); });
    server.stderr.on('data', data => error += data);
    server.once('error', reject);
    server.once('exit', code => reject(Error(`server ${code}: ${error}`)));
  });
  browser = await chromium.launch({headless: true, ...(process.env.PLAYWRIGHT_CHANNEL ? {channel: process.env.PLAYWRIGHT_CHANNEL} : {})});
  const page = await browser.newPage({viewport: {width: 1280, height: 900}, deviceScaleFactor: 1});
  const errors = []; page.on('pageerror', error => errors.push(error.message));
  await page.goto(origin + originPath + `?group=${encodeURIComponent(group)}&sort=priority&direction=asc`);
  assert.deepEqual(await order(page), [a.id, b.id, c.id, review.id]);
  assert.equal(await page.locator('[data-queue-handle]').count(), 4);
  assert.equal(await page.locator('#queue-reorder-live[role=status][aria-live=polite]').count(), 1);
  assert.equal(await page.locator('#queue-move-feedback[role],#queue-move-feedback[aria-live]').count(), 0,
    'visible feedback duplicates live announcements');
  const browseURL = page.url();
  // Picking up and committing at the same full-queue gap is a true no-op.
  // The full order lives inside #scope-order's template content.
  const unchangedOrder = activeOrder(), moveRequests = [];
  const countMoves = request => { if (request.method() === 'POST' && new URL(request.url()).pathname.endsWith('/move')) moveRequests.push(request.url()); };
  page.on('request', countMoves);
  await handle(page, b.id).focus();
  await page.keyboard.press('Space'); await page.keyboard.press('Enter');
  assert.match(await page.locator('#queue-reorder-live').textContent(), /Queue position unchanged/);
  assert.equal(await page.locator('#queue-move-feedback').isHidden(), true, 'no-op shifted the queue with a visible notice');
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  assert.equal(moveRequests.length, 0, 'unchanged pickup submitted a move');
  assert.deepEqual(activeOrder(), unchangedOrder, 'unchanged pickup changed hidden order');
  for (const key of ['Tab', 'Shift+Tab']) {
    await handle(page, b.id).focus();
    await page.keyboard.press('Space');
    assert.equal(await page.locator('.queue-insertion').count(), 1, 'keyboard pickup did not start');
    await page.keyboard.press(key);
    assert.equal(await page.locator('.queue-insertion,.queue-drag-preview').count(), 0, `${key} left a keyboard drag active`);
    assert.equal(moveRequests.length, 0, `${key} submitted a move`);
    assert.equal(await page.locator('#queue-move-feedback').isHidden(), true, `${key} shifted the queue with a visible notice`);
  }
  page.off('request', countMoves);
  // A filtered before-gap moves only the source; hidden records retain order.
  await drag(page, c.id, b.id);
  assert.deepEqual(activeOrder().slice(0, 5), [a.id, hidden.id, c.id, b.id, review.id]);
  assert.equal(page.url(), browseURL);
  await page.reload();
  assert.deepEqual(await order(page), [a.id, c.id, b.id, review.id]);
  // First and final insertion gaps, including a review row, work by pointer.
  await drag(page, b.id, a.id);
  assert.deepEqual((await order(page)).slice(0, 4), [b.id, a.id, c.id, review.id]);
  await drag(page, b.id, review.id, 'after');
  assert.deepEqual((await order(page)).slice(0, 4), [a.id, c.id, review.id, b.id]);
  // Keyboard pickup, slot navigation, commit, focus return, and cancel.
  await handle(page, a.id).focus();
  await page.keyboard.press('Space'); await page.keyboard.press('ArrowDown'); await page.keyboard.press('Enter');
  await wait(async () => (await order(page))[1] === a.id, 'keyboard move did not persist');
  assert.equal(await handle(page, a.id).evaluate(el => el === document.activeElement), true);
  const beforeCancel = activeOrder();
  const noticeBeforeCancel = await page.locator('#queue-move-feedback').evaluate(el => ({hidden: el.hidden, text: el.textContent}));
  await handle(page, a.id).focus(); await page.keyboard.press('Space'); await page.keyboard.press('Escape');
  assert.deepEqual(await page.locator('#queue-move-feedback').evaluate(el => ({hidden: el.hidden, text: el.textContent})),
    noticeBeforeCancel, 'cancel replaced the previous queue notice');
  await handle(page, a.id).click();
  assert.deepEqual(activeOrder(), beforeCancel, 'cancel or untouched click submitted a move');
  // Existing menu actions use the same queue-local contract.
  await page.locator(`#task-${c.id} .row-menu > summary`).click();
  await page.locator(`#task-${c.id} [data-move-row=after]`).click();
  await wait(async () => (await page.locator('#queue-move-feedback').textContent()).includes(`Moved ${c.id}`), 'menu move did not finish');
  assert.equal(page.url(), browseURL);
  // Review expansion, an unsaved creation draft, and explicit scope selection survive.
  await page.locator(`#task-${review.id} .review-disclosure > summary`).click();
  await page.locator('.pellet-create > summary').click();
  await page.locator('[data-pellet-create] [name=title]').fill('Unsent draft');
  await page.locator('.pellet-create > summary').click();
  await page.locator(`#task-${a.id} .row-menu > summary`).click();
  await page.locator(`#task-${a.id} [data-checkpoint-select]`).check();
  await page.locator(`#task-${a.id} .row-menu > summary`).click();
  await drag(page, review.id, a.id);
  assert.equal(await page.locator(`#task-${review.id} .review-disclosure`).evaluate(el => el.open), true);
  assert.equal(await page.locator('[data-pellet-create] [name=title]').inputValue(), 'Unsent draft');
  assert.equal(await page.locator(`#task-${a.id} [data-checkpoint-select]`).isChecked(), true);
  assert.equal(await page.locator(`#task-${review.id} .review-targets li`).count(), 2);
  // A pending move owns the foreground mutation slot and blocks another gesture.
  let unblock, pendingPosts = 0;
  await page.route('**/move*', async route => {
    pendingPosts++;
    await new Promise(resolve => { unblock = resolve; });
    await route.continue();
  });
  await handle(page, a.id).focus(); await page.keyboard.press('Space'); await page.keyboard.press('ArrowDown'); await page.keyboard.press('Space');
  await wait(() => Promise.resolve(pendingPosts === 1), 'pending move was not submitted');
  assert.match(await page.locator('#queue-move-feedback').innerText(), /Moving /);
  await handle(page, b.id).focus(); await page.keyboard.press('Space');
  assert.equal(await page.locator('.queue-insertion').count(), 0, 'second gesture began during a pending move');
  unblock();
  await wait(async () => (await page.locator('#queue-move-feedback').textContent()).includes(`Moved ${a.id}`), 'pending move did not complete');
  assert.equal(pendingPosts, 1);
  await page.unroute('**/move*');
  // Non-priority sorts and inactive records explain why drag handles are absent.
  await page.goto(origin + originPath + `?group=${encodeURIComponent(group)}&sort=title&direction=asc`);
  assert.equal(await page.locator('[data-queue-handle]').count(), 0);
  assert.match(await page.locator('#queue-order').innerText(), /Drag to reorder in Queue order/);
  for (const theme of ['gruvbox-light', 'gruvbox-dark', 'light', 'dark', 'icy']) {
    await page.evaluate(theme => window.Workbench.applyTheme(theme), theme);
    for (const width of [1280, 800, 390]) {
      await page.setViewportSize({width, height: 850});
      if (width !== 1280) await page.locator('#toggle-execution').click();
      const layout = await page.locator('.filters').evaluate(filters => {
        const search = filters.querySelector('#search').getBoundingClientRect();
        const explanation = filters.querySelector('#queue-order').getBoundingClientRect();
        const bounds = filters.getBoundingClientRect();
        return {search: search.toJSON(), explanation: explanation.toJSON(), bounds: bounds.toJSON(), overflow: filters.scrollWidth > filters.clientWidth + 1};
      });
      assert.ok(layout.search.width >= 120 && layout.search.right <= layout.bounds.right + 1,
        `Search disappeared beside Queue order in ${theme} at ${width}px: ${JSON.stringify(layout)}`);
      assert.ok(layout.explanation.width > 0 && layout.explanation.right <= layout.bounds.right + 1 && !layout.overflow,
        `Queue order explanation overflows in ${theme} at ${width}px: ${JSON.stringify(layout)}`);
      await page.locator('#tasks-area').screenshot({path: path.join(artifacts, `${theme}-title-sort-${width}.png`)});
      if (width !== 1280) await page.locator('#toggle-execution').click();
    }
  }
  await page.setViewportSize({width: 1280, height: 900});
  await page.locator('#queue-order a').click();
  await wait(() => Promise.resolve(new URL(page.url()).searchParams.get('sort') === 'priority'), 'Queue order link lost navigation');
  assert.equal(new URL(page.url()).searchParams.get('group'), group);
  await page.goto(origin + originPath + '?status=maybe_later&sort=priority&direction=asc');
  assert.equal(await page.locator(`#task-${deferred.id} [data-queue-handle]`).count(), 0);
  // A target change during a gesture is rejected; the live patch cannot replace rows.
  await page.goto(origin + originPath + `?group=${encodeURIComponent(group)}&sort=priority&direction=asc`);
  const source = b.id, target = a.id;
  const sourceBox = await handle(page, source).boundingBox(), targetBox = await page.locator(`#task-${target}`).boundingBox();
  await page.mouse.move(sourceBox.x + 10, sourceBox.y + 10); await page.mouse.down();
  await page.mouse.move(sourceBox.x + 20, sourceBox.y + 22, {steps: 4});
  const oldTitle = await page.locator(`#task-${target} .task-title`).innerText();
  cli('edit', target, '--title', 'Changed while dragging');
  await page.waitForTimeout(400);
  assert.equal(await page.locator(`#task-${target} .task-title`).innerText(), oldTitle, 'live response replaced a guarded row');
  await page.mouse.move(targetBox.x + 15, targetBox.y + 3, {steps: 4}); await page.mouse.up();
  await wait(async () => (await page.locator(`#task-${target} .task-title`).innerText()) === 'Changed while dragging', 'conflict did not refresh authoritative queue');
  assert.match(await page.locator('#queue-move-feedback').innerText(), /Queue move could not be confirmed/);
  // If a write succeeds but its response is lost, reconcile instead of replaying.
  let posts = 0;
  await page.route('**/move*', async route => {
    posts++;
    await route.fetch();
    await route.abort('failed');
  });
  const prior = activeOrder();
  await handle(page, c.id).focus(); await page.keyboard.press('Space'); await page.keyboard.press('ArrowDown'); await page.keyboard.press('Space');
  await wait(async () => posts === 1 && activeOrder().join(',') !== prior.join(','), 'lost response fixture did not commit exactly once');
  await wait(async () => (await order(page)).join(',') === activeOrder().filter(id => [a.id,b.id,c.id,review.id].includes(id)).join(','), 'uncertain result did not reconcile');
  assert.equal(posts, 1, 'lost response replayed a move');
  await page.unroute('**/move*');
  // The same pointer controller handles touch events, and the queue scrolls at edges.
  await page.goto(origin + originPath);
  const main = page.locator('#main');
  await main.evaluate(el => el.scrollTop = 0);
  const first = await handle(page, a.id).boundingBox(), pane = await main.boundingBox();
  await page.mouse.move(first.x + 10, first.y + 10); await page.mouse.down();
  await page.mouse.move(first.x + 20, pane.y + pane.height - 12, {steps: 12});
  await wait(async () => (await main.evaluate(el => el.scrollTop)) > 20, 'drag did not autoscroll');
  await page.keyboard.press('Escape'); await page.mouse.up();
  const touch = await browser.newContext({viewport: {width: 800, height: 850}, deviceScaleFactor: 1, hasTouch: true, isMobile: true});
  const touchPage = await touch.newPage();
  await touchPage.goto(origin + originPath + `?group=${encodeURIComponent(group)}&sort=priority&direction=asc`);
  const touchSource = await handle(touchPage, b.id).boundingBox(), touchTarget = await touchPage.locator(`#task-${a.id}`).boundingBox();
  const cdp = await touch.newCDPSession(touchPage);
  const p1 = {x: touchSource.x + 10, y: touchSource.y + 12}, p2 = {x: touchTarget.x + 12, y: touchTarget.y + 3};
  await cdp.send('Input.dispatchTouchEvent', {type: 'touchStart', touchPoints: [p1]});
  await cdp.send('Input.dispatchTouchEvent', {type: 'touchMove', touchPoints: [{x:p1.x+9,y:p1.y+9}]});
  await cdp.send('Input.dispatchTouchEvent', {type: 'touchMove', touchPoints: [p2]});
  await cdp.send('Input.dispatchTouchEvent', {type: 'touchEnd', touchPoints: []});
  await wait(async () => (await touchPage.locator('#queue-move-feedback').textContent()).includes('Moved '), 'touch move did not finish');
  await touch.close();
  await page.goto(origin + originPath + '?workspace=1&sort=priority&direction=asc');
  assert.ok((await page.locator('[data-queue-handle]').count()) > 1, 'workspace results did not expose project priority');
  const workspaceURL = page.url();
  await drag(page, c.id, a.id);
  assert.equal(page.url(), workspaceURL, 'workspace browsing context changed');
  // Inspect the real queue at all themes and requested layout widths.
  await page.goto(origin + originPath + `?group=${encodeURIComponent(group)}&sort=priority&direction=asc`);
  for (const theme of ['gruvbox-light', 'gruvbox-dark', 'light', 'dark', 'icy']) {
    await page.evaluate(theme => window.Workbench.applyTheme(theme), theme);
    for (const width of [1280, 800, 390]) {
      await page.setViewportSize({width, height: 850});
      await page.locator('#tasks-area').screenshot({path: path.join(artifacts, `${theme}-${width}.png`)});
      const ordinary = page.locator('#queue-rows > .task-row').first();
      const geometry = await ordinary.evaluate(row => ({row: row.getBoundingClientRect().height, grip: row.querySelector('[data-queue-handle]')?.getBoundingClientRect().width, overflow: row.scrollWidth > row.clientWidth + 1, id: row.querySelector('.row-reference').getBoundingClientRect().x, title: row.querySelector('.task-title').getBoundingClientRect().x}));
      assert.equal(geometry.row, 37, 'ordinary row changed height');
      assert.ok(geometry.grip >= 20 && !geometry.overflow, `${theme}/${width} grip is cramped or overflows: ${JSON.stringify(geometry)}`);
      await ordinary.locator('[data-queue-handle]').hover();
      await ordinary.locator('[data-queue-handle]').focus();
      await page.keyboard.press('Tab');
      await page.keyboard.press('Shift+Tab');
      const focused = await ordinary.evaluate(row => ({row: row.getBoundingClientRect().height, id: row.querySelector('.row-reference').getBoundingClientRect().x, title: row.querySelector('.task-title').getBoundingClientRect().x, outline: getComputedStyle(row.querySelector('[data-queue-handle]')).outlineStyle}));
      assert.deepEqual([focused.row, focused.id, focused.title], [geometry.row, geometry.id, geometry.title], 'hover/focus shifted the row');
      assert.notEqual(focused.outline, 'none', 'focused grip lost its indicator');
      if (width !== 1280) {
        await page.locator('#toggle-execution').click();
        await page.screenshot({path: path.join(artifacts, `${theme}-${width}-sidebars.png`)});
        if (width === 800) assert.equal(await ordinary.locator('[data-queue-handle]').isVisible(), true, 'grip hidden in narrow pane');
        await page.locator('#toggle-execution').click();
      }
    }
  }
  await page.setViewportSize({width: 1280, height: 850});
  const activeHandle = await handle(page, c.id).boundingBox(), activeTarget = await page.locator(`#task-${b.id}`).boundingBox();
  await page.mouse.move(activeHandle.x + 10, activeHandle.y + 10); await page.mouse.down();
  await page.mouse.move(activeTarget.x + 18, activeTarget.y + 4, {steps: 8});
  assert.equal(await page.locator('.queue-drag-preview').count(), 1);
  assert.equal(await page.locator('.queue-insertion').count(), 1);
  await page.screenshot({path: path.join(artifacts, 'active-drag.png')});
  await page.keyboard.press('Escape'); await page.mouse.up();
  assert.deepEqual(errors, []);
  console.log('Queue reorder browser checks passed');
})().catch(error => { console.error(error); process.exitCode = 1; }).finally(async () => { await browser?.close(); server?.kill(); });
