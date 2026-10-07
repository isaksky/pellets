// Start next model choices: real disposable server, native forms and catalog menus.
// PELLETS_EXECUTION_MODEL_BASELINE=/path/to/pl captures the original controls.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium} = require('playwright');
const root = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-execution-model-'));
const baseline = process.env.PELLETS_EXECUTION_MODEL_BASELINE;
const binary = baseline || path.join(temporary, 'pl');
const fixture = path.join(temporary, 'fixture');
const artifacts = process.env.PELLETS_BROWSER_ARTIFACTS || path.join(root, 'artifacts', 'ui-review', 'execution-model', new Date().toISOString().replaceAll(':','-'));
fs.mkdirSync(artifacts,{recursive:true});
let server, browser, page;
async function until(fn) {
  for (let i = 0; i < 150; i++) { if (await fn()) return; await new Promise(r => setTimeout(r, 100)); }
  throw Error('Execution model state did not settle');
}
(async () => {
  if (!baseline) execFileSync('go', ['build', '-o', binary, './cmd/pl'], {cwd:root});
  fs.mkdirSync(fixture); execFileSync('git', ['init', '-q'], {cwd:fixture});
  const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd:fixture, encoding:'utf8'})).data;
  cli('init-db'); const pellet = cli('add', 'Choose a model before starting');
  const binding = JSON.parse(fs.readFileSync(path.join(fixture, '.git', 'pellets-database.json')));
  assert.ok(fs.realpathSync(path.resolve(fixture, '.git', binding.path)).startsWith(fs.realpathSync(fixture) + path.sep));
  server = spawn(binary, ['server', '--port', '0', '--no-open'], {cwd:fixture, env:{...process.env, PELLETS_CODEX_EXECUTABLE:path.join(temporary, 'unavailable')}});
  const origin = await new Promise((resolve, reject) => {
    let out = '', errors = '';
    server.stdout.on('data', d => { out += d; if (out.includes('\n')) resolve(out.split('\n')[0].trim()); });
    server.stderr.on('data', d => { errors += d; });
    server.once('error', reject); server.once('exit', code => reject(Error(`Server ${code}: ${errors}`)));
  });
  browser = await chromium.launch({headless:true});
  page = await browser.newPage({viewport:{width:1280, height:850}, deviceScaleFactor:1});
  page.setDefaultTimeout(15000);
  const errors = []; page.on('pageerror', e => errors.push(e.message));
  let catalog = {models:[{id:'model-a', name:'Model A', efforts:['medium','high']}, {id:'model-b', name:'Model B', efforts:['low']}], refreshing:false, stale:false, fetched_at:100};
  await page.route('**/models', route => route.fulfill({json:catalog}));
  let refreshes = 0;
  await page.route('**/models/refresh', route => { refreshes++; return route.fulfill({status:202,json:{refreshing:true}}); });
  const tasks = origin + '/projects/' + pellet.project + '/tasks?workspace=1';
  await page.goto(tasks);
  if (!await page.locator('#right-panel').isVisible()) await page.locator('#toggle-execution').click();
  await page.locator('#execution-tab').click();
  const form = page.locator('#execution form[data-schedule]').first();
  const model = form.locator('[name=model]'), effort = form.locator('[name=reasoning_effort]');
  if (baseline) assert.equal(await model.count(), 0, 'Baseline unexpectedly has a model selector');
  else await until(() => model.locator('option[value=model-a]').count());
  for (const theme of ['light','dark','icy','gruvbox-light','gruvbox-dark']) {
    await page.evaluate(theme => window.Workbench.applyTheme(theme), theme);
    for (const width of [1280,800,390]) {
      await page.setViewportSize({width,height:850});
      await page.locator('#right-panel .run-workspace').evaluate(el => { el.scrollTop = 0; });
      await page.evaluate(() => document.activeElement?.blur());
      await page.mouse.move(0,0);
      const box = await page.locator('#right-panel').boundingBox();
      const clip = {x:box.x,y:box.y,width:box.width,height:Math.min(500,box.height)};
      const directory = path.join(artifacts, `${theme}-${width}`); fs.mkdirSync(directory, {recursive:true});
      await page.screenshot({path:path.join(directory,baseline?'before.png':'after.png'),clip,animations:'disabled',caret:'hide'});
      if (baseline) continue;
      assert.equal(await form.evaluate(el => el.scrollWidth <= el.clientWidth + 1), true, 'Start form overflows');
      const start = form.getByRole('button',{name:/Start next/});
      await start.scrollIntoViewIfNeeded();
      const startBox = await start.boundingBox();
      assert.ok(startBox.y >= 0 && startBox.y + startBox.height <= 850, 'Start next cannot be reached');
      for (const label of ['Execution model','Reasoning effort']) {
        const button = form.getByRole('combobox',{name:label,exact:true});
        await button.focus(); await page.keyboard.press('Enter');
        const menu = page.locator('.select-popover:visible');
        const menuBox = await menu.boundingBox();
        assert.ok(menuBox.x >= 0 && menuBox.x + menuBox.width <= width + 1 && menuBox.y >= 0 && menuBox.y + menuBox.height <= 851, 'Menu clipped');
        await page.screenshot({path:path.join(directory,label.replaceAll(' ','-')+'.png'),animations:'disabled',caret:'hide'});
        await page.keyboard.press('Escape');
        assert.equal(await button.evaluate(el => el === document.activeElement), true, 'Escape lost focus');
      }
    }
  }
  if (baseline) { console.log('PASS reproduced missing Start next model selector; artifacts ' + artifacts); return; }
  await page.setViewportSize({width:1280,height:850});
  await form.getByRole('combobox',{name:'Execution model',exact:true}).click();
  await page.getByRole('option',{name:'Model A',exact:true}).click();
  await form.getByRole('combobox',{name:'Reasoning effort',exact:true}).click();
  await page.getByRole('option',{name:'high',exact:true}).click();
  await page.locator('#plan-tab').click(); await page.locator('#plan-message').waitFor();
  assert.equal(await page.locator('#plan-model').inputValue(), '', 'Execution choice changed planning model');
  await page.locator('#execution-tab').click();
  // Real SSE patch, then navigation away and back, must retain dynamically loaded options.
  cli('add', 'Live refresh keeps the chosen model');
  await page.getByText('Live refresh keeps the chosen model',{exact:true}).first().waitFor();
  assert.equal(await model.inputValue(),'model-a'); assert.equal(await effort.inputValue(),'high');
  await page.locator('#view-switcher > summary').click();
  await page.getByRole('menuitem',{name:'Memories',exact:true}).click();
  await page.waitForURL('**/memories?*');
  await page.locator('#view-switcher > summary').click();
  await page.getByRole('menuitem',{name:'Queue',exact:true}).click();
  await page.waitForURL('**/tasks?*');
  assert.equal(await model.inputValue(),'model-a'); assert.equal(await effort.inputValue(),'high');
  catalog = {...catalog,models:[],stale:true,error:'Catalog unavailable'};
  await page.evaluate(() => document.dispatchEvent(new CustomEvent('pellets-refresh')));
  await until(() => model.getAttribute('data-menu-status').then(v => v === 'Catalog unavailable'));
  assert.equal(await model.inputValue(),'model-a'); assert.equal(await effort.inputValue(),'high');
  await form.getByRole('combobox',{name:'Execution model',exact:true}).click();
  await page.getByRole('button',{name:'Retry refresh',exact:true}).click();
  assert.equal(refreshes,1); await page.keyboard.press('Escape');
  // Failed admission keeps exactly the submitted pair for an explicit retry.
  const request = page.waitForRequest(r => r.method() === 'POST' && r.url().endsWith('/schedules'));
  await form.getByRole('button',{name:/Start next/}).click();
  const sent = new URLSearchParams((await request).postData());
  assert.equal(sent.get('model'),'model-a'); assert.equal(sent.get('reasoning_effort'),'high');
  await page.getByRole('button',{name:'Check again',exact:true}).waitFor();
  assert.equal(await model.inputValue(),'model-a'); assert.equal(await effort.inputValue(),'high');
  assert.equal(cli('show',pellet.id).status,'open');
  // The empty selection is submitted as inheritance, with server coverage for resolution.
  await model.selectOption(''); await effort.selectOption('');
  const retry = page.waitForRequest(r => r.method() === 'POST' && r.url().endsWith('/schedules'));
  await page.getByRole('button',{name:'Check again',exact:true}).click();
  const inherited = new URLSearchParams((await retry).postData());
  assert.equal(inherited.get('model'),''); assert.equal(inherited.get('reasoning_effort'),'');
  await page.getByRole('button',{name:'Check again',exact:true}).waitFor();
  assert.deepEqual(errors,[]);
  console.log('PASS Start next models: five themes, three widths, keyboard, live refresh, navigation, planning independence, catalog retry, submission and failed admission; artifacts ' + artifacts);
})().catch(async error => {
  console.error(error);
  if (page) await page.screenshot({path:path.join(artifacts,'failure.png')});
  process.exitCode = 1;
}).finally(async () => {
  await browser?.close();
  if (server?.exitCode === null) { const done = new Promise(r => server.once('exit',r)); server.kill('SIGINT'); await done; }
  fs.rmSync(temporary,{recursive:true,force:true});
});
