// Mermaid security, bounded rendering and lifecycle against a disposable server.
// NODE_PATH=/path/to/node_modules node scripts/test-web-mermaid-browser.cjs
// PLAYWRIGHT_BROWSER=webkit selects the second engine.
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-mermaid-browser-'));
const binary = path.join(temporary, 'pl');
const fixture = path.join(temporary, 'markdown');
let server, browser, origin;
const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd: fixture, encoding: 'utf8'})).data;
const until = async (fn, message) => {
  const end = Date.now() + 15000;
  while (Date.now() < end) { if (await fn()) return; await new Promise(r => setTimeout(r, 70)); }
  throw Error(message);
};
const fence = text => '```mermaid\n' + text + '\n```';
const examples = [
  'flowchart LR\n  A[Write source] --> B{Review}\n  B -->|Ready| C[Ship]\n  B -->|Revise| A',
  'sequenceDiagram\n  accTitle: Local sequence\n  participant Agent\n  participant Pellets\n  Agent->>Pellets: Save description\n  Pellets-->>Agent: Source preserved',
  'stateDiagram-v2\n  [*] --> Open\n  Open --> Active: Start\n  Active --> Closed: Finish\n  Closed --> [*]',
];
const source = examples.map(fence).join('\n\n') + '\n\n```js\nconst ordinary = true;\n```\n';

