// Optional: NODE_PATH=/path/to/node_modules node scripts/test-web-design-system-browser.cjs
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-design-system-'));
const servers = [];
let browser;
async function start(binary) {
  const server = spawn(binary, ['server', '--port', '0', '--no-open'], {cwd: temporary});
  servers.push(server);
  return new Promise((resolve, reject) => {
    let output = '';
    const timeout = setTimeout(() => reject(new Error('Server did not start')), 10000);
    server.stdout.on('data', chunk => {
      output += chunk;
      if (output.includes('\n')) { clearTimeout(timeout); resolve(output.split('\n')[0].trim()); }
    });
    server.once('error', reject);
    server.once('exit', code => { clearTimeout(timeout); reject(new Error(`Server exited: ${code}`)); });
  });
}
(async () => {
  const dev = path.join(temporary, 'pl-dev');
  const release = path.join(temporary, 'pl-release');
  execFileSync('go', ['build', '-o', dev, './cmd/pl'], {cwd: repository});
  execFileSync('go', ['build', '-ldflags=-X main.version=1.2.3', '-o', release, './cmd/pl'], {cwd: repository});
  execFileSync('git', ['init', '-q'], {cwd: temporary});
  const origin = await start(dev);
  const engine = process.env.PLAYWRIGHT_BROWSER === 'webkit' ? webkit : chromium;
  browser = await engine.launch({headless: true, ...(engine === chromium && process.env.PLAYWRIGHT_CHANNEL ? {channel: process.env.PLAYWRIGHT_CHANNEL} : {}), ...(engine === webkit && process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE ? {executablePath: process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE} : {})});
  const page = await browser.newPage({viewport: {width: 1440, height: 1000}});
  const errors = [], writes = [], unexpected = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => { if (message.type() === 'error') errors.push(message.text()); });
  page.on('request', request => {
    if (request.method() !== 'GET') writes.push(request.url());
    if (!request.url().startsWith(origin + '/assets/') && request.url() !== origin + '/dev/design-system') unexpected.push(request.url());
  });
  await page.goto(origin + '/dev/design-system');
  await page.locator('#theme-select-trigger').waitFor();
  assert.equal(await page.title(), 'Design system · Pellets');
  const contents = page.locator('#contents');
  await contents.getByRole('navigation', {name:'Description contents'}).waitFor();
  assert.equal(await contents.locator('nav a').count(), 5);
  await contents.locator('nav a').last().focus();
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => document.querySelector('#contents nav a:last-child[aria-current]')?.textContent.includes('日本語'));
  assert.equal(new URL(page.url()).hash, '', 'Document navigation changed gallery routing');
  await require('./web-components-contract.cjs')(page);
  // Exercise the public component API independently of application transport.
  assert.deepEqual(await page.evaluate(() => ['button','field','checkbox','select','number','menu','disclosure','dialog','tabs','badge','notice','icon','resizer'].filter(name => !customElements.get('pl-'+name))), []);
  const componentResult = await page.evaluate(async () => {
    const frame = () => new Promise(resolve => requestAnimationFrame(resolve));
    const fixture = document.createElement('section');
    fixture.innerHTML = `<form id="component-form"><label for="component-title">Title</label><pl-field><input id="component-title" name="title" required value="Original"></pl-field><pl-checkbox><input type="checkbox" name="review" value="yes" checked></pl-checkbox><pl-select><select id="component-select" name="mode"><option value="one" selected>One</option><option value="all">All</option></select></pl-select><pl-number class="number-control"><pl-field><input aria-label="Count" name="count" type="number" min="1" max="3" value="1"></pl-field><pl-button><button type="button" data-number-step="up">Increase</button></pl-button></pl-number><pl-button id="component-action" variant="primary"><button type="button">Action</button></pl-button></form><pl-menu><details class="row-menu"><summary>Actions</summary><button type="button" role="menuitem">First</button></details></pl-menu><pl-dialog><dialog aria-label="Lifecycle test"><button type="button" data-dialog-dismiss>Close</button></dialog></pl-dialog>`;
    document.body.append(fixture);
    const form = fixture.querySelector('form');
    const number = fixture.querySelector('pl-number');
    let changes = 0;
    number.addEventListener('change', () => changes++);
    for (let i=0;i<4;i++) {number.remove();form.append(number);}
    number.querySelector('button').click();
    const stepped = number.value;
    const field = fixture.querySelector('pl-field');
    field.value = 'Edited';
    const checkbox = fixture.querySelector('pl-checkbox');
    checkbox.indeterminate = true;
    const submitted = Object.fromEntries(new FormData(form));
    const select = fixture.querySelector('pl-select');
    for (let i=0;i<4;i++) {select.remove();form.append(select);}
    select.value = 'all';
    const selected = select.querySelector('.select-trigger').textContent.trim();
    form.reset();await frame();
    const reset = select.querySelector('.select-trigger').textContent.trim();
    select.scrollIntoView({block: "center"});await frame();
    select.open();
    const opened = !!document.querySelector('#component-select-listbox');
    select.remove();
    const removedClosed = !document.querySelector('#component-select-listbox');
    form.append(select);
    select.innerHTML = '<select id="component-select" name="mode"><option value="">Choose a mode</option><option value="new" selected>New options</option></select>';
    await frame();
    const replaced = select.querySelector('.select-trigger').textContent.trim();
    const triggerCount = select.querySelectorAll('.select-trigger').length;
    select.control.required = true;
    select.value = '';
    const invalid = !form.reportValidity();await frame();
    const invalidFocused = document.activeElement === select.querySelector('.select-trigger') && document.activeElement.getAttribute('aria-invalid') === 'true';
    select.value = 'new';
    const validAgain = select.checkValidity() && !select.querySelector('.select-trigger').hasAttribute('aria-invalid');
    const action = fixture.querySelector('#component-action');
    action.disabled = true;const disabled = action.control.disabled;
    action.disabled = false;action.setAttribute('busy','');const busy = action.control.disabled && action.control.getAttribute('aria-busy') === 'true';
    action.removeAttribute('busy');const restored = !action.control.disabled;
    const menu = fixture.querySelector('pl-menu');
    menu.open = true;menu.remove();document.body.click();const detachedPreserved = menu.open;
    fixture.append(menu);document.body.click();const reconnectedClosed = !menu.open;
    const dialog = fixture.querySelector('pl-dialog');
    dialog.showModal();
    const veto = event => event.preventDefault();
    dialog.addEventListener('pl-close-request', veto);
    dialog.requestClose();const guarded = dialog.open;
    dialog.removeEventListener('pl-close-request', veto);
    dialog.requestClose();const dismissed = !dialog.open;
    fixture.remove();
    return {changes,stepped,submitted,selected,reset,opened,removedClosed,replaced,triggerCount,invalid,invalidFocused,validAgain,disabled,busy,restored,detachedPreserved,reconnectedClosed,guarded,dismissed};
  });
  assert.deepEqual(componentResult, {changes:1,stepped:'2',submitted:{title:'Edited',review:'yes',mode:'one',count:'2'},selected:'All',reset:'One',opened:true,removedClosed:true,replaced:'New options',triggerCount:1,invalid:true,invalidFocused:true,validAgain:true,disabled:true,busy:true,restored:true,detachedPreserved:true,reconnectedClosed:true,guarded:true,dismissed:true});
  assert.deepEqual(await page.evaluate(() => Array.from(document.querySelectorAll('button, input:not([type=hidden]), textarea, select, dialog, details')).filter(control => !control.closest('pl-button, pl-field, pl-checkbox, pl-select, pl-number, pl-dialog, pl-menu, pl-disclosure, pl-diagram') && !control.matches('#contents [data-description-contents]')).map(control => control.outerHTML.slice(0,100))), [], 'Gallery controls must use the component library or production description controls');
  await page.waitForFunction(() => document.querySelectorAll('#ds-diagrams pl-diagram[data-state=ready]').length === 3);
  assert.equal(await page.locator('#ds-diagrams pl-diagram[data-state=error]').count(), 4);

  for (const value of ['gruvbox-dark', 'light', 'dark', 'icy', 'gruvbox-light']) {
    await page.locator('#theme-select-trigger').click();
    await page.locator(`[role=option][data-value="${value}"]`).click();
    assert.equal(await page.locator('html').getAttribute('data-theme'), value);
  }
  await page.locator('#ds-mode-trigger').click();
  await page.keyboard.press('End');
  await page.keyboard.press('Enter');
  assert.equal(await page.locator('#ds-mode').inputValue(), 'watch');
  assert.equal(await page.locator('#ds-disabled-select-trigger').isDisabled(), true);
  await page.getByRole('button', {name: 'Increase run limit'}).click();
  assert.equal(await page.locator('#ds-number').inputValue(), '4');
  await page.getByRole('button', {name: 'Decrease run limit'}).click();
  assert.equal(await page.locator('#ds-number').inputValue(), '3');
  await page.getByRole('separator', {name: 'Example panel width'}).focus();
  await page.keyboard.press('ArrowRight');
  assert.equal(await page.locator('#ds-resize-value').textContent(), '190px');
  const handle = page.getByRole('separator', {name: 'Example panel width'});
  await handle.evaluate(el => el.addEventListener('gotpointercapture', event => { el.dataset.pointer = event.pointerId; }, {once: true}));
  const handleBounds = await handle.boundingBox();
  const x = handleBounds.x + handleBounds.width / 2, y = handleBounds.y + handleBounds.height / 2;
  await page.mouse.move(x, y);
  await page.mouse.down();
  await page.mouse.move(x + 20, y);
  assert.equal(await page.locator('#ds-resize-value').textContent(), '210px');
  await handle.evaluate(el => el.releasePointerCapture(Number(el.dataset.pointer)));
  await page.mouse.up();
  await page.waitForFunction(() => document.getElementById('ds-resize-value').textContent === '190px');
  await handle.dblclick();
  assert.equal(await page.locator('#ds-resize-value').textContent(), '180px');
  await page.locator('#ds-plan-tab').focus();
  await page.keyboard.press('ArrowRight');
  assert.equal(await page.locator('#ds-run-panel').isVisible(), true);
  for (const width of [1440, 390]) {
    await page.setViewportSize({width, height: 900});
    for (const kind of ['pellet', 'memory', 'checkpoint', 'conflict']) {
      const trigger = page.locator(`#dialogs [data-record="${kind}"]`);
      await trigger.click();
      assert.equal(await page.locator('#record-dialog').isVisible(), true);
      if (kind === 'checkpoint') {
        await page.getByText('Edit scope', {exact: true}).click();
        assert.equal(await page.locator('#record-dialog .checkpoint-scope-options input').isChecked(), true);
      }
      if (kind === 'conflict') assert.equal(await page.locator('#record-dialog .conflict-state').isVisible(), true);
      await page.keyboard.press('Escape');
      assert.equal(await trigger.evaluate(el => el === document.activeElement), true);
    }
    for (const id of ['insert-dialog', 'ds-draft-dialog', 'plan-new-dialog', 'ds-drawer-dialog']) {
      await page.locator(`#dialogs [data-open-dialog="${id}"]`).click();
      const box = await page.locator(`#${id}`).boundingBox();
      assert.ok(box.width <= width && box.x >= 0, `${id} overflows at ${width}px`);
      await page.keyboard.press('Escape');
      assert.equal(await page.locator(`#${id}`).isVisible(), false);
    }
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `Page overflow at ${width}px`);
  }
  await page.locator('#dialogs [data-record=pellet]').click();
  await page.locator('#record-dialog input[name=title]').fill('A preview edit');
  await page.locator('#record-dialog').getByRole('button', {name: 'Save changes', exact: true}).click();
  assert.equal(await page.locator('#record-dialog').isVisible(), false);
  assert.match(await page.locator('#ds-notice').textContent(), /preview only/);
  await page.locator('#dialogs [data-open-dialog=insert-dialog]').click();
  await page.locator('#insert-dialog input[name=target]').first().uncheck();
  assert.equal(await page.locator('[data-insert-submit]').isDisabled(), true);
  await page.keyboard.press('Escape');
  await page.locator('#dialogs [data-open-dialog=ds-draft-dialog]').click();
  await page.getByRole('button', {name: 'Split pellet', exact: true}).click();
  assert.equal(await page.locator('.plan-split').isVisible(), true);
  await page.keyboard.press('Escape');
  page.once('dialog', dialog => dialog.dismiss());
  await page.locator('[data-confirm=discard]').click();
  assert.match(await page.locator('#ds-notice').textContent(), /Cancelled/);
  await page.locator('[data-demo-undo]').click();
  await page.locator('[data-undo]').click();
  assert.match(await page.locator('#ds-notice').textContent(), /restored/);
  await page.locator('.create-popover > summary').click();
  await page.locator('.create-popover input').fill('Sample only');
  await page.locator('.create-popover').getByRole('button', {name: 'Create pellet', exact: true}).click();
  assert.match(await page.locator('#ds-notice').textContent(), /preview only/);
  await page.locator('#queue-filters > summary').click();
  assert.equal(await page.locator('.filter-fields').isVisible(), true);
  await page.locator('.filter-fields .select-trigger').first().click();
  await page.locator('[role=option]').filter({hasText: 'Closed'}).click();
  await page.locator('.filter-fields button[type=reset]').click();
  await page.waitForFunction(() => document.querySelector('.filter-fields .select-trigger').textContent.includes('Active'));
  await page.keyboard.press('Escape');
  assert.equal(await page.locator('.filter-fields').isVisible(), false);
  assert.deepEqual(writes, [], 'Gallery must never write data');
  assert.deepEqual(unexpected, [], 'Gallery must not load application transport or external resources');
  assert.deepEqual(errors, [], 'Browser errors');
  if (process.env.DESIGN_SYSTEM_SCREENSHOT) {
    await page.setViewportSize({width: 1440, height: 1050});
    await page.evaluate(() => scrollTo(0, 0));
    await page.screenshot({path: process.env.DESIGN_SYSTEM_SCREENSHOT, fullPage: true});
  }
  const releaseOrigin = await start(release);
  for (const route of ['/dev/design-system', '/assets/design-system.js', '/assets/design-system.css']) {
    assert.equal((await page.request.get(releaseOrigin + route)).status(), 404, `Release exposes ${route}`);
  }
  assert.ok(!(await (await page.request.get(releaseOrigin)).text()).includes('href="/dev/design-system"'));
  console.log('Design system checks passed: dev/release gate, five themes, controls, dialogs, keyboard, mobile, and no writes.');
})().catch(error => { console.error(error); process.exitCode = 1; }).finally(async () => {
  if (browser) await browser.close();
  await Promise.all(servers.map(server => new Promise(resolve => {
    if (server.exitCode !== null) return resolve();
    server.once('exit', resolve);
    server.kill('SIGINT');
  })));
  fs.rmSync(temporary, {recursive: true, force: true});
});
