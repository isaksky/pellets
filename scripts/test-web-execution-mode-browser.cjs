// Real production selectors and SQLite; all projects live in a disposable fixture.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-mode-browser-'));
const binary = process.env.PELLETS_MODE_BINARY || path.join(temporary, 'pl');
const baseline = process.env.PELLETS_MODE_BASELINE === '1';
let browser, server;
const cli = (cwd, ...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd, encoding: 'utf8'})).data;
const git = (cwd, ...args) => execFileSync('git', args, {cwd, encoding: 'utf8'});
async function until(fn) {
  for (let i = 0; i < 200; i++) { if (await fn()) return; await new Promise(r => setTimeout(r, 50)); }
  throw Error('Execution mode did not settle');
}
async function stopServer() {
  if (!server || server.exitCode !== null || server.signalCode !== null) return;
  const stopped = new Promise(resolve => server.once('exit', resolve));
  server.kill('SIGINT'); await stopped;
}
async function startServer(repo) {
  server = spawn(binary, ['server', '--port', '0', '--no-open'], {cwd: repo});
  return new Promise((resolve, reject) => {
    let output = '';
    server.stdout.on('data', chunk => { output += chunk; if (output.includes('\n')) resolve(output.split('\n')[0].trim()); });
    server.once('error', reject);
    server.once('exit', code => reject(Error('Server exited: ' + code)));
  });
}
(async () => {
  if (!process.env.PELLETS_MODE_BINARY) execFileSync('go', ['build', '-o', binary, './cmd/pl']);
  const repo = path.join(temporary, 'settings'); fs.mkdirSync(repo);
  git(repo, 'init', '-q');
  cli(repo, 'init-db');
  const pellet = cli(repo, 'add', 'Execution preference test');
  let origin = await startServer(repo);
  const engine = process.env.PLAYWRIGHT_BROWSER === 'webkit' ? webkit : chromium;
  browser = await engine.launch({headless: true, ...(engine === chromium ? {channel: 'chrome'} : process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE ? {executablePath: process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE} : {})});
  const page = await browser.newPage({viewport: {width: 1280, height: 800}, deviceScaleFactor: 1});
  const errors = []; page.on('pageerror', e => errors.push(e.message));
  const route = '/projects/' + pellet.project + '/tasks';
  const mode = () => page.locator('select[aria-label="Execution intention"]').first();
  const settings = async () => (await (await page.request.get(origin + '/settings')).json()).settings;
  async function visit(url = route) {
    await page.goto(origin + url);
    await page.waitForFunction(() => !document.documentElement.hasAttribute('data-nonce'));
  }
  async function choose(value) {
    await mode().locator('..').getByRole('combobox').click();
    await page.locator('[role="option"][data-value="' + value + '"]').click();
  }
  await visit();
  assert.equal(await mode().inputValue(), 'run_one');
  await choose('drain');
  if (!baseline) await until(async () => (await settings()).execution_mode === 'drain');
  await visit();
  assert.equal(await mode().inputValue(), baseline ? 'run_one' : 'drain');
  const artifacts = process.env.PELLETS_BROWSER_ARTIFACTS;
  if (artifacts) {
    const dir = path.join(artifacts, process.env.PLAYWRIGHT_BROWSER || 'chromium'); fs.mkdirSync(dir, {recursive: true});
    const clip = await page.locator('.run-controls').first().boundingBox();
    assert.ok(clip, 'Execution controls are visible');
    const capture = path.join(dir, baseline ? 'before.png' : 'after.png');
    await page.screenshot({path: capture, clip});
    assert.ok(fs.statSync(capture).size > 0, 'Screenshot is not empty');
  }
  if (baseline) { console.log('PASS reproduced selection lost on reload'); return; }
  // A new browser context must read SQLite, without any browser storage.
  const fresh = await browser.newPage(); await fresh.goto(origin + route);
  await until(async () => await fresh.locator('select[aria-label="Execution intention"]').first().inputValue() === 'drain');
  await fresh.close();
  // Register another independent Git project against this fixture's ancestor DB.
  const other = path.join(repo, 'other'); fs.mkdirSync(other); git(other, 'init', '-q');
  const otherPellet = cli(other, 'add', 'Other project');
  const binding = JSON.parse(fs.readFileSync(path.join(other, '.git', 'pellets-database.json'), 'utf8'));
  assert.equal(fs.realpathSync(path.resolve(other, '.git', binding.path)), fs.realpathSync(path.join(repo, '.pellets', 'pellets.db')));
  await visit('/projects/' + otherPellet.project + '/tasks');
  assert.equal(await mode().inputValue(), 'drain');
  await choose('watch'); await until(async () => (await settings()).execution_mode === 'watch');
  await visit(); assert.equal(await mode().inputValue(), 'watch');
  // Background invalidation/replacement must not reset the selected mode.
  cli(repo, 'add', 'Trigger live queue refresh');
  await page.getByText('Trigger live queue refresh', {exact: true}).first().waitFor();
  assert.equal(await mode().inputValue(), 'watch');
  // Save failures retain the choice and use the existing retry notice.
  await page.route('**/settings', route => route.request().method() === 'POST' ? route.abort() : route.continue());
  await choose('run_one'); await page.locator('.settings-save-notice').waitFor();
  assert.equal(await mode().inputValue(), 'run_one');
  await page.unroute('**/settings'); await page.locator('.settings-save-notice button').click();
  await until(async () => (await settings()).execution_mode === 'run_one');
  await page.locator('.settings-save-notice').waitFor({state: 'detached'});
  // All workspaces share the same preference, including unscheduled recovery.
  git(repo, '-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', '-c', 'commit.gpgSign=false', 'commit', '--allow-empty', '-m', 'fixture');
  const linked = path.join(temporary, 'linked');
  git(repo, 'worktree', 'add', '-q', '-b', 'linked', linked);
  cli(linked, 'project', 'show');
  const project = cli(repo, 'project', 'show');
  const normalizedPath = value => {
    const resolved = fs.realpathSync(value);
    return process.platform === 'darwin' || process.platform === 'win32' ? resolved.toLowerCase() : resolved;
  };
  const workspace = project.workspaces.find(w => normalizedPath(w.root_path_relative ? path.resolve(repo, w.root_path) : w.root_path) === normalizedPath(linked));
  assert.ok(workspace, 'Linked workspace registered');
  await visit('/projects/' + pellet.project + '/workspaces/' + workspace.id);
  assert.equal(await mode().inputValue(), 'run_one');
  await choose('drain'); await until(async () => (await settings()).execution_mode === 'drain');
  cli(linked, 'start-next');
  await visit('/projects/' + pellet.project + '/workspaces/' + workspace.id);
  const resume = page.locator('form[data-no-run-resume] select[name="mode"]');
  await until(async () => await resume.inputValue() === 'drain');
  await resume.locator('..').getByRole('combobox').click();
  await page.locator('[role="option"][data-value="watch"]').click();
  await until(async () => (await settings()).execution_mode === 'watch');
  await visit(); assert.equal(await mode().inputValue(), 'watch');
  // Serialized rapid writes and a server restart retain the last explicit choice.
  // Start from a different saved value so polling cannot match an older write.
  await choose('run_one'); await until(async () => (await settings()).execution_mode === 'run_one');
  await page.evaluate(() => {
    const select = document.querySelector('select[aria-label="Execution intention"]');
    for (const value of ['drain', 'run_one', 'watch']) { select.value = value; select.dispatchEvent(new Event('change', {bubbles: true})); }
  });
  await until(async () => (await settings()).execution_mode === 'watch');
  await stopServer(); origin = await startServer(repo); await visit();
  assert.equal(await mode().inputValue(), 'watch');
  await stopServer();
  const separate = path.join(temporary, 'separate'); fs.mkdirSync(separate); git(separate, 'init', '-q');
  cli(separate, 'init-db'); const independent = cli(separate, 'add', 'Independent database');
  origin = await startServer(separate); await visit('/projects/' + independent.project + '/tasks');
  assert.equal(await mode().inputValue(), 'run_one', 'Another database inherited the preference');
  assert.equal((await settings()).execution_mode, undefined);
  assert.deepEqual(errors, []);
  console.log('PASS execution mode: SQLite, reload, fresh browser, projects, workspaces, recovery, live refresh, retry, rapid writes, server restart');
})().catch(error => { console.error(error); process.exitCode = 1; }).finally(async () => {
  await browser?.close(); await stopServer(); fs.rmSync(temporary, {recursive: true, force: true});
});
