// Compare actual screens against a pre-migration executable, using disposable data.
// NODE_PATH=/path/to/node_modules node scripts/test-web-components-parity-browser.cjs
// Builds HEAD as the baseline, or accepts PELLETS_UI_BASELINE=/path/to/pl.
const assert = require('node:assert/strict');
const fs = require('node:fs'), path = require('node:path'), os = require('node:os');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');

const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-ui-parity-'));
const fixture = path.join(temporary, 'parity'), current = path.join(temporary, 'pl-current');
const baseline = process.env.PELLETS_UI_BASELINE ? path.resolve(process.env.PELLETS_UI_BASELINE) : path.join(temporary, 'pl-baseline');
const results = {};
let server, browser;

async function start(binary) {
  server = spawn(binary, ['server', '--port', '0', '--no-open'], {cwd: fixture});
  return new Promise((resolve, reject) => {
    let output = '', error = '';
    const timeout = setTimeout(() => reject(Error('Server did not start: ' + error)), 10000);
    server.stdout.on('data', chunk => {
      output += chunk;
      if (output.includes('\n')) { clearTimeout(timeout); resolve(output.split('\n')[0].trim()); }
    });
    server.stderr.on('data', chunk => { error += chunk; });
    server.once('error', reject);
    server.once('exit', code => { clearTimeout(timeout); reject(Error('Server exited: ' + code + ': ' + error)); });
  });
}
async function stop() {
  if (server?.exitCode === null) {
    const ended = new Promise(resolve => server.once('exit', resolve));
    server.kill('SIGINT');
    await ended;
  }
}
async function measure(page, scene, build) {
  // Native dialog autofocus can select the title text. Normalize the caret for
  // screenshots; the workflow suites independently verify focus preservation.
  if (scene.endsWith('-record-actions') || scene.endsWith('-checkpoint-scope'))
    await page.locator('#record-dialog input[name=title]').evaluate(input => input.setSelectionRange(input.value.length, input.value.length));
  await page.screenshot({path: path.join(temporary, `${build}-${scene}.png`), animations: 'disabled', caret: 'hide'});
  results[build][scene] = await page.evaluate(() => {
    const closed = Array.from(document.querySelectorAll('details:not([open])'));
    const selectors = 'button, input:not([type=hidden]):not(.select-native), textarea, select:not(.select-native), label, summary, dialog[open], .task-row, .checkpoint-row, .memory-card, .section-heading, #main, #right-panel, #project-drawer, .plan-tabs, .create-popover[open] > form, .filter-fields:popover-open, .select-popover, .select-value, .select-chevron, .inspector > header, .dialog-footer';
    return Array.from(document.querySelectorAll(selectors))
      .filter(el => el.getClientRects().length && getComputedStyle(el).visibility !== 'hidden' &&
        !closed.some(details => details.contains(el) && !details.querySelector('summary')?.contains(el)))
      .map(el => {
        const bounds = el.getBoundingClientRect(), style = getComputedStyle(el);
        return {
          tag: el.tagName, id: el.id.replace(/workbench-select-\d+/g, 'generated-select'),
          name: el.getAttribute('name'), label: el.getAttribute('aria-label'),
          bounds: [bounds.x, bounds.y, bounds.width, bounds.height].map(value => Math.round(value * 100) / 100),
          style: Object.fromEntries(['display', 'backgroundColor', 'color', 'fontFamily', 'fontSize', 'fontWeight', 'lineHeight', 'borderRadius', 'borderWidth', 'borderColor', 'boxShadow', 'padding', 'margin', 'gap'].map(key => [key, style[key]])),
        };
      });
  });
}

