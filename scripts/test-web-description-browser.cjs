// Markdown rendering and native-source lifecycle against a disposable server.
// NODE_PATH=/path/to/node_modules node scripts/test-web-description-browser.cjs
// PLAYWRIGHT_BROWSER=webkit selects the second engine. PELLETS_DESCRIPTION_BASELINE
// captures the old inspector from an explicit pre-change executable for comparison.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-description-browser-'));
const binary = process.env.PELLETS_DESCRIPTION_BASELINE || path.join(temporary, 'pl');
const fixture = path.join(temporary, 'markdown');
let server, browser, origin;
const cli = (...args) => JSON.parse(execFileSync(binary, args, {cwd: fixture, encoding: 'utf8'})).data;
const until = async (fn, message) => {
  const end = Date.now() + 15000;
  while (Date.now() < end) { if (await fn()) return; await new Promise(r => setTimeout(r, 70)); }
  throw Error(message);
};
const source = '# Delivery &amp; verification\n\nParagraph with **strong**, *emphasis*, ~~removed~~ and `inline <code>`.\n\n[Local guide](/help) and [HTTPS](https://example.invalid/docs) and [email](mailto:test@example.invalid).\n\n- Parent\n  1. Ordered child\n     - Nested child\n- [x] Done\n- [ ] Pending\n\n> A quoted paragraph\n>\n> Second quoted paragraph\n\n```js\nconst answer = "<script>";\n// Readable comment\nreturn 42;\n```\n\n```unrecognized-language\n<script>alert("code")</script> & literal\n' + 'very_wide_code_'.repeat(40) + '\n```\n\n```mermaid\ngraph TD; A-->B;\n```\n\n| Column | Wide value |\n|:--|--:|\n| literal | ' + 'wide_table_'.repeat(25) + ' |\n\n---\n\n<img src="/evil-image" onerror="window.markdownExecuted=1">\n<script>window.markdownExecuted=1</script>\n<svg onload="window.markdownExecuted=1"></svg>\n\n[bad](javascript:alert(1)) [entity](javascript&#58;alert(1)) [control](java&#x09;script:alert(1)) [data](data:text/html,bad) [file](file:///tmp/test) [vb](vbscript:bad)\n\n![Remote image](https://example.invalid/track.png)\n\n' + Array.from({length: 18}, (_, i) => `## Section ${i}\n\nDetails ${i} preserve scrolling and source.\n`).join('\n') + '\n';
(async () => {
  if (!process.env.PELLETS_DESCRIPTION_BASELINE) execFileSync('go', ['build', '-o', binary, './cmd/pl'], {cwd: repository});
  fs.mkdirSync(fixture);
  execFileSync('git', ['init', '-q'], {cwd: fixture});
  const record = cli('add', 'Markdown fixture', '--description', source);
  const plain = cli('add', 'Plain fixture', '--description', 'Existing plain text.\nAnother line.');
  server = spawn(binary, ['server', '--port', '0', '--no-open'], {cwd: fixture});
  origin = await new Promise((resolve, reject) => {
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
  const errors = [], external = [];
  page.on('pageerror', e => errors.push(e.message));
  page.on('request', r => { if (!r.url().startsWith(origin)) external.push(r.url()); });
  await page.addInitScript(() => {
    document.addEventListener('securitypolicyviolation', e => window.cspFailures = [...(window.cspFailures || []), e.violatedDirective]);
    document.addEventListener('datastar-fetch', e => {
      if (e.detail.type === 'datastar-patch-signals') window.webStatuses = [...(window.webStatuses || []), JSON.parse(e.detail.argsRaw.signals)._webResult?.status];
    });
  });
  const screenshot = async name => {
    assert.deepEqual(await page.evaluate(() => window.cspFailures || []), []);
    await page.screenshot({caret:'initial', path:path.join(temporary, name)});
    await page.waitForTimeout(20);
    // Playwright's WebKit screenshotter injects exactly one `body {}` style
    // to synchronize animations, even with animations enabled. Account for
    // that tool-owned violation only at the screenshot boundary; application
    // interactions before and after still require zero CSP violations.
    const failures = await page.evaluate(() => (window.cspFailures || []).splice(0));
    assert.deepEqual(failures, engine === webkit ? ['style-src-elem'] : []);
  };
  await page.goto(origin);
  await page.locator(`#task-${record.id} .task-title`).click();
  const host = page.locator('#record-dialog [data-description]');
  const field = page.locator('#record-dialog textarea[name=description]');
  const view = host.locator('.markdown-body');
  const mode = (name, container = host) => container.locator(`[data-description-mode=${name}]`);
  if (process.env.PELLETS_DESCRIPTION_BASELINE) {
    await screenshot('before-description.png');
    console.log('BASELINE ' + temporary); return;
  }
  await view.waitFor({state: 'visible'});
  assert.equal(await field.isVisible(), false, 'Existing records open rendered');
  assert.equal(await field.inputValue(), source);
  assert.equal(await view.locator('h1').innerText(), 'Delivery & verification');
  for (const selector of ['strong', 'em', 'del', 'blockquote', 'ul ol ul', 'hr', 'table', 'pre[data-language=mermaid]', '.code-keyword'])
    assert.ok(await view.locator(selector).count(), selector + ' must render');
  assert.equal(await view.locator('input[type=checkbox]:checked:disabled').count(), 1);
  assert.equal(await view.locator('input[type=checkbox]:not(:checked):disabled').count(), 1);
  assert.equal(await view.locator('pre[data-language=js] code').textContent(), 'const answer = "<script>";\n// Readable comment\nreturn 42;');
  assert.match(await view.locator('pre[data-language=unrecognized-language]').textContent(), /^<script>alert\("code"\)<\/script> & literal/);
  assert.equal(await view.locator('pre[data-language=unrecognized-language] .code-keyword').count(), 0);
  assert.equal(await view.locator('a').count(), 3);
  assert.equal(await view.locator('script,img,svg,iframe,object,style,[onerror],[onload]').count(), 0);
  assert.equal(await page.evaluate(() => window.markdownExecuted), undefined);
  assert.equal(await view.locator('a[target=_blank][rel="noopener noreferrer"]').count(), 3);
  // Exercise the reusable renderer without CSP as a fallback safety mechanism.
  await page.evaluate(async () => {
    const {renderMarkdown} = await import(document.querySelector('script[type=module]').src.replace('app.js', 'markdown.js'));
    const malicious = '[bad](JaVaScRiPt&#58;alert(1)) [bad](java&Tab;script:bad) [bad](data:text/html,hi) <img src=x onerror=alert(1)>\n\n<iframe srcdoc="<script>alert(1)</script>"></iframe>';
    const node = document.createElement('div'); node.append(renderMarkdown(malicious));
    if (node.querySelector('a,iframe,img,script,svg')) throw Error('Unsafe Markdown DOM');
  });
  // All palettes, three viewport sizes, both sidebars retained; scrolling stays local.
  for (const theme of ['gruvbox-light','gruvbox-dark','light','dark','icy']) {
    await page.evaluate(theme => window.Workbench.applyTheme(theme), theme);
    const contrast = await view.evaluate(view => {
      const canvas = document.createElement('canvas'); canvas.width = canvas.height = 1;
      const ctx = canvas.getContext('2d');
      const luminance = color => {
        ctx.fillStyle = color; ctx.fillRect(0,0,1,1);
        const values = [...ctx.getImageData(0,0,1,1).data].slice(0,3).map(x => {
          x /= 255; return x <= .04045 ? x / 12.92 : ((x+.055)/1.055)**2.4;
        });
        return values[0]*.2126+values[1]*.7152+values[2]*.0722;
      };
      return ['.code-keyword','.code-comment','.code-string','.code-number'].map(selector => {
        const token = view.querySelector(selector), pre = token.closest('pre');
        const a = luminance(getComputedStyle(token).color), b = luminance(getComputedStyle(pre).backgroundColor);
        return (Math.max(a,b)+.05)/(Math.min(a,b)+.05);
      });
    });
    assert.ok(contrast.every(ratio => ratio >= 4.5), theme + ' code contrast ' + contrast);
    for (const width of [1280, 1092, 390]) {
      await page.setViewportSize({width, height: 850});
      const geometry = await view.evaluate(node => {
        const box = node.getBoundingClientRect();
        return {left: box.left, right: box.right, width: innerWidth, overflow: node.scrollWidth > node.clientWidth + 1,
          display: getComputedStyle(node).display, blocks: [...node.querySelectorAll('pre[data-language=unrecognized-language],.markdown-table')].map(el => el.scrollWidth > el.clientWidth + 1)};
      });
      assert.ok(geometry.left >= 0 && geometry.right <= width && !geometry.overflow, JSON.stringify(geometry));
      await screenshot(`${theme}-${width}.png`);
      assert.deepEqual(geometry.blocks, [true, true], JSON.stringify(geometry) + " artifacts: " + temporary);
    }
  }
  await page.setViewportSize({width: 1280, height: 850});
  await view.evaluate(el => { el.scrollTop = 240; el.querySelector('.markdown-table').scrollLeft = 100; });
  await view.evaluate(el => {
    window.descriptionView = el;
    const range = document.createRange(); range.selectNodeContents(el.querySelector('h1'));
    const selection = getSelection(); selection.removeAllRanges(); selection.addRange(range);
    el.focus({preventScroll:true});
  });
  for (let i=0; i<3; i++) {
    const refresh = page.waitForResponse(r => r.request().headers()['pellets-target'] === 'live');
    await page.evaluate(() => document.dispatchEvent(new CustomEvent('pellets-refresh')));
    await (await refresh).finished();
    await page.waitForTimeout(100);
    assert.equal(await view.evaluate(el => el === window.descriptionView), true, 'Refresh replaced rendered DOM');
    assert.equal(await view.evaluate(el => el === document.activeElement), true, 'Refresh lost rendered focus');
    assert.equal(await page.evaluate(() => getSelection().toString()), 'Delivery & verification');
    assert.equal(await view.evaluate(el => el.scrollTop), 240);
  }
  await mode('edit').focus();
  await page.keyboard.press('Enter');
  await field.evaluate(el => { el.setSelectionRange(13, 32, 'backward'); el.scrollTop = 80; });
  await mode('view').click();
  assert.equal(await view.evaluate(el => el.scrollTop), 240);
  assert.equal(await view.locator('.markdown-table').evaluate(el => el.scrollLeft), 100);
  await mode('edit').click();
  assert.deepEqual(await field.evaluate(el => [el.selectionStart,el.selectionEnd,el.selectionDirection]), [13,32,'backward']);
  assert.equal(await field.evaluate(el => el === document.activeElement), true);
  assert.equal(await field.evaluate(el => el.scrollTop), 80);
  const draft = source + '\nUnsaved **preview** & trailing spaces  \n';
  await field.fill(draft);
  await page.locator('#record-dialog [name=title]').fill('Unsaved title');
  await field.focus();
  await field.evaluate(el => el.setSelectionRange(20, 35));
  for (let i=0; i<3; i++) {
    cli('edit', plain.id, '--title', 'Live refresh ' + i);
    await page.evaluate(() => document.dispatchEvent(new CustomEvent('pellets-refresh')));
    await page.waitForTimeout(200);
    assert.equal(await field.inputValue(), draft);
    assert.equal(await field.evaluate(el => el === document.activeElement), true);
    assert.deepEqual(await field.evaluate(el => [el.selectionStart,el.selectionEnd]), [20,35]);
  }
  await page.context().setOffline(true);
  await mode('view').click();
  assert.match(await view.innerText(), /Unsaved preview/);
  await page.context().setOffline(false);
  cli('edit', record.id, '--title', 'Concurrent title');
  let response = page.waitForResponse(r => r.url().includes('/edit') && r.request().method() === 'POST');
  await page.getByRole('button', {name:'Save changes', exact:true}).click();
  await response;
  await until(() => page.evaluate(() => (window.webStatuses || []).includes(409)), 'Missing conflict receipt');
  await page.locator('#record-dialog .conflict-state').waitFor();
  assert.equal(await field.inputValue(), draft);
  assert.equal(await page.locator('#record-dialog [name=title]').inputValue(), 'Unsaved title');
  assert.equal(await view.isVisible(), true, 'Conflict retains preview');
  page.once('dialog', d => d.dismiss());
  await page.getByRole('button', {name:'Cancel', exact:true}).click();
  assert.equal(await view.isVisible(), true);
  // Conflict response retains the draft and current version for explicit retry.
  response = page.waitForResponse(r => r.url().includes('/edit') && r.request().method() === 'POST');
  await page.getByRole('button', {name:'Save changes', exact:true}).click();
  assert.equal((await response).status(), 200);
  await until(() => cli('show', record.id).description === draft, 'Saved source changed');
  await page.getByRole('link', {name:'Close inspector', exact:true}).click();
  await page.locator(`#task-${record.id} .task-title`).click();
  await view.waitFor({state:'visible'});
  await mode('edit').click();
  await page.getByRole('link', {name:'Close inspector', exact:true}).click();
  await page.locator(`#task-${record.id} .task-title`).click();
  await field.waitFor({state:'visible'});
  await page.reload();
  await field.waitFor({state:'visible'});
  assert.equal(await field.inputValue(), draft);
  assert.equal(cli('show', record.id).description, draft);
  assert.ok(cli('search', 'trailing spaces').some(item => item.reference === record.id || item.id === record.id), 'Source remains searchable');
  await page.getByRole('link', {name:'Close inspector', exact:true}).click();
  await page.locator(`#task-${plain.id} .task-title`).click();
  assert.match(await view.innerText(), /Existing plain text\.\s+Another line\./);
  await page.getByRole('link', {name:'Close inspector', exact:true}).click();
  // Creation preview survives live morphs and never serializes rendered text.
  await page.locator('.create-popover summary').click();
  const create = page.locator('.create-popover form');
  const createHost = create.locator('[data-description]');
  await create.locator('[name=title]').fill('Created from Markdown');
  await create.locator('[name=description]').fill(source);
  await mode('view', createHost).click();
  for (let i=0; i<3; i++) {
    cli('edit', plain.id, '--title', 'Creation refresh ' + i);
    await page.evaluate(() => document.dispatchEvent(new CustomEvent('pellets-refresh')));
    await page.waitForTimeout(250);
    assert.equal(await createHost.locator('.markdown-body').isVisible(), true);
    assert.equal(await create.locator('[name=description]').inputValue(), source);
  }
  for (const width of [1280, 678, 390]) {
    await page.setViewportSize({width, height:850});
    const preview = createHost.locator('.markdown-body');
    await preview.evaluate(el => el.querySelector('.markdown-table').scrollIntoView({block:'center'}));
    const box = await preview.boundingBox();
    assert.ok(box.x >= 0 && box.x + box.width <= width + 1, 'Creation preview is clipped');
    await screenshot(`creation-${width}.png`);
    const submit = create.getByRole('button', {name:'Create pellet',exact:true});
    await submit.scrollIntoViewIfNeeded();
    assert.equal(await submit.evaluate(el => {
      const box=el.getBoundingClientRect();
      return el.contains(document.elementFromPoint(box.x+box.width/2,box.y+box.height/2));
    }), true, 'Create button remains reachable at ' + width);
  }
  await page.setViewportSize({width:1280,height:850});
  await create.getByRole('button', {name:'Create pellet', exact:true}).click();
  await until(() => cli('list').some(item => item.title === 'Created from Markdown'), 'Creation failed');
  const created = cli('list').find(item => item.title === 'Created from Markdown');
  assert.equal(cli('show', created.id || created.reference).description, source);
  // Proposed pellet source and preview share the same control and autosave.
  if (await page.locator('#record-dialog').evaluate(el => el.open)) await page.getByRole('link', {name:'Close inspector', exact:true}).click();
  await page.locator('#plan-tab').click();
  await page.locator('[data-plan=add-draft]').evaluate(button => button.click());
  const proposal = page.locator('.plan-draft-dialog[open]');
  const proposalHost = proposal.locator('[data-description]');
  await proposal.locator('[name=title]').fill('Markdown proposal');
  await proposal.locator('[name=description]').fill(source);
  await mode('view', proposalHost).click();
  await until(async () => (await proposal.locator('.plan-editor-status').innerText()).includes('Changes saved automatically'), 'Proposal did not autosave');
  assert.equal(await proposalHost.locator('.markdown-body h1').innerText(), 'Delivery & verification');
  assert.equal(await proposalHost.locator('.markdown-task input').evaluateAll(inputs => inputs.every(input => {
    const box=input.getBoundingClientRect(), li=input.closest('li').getBoundingClientRect();
    return box.width <= 18 && box.height <= 18 && box.right < li.left+3;
  })), true, 'Task markers must stay beside their text, outside planner field styling');
  await proposalHost.locator('.markdown-body').evaluate(el => el.scrollTop = 180);
  await proposal.getByRole('button', {name:'Done', exact:true}).click();
  await page.evaluate(() => window.Workbench.initialize());
  await page.locator('[data-plan=edit-draft]').click();
  assert.equal(await proposalHost.locator('.markdown-body').evaluate(el => el.scrollTop), 180, 'Closed proposal refresh lost reading position');
  assert.equal(await proposalHost.locator('.markdown-body').isVisible(), true);
  for (const width of [1280, 678, 390]) {
    await page.setViewportSize({width,height:850});
    const preview = proposalHost.locator('.markdown-body');
    await preview.evaluate(el => el.querySelector('pre').scrollIntoView({block:'center'}));
    const box = await preview.boundingBox();
    assert.ok(box.x >= 0 && box.x + box.width <= width + 1, 'Proposal preview is clipped');
    await screenshot(`proposal-${width}.png`);
  }
  await mode('edit', proposalHost).click();
  assert.equal(await proposal.locator('[name=description]').inputValue(), source);
  assert.deepEqual(errors, []);
  assert.deepEqual(external, [], 'Renderer must make no external requests');
  assert.deepEqual(await page.evaluate(() => window.cspFailures || []), []);
  console.log('PASS Markdown source round-trip, safe DOM, offline rendering, themes/layout, source selection, refresh/conflict/reopen, creation and proposal preview');
  console.log('Visual artifacts: ' + temporary);
})().catch(e => { console.error(e); process.exitCode=1; }).finally(async () => {
  if (browser) await browser.close();
  if (server?.exitCode === null) { const stopped = new Promise(r => server.once('exit', r)); server.kill('SIGINT'); await stopped; }
});
