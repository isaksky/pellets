// Isolated workspace navigation/pruning regression. No real runtime or database.
// NODE_PATH=/path/to/node_modules node scripts/test-web-workspace-prune-browser.cjs
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium} = require('playwright');
const source = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-workspace-prune-'));
const repo = path.join(temporary, 'workspace-demo'), binary = path.join(temporary, 'pl');
const evidence = process.env.PELLETS_UI_EVIDENCE || path.join(source, 'artifacts/ui-review/workspace-prune', new Date().toISOString().replace(/[:.]/g, '-'));
const env = {...process.env, PELLETS_CODEX_EXECUTABLE: path.join(temporary, 'unavailable')};
for (const key of ['GIT_DIR', 'GIT_WORK_TREE', 'GIT_COMMON_DIR', 'GIT_INDEX_FILE']) delete env[key];
const command = (exe, args, cwd = repo) => execFileSync(exe, args, {cwd, env, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe']});
const cli = (...args) => JSON.parse(command(binary, ['--json', ...args])).data;
let server, browser;
(async () => {
  command('go', ['build', '-o', binary, './cmd/pl'], source);
  fs.mkdirSync(repo); command('git', ['init', '-q']);
  command('git', ['-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', '-c', 'commit.gpgSign=false', 'commit', '--allow-empty', '-qm', 'Initial']);
  cli('init-db'); cli('add', 'Visible task');
  for (let i = 1; i <= 16; i++) {
    const linked = path.join(temporary, 'agent-' + i);
    command('git', ['worktree', 'add', '--detach', linked]);
    command(binary, ['project', 'show'], linked);
  }
  command('git', ['worktree', 'remove', path.join(temporary, 'agent-16')]);
  const project = cli('project', 'show');
  assert.equal(cli('workspace', 'prune', '--dry-run').pruned.length, 1);
  assert.equal(cli('project', 'show').workspaces.length, 17);
  server = spawn(binary, ['server', '--port', '0', '--no-open'], {cwd: repo, env});
  const origin = await new Promise((resolve, reject) => {
    let output = '', error = '';
    server.stdout.on('data', data => {output += data; if (output.includes('\n')) resolve(output.split('\n')[0].trim());});
    server.stderr.on('data', data => error += data);
    server.once('error', reject); server.once('exit', code => reject(Error('Server ' + code + ': ' + error)));
  });
  browser = await chromium.launch({headless: true, ...(process.env.PLAYWRIGHT_CHANNEL ? {channel: process.env.PLAYWRIGHT_CHANNEL} : {})});
  const page = await browser.newPage({viewport: {width: 1280, height: 900}});
  const errors = []; page.on('pageerror', error => errors.push(error.message));
  await page.goto(origin + '/projects/' + project.code + '/tasks');
  assert.equal(await page.locator('nav[aria-label="Workspaces"] a').count(), 2);
  await page.locator('#view-switcher summary').click();
  assert.equal(await page.locator('#view-switcher a').count(), 5);
  await page.keyboard.press('Escape');
  assert.equal(await page.locator('#view-switcher').evaluate(el => el.open), false);
  await page.getByRole('link', {name: 'All workspaces (17)', exact: true}).click();
  await page.locator('#workspaces-area').waitFor();
  assert.equal(await page.locator('.workspace-directory a').count(), 17);
  await page.locator('.workspace-directory a').nth(1).click();
  await page.locator('#tasks-area').waitFor();
  assert.equal(await page.locator('nav[aria-label="Workspaces"] a').count(), 3);
  await page.getByRole('link', {name: 'All workspaces (17)', exact: true}).click();
  await page.getByRole('button', {name: 'Prune missing workspaces', exact: true}).focus();
  await page.keyboard.press('Enter');
  await page.getByText('Pruned 1 workspace(s).', {exact: true}).waitFor();
  await page.waitForFunction(() => document.activeElement.id === 'prune-workspaces');
  assert.equal(cli('project', 'show').workspaces.length, 16);
  assert.equal(await page.locator('.workspace-directory a').count(), 16);
  cli('add', 'Trigger a live update');
  await page.getByText('Pruned 1 workspace(s).', {exact: true}).waitFor();
  fs.mkdirSync(evidence, {recursive: true});
  for (const theme of ['gruvbox-light', 'gruvbox-dark', 'light', 'dark', 'icy']) {
    for (const width of [1280, 900, 390]) {
      await page.setViewportSize({width, height: 900});
      if (!await page.locator("#right-panel").isVisible()) await page.locator("#toggle-execution").click();
      await page.locator("#theme-select").selectOption(theme);
      assert.ok(await page.getByRole('button', {name: 'Prune missing workspaces', exact: true}).isVisible());
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth);
      assert.equal(overflow, false, `${theme}/${width}: horizontal overflow`);
      await page.screenshot({path: path.join(evidence, `${theme}-${width}.png`)});
    }
  }
  assert.deepEqual(errors, []);
  console.log('Workspace pruning, navigation, keyboard flow, and theme/viewport checks passed. Evidence: ' + evidence);
})().catch(error => {console.error(error); process.exitCode = 1;}).finally(async () => {
  await browser?.close();
  if (server && server.exitCode === null && server.signalCode === null) {
    const exited = new Promise(resolve => server.once('exit', resolve)); server.kill('SIGINT'); await exited;
  }
  fs.rmSync(temporary, {recursive: true, force: true});
});
