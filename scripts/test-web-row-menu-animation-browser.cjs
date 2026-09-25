// Live row-menu regression, with independent disposable databases.
// NODE_PATH=/path/to/node_modules [PLAYWRIGHT_BROWSER=webkit] node scripts/test-web-row-menu-animation-browser.cjs
// PELLETS_MENU_BASELINE=/path/to/pl records failures without assertions.
// PELLETS_BROWSER_ARTIFACTS=/path stores matched <case>/before.png and after.png.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-row-menu-'));
const fixture = path.join(temporary, 'menu-fixture');
const baseline = process.env.PELLETS_MENU_BASELINE;
const binary = baseline || path.join(temporary, 'pl');
const engineName = process.env.PLAYWRIGHT_BROWSER || 'chromium';
const artifacts = process.env.PELLETS_BROWSER_ARTIFACTS || fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-row-menu-evidence-'));
const results = [];
let browser, server;
const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd: fixture, encoding: 'utf8'})).data;
const frame = page => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
const check = (value, message) => { if (!baseline) assert.ok(value, message); };

async function geometry(page, reference, label) {
  await frame(page);
  const result = await page.locator('#row-menu-' + reference).evaluate(menu => {
    const panel = menu.querySelector('.row-popover');
    const r = panel.getBoundingClientRect(), a = menu.querySelector('summary').getBoundingClientRect();
    const rect = r => ({x:r.x, y:r.y, width:r.width, height:r.height});
    const expectedX = Math.max(8, Math.min(a.right - r.width, innerWidth - r.width - 8));
    return {panel:rect(r), anchor:rect(a), scroll:document.querySelector('#main').scrollTop,
      bounded:r.left >= 7 && r.right <= innerWidth - 7 && r.top >= 7 && r.bottom <= innerHeight - 7,
      anchored:Math.abs(r.left - expectedX) < 1 && Math.min(Math.abs(r.top - a.bottom - 4), Math.abs(a.top - r.bottom - 4)) < 1,
      hit: [...panel.querySelectorAll('[role=menuitem]')].filter(e => !e.disabled).every(e => {
        const b = e.getBoundingClientRect();
        return b.top >= r.top && b.bottom <= r.bottom && e.contains(document.elementFromPoint(b.x+b.width/2, b.y+b.height/2));
      })};
  });
  results.push({label, ...result});
  for (const key of ['bounded','anchored','hit']) check(result[key], `${label}: ${key}: ${JSON.stringify(result)}`);
}

async function sampleAnimation(page, reference, label, capture = false) {
  const row = page.locator('#task-' + reference);
  check(await row.evaluate(el => el.getAnimations().some(a => a.animationName === 'state-changed')), label + ': live update animates');
  for (const time of [0, 425, 849]) {
    await row.evaluate((el, time) => el.getAnimations().forEach(a => { a.pause(); a.currentTime = time; }), time);
    await geometry(page, reference, label + '/' + time);
    if (capture && time === 425) {
      const directory = path.join(artifacts, engineName + '-' + label);
      fs.mkdirSync(directory, {recursive:true});
      // Fixed crop includes the queue pane, its scrollport and the displaced menu.
      await page.screenshot({path:path.join(directory, baseline ? 'before.png' : 'after.png'), clip:{x:190,y:90,width:740,height:610}});
    }
  }
  await row.evaluate(el => el.getAnimations().forEach(a => a.finish()));
  await geometry(page, reference, label + '/finished');
}

async function open(page, reference) {
  const menu = page.locator('#row-menu-' + reference);
  await menu.locator('summary').focus();
  await page.keyboard.press('Space');
  await page.waitForFunction(id => document.getElementById(id)?.open, 'row-menu-' + reference);
  return menu;
}

