// Document contents against real rendering, live patches, and disposable data.
// NODE_PATH=/path/to/node_modules node scripts/test-web-description-outline-browser.cjs
// Repeat with PLAYWRIGHT_BROWSER=webkit.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-outline-browser-'));
const binary = path.join(temporary, 'pl'), fixture = path.join(temporary, 'outline');
const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd: fixture, encoding: 'utf8'})).data;
let server, browser;
const until = async (fn, message) => {
  const end = Date.now() + 15000;
  while (Date.now() < end) { if (await fn()) return; await new Promise(r => setTimeout(r, 50)); }
  throw Error(message);
};
const paragraph = 'Readable section text with enough detail to exercise local scrolling. '.repeat(14) + '\n\n';
const titles = ['Overview & verification', 'Skipped level', 'Repeat', 'Repeat', 'Repeat-2', '日本語 café v2', '!!!', 'Diagram', 'After diagram', 'Last section'];
const source = '# Overview &amp; *verification*\n\n' + paragraph +
  '```text\n# Not a heading\n## Neither is this\n```\n\n<h2>Not an HTML heading</h2>\n\n' +
  '### Skipped **level**\n\n' + paragraph +
  '## Repeat\n\n' + paragraph + '## Repeat\n\n' + paragraph + '## Repeat-2\n\n' + paragraph +
  '#### 日本語 café `v2`\n\n' + paragraph + '# !!!\n\n' + paragraph +
  '## Diagram\n\n```mermaid\ngraph TD\n  %% # Not a Markdown heading\n  A-->B-->C-->D-->E-->F-->G-->H\n```\n\n' +
  '## After diagram\n\n' + paragraph + '### Last `section`\n\n' + paragraph.repeat(2);
