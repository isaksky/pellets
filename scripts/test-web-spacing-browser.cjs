// Production spacing regression: disposable data, both engines, five themes.
// NODE_PATH=/path/to/node_modules [PLAYWRIGHT_BROWSER=webkit] node scripts/test-web-spacing-browser.cjs
// PELLETS_SPACING_BASELINE=/path/to/pl captures the same scenes without assertions.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-spacing-browser-'));
const baseline = process.env.PELLETS_SPACING_BASELINE;
const binary = baseline || path.join(temporary, 'pl');
const fixture = path.join(temporary, 'workspace-with-a-very-long-unbroken-name-for-layout-audit');
const title = 'Keep the complete title available while this long row remains compact. '.repeat(5);
const group = 'AnUnbrokenGroupNameThatMustNotShrinkCheckboxesOrOverflowTheAssignmentForm';
let server, browser;
const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd: fixture, encoding: 'utf8'})).data;
const check = (condition, message) => { if (!baseline) assert.ok(condition, message); };
const frame = page => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
async function reachable(locator, message) {
  await locator.scrollIntoViewIfNeeded();
  // WebKit scroll offsets can precede the corresponding hit-testing update.
  if (!baseline) await locator.page().waitForFunction(el => {
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.x >= 0 && r.right <= innerWidth + 1 && r.y >= 0 && r.bottom <= innerHeight + 1 &&
      el.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2));
  }, await locator.elementHandle(), {timeout: 5000}).catch(error => { throw Error(message + ': ' + error.message); });
}
async function openMenu(page, selector) {
  const menu = page.locator(selector);
  await menu.locator(':scope > summary').click();
  await page.waitForFunction(selector => document.querySelector(selector)?.open, selector);
  await frame(page);
  return menu;
}
(async () => {
  if (!baseline) execFileSync('go', ['build', '-o', binary, './cmd/pl'], {cwd: repository});
  fs.mkdirSync(fixture); execFileSync('git', ['init', '-q'], {cwd: fixture});
  for (const args of [['--help'], ['add', '--help'], ['group', '--help']]) execFileSync(binary, args, {cwd: fixture});
  const first = cli('add', title, '--group', group);
  cli('add', title, '--review-targets', first.id);
  cli('group', 'create', 'Empty group');
  server = spawn(binary, ['server', '--port', '0', '--no-open'], {cwd: fixture});
  const origin = await new Promise((resolve, reject) => {
    let output = '', error = '';
    server.stdout.on('data', d => { output += d; if (output.includes('\n')) resolve(output.split('\n')[0].trim()); });
    server.stderr.on('data', d => error += d);
    server.once('error', reject); server.once('exit', code => reject(Error(`Server ${code}: ${error}`)));
  });
  const engine = process.env.PLAYWRIGHT_BROWSER === 'webkit' ? webkit : chromium;
  browser = await engine.launch({headless: true});
  const page = await browser.newPage({viewport: {width: 1280, height: 900}, hasTouch: true});
  page.setDefaultTimeout(10000);
  const errors = []; page.on('pageerror', e => errors.push(e.message));
  const url = origin + '/projects/' + first.project + '/tasks?workspace=1';
  await page.goto(url);
  const screenshot = name => page.screenshot({path: path.join(temporary, name + '.png'), animations: 'disabled'});
  for (const theme of ['gruvbox-light', 'gruvbox-dark', 'light', 'dark', 'icy']) {
    await page.locator('#theme-select-trigger').click();
    await page.locator(`[role=option][data-value="${theme}"]`).click();
    for (const width of [1280, 1092, 800, 678, 601, 390]) {
      await page.setViewportSize({width, height: 900});
      await page.locator('#main').evaluate(el => el.scrollTop = 0);
      await frame(page);
      const geometry = await page.evaluate(() => {
        const fits = selector => [...document.querySelectorAll(selector)].filter(e => e.checkVisibility()).every(e => e.scrollWidth <= e.clientWidth + 1);
        const oneLine = selector => [...document.querySelectorAll(selector)].every(e => e.getBoundingClientRect().height <= parseFloat(getComputedStyle(e).lineHeight) + 10);
        const names = [...document.querySelectorAll('.workspace-link .workspace-name')];
        return {chrome: fits('.chrome'), names: names.length && names.every(e => e.getBoundingClientRect().right <= e.closest('a').getBoundingClientRect().right),
          rows: oneLine('.task-title,.checkpoint-open'), rowHeight: document.querySelector('.pellet-row').getBoundingClientRect().height,
          reviewHeight: document.querySelector('.checkpoint-row').getBoundingClientRect().height,
          footer: [...document.querySelectorAll('.statusbar > span,.statusbar .theme-control')].filter(e=>e.checkVisibility()).every(e=>e.getBoundingClientRect().height<=24),
          titles: [...document.querySelectorAll('.task-title,.checkpoint-open')].every(e=>e.title===e.textContent),
          main: fits('#main')};
      });
      for (const key of ['chrome','names','rows','footer','titles','main']) check(geometry[key], `${theme}/${width}: ${key} ${JSON.stringify(geometry)}`);
      check(geometry.rowHeight === 37 && geometry.reviewHeight <= 110, 'Compact list rows: ' + JSON.stringify(geometry));
      await screenshot(`${theme}-${width}-queue`);
      for (const selector of ['#project-switcher', '#view-switcher', '#assignment-popover', '#assignment-remaining', '.checkpoint-row .row-menu', '.pellet-row .row-menu']) {
        const menu = await openMenu(page, selector);
        const panel = menu.locator(':scope > :not(summary)').first();
        const rect = await panel.boundingBox();
        check(rect.x >= 7 && rect.x + rect.width <= width - 7, `${selector} fits width ${width}: ${JSON.stringify(rect)}`);
        const last = panel.locator('button:not(.select-trigger),a').last();
        if (await last.count()) await reachable(last, `${selector} last action at ${width}`);
        if (selector === '#assignment-popover') {
          const sizes = await panel.locator('input[type=checkbox]').evaluateAll(inputs => inputs.map(e=>e.getBoundingClientRect().width));
          check(sizes.every(n=>n>=13), 'Long labels must not shrink checkboxes: ' + sizes);
          check(await panel.evaluate(e=>e.scrollWidth<=e.clientWidth+1), 'Assignment labels fit');
          await screenshot(`${theme}-${width}-assignment`);
        }
        await menu.locator('summary').focus(); await page.keyboard.press('Escape');
        check(!(await menu.evaluate(e => e.open)), 'Escape closes native menu');
      }
      await page.locator('#plan-tab').click(); await page.locator('#plan-message').waitFor();
      await page.locator('#plan-message').fill('Preserve this spacing audit draft');
      await reachable(page.locator('.plan-send'), 'Planning send is reachable');
      const insets = await page.locator('#plan-model-trigger').evaluate(e=>{const s=getComputedStyle(e);return [parseFloat(s.paddingLeft),parseFloat(s.paddingRight)];});
      check(insets.every(n=>n>=6), 'Inline selectors have visible background insets');
      await screenshot(`${theme}-${width}-planner`);
      await page.locator('#execution-tab').click();
      console.log(`PASS ${theme}/${width}: navigation, compact rows, menus, checkbox and selector insets, composer`);
    }
  }
  // Nested picker dismissal, live refresh, dirty values, and keyboard focus.
  await page.setViewportSize({width: 678, height: 900});
  const assignment = await openMenu(page, '#assignment-popover');
  const field = assignment.locator('[name=new_group]');
  await field.fill('Unsubmitted group draft'); await field.focus();
  await page.evaluate(() => document.dispatchEvent(new CustomEvent('pellets-refresh')));
  await frame(page);
  check(await field.inputValue() === 'Unsubmitted group draft', 'Refresh preserves assignment draft');
  await assignment.locator('.select-trigger').click();
  await page.keyboard.press('Escape');
  check(await assignment.evaluate(e=>e.open), 'Nested Escape preserves outer assignment');
  await page.keyboard.press('Escape');
  check(await page.locator('#assignment-popover > summary').evaluate(e=>e===document.activeElement), 'Escape restores assignment focus');
  await page.locator('#project-record > summary').click();
  await page.waitForFunction(() => document.querySelector('#project-record').open);
  await screenshot('settings');
  await reachable(page.locator('#project-record button[type=submit]').last(), 'Settings save is reachable');
  await page.locator('#project-record > summary').click();
  await page.locator('.task-title').first().click();
  await page.locator('#record-dialog[open]').waitFor();
  check(await page.locator('#record-dialog input[name=title]').inputValue() === title, 'Full row title is accessible in editor');
  await screenshot('record'); await page.keyboard.press('Escape');
  await page.goto(origin + '/projects/' + first.project + '/groups?workspace=1');
  await screenshot('groups');
  check(await page.locator('#main').evaluate(e=>e.scrollWidth<=e.clientWidth+1), 'Long group cards fit narrow panes');
  await page.goto(url);
  await page.locator('#search').fill('No matching records');
  await page.locator('.empty-state').waitFor(); await screenshot('empty');
  if (!baseline) assert.deepEqual(errors, []);
  console.log('Visual artifacts: ' + temporary);
})().catch(e => { console.error(e); process.exitCode = 1; }).finally(async () => {
  await browser?.close();
  if (server?.exitCode === null) { const ended = new Promise(resolve=>server.once('exit',resolve)); server.kill('SIGINT'); await ended; }
});
