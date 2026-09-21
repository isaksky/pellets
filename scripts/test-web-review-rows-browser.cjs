// Production queue, disposable records, no model calls. Set PLAYWRIGHT_BROWSER=webkit.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-review-rows-'));
const root = path.join(temporary, 'queue');
const binary = path.join(temporary, 'pl');
const artifacts = process.env.PELLETS_BROWSER_ARTIFACTS || path.join(temporary, 'screenshots');
let server, browser;
const until = async (fn, message) => {
  const deadline = Date.now() + 15000;
  while (Date.now() < deadline) { if (await fn()) return; await new Promise(r => setTimeout(r, 60)); }
  throw Error(message);
};
(async () => {
  fs.mkdirSync(root); fs.mkdirSync(artifacts, {recursive: true});
  execFileSync('go', ['build', '-o', binary, './cmd/pl'], {cwd: repository});
  execFileSync('git', ['init', '-q'], {cwd: root});
  const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd: root, encoding: 'utf8'})).data;
  const targets = Array.from({length: 40}, (_, i) => cli('add', `Implementation ${i + 1}: readable Markdown and diagrams`, '--group', i % 2 ? 'runtime' : 'rich-descriptions'));
  const names = ['Review Markdown and diagrams', 'Review CLI interaction and output', 'Review shared group context', 'Review execution feedback', 'Custom title; create follow-up pellets'];
  const scopes = [[targets[0], targets[2]], [targets[2], targets[5]], [targets[1], targets[5], targets[9]], [targets[0]], targets];
  const reviews = names.map((name, i) => cli('add', name, '--review-targets', scopes[i].map(t => t.id).join(','), '--group', 'reviews'));
  // Put the five explicit, overlapping reviews together to reproduce the report.
  for (let i = reviews.length - 1; i >= 0; i--) cli('move', reviews[i].id, '--before', targets[0].id);
  cli('close', targets[5].id); cli('defer', targets[9].id);
  const project = targets[0].project;
  server = spawn(binary, ['server', '--port', '0', '--no-open'], {cwd: root});
  const origin = await new Promise((resolve, reject) => {
    let output = '', errors = '';
    server.stdout.on('data', x => { output += x; if (output.includes('\n')) resolve(output.split('\n')[0]); });
    server.stderr.on('data', x => errors += x);
    server.once('error', reject); server.once('exit', c => reject(Error(`Server exited ${c}: ${errors}`)));
  });
  const engine = process.env.PLAYWRIGHT_BROWSER === 'webkit' ? webkit : chromium;
  browser = await engine.launch({headless: true, ...(engine === chromium && process.env.PLAYWRIGHT_CHANNEL ? {channel: process.env.PLAYWRIGHT_CHANNEL} : {}), ...(engine === webkit && process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE ? {executablePath: process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE} : {})});
  const page = await browser.newPage({viewport: {width: 1280, height: 900}});
  const errors = []; page.on('pageerror', e => errors.push(e.message));
  const url = origin + `/projects/${project}/tasks`;
  const row = i => page.locator('#task-' + reviews[i].id);
  const counts = async (active, pellets, reviews) => {
    await until(async () => (await page.locator('#area-tabs a').first().innerText()).includes(String(active)) &&
      (await page.locator('#tasks-title').innerText()).includes(`${pellets} pellet${pellets === 1 ? "" : "s"} · ${reviews} review${reviews === 1 ? "" : "s"}`) &&
      (await page.locator('#project-counts').innerText()) === `${active} active in project`, 'Counts did not update together').catch(async error => { await page.screenshot({path: path.join(artifacts, 'failure.png')}); throw Error(error.message + ': ' + JSON.stringify(await page.locator('#area-tabs, #tasks-title, #project-counts, #checkpoint-undo').allTextContents()) + '; records: ' + cli('list', '--all').map(p => p.id + '=' + p.status).join(',')); });
  };
  await page.goto(url); await counts(43, 38, 5);
  assert.equal(await page.locator('.scope-brackets').count(), 0);
  assert.equal(await page.locator('#queue-rows').evaluate(el => getComputedStyle(el).paddingLeft), '0px');
  assert.equal(await row(4).locator('.checkpoint-open').textContent(), names[4], 'Custom saved title was rewritten');
  assert.match(await row(0).locator('.review-status').innerText(), /Waiting for 2 pellets/);
  // Native disclosure and editor have independent keyboard activation.
  await row(0).locator('.review-disclosure > summary').focus(); await page.keyboard.press('Enter');
  assert.equal(await row(0).locator('details.review-disclosure').evaluate(el => el.open), true);
  assert.equal(await page.locator('#record-dialog').evaluate(el => el.open), false);
  assert.deepEqual(await row(0).locator('.review-target-meta code').allTextContents(), scopes[0].map(t => t.id));
  await row(0).locator('.checkpoint-open').focus(); await page.keyboard.press('Enter');
  await page.locator('#record-dialog[open]').waitFor();
  const title = page.locator('[data-inspector] input[name=title]');
  await title.fill('Unfinished custom review title'); await title.press('ArrowLeft');
  const caret = await title.evaluate(el => el.selectionStart);
  cli('add', 'Live update during review draft');
  await until(async () => (await page.locator('#area-tabs a').first().innerText()).includes('44'), 'Live queue refresh missing');
  assert.equal(await title.inputValue(), 'Unfinished custom review title');
  assert.equal(await title.evaluate(el => el.selectionStart), caret);
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('link', {name: 'Close inspector', exact: true}).click();
  await page.locator('#record-dialog').waitFor({state: 'hidden'});
  // Refresh preserves disclosure focus, ordinary selection and scroll within a large scope.
  await row(4).locator('.review-disclosure > summary').click();
  const content = row(4).locator('.review-content');
  await content.focus(); await page.keyboard.press('End');
  let lastScroll = -1, settledScroll = 0;
  await until(async () => {
    const value = await content.evaluate(el => el.scrollTop);
    settledScroll = value > 0 && Math.abs(value - lastScroll) < 1 ? settledScroll + 1 : 0;
    lastScroll = value;
    return settledScroll >= 3;
  }, 'Large scope cannot be keyboard scrolled');
  const scroll = await content.evaluate(el => el.scrollTop);
  const selectionMenu = page.locator('#task-' + targets[0].id + ' .row-menu');
  await selectionMenu.locator('summary').click(); await selectionMenu.locator('[data-checkpoint-select]').check();
  await selectionMenu.locator('summary').click(); await content.focus();
  const mainScroll = await page.locator('#main').evaluate(el => el.scrollTop);
  cli('edit', targets[39].id, '--title', 'Updated visible target title');
  await until(async () => (await page.locator('#task-' + targets[39].id + ' .task-title').textContent()) === 'Updated visible target title', 'Target update missing');
  assert.equal(await content.evaluate(el => el === document.activeElement), true);
  assert.equal(await content.evaluate(el => el.scrollTop), scroll);
  assert.equal(await page.locator('#main').evaluate(el => el.scrollTop), mainScroll);
  assert.equal(await selectionMenu.locator('[data-checkpoint-select]').isChecked(), true);
  await row(0).locator('.review-disclosure > summary').focus();
  cli('edit', targets[38].id, '--title', 'Another live update');
  await until(async () => (await page.locator('#task-' + targets[38].id + ' .task-title').textContent()) === 'Another live update', 'Second update missing');
  assert.equal(await row(0).locator('.review-disclosure > summary').evaluate(el => el === document.activeElement), true);
  // A search can leave only a review, while its complete scope remains visible.
  const search = page.locator('#search');
  await search.fill(names[2]); await search.press('Enter');
  await counts(44, 0, 1);
  await row(2).locator('.review-disclosure > summary').click();
  assert.equal(await row(2).locator('.review-targets li').count(), 3);
  assert.equal(await row(2).locator('.review-targets').getByText(/Not shown by current filters/).count(), 3);
  assert.match(await row(2).locator('.review-targets').textContent(), /Closed/);
  assert.match(await row(2).locator('.review-targets').textContent(), /Maybe later/);
  // Clear using the filter navigation: expansions survive disappearing results.
  await search.fill(''); await search.press('Enter'); await counts(44, 39, 5);
  assert.equal(await row(0).locator('.review-disclosure').evaluate(el => el.open), true);
  await page.locator('#filter-summary').click();
  await page.getByRole('combobox', {name: 'Sort', exact: true}).click();
  await page.getByRole('option', {name: 'Title', exact: true}).click();
  await page.keyboard.press('Escape');
  await until(() => new URL(page.url()).searchParams.get('sort') === 'title', 'Sort did not apply');
  assert.equal(await row(0).locator('.review-disclosure').evaluate(el => el.open), true);
  assert.deepEqual(await row(0).locator('.review-target-meta code').allTextContents(), scopes[0].map(t => t.id));
  // Ordinary claim/release must keep the shared active total stable.
  cli('start', targets[0].id); await counts(44, 39, 5);
  cli('release', targets[0].id); await counts(44, 39, 5);
  await until(async () => (await page.locator('#task-' + targets[0].id).getAttribute('class')).includes('status-open'), 'Release refresh missing');
  // Remove/undo use the standard keyboard menu and preserve order.
  const before = await page.locator('#queue-rows > article').evaluateAll(rows => rows.map(el => el.dataset.rowId));
  await row(1).locator('.row-menu > summary').focus(); await page.keyboard.press('End');
  assert.match(await page.evaluate(() => document.activeElement.textContent), /Remove review/);
  await page.keyboard.press('Enter'); await counts(43, 39, 4);
  assert.equal(await page.locator('[data-checkpoint-undo] button').evaluate(el => el === document.activeElement), true);
  await page.locator('[data-checkpoint-undo] button').press('Enter'); await counts(44, 39, 5);
  await until(() => row(1).locator('.row-menu > summary').evaluate(el => el === document.activeElement), 'Undo lost keyboard focus');
  assert.deepEqual(await page.locator('#queue-rows > article').evaluateAll(rows => rows.map(el => el.dataset.rowId)), before);
  cli('defer', reviews[3].id); await counts(43, 39, 4);
  await page.goto(url + '?status=all'); await counts(43, 41, 5);
  assert.equal(await row(3).locator('.review-status').innerText(), 'Deferred');
  cli('reopen', reviews[3].id); cli('move', reviews[3].id, '--before', targets[0].id);
  // Five adjacent rows and expanded large scope, in every theme and layout.
  await page.goto(url + '?status=all');
  for (const theme of ['gruvbox-light', 'gruvbox-dark', 'light', 'dark', 'icy']) {
    await page.evaluate(t => window.Workbench.applyTheme(t), theme);
    for (const width of [1280, 1092, 800, 390]) {
      await page.setViewportSize({width, height: 900});
      await page.locator('#main').evaluate(el => el.scrollTop = 0);
      await page.mouse.move(0, 0);
      for (let i = 0; i < 5; i++) {
        const metrics = await row(i).evaluate(el => {
          const title = el.querySelector('.checkpoint-open'), action = el.querySelector('.row-menu > summary');
          const main = document.querySelector('#main').getBoundingClientRect();
          return {overflow: el.scrollWidth > el.clientWidth, title: title.getBoundingClientRect().toJSON(), action: action.getBoundingClientRect().toJSON(), size: parseFloat(getComputedStyle(title).fontSize), left: main.left, right: main.right};
        });
        assert.ok(!metrics.overflow && metrics.size >= 13 && metrics.title.width > 80 && metrics.action.x >= metrics.left && metrics.action.right <= metrics.right, `Review controls clipped at ${theme}/${width}: ${JSON.stringify(metrics)}`);
      }
      await page.screenshot({path: path.join(artifacts, `${theme}-${width}-five-reviews.png`)});
      const detail = row(4).locator('.review-disclosure');
      if (!await detail.evaluate(el => el.open)) await detail.locator('summary').click();
      await content.scrollIntoViewIfNeeded();
      assert.ok(await content.evaluate(el => el.clientHeight <= 420 && el.scrollHeight > el.clientHeight && el.scrollWidth <= el.clientWidth), 'Large scope is not bounded');
      await page.screenshot({path: path.join(artifacts, `${theme}-${width}-scope.png`)});
      await detail.locator('summary').click();
    }
  }
  // Row reordering updates the authoritative queue without changing membership.
  await page.goto(url);
  const originalOrder = await page.locator('#queue-rows > article').evaluateAll(rows => rows.map(el => el.dataset.rowId));
  const originalIndex = originalOrder.indexOf(reviews[2].id);
  await row(2).locator('.review-disclosure > summary').click();
  await row(2).locator('.row-menu > summary').click();
  await row(2).getByRole('menuitem', {name: 'Move review up', exact: true}).click();
  await page.locator('#record-dialog[open]').waitFor();
  await page.getByRole('link', {name: 'Close inspector', exact: true}).click();
  await page.locator('#record-dialog').waitFor({state: 'hidden'});
  const movedOrder = await page.locator('#queue-rows > article').evaluateAll(rows => rows.map(el => el.dataset.rowId));
  assert.equal(movedOrder.indexOf(reviews[2].id), originalIndex - 1);
  assert.equal(await row(2).locator('.review-disclosure').evaluate(el => el.open), true);
  assert.deepEqual(cli('show', reviews[2].id).checkpoint.targets.map(t => t.reference), scopes[2].map(t => t.id));
  // All states exposes removed records and the row menu restores them directly.
  await page.goto(url + '?status=all');
  await row(1).locator('.row-menu > summary').click();
  await row(1).getByRole('menuitem', {name: 'Remove checkpoint ' + reviews[1].id, exact: true}).click();
  await counts(43, 41, 5);
  assert.equal(await row(1).locator('.review-status').innerText(), 'Removed');
  await row(1).locator('.row-menu > summary').click();
  await row(1).getByRole('menuitem', {name: 'Restore review', exact: true}).click();
  await page.locator('#record-dialog[open]').waitFor();
  await counts(44, 41, 5);
  assert.match(await row(1).locator('.review-status').textContent(), /Waiting/);
  await page.getByRole('link', {name: 'Close inspector', exact: true}).click();
  await page.locator('#record-dialog').waitFor({state: 'hidden'});
  cli('purge', '--project', project, '--yes');
  await page.goto(url + '?status=all'); await counts(44, 40, 5);
  const unavailable = row(1).locator('.review-targets li').filter({hasText: targets[5].title});
  assert.match(await unavailable.textContent(), /Unavailable/);
  assert.equal(await unavailable.locator('a').count(), 0, 'Unavailable target acquired a replacement link');
  await page.goto(url + '?q=Live+update+during+review+draft'); await counts(44, 1, 0);
  await page.goto(url + '?q=does-not-exist'); await counts(44, 0, 0);
  assert.deepEqual(errors, []);
  console.log(`PASS review rows: exact scopes, hidden targets, status/count lifecycle, native menus, draft/focus/selection/scroll, five themes × four widths. Artifacts: ${artifacts}`);
})().catch(e => {console.error(e); process.exitCode = 1;}).finally(async () => {
  await browser?.close();
  if (server?.exitCode === null) { const done = new Promise(r => server.once('exit', r)); server.kill('SIGINT'); await done; }
  // Keep screenshot artifacts while deleting the disposable repository and executable.
  fs.rmSync(root, {recursive: true, force: true});
});
