// Task marker layout in real description surfaces, including planner field CSS.
// Run with Playwright on NODE_PATH; repeat with PLAYWRIGHT_BROWSER=webkit.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-task-list-browser-'));
const binary = path.join(temporary, 'pl'), fixture = path.join(temporary, 'tasks');
const source = `## Tight tasks

- [x] Tight done
- [ ] Tight pending

## Loose tasks

- [x] Done

- [ ] Pending

## Nested tight tasks

- [x] Parent done
  - [ ] Child pending
  - [x] Child done

## Nested loose tasks

- [ ] Parent pending

  - [x] Child done

  - [ ] Child pending
`;
const expectedParents = ['LI', 'LI', 'P', 'P', 'LI', 'LI', 'LI', 'P', 'P', 'P'];
const expectedChecked = [true, false, true, false, true, false, true, false, true, false];
const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd: fixture, encoding: 'utf8'})).data;
let server, browser;
(async () => {
  console.log('Task-list artifacts: ' + temporary);
  execFileSync('go', ['build', '-o', binary, './cmd/pl'], {cwd: repository});
  fs.mkdirSync(fixture);
  execFileSync('git', ['init', '-q'], {cwd: fixture});
  const record = cli('add', 'Task-list fixture', '--description', source, '--group', 'Task lists');
  const group = cli('group', 'list')[0];
  cli('group', 'edit', String(group.id), '--context', source);
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
  const page = await browser.newPage({viewport: {width: 1280, height: 850}});
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const mode = (host, name) => host.locator(`[data-description-mode=${name}]`).click();
  const assertLayout = async (view, label) => {
    const markers = await view.locator('.markdown-task input[type=checkbox]').evaluateAll(inputs => inputs.map(input => {
      const box = input.getBoundingClientRect(), li = input.closest('li').getBoundingClientRect();
      const text = input.nextSibling, start = text.textContent.search(/\S/);
      const range = document.createRange();
      range.setStart(text, start); range.setEnd(text, start + 1);
      const glyph = range.getBoundingClientRect(), style = getComputedStyle(input);
      return {parent: input.parentElement.tagName, checked: input.checked, disabled: input.disabled,
        nested: !!input.closest('li').parentElement.closest('li'), width: box.width, height: box.height,
        padding: style.padding, gap: glyph.left - box.right, offset: box.right - li.left,
        beside: box.top < glyph.bottom && box.bottom > glyph.top};
    }));
    assert.deepEqual(markers.map(marker => marker.parent), expectedParents, label + ' covers tight and loose DOM');
    assert.deepEqual(markers.map(marker => marker.checked), expectedChecked, label + ' retains checked states');
    assert.equal(markers.filter(marker => marker.nested).length, 4, label + ' covers both nested lists');
    for (const marker of markers) {
      assert.ok(marker.disabled && marker.width > 0 && marker.width <= 18 && marker.height > 0 && marker.height <= 18 &&
        marker.padding === '0px' && marker.offset < 3 && marker.gap >= 0 && marker.gap <= 14 && marker.beside,
      label + ': marker must stay compact beside its text: ' + JSON.stringify(marker));
    }
    assert.equal(await view.evaluate(el => el.scrollWidth > el.clientWidth + 1), false, label + ' has no horizontal overflow');
  };
  const assertPlannerFields = async proposal => {
    const fields = await proposal.locator('[name=title], [name=group], [name=acceptance], [name=description]').evaluateAll(fields =>
      fields.filter(field => field.getClientRects().length).map(field => {
        const style = getComputedStyle(field);
        return {name: field.name, width: field.getBoundingClientRect().width, availableWidth: field.parentElement.getBoundingClientRect().width,
          padding: style.padding, border: style.borderWidth, minHeight: style.minHeight};
      }));
    assert.ok(fields.length >= 3);
    for (const field of fields) {
      assert.ok(Math.abs(field.width - field.availableWidth) <= 1, JSON.stringify(field));
      assert.equal(field.padding, '10px 12px', field.name + ' retains text-field padding');
      assert.equal(field.border, '1px', field.name + ' retains its border');
      if (field.name === 'description') assert.equal(field.minHeight, '140px');
      if (field.name === 'acceptance') assert.equal(field.minHeight, '110px');
    }
  };
  const matrix = async (host, surface, proposal) => {
    const view = host.locator('.markdown-body');
    await view.waitFor({state: 'visible'});
    for (const theme of ['gruvbox-light', 'gruvbox-dark', 'light', 'dark', 'icy']) {
      await page.evaluate(theme => window.Workbench.applyTheme(theme), theme);
      for (const width of [1280, 1092, 390]) {
        await page.setViewportSize({width, height: 850});
        await view.scrollIntoViewIfNeeded();
        await view.evaluate(el => el.scrollTop = 0);
        const label = `${surface}-${theme}-${width}`;
        // Capture before asserting so a failing regression has visible evidence.
        if (proposal || theme === 'gruvbox-light') await page.screenshot({path: path.join(temporary, label + '.png')});
        await assertLayout(view, label);
        const box = await view.boundingBox();
        assert.ok(box.x >= 0 && box.x + box.width <= width + 1, label + ' remains inside the viewport');
        assert.equal(await view.evaluate(el => {
          const box = el.getBoundingClientRect();
          return el.contains(document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2));
        }), true, label + ' remains visible and reachable');
        assert.equal(await host.locator('textarea').inputValue(), source, label + ' preserves Markdown source');
        if (proposal) {
          await assertPlannerFields(proposal);
          await view.evaluate(el => el.scrollTop = el.scrollHeight);
          await page.screenshot({path: path.join(temporary, label + '-nested.png')});
        }
      }
    }
    await page.setViewportSize({width: 1280, height: 850});
    await mode(host, 'edit');
    assert.equal(await host.locator('textarea').inputValue(), source, surface + ' edit retains original Markdown');
    if (proposal) await assertPlannerFields(proposal);
    await mode(host, 'view');
  };
  await page.goto(origin);
  // Start with the affected proposal flow, including a real autosave/reopen.
  await page.locator('#plan-tab').click();
  // The empty proposal tray hides its actions; seed through the real handler.
  await page.locator('[data-plan=add-draft]').evaluate(button => button.click());
  const proposal = page.locator('.plan-draft-dialog[open]'), proposalHost = proposal.locator('[data-description]');
  await proposal.locator('[name=title]').fill('Task-list proposal');
  await proposal.locator('[name=description]').fill(source);
  await mode(proposalHost, 'view');
  await matrix(proposalHost, 'proposal', proposal);
  await page.evaluate(() => window.Planner.flush());
  const planningURL = origin + '/projects/' + record.project + '/planning';
  assert.equal((await (await page.request.get(planningURL)).json()).chat.state.drafts[0].description, source);
  await proposal.getByRole('button', {name: 'Done', exact: true}).click();
  await page.locator('[data-plan=edit-draft]').click();
  await assertLayout(proposalHost.locator('.markdown-body'), 'Reopened proposal');
  assert.equal(await proposal.locator('[name=description]').inputValue(), source);
  await proposal.getByRole('button', {name: 'Done', exact: true}).click();
  // Other production authoring surfaces share exactly the same marker rule.
  // Leave the phone planning overlay before inspecting the queue's creation form.
  await page.locator('#execution-tab').click();
  await page.locator(`#task-${record.id} .task-title`).click();
  const dialog = page.locator('#record-dialog'), host = dialog.locator('[data-description]');
  await matrix(host, 'record');
  await dialog.getByRole('link', {name: 'Close inspector', exact: true}).click();
  assert.equal(cli('show', record.id).description, source);
  await page.locator('.create-popover summary').click();
  const create = page.locator('.create-popover form'), createHost = create.locator('[data-description]');
  await create.locator('[name=title]').fill('Created task lists');
  await create.locator('[name=description]').fill(source);
  await mode(createHost, 'view');
  await matrix(createHost, 'creation');
  await create.getByRole('button', {name: 'Create pellet', exact: true}).click();
  await page.getByRole('link', {name: 'Created task lists', exact: true}).waitFor();
  const created = cli('list').find(item => item.title === 'Created task lists');
  assert.equal(cli('show', created.id).description, source);
  if (await dialog.evaluate(el => el.open)) await dialog.getByRole('link', {name: 'Close inspector', exact: true}).click();
  await page.locator(`#task-${record.id} .group-name`).click();
  await matrix(host, 'group');
  assert.equal(cli('group', 'show', String(group.id)).context, source);
  assert.deepEqual(errors, []);
  console.log(`PASS task-list layout (${process.env.PLAYWRIGHT_BROWSER || 'chromium'}): tight, loose and nested lists; four surfaces × five themes × three widths; planner fields and source round trips`);
})().catch(error => { console.error(error); process.exitCode = 1; }).finally(async () => {
  if (browser) await browser.close();
  if (server?.exitCode === null) { const stopped = new Promise(resolve => server.once('exit', resolve)); server.kill('SIGINT'); await stopped; }
});