(async () => {
  if (!process.env.PELLETS_UI_BASELINE) {
    const source = path.join(temporary, 'baseline-source');
    fs.mkdirSync(source);
    const archive = path.join(temporary, 'baseline.tar');
    fs.writeFileSync(archive, execFileSync('git', ['archive', 'HEAD'], {cwd: repository, maxBuffer: 30 * 1024 * 1024}));
    execFileSync('tar', ['-xf', archive, '-C', source]);
    execFileSync('go', ['build', '-o', baseline, './cmd/pl'], {cwd: source});
    const commit = execFileSync('git', ['rev-parse', 'HEAD'], {cwd: repository, encoding: 'utf8'}).trim();
    fs.writeFileSync(path.join(temporary, 'baseline-commit.txt'), commit + '\n');
    console.log('Baseline commit: ' + commit);
  }
  execFileSync('go', ['build', '-o', current, './cmd/pl'], {cwd: repository});
  fs.mkdirSync(fixture);
  execFileSync('git', ['init', '-q'], {cwd: fixture});
  const cli = (...args) => JSON.parse(execFileSync(current, args, {cwd: fixture, encoding: 'utf8'})).data;
  const first = cli('add', 'Preserve the existing interface', '--group', 'Interface');
  cli('add', 'Keep interactions predictable', '--group', 'Runtime');
  cli('add', 'Review interface work', '--review-targets', first.id);
  cli('memory', 'add', '--text', 'Preserve spacing, colors, and keyboard behavior.', '--created-by', 'agent');
  const engine = process.env.PLAYWRIGHT_BROWSER === 'webkit' ? webkit : chromium;
  browser = await engine.launch({headless: true,
    ...(engine === chromium && process.env.PLAYWRIGHT_CHANNEL ? {channel: process.env.PLAYWRIGHT_CHANNEL} : {}),
    ...(engine === webkit && process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE ? {executablePath: process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE} : {})});
  for (const [build, binary] of [['before', baseline], ['after', current]]) {
    results[build] = {};
    const origin = await start(binary), page = await browser.newPage({viewport: {width: 1280, height: 900}});
    page.setDefaultTimeout(12000);
    const tasksURL = origin + '/projects/' + first.project + '/tasks?workspace=1';
    for (const theme of ['icy', 'gruvbox-light', 'gruvbox-dark', 'light', 'dark']) {
    await page.setViewportSize({width: 1280, height: 900});
    await page.goto(tasksURL);
    await page.locator('#theme-select-trigger').click();
    await page.locator(`[role=option][data-value="${theme}"]`).click();
    const capture = scene => measure(page, `${theme}-${scene}`, build);
    await page.locator('#execution-tab').click();
    await page.getByRole('button', {name: '▷ Start next', exact: true}).waitFor();
    await capture('queue');
    await page.getByText('+ New pellet', {exact: true}).click();
    await capture('create');
    await page.getByText('+ New pellet', {exact: true}).click();
    await page.locator('#filter-summary').click();
    await page.locator('.filter-fields:popover-open').waitFor();
    await capture('filters');
    await page.locator('.filter-fields .select-trigger').first().click();
    await capture('filter-options');
    await page.keyboard.press('Escape');
    await page.keyboard.press('Escape');
    await page.locator('.task-title').first().click();
    await page.locator('#record-dialog[open]').waitFor();
    await capture('editor');
    await page.locator('#record-dialog .record-actions > summary').click();
    await capture('record-actions');
    await page.keyboard.press('Escape');
    await page.keyboard.press('Escape');
    await page.locator('.checkpoint-open').click();
    await page.locator('#record-dialog[open]').waitFor();
    await page.getByText('Edit scope', {exact: true}).click();
    await capture('checkpoint-scope');
    await page.keyboard.press('Escape');
    await page.locator('#assignment-popover > summary').click();
    await capture('assignment');
    await page.keyboard.press('Escape');
    await page.goto(origin + '/projects/' + first.project + '/memories?workspace=1');
    await capture('memories');
    await page.getByText('New memory', {exact: true}).click();
    await capture('create-memory');
    await page.getByText('New memory', {exact: true}).click();
    await page.locator('.memory-card > a').click();
    await page.locator('#record-dialog[open]').waitFor();
    await capture('memory-editor');
    await page.keyboard.press('Escape');
    await page.goto(tasksURL);
    await page.locator('#plan-tab').click();
    await page.locator('#plan-message').waitFor();
    await page.waitForFunction(() => document.getElementById('plan-model-trigger')?.textContent.includes('Configured model'));
    await capture('planner');
    await page.setViewportSize({width: 1092, height: 859});
    await page.getByText('+ New pellet', {exact: true}).click();
    await capture('medium-create');
    await page.getByText('+ New pellet', {exact: true}).click();
    await page.setViewportSize({width: 390, height: 844});
    await capture('mobile-plan');
    await page.locator('#execution-tab').click();
    await capture('mobile-run');
    }
    await page.close();
    await stop();
  }
  fs.writeFileSync(path.join(temporary, 'measurements.json'), JSON.stringify(results, null, 2));
  console.log('Visual artifacts: ' + temporary);
  const labels = {status: ['Status'], sort: ['Sort'], direction: ['Direction', 'Move'], group: ['Group'], target: ['Task'], workspace_id: ['Workspace']};
  for (const scene of Object.keys(results.before)) {
    const before = results.before[scene], after = structuredClone(results.after[scene]);
    assert.equal(after.length, before.length, scene + ': visible control count changed');
    // macOS WebKit ignores authored padding on appearance:auto selects. The
    // requested chevron correction now uses the app's 31px form-control height
    // and 4px corners there too. Account only for this documented 11px change.
    const nativeIndex = before.findIndex(control => control.tag === 'SELECT' && control.name === 'status');
    if (scene.endsWith('-create') && nativeIndex >= 0 && before[nativeIndex].style.padding === '0px') {
      assert.equal(engine, webkit);
      assert.equal(before[nativeIndex].bounds[3], 20);
      assert.equal(after[nativeIndex].bounds[3], 31);
      assert.equal(before[nativeIndex].style.borderRadius, '5px');
      assert.equal(after[nativeIndex].style.borderRadius, '4px');
      after[nativeIndex].bounds[3] = 20;
      after[nativeIndex].style.borderRadius = '5px';
      assert.equal(after[nativeIndex - 1].tag, 'LABEL');
      assert.equal(after[nativeIndex + 1].tag, 'BUTTON');
      after[nativeIndex - 1].bounds[3] = Math.round((after[nativeIndex - 1].bounds[3] - 11) * 100) / 100;
      after[nativeIndex + 1].bounds[1] = Math.round((after[nativeIndex + 1].bounds[1] - 11) * 100) / 100;
      const form = after.find(control => control.tag === 'FORM');
      form.bounds[3] = Math.round((form.bounds[3] - 11) * 100) / 100;
    }
    after.forEach((control, index) => {
      // Approved native-select correction: equal text/chevron insets, same geometry.
      if (scene.endsWith('-create') && control.tag === 'SELECT' && control.name === 'status' &&
          control.style.padding !== before[index].style.padding) {
        assert.ok(['6px 8px', '0px'].includes(before[index].style.padding));
        assert.equal(control.style.padding, '6px 34px 6px 12px');
        control.style.padding = before[index].style.padding;
      }
      if (control.label !== before[index].label) {
        assert.ok(labels[before[index].label]?.includes(control.label), scene + ': unexpected accessible-label change');
        console.log(`${scene}: accessible label ${before[index].label} → ${control.label}`);
        control.label = before[index].label;
      }
    });
    assert.deepEqual(after, before, scene + ': geometry or computed styling changed');
    console.log('PASS ' + scene + ': geometry and styles match the baseline');
  }
})().catch(error => { console.error(error); process.exitCode = 1; }).finally(async () => { await browser?.close(); await stop(); });