(async () => {
  execFileSync('go', ['build', '-o', binary, './cmd/pl'], {cwd: repository});
  fs.mkdirSync(fixture);
  execFileSync('git', ['init', '-q'], {cwd: fixture});
  cli('init-db');
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
      if (e.detail.type === 'finished' && e.detail.el === document.body) window.liveRefreshFinished = (window.liveRefreshFinished || 0) + 1;
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
  const field = host.locator('textarea');
  const view = host.locator('.markdown-body');
  const settled = async (scope = view, count = 3) => {
    await until(async () => await scope.locator('pl-diagram[data-state=ready],pl-diagram[data-state=error]').count() === count &&
      await scope.locator('pl-diagram[data-state=pending]').count() === 0, 'Diagrams did not settle: ' + await scope.innerText());
  };
  const ready = async (scope = view, count = 3) => {
    await settled(scope, count);
    assert.equal(await scope.locator('pl-diagram[data-state=error]').count(), 0, await scope.innerText());
    assert.equal(await scope.locator('.mermaid-canvas > svg').count(), count);
  };
  await ready();
  assert.equal(await view.locator('pre[data-language=js]').textContent(), 'const ordinary = true;');
  assert.deepEqual(await view.locator('pre[data-language=mermaid]').allTextContents(), examples);
  assert.equal(await view.locator('svg title').first().textContent(), 'Local sequence');
  const fresh = async text => {
    await host.locator('[data-description-mode=edit]').click();
    await field.fill(text);
    await host.locator('[data-description-mode=view]').click();
  };
  const noDuplicateIDs = async () => assert.deepEqual(await page.evaluate(() => {
    const ids = [...document.querySelectorAll('pl-diagram svg [id],pl-diagram svg[id],.diagram-viewer [id]')].map(node => node.id);
    return ids.filter((id, i) => ids.indexOf(id) !== i);
  }), []);
  const refresh = async () => {
    const completed = await page.evaluate(() => window.liveRefreshFinished || 0);
    const response = page.waitForResponse(r => r.request().headers()['pellets-target'] === 'live');
    await page.evaluate(() => document.dispatchEvent(new CustomEvent('pellets-refresh')));
    assert.equal((await response).status(),200);
    await page.waitForFunction(count => (window.liveRefreshFinished || 0) > count, completed);
  };
  await noDuplicateIDs();
  await require('./web-diagram-marker-contract.cjs')({page, view, fresh, ready, screenshot, noDuplicateIDs, source, fence});
  // Defense at the SVG boundary does not rely on CSP or Mermaid's sanitizer.
  await page.evaluate(async () => {
    const {diagramSVG} = await import(document.querySelector('script[type=module]').src.replace('app.js','diagrams.js'));
    const {svg} = diagramSVG('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><script>alert(1)</script><foreignObject><p>HTML</p></foreignObject><image href="https://example.invalid/pixel"/><a href="javascript:alert(1)"><text>Link</text></a><rect id="shape" width="10" height="10" fill="url(https://example.invalid/fill)" onclick="alert(1)"/><text>Safe text</text></svg>', 'boundary-test');
    if (svg.querySelector('script,foreignObject,image,a,[href],[onclick],[style],[fill]')) throw Error('Unsafe SVG escaped allowlist');
    if (svg.querySelector('rect').id !== 'boundary-test-0-shape' || !svg.textContent.includes('Safe text')) throw Error('SVG source alternative/ID remapping failed');
    const unusual = diagramSVG('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><rect id="unsafe id:shape"/><rect id="shape"/></svg>', 'unusual-test').svg;
    if (unusual.querySelector('rect').id !== 'unusual-test-0' || unusual.querySelectorAll('[id]').length !== 2) throw Error('Unsafe ID characters must retain isolated numeric IDs');
    let rejected = false;
    try { diagramSVG('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100" style="filter:url(https://example.invalid/filter)"/>', 'style-test'); }
    catch { rejected = true; }
    if (!rejected) throw Error('Inline CSS must be rejected before inert XML parsing');
  });
  // A reusable keyboard activation point, one event per activation/refresh.
  await page.evaluate(() => document.addEventListener('pellets-diagram-activate', event => {
    window.activations = (window.activations || 0) + 1;
    window.activatedSource = event.detail.source;
  }));
  const activation = view.locator('.mermaid-activate').first();
  await activation.focus(); await page.keyboard.press('Enter');
  await page.getByRole('dialog', {name:'Diagram viewer'}).waitFor();
  assert.equal(await page.evaluate(() => window.activatedSource), examples[0]);
  await noDuplicateIDs();
  // Repeated real Datastar refreshes preserve one SVG, source, and activation.
  await view.evaluate(node => { window.originalDiagram = node.querySelector('svg'); });
  for (let i=0; i<3; i++) {
    await refresh(); await page.waitForTimeout(100);
    await ready(); await noDuplicateIDs();
    assert.equal(await view.evaluate(node => window.originalDiagram === node.querySelector('svg')), true);
  }
  assert.equal(await page.locator('.diagram-viewer[open]').count(), 1);
  await page.keyboard.press('Escape');
  await page.locator('.diagram-viewer').waitFor({state:'detached'});
  assert.equal(await activation.evaluate(node => node === document.activeElement), true);
  await activation.click();
  assert.equal(await page.evaluate(() => window.activations), 2);
  await page.getByRole('button', {name:'Close diagram viewer'}).click();
  await require('./web-diagram-viewer-contract.cjs')({page, view, field, fresh, ready, refresh, screenshot, noDuplicateIDs, source, fence, engine});
  // All themes and widths; inspect text/shape contrast and preserve SVG ratio.
  for (const theme of ['gruvbox-light', 'gruvbox-dark', 'light', 'dark', 'icy']) {
    await page.evaluate(theme => window.Workbench.applyTheme(theme), theme);
    await ready();
    for (const width of [1280, 1092, 390]) {
      await page.setViewportSize({width, height: 850});
      for (let index=0; index<3; index++) {
        const diagram = view.locator('pl-diagram').nth(index);
        await diagram.scrollIntoViewIfNeeded();
        const dimensions = await diagram.evaluate(node => {
          const svg = node.querySelector('svg'), rect = svg.getBoundingClientRect(), box = svg.viewBox.baseVal;
          const text = svg.querySelector('.node text') || svg.querySelector('text.actor') || [...svg.querySelectorAll('text')].find(node => node.textContent.trim());
          const shape = svg.querySelector('.node .label-container') || svg.querySelector('rect.actor') || svg.querySelector('.statediagram-state rect');
          const luminance = color => {
            const canvas=document.createElement('canvas'), ctx=canvas.getContext('2d');
            ctx.fillStyle=color;ctx.fillRect(0,0,1,1);
            const rgb=[...ctx.getImageData(0,0,1,1).data].slice(0,3).map(v => {v/=255;return v<=.04045?v/12.92:((v+.055)/1.055)**2.4;});
            return rgb[0]*.2126+rgb[1]*.7152+rgb[2]*.0722;
          };
          const fg=luminance(getComputedStyle(text.querySelector('tspan') || text).fill), bg=luminance(getComputedStyle(shape).fill);
          return {width: rect.width, available: node.clientWidth, ratio:rect.width/rect.height, expected:box.width/box.height,
            contrast:(Math.max(fg,bg)+.05)/(Math.min(fg,bg)+.05), overflow: node.scrollWidth > node.clientWidth+1};
        });
        assert.ok(!dimensions.overflow && dimensions.width <= dimensions.available, JSON.stringify(dimensions));
        assert.ok(Math.abs(dimensions.ratio-dimensions.expected) < .02, JSON.stringify(dimensions));
        assert.ok(dimensions.contrast >= 4.5, theme + ' text contrast ' + JSON.stringify(dimensions));
        await screenshot(`${theme}-${width}-${index}.png`);
        await diagram.locator('.mermaid-canvas').click();
        await noDuplicateIDs();
        const modal = page.getByRole('dialog', {name:'Diagram viewer'});
        await modal.waitFor();
        assert.equal(await modal.locator('svg svg text').first().evaluate(node => getComputedStyle(node).fill),
          await diagram.locator('svg text').first().evaluate(node => getComputedStyle(node).fill));
        assert.ok(await modal.evaluate(node => node.scrollWidth <= node.clientWidth && node.scrollHeight <= node.clientHeight));
        await screenshot(`viewer-${theme}-${width}-${index}.png`);
        await page.keyboard.press('Escape');
        assert.equal(await diagram.locator('.mermaid-canvas').evaluate(node => node === document.activeElement), true);
      }
    }
  }
  await page.setViewportSize({width:1280,height:850});
  // These must never reach layout or load resources; local failures retain code.
  const rejected = [
    ['flowchart LR\nA[unfinished', /Could not render Mermaid/],
    ['pie\n"Unsupported" : 5', /Unsupported diagram/],
    ['%%{init: {"securityLevel":"loose","htmlLabels":true}}%%\nflowchart LR\nA-->B', /unsupported configuration/],
    ['%% { init: {"themeCSS":"@import url(https://example.invalid/css)"} } %%\nflowchart LR\nA-->B', /unsupported configuration/],
    ['---\nconfig:\n  securityLevel: loose\n---\nflowchart LR\nA-->B', /unsupported configuration/],
    ['flowchart LR\nA-->B\nclick A "javascript:alert(1)"', /unsupported configuration/],
    ['flowchart LR\nA["<img src=https://example.invalid/track>"]', /unsupported configuration/],
    ['flowchart LR\nA["&#60;img src=x&#62;"]', /unsupported configuration/],
    ['flowchart LR\nA@{ img: "https://example.invalid/track" }', /unsupported configuration/],
    ['flowchart LR\nA-->B\nstyle A fill:url(https://example.invalid/svg)', /unsupported configuration/],
    ['sequenceDiagram\nparticipant A\nlinks A: {"Docs":"https://example.invalid"}', /unsupported configuration/],
    ['flowchart LR\nA[' + 'a'.repeat(4100) + ']', /too large/],
    ['flowchart LR\n' + Array.from({length:65}, (_,i) => `N${i}-->N${i+1}`).join('\n'), /too complex/],
    ['flowchart LR\nA & B --> C & D', /too complex/],
    ['flowchart LR\n' + Array.from({length:61}, (_,i) => `N${i}[Node ${i}]`).join('\n'), /too complex/],
    ['flowchart LR\nsubgraph A\nsubgraph B\nsubgraph C\nsubgraph D\nsubgraph E\nN[Node]\nend\nend\nend\nend\nend', /too deeply nested/],
  ];
  for (const [input, message] of rejected) {
    await fresh(fence(input) + '\n\nStill usable **after error**.\n\n' + fence(examples[1]));
    await settled(view, 2);
    assert.match(await view.locator('pl-diagram').first().innerText(), message);
    assert.equal(await view.locator('pl-diagram').first().locator('details').getAttribute('open'), '');
    assert.equal(await view.locator('pl-diagram').first().locator('pre').textContent(), input);
    assert.equal(await view.locator('pl-diagram[data-state=ready]').count(), 1);
    assert.equal(await view.locator('strong').textContent(), 'after error');
  }
  // Rendering limits also count fences nested inside lists/quotes.
  await fresh(Array.from({length:10}, () => '> ' + fence('graph TD; A-->B;').replaceAll('\n','\n> ')).join('\n\n'));
  await settled(view, 10);
  assert.equal(await view.locator('pl-diagram[data-state=ready]').count(), 8);
  assert.equal(await view.locator('pl-diagram[data-state=error]').count(), 2);
  // Rapid changes while an import/render is pending cannot replace the last text.
  await fresh(source);
  await view.evaluate(async node => {
    const {renderMarkdown} = await import(document.querySelector('script[type=module]').src.replace('app.js','markdown.js'));
    for (let i=0;i<12;i++) {
      window.Workbench.applyTheme(i % 2 ? 'dark' : 'light');
      node.replaceChildren(renderMarkdown('```mermaid\nflowchart LR\nA[Edit '+i+']-->B\n```'));
      await new Promise(resolve => setTimeout(resolve, 15));
    }
  });
  await ready(view, 1); await page.waitForTimeout(250);
  assert.match(await view.locator('svg').textContent(), /Edit 11/);
  assert.doesNotMatch(await view.locator('svg').textContent(), /Edit (?:0|1|2|3|4|5|6|7|8|9|10)\D/);
  assert.equal(await view.locator('svg text').last().evaluate(node => getComputedStyle(node).fill), await page.evaluate(() => {
    const node=document.createElement('span');node.style.color='var(--ink)';document.body.append(node);
    const color=getComputedStyle(node).color;node.remove();return color;
  }), 'A stale theme must not replace the final diagram');
  await fresh(source + '\nFinal source.  \n'); await ready();
  // Renderer works with the network offline once local application assets load.
  await page.context().setOffline(true);
  await fresh(source + '\nOffline preview.'); await ready();
  await view.locator('.mermaid-canvas').first().click();
  await page.getByRole('button',{name:'Zoom in',exact:true}).click();
  await page.getByRole('button',{name:'Close diagram viewer'}).click();
  await page.context().setOffline(false);
  // Repeated surfaces require globally unique SVG/marker IDs and stylesheet cleanup.
  await page.evaluate(async source => {
    const {renderMarkdown} = await import(document.querySelector('script[type=module]').src.replace('app.js','markdown.js'));
    const extra=document.createElement('div');extra.id='diagram-extra';extra.className='markdown-body';
    extra.append(renderMarkdown(source));document.querySelector('#record-dialog').append(extra);
    window.sheetsBeforeExtra=document.adoptedStyleSheets.length;
  }, source);
  await ready(page.locator('#diagram-extra')); await noDuplicateIDs();
  assert.equal(await page.locator('pl-diagram .mermaid-canvas > svg').count(), 6);
  await page.locator('#diagram-extra').evaluate(node => node.remove());
  assert.equal(await page.evaluate(() => document.adoptedStyleSheets.length), 3);
  // Queue saturation has an explicit recovery control; detached pending work
  // cannot keep draining or install a stale SVG/sheet in another surface.
  await page.evaluate(async () => {
    const {createDiagram} = await import(document.querySelector('script[type=module]').src.replace('app.js','diagrams.js'));
    const extra=document.createElement('div');extra.id='diagram-overload';extra.className='markdown-body';
    for(let i=0;i<33;i++) extra.append(createDiagram('flowchart LR\nA-->B'));
    document.querySelector('#record-dialog').append(extra);
    const last=extra.lastElementChild;
    if (last.dataset.state !== 'error' || last.querySelector('.mermaid-retry').hidden) throw Error('Queue saturation has no retry');
    for (const child of [...extra.children].slice(0,-1)) child.remove();
  });
  const recovery = page.locator('#diagram-overload');
  await recovery.getByRole('button',{name:'Retry diagram'}).click();
  await ready(recovery,1);
  await recovery.evaluate(node=>node.remove());
  assert.equal(await page.evaluate(() => document.adoptedStyleSheets.length), 3);
  await page.evaluate(async () => {
    const {createDiagram} = await import(document.querySelector('script[type=module]').src.replace('app.js','diagrams.js'));
    const pending=createDiagram('flowchart LR\nA-->B');document.body.append(pending);pending.remove();
  });
  await page.waitForTimeout(80);
  // Save then CLI show/reload prove exact Mermaid source preservation.
  await fresh(source); await ready();
  const response = page.waitForResponse(r => r.url().includes('/edit') && r.request().method()==='POST');
  await page.getByRole('button',{name:'Save changes',exact:true}).click();
  assert.equal((await response).status(), 200);
  await until(() => cli('show',record.id).description === source, 'Source changed on save');
  await page.reload(); await ready();
  assert.equal(await field.inputValue(), source);
  assert.deepEqual(errors, []);
  assert.deepEqual(external, []);
  assert.deepEqual(await page.evaluate(() => window.cspFailures || []), []);
  console.log('PASS Mermaid families, source, CSP/offline, unsafe input, limits, themes/contrast/layout, rapid edits, activation, refresh, IDs and cleanup');
  console.log('Visual artifacts: ' + temporary);
})().catch(e => { console.error(e); process.exitCode=1; }).finally(async () => {
  if (browser) await browser.close();
  if (server?.exitCode === null) { const stopped = new Promise(r => server.once('exit', r)); server.kill('SIGINT'); await stopped; }
});