(async () => {
  execFileSync('go', ['build', '-o', binary, './cmd/pl'], {cwd: repository});
  fs.mkdirSync(fixture);
  execFileSync('git', ['init', '-q'], {cwd: fixture});
  const record = cli('add', 'Long nested document', '--description', source, '--group', 'docs', '--external-id', 'outline:fixture');
  const short = cli('add', 'Short sections', '--description', '# One\n\nBrief.\n\n## Two\n\nBrief.');
  const single = cli('add', 'One long section', '--description', '# Only heading\n\n' + paragraph.repeat(5));
  const plain = cli('add', 'No headings', '--description', paragraph.repeat(5));
  for (let i=0;i<25;i++) cli('add', 'Surrounding queue row ' + i, '--group', 'docs', '--external-id', 'outline:fixture');
  server = spawn(binary, ['server', '--port', '0', '--no-open'], {cwd: fixture});
  const origin = await new Promise((resolve, reject) => {
    let output = '', error = '';
    server.stdout.on('data', data => { output += data; if (output.includes('\n')) resolve(output.split('\n')[0].trim()); });
    server.stderr.on('data', data => error += data);
    server.once('error', reject);
    server.once('exit', code => reject(Error(`server ${code}: ${error}`)));
  });
  const engine = process.env.PLAYWRIGHT_BROWSER === 'webkit' ? webkit : chromium;
  browser = await engine.launch({headless: true,
    ...(engine === chromium && process.env.PLAYWRIGHT_CHANNEL ? {channel: process.env.PLAYWRIGHT_CHANNEL} : {}),
    ...(engine === webkit && process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE ? {executablePath: process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE} : {})});
  const page = await browser.newPage({viewport: {width:1280,height:850}, reducedMotion:'reduce'});
  page.setDefaultTimeout(15000);
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.addInitScript(() => {
    // Observe real lifecycle/resource behavior rather than counting calls to
    // refresh: one live observer must own the document even after replacement.
    const NativeResizeObserver = window.ResizeObserver;
    window.outlineObservers = [];
    window.ResizeObserver = class extends NativeResizeObserver {
      constructor(callback) { super(callback); this.targets = new Set(); window.outlineObservers.push(this); }
      observe(node, options) { this.targets.add(node); super.observe(node, options); }
      unobserve(node) { this.targets.delete(node); super.unobserve(node); }
      disconnect() { this.targets.clear(); super.disconnect(); }
    };
    document.addEventListener('securitypolicyviolation', event => window.cspFailures = [...(window.cspFailures || []), event.violatedDirective]);
    document.addEventListener('datastar-fetch', event => {
      if (event.detail.type === 'datastar-patch-elements') window.outlinePatches = (window.outlinePatches || 0) + 1;
    });
  });
  let releaseDiagram;
  const diagramGate = new Promise(resolve => releaseDiagram = resolve);
  await page.route('**/mermaid-*.js', async route => { await diagramGate; await route.continue(); });
  const groupFilter = 'v' + Buffer.from('docs').toString('base64url');
  await page.goto(origin + '/projects/outline/tasks?workspace=1&group=' + groupFilter + '&external_id=outline%3Afixture');
  await page.locator(`#task-${record.id} .task-title`).click();
  const host = page.locator('#record-dialog [data-description]');
  const reader = host.locator('pl-description-reader'), view = host.locator('.markdown-body');
  const nav = host.getByRole('navigation', {name:'Description contents', includeHidden:true});
  const links = nav.locator('a'), toggle = host.getByRole('button', {name:'Contents', exact:true, includeHidden:true});
  const field = host.locator('textarea'), mode = name => host.locator(`[data-description-mode=${name}]`);
  await until(async () => await links.count() === titles.length, 'Outline not built');
  assert.equal(await nav.isVisible(), true, 'Long document opens the desktop outline');
  assert.deepEqual(await links.allTextContents(), titles, 'Outline uses rendered inline text only');
  assert.deepEqual(await nav.locator('li').evaluateAll(items => items.map(item => Number(item.dataset.headingLevel))), [1,3,2,2,2,4,1,2,2,3]);
  assert.equal(await nav.locator('ol > li > ol > li > a').first().textContent(), 'Skipped level');
  assert.equal(await nav.locator('li[data-heading-level="4"]').evaluate(li => li.parentElement.parentElement.firstElementChild.textContent), 'Repeat-2');
  const ids = await view.locator('[data-markdown-heading]').evaluateAll(nodes => nodes.map(node => node.id));
  assert.equal(new Set(ids).size, ids.length);
  assert.equal(ids.every(id => id.startsWith('description-record-' + record.id + '-')), true);
  assert.match(ids[3], /-repeat-2$/);
  assert.match(ids[4], /-repeat-2-2$/);
  assert.match(ids[5], /-日本語-café-v2$/);
  assert.match(ids[6], /-section$/);
  assert.equal(await view.locator('pl-diagram').getAttribute('data-state'), 'pending');
  await page.locator('#main').evaluate(node => node.scrollTop = 150);
  const surroundings = async () => page.evaluate(() => ({url:location.href, window:[scrollX,scrollY],
    panes:[...document.querySelectorAll('#main,.navigation,.run-workspace,.activity-panel,.inspector-scroll')].map(node => [node.scrollLeft,node.scrollTop])}));
  const before = await surroundings();
  const navigate = async (index, keyboard = false) => {
    if (keyboard) { await links.nth(index).focus(); await page.keyboard.press('Enter'); }
    else await links.nth(index).click();
    await until(async () => await links.nth(index).getAttribute('aria-current') === 'location', 'Wrong active section ' + index);
    const reached = () => view.evaluate((node, id) => {
      const heading = document.getElementById(id), box = node.getBoundingClientRect(), section = heading.getBoundingClientRect();
      const expected = Math.max(0, Math.min(node.scrollHeight-node.clientHeight, node.scrollTop + section.top-box.top-node.clientTop-12));
      return Math.abs(node.scrollTop-expected) <= 2 && section.top >= box.top && section.top < box.bottom && document.activeElement === heading;
    }, ids[index]);
    await until(reached, 'Wrong local scroll/focus for ' + titles[index]);
    assert.deepEqual(await surroundings(), before, 'Navigation changed workbench/inspector scroll or URL');
  };
  for (let i=0;i<ids.length;i++) await navigate(i, i % 2 === 1);
  // Delayed Mermaid completion shifts headings without scrolling the viewport.
  await navigate(8);
  const previousTop = await view.evaluate(node => node.scrollTop);
  releaseDiagram();
  await view.locator('pl-diagram[data-state=ready]').waitFor();
  await until(async () => {
    const expected = await view.evaluate(node => {
      const top = node.getBoundingClientRect().top + node.clientTop + 13;
      return [...node.querySelectorAll('[data-markdown-heading]')].filter(heading => heading.getBoundingClientRect().top <= top).at(-1)?.id;
    });
    return await nav.locator('[aria-current]').getAttribute('data-description-anchor') === expected;
  }, 'Active section did not follow delayed diagram layout');
  assert.equal(await view.locator('pl-diagram svg').count(), 1);
  assert.ok(await view.evaluate(node => node.scrollHeight) > previousTop);
  for (let i=0;i<ids.length;i++) await navigate(i, true);
  await page.emulateMedia({reducedMotion:'no-preference'});
  await navigate(6);
  await page.emulateMedia({reducedMotion:'reduce'});
  console.log('PASS every heading, keyboard/smooth scrolling, delayed diagram layout');
  // Ordinary manual scrolling updates the current item and reveals it in the rail.
  await view.evaluate(node => node.scrollTop = 0);
  await until(async () => await links.first().getAttribute('aria-current') === 'location', 'Scroll tracking failed');
  await toggle.click();
  assert.equal(await nav.isVisible(), false);
  await toggle.focus(); await page.keyboard.press('Space');
  assert.equal(await nav.isVisible(), true);
  assert.equal(await toggle.getAttribute('aria-expanded'), 'true');
  // Selection/focus/reading position and one live observer survive repeated SSE morphs.
  await navigate(5);
  const localTop = await view.evaluate(node => node.scrollTop);
  await links.nth(5).focus();
  await reader.evaluate(node => window.originalReader = node);
  for (let i=0;i<5;i++) {
    cli('edit', plain.id, '--title', 'Refresh ' + i);
    const patches = await page.evaluate(() => window.outlinePatches || 0);
    const response = page.waitForResponse(r => r.request().headers()['pellets-target'] === 'live');
    await page.evaluate(() => document.dispatchEvent(new CustomEvent('pellets-refresh')));
    await response;
    await page.waitForFunction(count => window.outlinePatches > count, patches);
    await page.waitForTimeout(100);
    assert.equal(await reader.evaluate(node => node === window.originalReader), true);
    assert.equal(await links.nth(5).evaluate(node => node === document.activeElement), true);
    assert.equal(await view.evaluate(node => node.scrollTop), localTop);
    assert.deepEqual(await view.locator('[data-markdown-heading]').evaluateAll(nodes => nodes.map(node => node.id)), ids);
    assert.equal(await page.evaluate(() => window.outlineObservers.filter(observer => [...observer.targets].some(node => node.matches('.markdown-body'))).length), 1);
  }
  console.log('PASS repeated refreshes and observer ownership');
  // Unsaved source remains authoritative; insert content above the current section.
  await mode('edit').click();
  const draft = source.replace('# Overview', '# Newly inserted\n\n' + paragraph + '# Overview') + '\n## New tail\n\nDraft only.\n';
  await field.fill(draft);
  await field.evaluate(node => { node.setSelectionRange(17,29,'backward'); node.scrollTop = 45; });
  await mode('view').click();
  await until(async () => await links.count() === titles.length + 2, 'Draft headings not rebuilt');
  assert.equal(await nav.locator('[aria-current]').textContent(), titles[5], 'Draft edit lost the current section');
  await mode('edit').click();
  assert.equal(await field.inputValue(), draft);
  assert.deepEqual(await field.evaluate(node => [node.selectionStart,node.selectionEnd,node.selectionDirection,node.scrollTop]), [17,29,'backward',45]);
  await mode('view').click();
  assert.equal(cli('show', record.id).description, source, 'Preview must not save');
  const save = page.waitForResponse(r => r.url().includes('/edit') && r.request().method() === 'POST');
  await page.getByRole('button', {name:'Save changes',exact:true}).click();
  assert.equal((await save).status(), 200);
  await until(() => cli('show', record.id).description === draft, 'Save did not preserve source');
  assert.equal(await links.count(), titles.length + 2);
  // Live saved description changes rebuild the outline without switching pellets.
  const liveSource = draft + '\n## Live addition\n\nMore text.\n';
  cli('edit', record.id, '--description', liveSource);
  const refresh = page.waitForResponse(r => r.request().headers()['pellets-target'] === 'live');
  await page.evaluate(() => document.dispatchEvent(new CustomEvent('pellets-refresh')));
  await refresh;
  await until(async () => (await links.allTextContents()).includes('Live addition'), 'Live outline stayed stale');
  assert.equal(await field.inputValue(), liveSource);
  assert.equal(page.url(), before.url);
  console.log('PASS draft, saved and live heading edits');
  const screenshot = async name => {
    assert.deepEqual(await page.evaluate(() => (window.cspFailures || []).splice(0)), []);
    await page.screenshot({path:path.join(temporary,name)});
    await page.waitForTimeout(20);
    assert.deepEqual(await page.evaluate(() => (window.cspFailures || []).splice(0)), engine === webkit ? ['style-src-elem'] : []);
  };
  for (const theme of ['gruvbox-light','gruvbox-dark','light','dark','icy']) {
    await page.evaluate(theme => window.Workbench.applyTheme(theme), theme);
    for (const width of [1280,1092,959,678,390]) {
      await page.setViewportSize({width,height:850});
      await page.waitForTimeout(100);
      const compact = width < 960;
      assert.equal(await nav.isVisible(), !compact);
      if (compact) {
        await toggle.focus(); await page.keyboard.press('Enter');
        assert.equal(await nav.isVisible(), true);
        const boxes = await reader.evaluate(node => {
          const nav = node.querySelector('nav').getBoundingClientRect(), view = node.querySelector('.markdown-body').getBoundingClientRect();
          return {navBottom:nav.bottom, viewTop:view.top};
        });
        assert.ok(boxes.navBottom <= boxes.viewTop, 'Compact contents obscures the document');
      }
      const geometry = await view.boundingBox();
      assert.ok(geometry.x >= 0 && geometry.x + geometry.width <= width);
      if (!compact) assert.ok(geometry.width >= 630, 'Desktop document lost reading width');
      await screenshot(`${theme}-${width}.png`);
      if (compact) {
        await links.nth(2).focus(); await page.keyboard.press('Enter');
        assert.equal(await nav.isVisible(), false, 'Compact contents stays open after navigation');
        assert.equal(await field.inputValue(), liveSource);
        assert.equal(page.url(), before.url);
        assert.equal(await view.locator('[data-markdown-heading]').nth(2).evaluate(node => node === document.activeElement), true);
      }
    }
  }
  // Every compact entry still reaches the right section after closing its list,
  // including short trailing sections that share a clamped scroll destination.
  const compactIDs = await links.evaluateAll(nodes => nodes.map(node => node.dataset.descriptionAnchor));
  const background = () => page.evaluate(() => ({url:location.href, window:[scrollX,scrollY],
    panes:[...document.querySelectorAll('#main,.run-workspace,.activity-panel')].map(node => [node.scrollLeft,node.scrollTop])}));
  const compactBefore = await background();
  for (let i=0;i<compactIDs.length;i++) {
    await toggle.click();
    await links.nth(i).focus();
    await page.keyboard.press('Enter');
    await until(async () => await links.nth(i).getAttribute('aria-current') === 'location', 'Compact section tracking failed');
    assert.equal(await nav.isVisible(), false);
    assert.equal(await view.evaluate((node,id) => {
      const heading = document.getElementById(id), section = heading.getBoundingClientRect(), box = node.getBoundingClientRect();
      return heading === document.activeElement && section.top >= box.top && section.top < box.bottom;
    }, compactIDs[i]), true, 'Compact entry missed its section: ' + compactIDs[i]);
    assert.deepEqual(await background(), compactBefore);
  }
  await page.setViewportSize({width:1280,height:850});
  await page.getByRole('link', {name:'Close inspector',exact:true}).click();
  await until(async () => await page.evaluate(() => window.outlineObservers.every(observer => [...observer.targets].every(node => node.isConnected))), 'Disconnected reader retained observers');
  await page.locator('#queue-filters > summary').click();
  await page.getByRole('link', {name:'Clear filters',exact:true}).click();
  for (const item of [short,single,plain]) {
    await page.locator(`#task-${item.id} .task-title`).click();
    await view.waitFor({state:'visible'});
    await page.waitForTimeout(100);
    assert.equal(await nav.isVisible(), false, 'Short, single-section, and headingless documents start uncluttered');
    assert.equal(await toggle.isVisible(), item !== plain);
    if (item !== plain) {
      await toggle.click();
      assert.equal(await nav.isVisible(), true);
      await links.last().click();
      await until(async () => await links.last().getAttribute('aria-current') === 'location', 'Short-document navigation did not settle');
    }
    await page.getByRole('link', {name:'Close inspector',exact:true}).click();
  }
  assert.deepEqual(errors, []);
  assert.deepEqual(await page.evaluate(() => window.cspFailures || []), []);
  console.log('PASS hierarchical outline, unique anchors, local keyboard navigation, delayed layout, live edits, drafts, lifecycle, themes and responsive controls');
  console.log('Visual artifacts: ' + temporary);
})().catch(error => { console.error(error); process.exitCode = 1; }).finally(async () => {
  if (browser) await browser.close();
  if (server?.exitCode === null) { const stopped = new Promise(resolve => server.once('exit', resolve)); server.kill('SIGINT'); await stopped; }
});
