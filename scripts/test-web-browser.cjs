// Optional browser regression suite. Requires Playwright on Node's module path.
// NODE_PATH=/path/to/node_modules PLAYWRIGHT_CHANNEL=chrome node scripts/test-web-browser.cjs
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium} = require('playwright');

const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-browser-'));
const fixture = path.join(temporary, 'browser');
const binary = path.join(temporary, process.platform === 'win32' ? 'pl.exe' : 'pl');
let server, browser;
const cli = (...args) => JSON.parse(execFileSync(binary, args, {cwd: fixture, encoding: 'utf8'}));
const until = async (predicate, message) => {
  const deadline = Date.now() + 10000;
  while (Date.now() < deadline) {
    if (await predicate()) return;
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  throw new Error(message);
};

(async () => {
  execFileSync('go', ['build', '-o', binary, './cmd/pl'], {cwd: repository});
  fs.mkdirSync(fixture);
  execFileSync('git', ['init', '-q'], {cwd: fixture});
  const first = cli('add', 'Alpha browser task').data;
  cli('add', 'Zulu browser task');
  server = spawn(binary, ['web', '--port', '0', '--no-open'], {cwd: fixture});
  const origin = await new Promise((resolve, reject) => {
    let output = '';
    const timeout = setTimeout(() => reject(new Error('Server did not start')), 10000);
    server.stdout.on('data', chunk => {
      output += chunk;
      if (output.includes('\n')) { clearTimeout(timeout); resolve(output.split('\n')[0].trim()); }
    });
    server.once('error', reject);
    server.once('exit', code => { clearTimeout(timeout); reject(new Error(`Server exited: ${code}`)); });
  });
  browser = await chromium.launch({headless: true, ...(process.env.PLAYWRIGHT_CHANNEL ? {channel: process.env.PLAYWRIGHT_CHANNEL} : {})});
  const page = await browser.newPage();
  page.setDefaultTimeout(10000);
  const errors = [], external = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => { if (message.type() === 'error' && !message.text().includes('404')) errors.push(message.text()); });
  page.on('request', request => { if (!request.url().startsWith(origin)) external.push(request.url()); });
  await page.addInitScript(() => {
    window.results = [];
    document.addEventListener('datastar-signal-patch', event => {
      if (event.detail._webResult) window.results.push(event.detail._webResult.status);
    });
  });
  await page.goto(origin);
  await page.waitForFunction(() => document.querySelector('html').getAttribute('data-nonce') === null);
  const base = `/projects/${first.project}/tasks`;
  assert.equal(await page.locator('.row-link').count(), 2);

  await page.locator('.row-link').first().click();
  await page.locator('[data-inspector]').waitFor();
  await until(() => page.url().includes(first.id), 'Inspector did not update history');
  await page.locator('form.dirty-track input[name=title]').fill('Edited browser task');
  await page.getByRole('button', {name: 'Save changes', exact: true}).click();
  await until(async () => !(await page.locator('[data-inspector].is-dirty').count()), 'Saved inspector stayed dirty');
  await until(async () => (await page.locator('.task-title').allTextContents()).includes('Edited browser task'), 'Save did not refresh list');

  // Close and sort immediately using the canonical inspector-free route.
  await page.getByRole('link', {name: 'Close inspector'}).click();
  await page.locator('[data-inspector]').waitFor({state: 'detached'});
  await page.locator('#task-sort-title').click();
  await until(() => page.url().includes('sort=title'), 'Sort URL did not update');
  assert.equal(new URL(page.url()).pathname, base);
  await until(async () => (await page.locator('[aria-sort=ascending]').textContent()).includes('Title'), 'Sort heading did not update');
  await page.locator('#search').fill('Edited');
  await until(async () => (await page.locator('.row-link').count()) === 1 && page.url().includes('q=Edited'), 'Search did not filter');
  const filteredURL = page.url();
  await page.locator('#search').fill('Zulu');
  await until(() => page.url().includes('q=Zulu'), 'Second search did not complete');
  await page.goBack();
  await until(async () => page.url() === filteredURL && await page.locator('#search').inputValue() === 'Edited', 'Back did not restore filters');
  await page.reload();
  assert.equal(await page.locator('#search').inputValue(), 'Edited');

  await page.goto(origin + base);
  await page.waitForFunction(() => !document.documentElement.hasAttribute('data-nonce'));
  // Allow the monitor's 300ms initial data_version baseline to be sampled.
  await page.waitForTimeout(450);
  await page.locator('.row-link').first().focus();
  await page.evaluate(() => {
    window.retainedRow = document.querySelector('.task-row');
    window.retainedTable = document.querySelector('.table-scroll');
    window.retainedFocus = document.activeElement;
  });
  cli('add', 'External live update');
  await until(async () => (await page.locator('.task-title').allTextContents()).includes('External live update'), 'CLI commit did not refresh browser');

  assert.equal(await page.locator('#project-counts').textContent(), '3 open · 0 active · 0 memories');
  assert.equal(await page.locator('#area-tabs a').first().textContent(), 'Tasks 3');
  assert.equal(await page.evaluate(() => window.retainedRow === document.querySelector('.task-row') && window.retainedTable === document.querySelector('.table-scroll') && window.retainedFocus === document.activeElement), true, 'Live update replaced stable DOM or lost focus');
  let refreshRequests = 0;
  const countRefresh = request => {
    if (request.headers()['pellets-target']) refreshRequests++;
  };
  page.on('request', countRefresh);
  await page.evaluate(() => document.dispatchEvent(new CustomEvent('pellets-refresh')));
  await page.waitForTimeout(250);
  page.off('request', countRefresh);
  assert.equal(refreshRequests, 1, 'One invalidation must use one bundled request');

  // Hold a completed HTTP response until after a user interaction, to exercise
  // guards at patch time rather than just guards before starting requests.
  async function holdRefresh(target) {
    let release, captured;
    const gate = new Promise(resolve => { release = resolve; });
    const ready = new Promise(resolve => { captured = resolve; });
    const handler = async route => {
      if (route.request().headers()['pellets-target'] !== 'live') return route.continue();
      const response = await route.fetch();
      captured();
      await gate;
      await route.fulfill({response});
      await page.unroute('**/projects/**', handler);
    };
    await page.route('**/projects/**', handler);
    await page.evaluate(() => document.dispatchEvent(new CustomEvent('pellets-refresh')));
    await ready;
    return release;
  }
  let release = await holdRefresh('project-record');
  await page.locator('#project-record > summary').click();
  release();
  await page.waitForTimeout(250);
  assert.equal(await page.locator('#project-record').evaluate(el => el.open), true);
  await page.locator('#project-record > summary').click();

  await page.locator('.row-link').first().click();
  await page.locator('[data-inspector]').waitFor();
  await page.waitForTimeout(300);
  release = await holdRefresh('inspector-host');
  await page.locator('form.dirty-track input[name=title]').fill('Unsaved draft');
  release();
  await page.waitForTimeout(250);
  assert.equal(await page.locator('form.dirty-track input[name=title]').inputValue(), 'Unsaved draft');
  page.once('dialog', dialog => dialog.dismiss());
  await page.getByRole('link', {name: 'Close inspector'}).click();
  assert.equal(await page.locator('form.dirty-track input[name=title]').inputValue(), 'Unsaved draft');
  const selectedURL = page.url();
  const dismissedBack = new Promise(resolve => page.once('dialog', async dialog => {
    await dialog.dismiss();
    resolve();
  }));
  await page.evaluate(() => history.back());
  await dismissedBack;
  await until(() => page.url() === selectedURL, 'Cancelled Back did not restore the inspector URL');
  assert.equal(await page.locator('form.dirty-track input[name=title]').inputValue(), 'Unsaved draft');
  await page.locator('#search').fill('x'.repeat(1025));
  await page.locator('#task-list .error-state').waitFor();
  assert.equal(await page.locator('form.dirty-track input[name=title]').inputValue(), 'Unsaved draft');
  await page.locator('#search').fill('');
  await page.locator('#task-list .error-state').waitFor({state: 'detached'});

  // A second writer commits while this inspector retains an unsaved old version.
  const submission = await page.locator('form.dirty-track').evaluate(form => ({
    action: form.action, fields: Object.fromEntries(new FormData(form))
  }));
  const competing = await page.request.post(submission.action, {
    headers: {Origin: origin}, form: {...submission.fields, title: 'Changed elsewhere'}
  });
  assert.equal(competing.status(), 200);
  await page.getByRole('button', {name: 'Save changes', exact: true}).click();
  await page.locator('.conflict-state').waitFor();
  assert.ok((await page.locator('.conflict-state').textContent()).includes('Unsaved draft'));
  await page.evaluate(() => document.dispatchEvent(new CustomEvent('pellets-refresh')));
  await page.waitForTimeout(250);
  assert.equal(await page.locator('.conflict-state').count(), 1);
  assert.equal(await page.locator('form.dirty-track input[name=title]').inputValue(), 'Changed elsewhere');
  assert.ok((await page.evaluate(() => window.results)).includes(409));

  await page.locator('form.dirty-track input[name=title]').fill('Resolved browser task');
  await page.getByRole('button', {name: 'Save changes', exact: true}).click();
  await page.locator('.conflict-state').waitFor({state: 'detached'});
  await page.getByRole('button', {name: 'Start', exact: true}).click();
  await page.getByRole('button', {name: 'Release', exact: true}).waitFor();
  await page.getByRole('button', {name: 'Release', exact: true}).click();
  await page.getByRole('button', {name: 'Start', exact: true}).waitFor();

  await page.goto(origin + base);
  await page.getByText('New task', {exact: true}).click();
  await page.locator('.create-popover input[name=title]').fill('Browser created task');
  await page.getByRole('button', {name: 'Create task', exact: true}).click();
  await until(async () => await page.locator('form.dirty-track input[name=title]').inputValue() === 'Browser created task', 'Task creation did not render');
  assert.ok((await page.evaluate(() => window.results)).includes(201));

  await page.goto(origin + `/projects/${first.project}/memories`);
  await page.getByText('New memory', {exact: true}).click();
  await page.locator('.create-popover textarea').fill('Browser memory');
  await page.getByRole('button', {name: 'Create memory', exact: true}).click();
  await page.locator('[data-inspector]').waitFor();
  await page.locator('form.dirty-track textarea').fill('Edited memory');
  await page.getByRole('button', {name: 'Save text', exact: true}).click();
  await until(async () => (await page.locator('.memory-card p').allTextContents()).includes('Edited memory'), 'Memory save did not refresh cards');
  assert.equal(new URL(page.url()).searchParams.has('datastar'), false);

  // Validation is transported successfully but remains an application 422.
  await page.locator('form.dirty-track textarea').fill('   ');
  await page.getByRole('button', {name: 'Save text', exact: true}).click();
  await page.locator('.error-state').waitFor();
  assert.ok((await page.evaluate(() => window.results)).includes(422));
  await page.setViewportSize({width: 390, height: 844});
  await page.goto(origin + base + '/' + first.id);
  await until(async () => await page.locator('[data-inspector]').getAttribute('aria-modal') === 'true', 'Narrow inspector is not modal');
  await page.keyboard.press('Escape');
  await page.locator('[data-inspector]').waitFor({state: 'detached'});
  assert.equal(await page.evaluate(() => document.activeElement.matches('.row-link')), true);
  // A filter typed during a save is applied after the save's bundle, so its
  // response cannot suppress the mutation result or leave stale version tokens.
  await page.locator('.row-link').first().click();
  await page.locator('form.dirty-track input[name=title]').fill('Queued save');
  let releaseSave;
  const saveGate = new Promise(resolve => { releaseSave = resolve; });
  await page.route('**/pellets/**/edit*', async route => {
    await saveGate;
    await route.continue();
  });
  await page.getByRole('button', {name: 'Save changes', exact: true}).click();
  await page.locator('#search').fill('Queued');
  await page.waitForTimeout(400);
  releaseSave();
  await until(async () => page.url().includes('q=Queued') && !(await page.locator('[data-inspector].is-dirty').count()), 'Filter interrupted save or was dropped');
  assert.equal(await page.locator('.task-title').count(), 1);
  await page.unroute('**/pellets/**/edit*');
  await page.goto(origin + base);

  // A failed save must explain the failure without losing the draft or leaving
  // the submit control disabled. Hold the response to inspect its loading state.
  await page.locator('.row-link').first().click();
  await page.locator('form.dirty-track input[name=title]').fill('Retained failed draft');
  let releaseFailure;
  const failureGate = new Promise(resolve => { releaseFailure = resolve; });
  await page.route('**/pellets/**/edit*', async route => {
    await failureGate;
    await route.fulfill({status: 503, contentType: 'text/plain', body: 'Service Unavailable'});
  });
  const save = page.getByRole('button', {name: 'Save changes', exact: true});
  await save.click();
  await until(() => save.isDisabled(), 'Save was not disabled while pending');
  assert.equal(await page.locator('#request-feedback').textContent(), 'Saving…');
  releaseFailure();
  await until(async () => (await page.locator('#request-feedback').textContent()).includes('Save could not be confirmed'), 'Failed save had no feedback');
  assert.equal(await save.isEnabled(), true);
  assert.equal(await page.locator('form.dirty-track input[name=title]').inputValue(), 'Retained failed draft');
  assert.equal(await page.locator('[data-inspector].is-dirty').count(), 1);
  await page.unroute('**/pellets/**/edit*');
  await save.click();
  await until(async () => !(await page.locator('[data-inspector].is-dirty').count()), 'Retry did not save');
  assert.equal(await page.locator('#request-feedback').isHidden(), true);
  // Confirmed discard must clear live values even when both records have the
  // same server defaults. Keying only list rows does not protect inspector forms.
  await page.setViewportSize({width: 1440, height: 1000});
  await page.goto(origin + base);
  await page.locator('.row-link').first().click();
  const firstInspector = await page.locator('[data-inspector]').getAttribute('id');
  const description = page.locator('form.dirty-track textarea[name=description]');
  assert.equal(await description.inputValue(), '');
  await description.fill('Discard this task description');
  page.once('dialog', dialog => dialog.dismiss());
  await page.locator('.row-link').nth(1).click();
  assert.equal(await description.inputValue(), 'Discard this task description');
  assert.equal(await page.locator('[data-inspector]').getAttribute('id'), firstInspector);
  page.once('dialog', dialog => dialog.accept());
  await page.locator('.row-link').nth(1).click();
  await until(async () => await page.locator('[data-inspector]').getAttribute('id') !== firstInspector, 'Task inspector did not switch');
  assert.equal(await description.inputValue(), '');
  assert.equal(await page.locator('[data-inspector].is-dirty').count(), 0);

  // A failed lifecycle request must not reset the confirmed draft preemptively.
  await description.fill('Keep this draft on failure');
  await page.route('**/pellets/**/transition*', route => route.fulfill({status: 503, contentType: 'text/plain', body: 'Service Unavailable'}));
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', {name: 'Start', exact: true}).click();
  await until(async () => (await page.locator('#request-feedback').textContent()).includes('Save could not be confirmed'), 'Failed lifecycle had no feedback');
  assert.equal(await description.inputValue(), 'Keep this draft on failure');
  assert.equal(await page.locator('[data-inspector].is-dirty').count(), 1);
  await page.unroute('**/pellets/**/transition*');
  // The same record keeps its identity during a lifecycle patch, so discard also
  // needs an explicit reset after the patch guards accept the response.
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', {name: 'Start', exact: true}).click();
  await page.getByRole('button', {name: 'Release', exact: true}).waitFor();
  assert.equal(await description.inputValue(), '');
  assert.equal(await page.locator('[data-inspector].is-dirty').count(), 0);
  await page.getByRole('button', {name: 'Release', exact: true}).click();
  await page.getByRole('button', {name: 'Start', exact: true}).waitFor();

  // Edits made after discard confirmation supersede that confirmation. Delay the
  // navigation response and verify neither reset nor morph consumes newer text.
  await description.fill('Draft at confirmation');
  const protectedInspector = await page.locator('[data-inspector]').getAttribute('id');
  const protectedURL = page.url();
  let releaseNavigation;
  const navigationGate = new Promise(resolve => { releaseNavigation = resolve; });
  await page.route('**/projects/**/tasks/**', async route => {
    if (route.request().headers()['pellets-target'] === 'inspector-host') await navigationGate;
    await route.continue();
  });
  page.once('dialog', dialog => dialog.accept());
  await page.locator('.row-link').first().click();
  await description.fill('Newer draft after confirmation');
  releaseNavigation();
  await until(async () => (await page.locator('#request-feedback').textContent()).includes('Could not load updates'), 'Rejected navigation did not finish');
  assert.equal(await page.locator('[data-inspector]').getAttribute('id'), protectedInspector);
  assert.equal(page.url(), protectedURL);
  assert.equal(await page.locator('.task-row.selected').getAttribute('id'), protectedInspector.replace('inspector-', ''));
  assert.equal(await description.inputValue(), 'Newer draft after confirmation');
  assert.equal(await page.locator('[data-inspector].is-dirty').count(), 1);
  await page.unroute('**/projects/**/tasks/**');
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('link', {name: 'Close inspector'}).click();
  await page.locator('[data-inspector]').waitFor({state: 'detached'});

  // Memory text is required: use two records with the same nonempty baseline.
  await page.goto(origin + `/projects/${first.project}/memories`);
  const memoryCreation = await page.locator('.create-popover form').evaluate(form => ({
    action: form.action, fields: Object.fromEntries(new FormData(form))
  }));
  for (let i = 0; i < 2; i++) {
    const created = await page.request.post(memoryCreation.action, {
      headers: {Origin: origin}, form: {...memoryCreation.fields, text: 'Shared memory baseline'}
    });
    assert.equal(created.status(), 201);
  }
  await page.reload();
  const matchingMemories = page.locator('.memory-card').filter({has: page.getByText('Shared memory baseline', {exact: true})});
  assert.equal(await matchingMemories.count(), 2);
  await matchingMemories.nth(0).locator('a').click();
  const firstMemoryInspector = await page.locator('[data-inspector]').getAttribute('id');
  await page.locator('form.dirty-track textarea').fill('Discard this memory text');
  page.once('dialog', dialog => dialog.accept());
  await matchingMemories.nth(1).locator('a').click();
  await until(async () => await page.locator('[data-inspector]').getAttribute('id') !== firstMemoryInspector, 'Memory inspector did not switch');
  assert.equal(await page.locator('form.dirty-track textarea').inputValue(), 'Shared memory baseline');
  assert.equal(await page.locator('[data-inspector].is-dirty').count(), 0);
  // Chromium reports the intentionally injected HTTP 503 on its console.
  assert.deepEqual(errors.filter(message => !message.includes('503 (Service Unavailable)')), [], 'Browser errors');
  assert.deepEqual(external, [], 'Unexpected external requests');
  console.log('Browser checks passed: navigation, sort/filter/history, mutations, bundled refresh/counts, DOM/focus preservation, queued filters, loading/failure/retry, dirty/in-flight guards, confirmed discard across tasks/memories/lifecycle, conflicts, validation, CSP, and narrow-screen focus.');
})().catch(error => { console.error(error); process.exitCode = 1; }).finally(async () => {
  if (browser) await browser.close();
  if (server && server.exitCode === null) {
    const exited = new Promise(resolve => server.once('exit', resolve));
    server.kill('SIGINT');
    await exited;
  }
  fs.rmSync(temporary, {recursive: true, force: true});
});
