// Optional browser regression suite. Requires Playwright on Node's module path.
// NODE_PATH=/path/to/node_modules PLAYWRIGHT_CHANNEL=chrome node scripts/test-web-empty-browser.cjs
// Use PLAYWRIGHT_BROWSER=webkit to check the Safari browser engine.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');

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
  execFileSync(binary, ['init-db'], {cwd: temporary});
  fs.mkdirSync(fixture);
  execFileSync('git', ['init', '-q'], {cwd: fixture});
  server = spawn(binary, ['server', '--port', '0', '--no-open'], {cwd: temporary});
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
  const engine = process.env.PLAYWRIGHT_BROWSER === 'webkit' ? webkit : chromium;
  browser = await engine.launch({headless: true, ...(engine === chromium && process.env.PLAYWRIGHT_CHANNEL ? {channel: process.env.PLAYWRIGHT_CHANNEL} : {})});
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
  assert.equal(await page.getByRole('heading', {name: 'No registered projects'}).isVisible(), true);
  assert.match(await page.locator('.empty-card').textContent(), /pl add "First issue"/);
  await until(() => page.evaluate(() => window.results.includes(200)), 'Empty page did not complete a Datastar refresh');
  await page.evaluate(() => { window.bootstrapSentinel = 'same document'; });
  const first = cli('add', 'Alpha browser task').data;
  const base = `/projects/${first.project}/tasks`;
  await until(async () => (await page.locator('.row-link').count()) === 1 && new URL(page.url()).pathname === base,
    'First CLI issue did not replace the empty state through Datastar');
  assert.equal(await page.evaluate(() => window.bootstrapSentinel), 'same document', 'Bootstrap reloaded the page');
  assert.equal(await page.locator('.empty-database').count(), 0);
  assert.equal(await page.title(), `${first.project} · Pellets`);
  cli('add', 'Zulu browser task');
  await until(async () => (await page.locator('.row-link').count()) === 2, 'Later CLI issue did not refresh');
  assert.equal(await page.locator('.row-link').count(), 2);

  assert.deepEqual(errors, [], 'Browser errors');
  assert.deepEqual(external, [], 'Unexpected external requests');
  console.log('Browser checks passed: visible empty state, first CLI issue via Datastar without reload, canonical URL/title, and subsequent live updates.');
})().catch(error => { console.error(error); process.exitCode = 1; }).finally(async () => {
  if (browser) await browser.close();
  if (server && server.exitCode === null) {
    const exited = new Promise(resolve => server.once('exit', resolve));
    server.kill('SIGINT');
    await exited;
  }
  fs.rmSync(temporary, {recursive: true, force: true});
});