(async () => {
  if (!baseline) execFileSync('go', ['build', '-o', binary, './cmd/pl'], {cwd:repository});
  fs.mkdirSync(fixture); execFileSync('git', ['init', '-q'], {cwd:fixture});
  for (const command of [[], ['init-db'], ['add'], ['edit']]) execFileSync(binary, [...command, '--help'], {cwd:fixture});
  cli('init-db');
  const first = cli('add', 'Menu target');
  for (let i=0; i<20; i++) cli('add', 'Surrounding queue row ' + i);
  const ordinary = cli('add', 'Ordinary live row');
  const review = cli('add', 'Checkpoint live row', '--review-targets', ordinary.id);
  for (let i=0; i<15; i++) cli('add', 'Following queue row ' + i);
  server = spawn(binary, ['server', '--port', '0', '--no-open'], {cwd:fixture});
  const origin = await new Promise((resolve, reject) => {
    let output='', error='';
    const timer = setTimeout(() => reject(Error('Server startup timeout: '+error)), 15000);
    server.stdout.on('data', d => { output += d; if (output.includes('\n')) { clearTimeout(timer); resolve(output.split('\n')[0].trim()); } });
    server.stderr.on('data', d => error += d);
    server.once('error', reject); server.once('exit', code => { clearTimeout(timer); reject(Error(`Server ${code}: ${error}`)); });
  });
  browser = await (engineName === 'webkit' ? webkit : chromium).launch({headless:true});
  const page = await browser.newPage({viewport:{width:1092,height:800}, reducedMotion:'no-preference', deviceScaleFactor:1});
  page.setDefaultTimeout(10000);
  const errors=[]; page.on('pageerror', e => errors.push(e.message));
  // Pause the real CSS animation on arrival, before automation can miss 850ms.
  // Seek it deterministically below without changing production keyframes.
  await page.addInitScript(() => document.addEventListener('animationstart', event => {
    if (event.animationName === 'state-changed') event.target.getAnimations().forEach(a => { a.pause(); a.currentTime=200; });
  }, true));
  const url = origin + '/projects/' + first.project + '/tasks?workspace=1';
  let revision=0;
  for (const theme of ['gruvbox-light','gruvbox-dark','light','dark','icy']) {
    for (const width of [1280,1092,390]) {
      await page.setViewportSize({width,height:800});
      await page.goto(url);
      await page.locator('#theme-select-trigger').click();
      await page.locator(`[role=option][data-value="${theme}"]`).click();
      for (const [kind, pellet] of [['ordinary',ordinary],['checkpoint',review]]) {
        // Start each case with an unanimated row, then mutate through CLI/SSE.
        await page.goto(url);
        const row = page.locator('#task-' + pellet.id);
        await row.waitFor();
        await row.evaluate(el => { const main=document.querySelector('#main'); main.scrollTop += el.getBoundingClientRect().top - 400; });
        await frame(page);
        check(await page.locator('#main').evaluate(el => el.scrollTop > 0), 'Fixture must exercise a scrolled pane');
        let old = await row.getAttribute('data-row-version');
        cli('edit', pellet.id, '--description', 'Live revision ' + ++revision);
        await page.waitForFunction(({id,old}) => document.getElementById(id)?.dataset.rowVersion !== old, {id:'task-'+pellet.id,old});
        const menu = await open(page, pellet.id);
        const label = `${theme}-${width}-${kind}`;
        await sampleAnimation(page, pellet.id, label, theme==='light' && width===1092);
        // Live patches intentionally wait while a row menu is open. The menu
        // must stay usable, and the queued change must animate after dismissal.
        old = await row.getAttribute('data-row-version');
        cli('edit', pellet.id, '--description', 'Open menu revision ' + ++revision);
        await page.evaluate(() => document.dispatchEvent(new CustomEvent('pellets-refresh')));
        await frame(page);
        check(await row.getAttribute('data-row-version') === old, 'Open menu defers row replacement');
        check(await menu.evaluate(el => el.open), 'Live update preserves open menu');
        check(await menu.locator('summary').evaluate(el => el===document.activeElement), 'Live update preserves trigger focus');
        await sampleAnimation(page, pellet.id, label+'-open-update');
        // Pane scrolling repositions the open menu, including an above-trigger flip.
        await page.locator('#main').evaluate(el => el.scrollTop -= 200);
        await geometry(page, pellet.id, label+'-scroll');
        for (const [key, last] of [['ArrowDown',false],['End',true],['Home',false]]) {
          await page.keyboard.press(key);
          check(await menu.evaluate((el,last) => {
            const items=[...el.querySelectorAll('[role=menuitem]')].filter(e=>!e.disabled);
            return document.activeElement===items[last ? items.length-1 : 0];
          },last), label+': keyboard '+key);
        }
        await page.keyboard.press('Escape');
        check(!(await menu.evaluate(el=>el.open)), 'Escape dismisses');
        check(await menu.locator('summary').evaluate(el=>el===document.activeElement), 'Escape restores focus');
        await page.evaluate(() => document.dispatchEvent(new CustomEvent('pellets-refresh')));
        await page.waitForFunction(({id,old}) => document.getElementById(id)?.dataset.rowVersion !== old, {id:'task-'+pellet.id,old});
        await open(page, pellet.id);
        await sampleAnimation(page,pellet.id,label+'-deferred-update');
        await page.locator('#search').click();
        check(!(await menu.evaluate(el=>el.open)), 'Outside click dismisses');
        console.log((baseline ? 'BASELINE ' : 'PASS ')+engineName+'/'+label);
      }
    }
  }
  // Insertions use the same live-refresh path, for both production row templates.
  await page.setViewportSize({width:1092,height:800}); await page.goto(url);
  const inserted = cli('add','Inserted ordinary row','--before',first.id);
  const insertedReview = cli('add','Inserted checkpoint row','--review-targets',inserted.id);
  for (const [kind,pellet] of [['ordinary',inserted],['checkpoint',insertedReview]]) {
    await page.locator('#task-'+pellet.id+'.state-changed').waitFor();
    await open(page,pellet.id);
    await sampleAnimation(page,pellet.id,'insert-'+kind);
    await page.keyboard.press('Escape');
  }
  assert.deepEqual(errors,[]);
})().catch(error => { console.error(error); process.exitCode=1; }).finally(async () => {
  fs.mkdirSync(artifacts,{recursive:true});
  fs.writeFileSync(path.join(artifacts,engineName+'-'+(baseline?'before':'after')+'-measurements.json'),JSON.stringify(results,null,2)+'\n');
  await browser?.close();
  if (server?.exitCode === null) { const ended=new Promise(resolve=>server.once('exit',resolve)); server.kill('SIGINT'); await ended; }
  fs.rmSync(temporary,{recursive:true,force:true});
  console.log('Evidence: '+artifacts);
});
