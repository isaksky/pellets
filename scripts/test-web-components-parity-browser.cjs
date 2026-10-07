// Compare actual screens against a pre-migration executable, using disposable data.
// NODE_PATH=/path/to/node_modules node scripts/test-web-components-parity-browser.cjs
// Builds HEAD as the baseline, or accepts PELLETS_UI_BASELINE=/path/to/pl.
const assert = require('node:assert/strict');
const fs = require('node:fs'), path = require('node:path'), os = require('node:os');
const {execFileSync, spawn} = require('node:child_process');
const {chromium} = require('playwright');

const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-ui-parity-'));
const fixture = path.join(temporary, 'parity'), current = path.join(temporary, 'pl-current');
const baseline = process.env.PELLETS_UI_BASELINE ? path.resolve(process.env.PELLETS_UI_BASELINE) : path.join(temporary, 'pl-baseline');
const emphasis = {};
const results = {}, groupAdditions = {}, reviewLayouts = {}, gripLayouts = {}, executionPreferences = {}, interactionStyles = {}, recordActionLayouts = {};
const creationLayouts = {};
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
  // Capture the same idle pointer state on both builds.
  await page.mouse.move(0, 0);
  // Native dialog autofocus can select the title text. Normalize the caret for
  // screenshots; the workflow suites independently verify focus preservation.
  if (scene.endsWith('-record-actions') || scene.endsWith('-checkpoint-scope'))
    await page.locator('#record-dialog input[name=title]').evaluate(input => input.setSelectionRange(input.value.length, input.value.length));
  await page.screenshot({path: path.join(temporary, `${build}-${scene}.png`), animations: 'disabled', caret: 'hide'});
  // The requested creation redesign changes this form's geometry and visible
  // fields. Verify that exact surface separately; keep strict comparisons for
  // every surrounding control and all other forms.
  if (scene.endsWith('-create')) {
    creationLayouts[build + '-' + scene] = await page.locator('#tasks-area .create-popover form').evaluate(form => ({
      width: form.getBoundingClientRect().width,
      editorHeight: form.querySelector('textarea').getBoundingClientRect().height,
      fields: [...form.elements].filter(el => el.name && el.type !== 'hidden' && el.getClientRects().length && !el.closest('details:not([open])')).map(el => el.name),
      options: form.querySelector('.pellet-create-options')?.open,
      footer: !!form.querySelector('.pellet-create-footer button[type=submit]'),
    }));
  }
  if (scene.endsWith('-record-actions')) recordActionLayouts[build + '-' + scene] = await page.evaluate(() => {
    const menu = document.querySelector('#record-dialog .record-actions-panel');
    const selector = menu.querySelector('.select-trigger');
    const box = el => {const r = el.getBoundingClientRect();return {width:r.width,height:r.height};};
    return {menu:box(menu), selector:box(selector), value:selector?.innerText.trim(),
      items:[...menu.querySelectorAll(':scope > a, .action-row > form > button')].map(el => el.textContent.trim())};
  });
  // The emphasis audit intentionally removes duplicate source labels and
  // collapses secondary memory metadata. Screenshots above show the delivered
  // UI; restore only those exact presentation differences for strict comparison
  // of all remaining control geometry and styling.
  if (build === 'before') emphasis[scene] = await page.evaluate(() => ({
    sourceLabels: !document.querySelector('.description-source > .visually-hidden'),
    memory: !!document.querySelector('[id^="inspector-memory-"] section.metadata'),
    clear: !document.querySelector('#clear-filters')?.hidden,
  }));
  if (build === 'after') await page.evaluate(baseline => {
    window.emphasisRestore = [];
    const undo = callback => window.emphasisRestore.push(callback);
    if (baseline.sourceLabels) for (const span of document.querySelectorAll('.description-source > .visually-hidden')) {
      const text = span.firstChild; span.replaceWith(text);
      undo(() => { text.replaceWith(span); span.append(text); });
    }
    const clear = document.getElementById('clear-filters');
    if (baseline.clear && clear?.hidden) { clear.hidden = false; undo(() => clear.hidden = true); }
    const inspector = document.querySelector('[id^="inspector-memory-"]');
    if (baseline.memory && inspector) {
      const details = inspector.querySelector('details.metadata');
      if (!details || details.open || inspector.querySelector('.eyebrow')) throw Error('Memory details must start collapsed without repeated heading');
      const title = inspector.querySelector('h2'), eyebrow = document.createElement('span');
      eyebrow.className = 'eyebrow'; eyebrow.textContent = 'Memory'; title.before(eyebrow);undo(() => eyebrow.remove());
      const section = document.createElement('section'), heading = document.createElement('h3'), dl = details.querySelector('dl');
      section.className = 'metadata'; heading.textContent = 'Record'; section.append(heading, dl);
      const provenance = document.createElement('div'), dt = document.createElement('dt'), dd = document.createElement('dd');
      dt.textContent = 'Provenance'; dd.textContent = inspector.querySelector('.provenance').textContent;
      provenance.append(dt,dd); dl.firstElementChild.after(provenance);
      details.replaceWith(section);undo(() => { provenance.remove();details.append(dl);section.replaceWith(details); });
    }
  }, emphasis[scene]);
  groupAdditions[build][scene] = await page.evaluate(() => {
    const navigation = [...document.querySelectorAll('#area-tabs a')].find(a => new URL(a.href).pathname.endsWith('/groups'));
    const details = document.querySelector('[data-group-details-link]');
    return {navigation: navigation?.getBoundingClientRect().height || 0, details: details?.getBoundingClientRect().height || 0};
  });
  reviewLayouts[build][scene] = await page.evaluate(() => {
    const queue = document.querySelector('#queue-rows');
    return {indent: queue ? parseFloat(getComputedStyle(queue).paddingLeft) : 0,
      brackets: document.querySelectorAll('.scope-brackets').length,
      rows: [...document.querySelectorAll('.checkpoint-row')].map(row => ({
        id: row.id, height: row.getBoundingClientRect().height,
        titleSize: getComputedStyle(row.querySelector('.checkpoint-open')).fontSize,
        disclosure: !!row.querySelector('.review-disclosure > summary'),
        menu: !!row.querySelector('.row-menu > summary'),
        targets: row.dataset.scope,
      }))};
  });
  gripLayouts[build + '-' + scene] = await page.locator('#queue-rows .queue-grip').evaluateAll(grips => grips.map(grip => {
    const box = grip.getBoundingClientRect();
    return {label: grip.getAttribute('aria-label'), width: box.width, height: box.height, visible: grip.checkVisibility()};
  }));
  // Retain only the explicitly changed target properties for comparison. Real
  // screenshots above and the interaction suite verify the delivered targets;
  // restoration below lets every other size/style remain a strict comparison.
  if (build === 'before') interactionStyles[scene] = await page.evaluate(() => {
    const read = (selector, properties, excluded) => [...document.querySelectorAll(selector)].filter(el => !excluded || !el.closest(excluded)).map(el =>
      Object.fromEntries(properties.map(property => [property, getComputedStyle(el).getPropertyValue(property)])));
    return {
      menus: read('.row-menu > summary', ['display','min-width','min-height','border-radius']),
      summaries: read('.project-record > summary, .metadata > summary', ['padding','border-radius']),
      memories: read('.memory-card', ['padding']), memoryLinks: read('.memory-card > a', ['padding']),
      inlineSelects: read('.select-trigger', ['padding-left','padding-right'], '.filters,.assignment-form,.run-controls,.theme-control,.plan-composer-settings,.plan-composer-tools,.record-actions-panel,[data-execution-preferences]'),
      positionedMenus: read('.switcher-menu,.row-popover,.assignment-form,.recipient-form', ['position']),
      creationActions: read('.create-popover form button.primary-button', ['background-color','color','border','padding','font-weight']),
    };
  });
  // The new, feature-owned preference row intentionally increases form height.
  // Capture its real geometry in the screenshot and separately verify that
  // removing exactly that addition restores every original control unchanged.
  executionPreferences[build+'-'+scene] = await page.evaluate(() => [...document.querySelectorAll('[data-execution-preferences]')].filter(el=>el.checkVisibility()).map(el=>({
    width:el.getBoundingClientRect().width,height:el.getBoundingClientRect().height,
    fields:[...el.querySelectorAll('select')].map(s=>s.name),
    fits:[...el.querySelectorAll('.select-trigger')].every(s=>{const b=s.getBoundingClientRect();return b.x>=0&&b.right<=innerWidth+1;})
  })));
  for(const row of executionPreferences[build+'-'+scene]) {
    assert.deepEqual(row.fields,['model','reasoning_effort']);
    assert.ok(row.width>0&&row.height>0&&row.fits,scene+': preference row must fit');
  }
  await page.evaluate(()=>{for(const el of document.querySelectorAll('[data-execution-preferences]')){el.dataset.parityDisplay=el.style.display;el.style.display='none';}});
  // Screenshots above retain the delivered spacing. The dedicated spacing suite
  // checks long labels, six widths, hit targets and expanded menus. For parity
  // of preexisting controls, reverse only the two shared spacing changes here:
  // 6px inline-select insets and viewport-positioned details menu surfaces.
  if (build === 'after') await page.evaluate(baseline => {
    window.spacingProbe = [];
    const change = (el, properties) => {
      window.spacingProbe.push([el, el.style.cssText]);
      for (const [key, value] of Object.entries(properties)) el.style.setProperty(key, value, 'important');
    };
    const restore = (selector, saved, verify) => [...document.querySelectorAll(selector)].forEach((el, index) => {
      if (!saved[index]) throw Error('Changed interaction control count: ' + selector);
      verify?.(el);
      change(el, saved[index]);
    });
    restore('.row-menu > summary', baseline.menus, el => {
      if (getComputedStyle(el).minHeight !== '28px' || getComputedStyle(el).opacity !== '1')
        throw Error('Row menu must keep a visible 28px target');
    });
    restore('.project-record > summary, .metadata > summary', baseline.summaries, el => {
      if (getComputedStyle(el).padding !== '3px 6px') throw Error('Disclosure inset changed');
    });
    restore('.memory-card', baseline.memories);
    restore('.memory-card > a', baseline.memoryLinks, el => {
      if (!el.contains(el.closest('.memory-card').querySelector('footer'))) throw Error('Memory metadata is outside its link');
    });
    // The heading rule previously made these two primary submitters quiet.
    // Screenshots retain the correction; restore only its exact properties for
    // strict comparison of every other control and the form's previous height.
    restore('.create-popover form button.primary-button', baseline.creationActions, el => {
      const style = getComputedStyle(el);
      if (style.padding !== '5px 9px' || style.borderTopWidth !== '1px')
        throw Error('Creation submitter lost its primary geometry');
    });
    // Match only the controls being compared. The separately measured model
    // row adds selectors, so indexing every selector shifts unrelated controls.
    const inlineSelects = [...document.querySelectorAll('.select-trigger')].filter(el =>
      !el.closest('.filters,.assignment-form,.run-controls,.theme-control,.plan-composer-settings,.plan-composer-tools,.record-actions-panel,[data-execution-preferences]'));
    if (inlineSelects.length !== baseline.inlineSelects.length) throw Error('Changed inline selector count');
    for (const [index, el] of inlineSelects.entries()) {
      if (getComputedStyle(el).paddingLeft !== '6px' || getComputedStyle(el).paddingRight !== '6px')
        throw Error('Unexpected inline selector inset: ' + el.outerHTML + ' / ' + getComputedStyle(el).padding);
      change(el, baseline.inlineSelects[index]);
    }
    for (const [index, el] of [...document.querySelectorAll('.switcher-menu,.row-popover,.assignment-form,.recipient-form')].entries()) {
      if (getComputedStyle(el).position !== 'fixed') throw Error('Details menu must escape scroll clipping');
      if (baseline.positionedMenus[index]?.position === 'fixed') continue;
      change(el, {position: 'absolute', inset: 'auto', top: 'calc(100% + 8px)', left: '0px',
        width: el.matches('.assignment-form,.recipient-form') ? '275px' : 'auto',
        'min-width': '220px', 'max-width': 'min(360px, 90vw)', 'max-height': '65vh'});
      if (el.matches('.row-popover')) { el.style.setProperty('left','auto','important'); el.style.setProperty('right','0px','important'); el.style.setProperty('min-width','205px','important'); }
      if (innerWidth <= 600 && el.matches('.assignment-form')) { el.style.setProperty('left','auto','important'); el.style.setProperty('right','0px','important'); }
      if (innerWidth <= 600 && el.matches('.switcher-menu')) { el.style.setProperty('min-width','190px','important'); el.style.setProperty('max-width','70vw','important'); }
    }
  }, interactionStyles[scene]);
  results[build][scene] = await page.evaluate(() => {
    const closed = Array.from(document.querySelectorAll('details:not([open])'));
    const selectors = 'button, input:not([type=hidden]):not(.select-native), textarea, select:not(.select-native), label, summary, dialog[open], .task-row, .checkpoint-row, .memory-card, .section-heading, #main, #right-panel, #project-drawer, .plan-tabs, .create-popover[open] > form, .filter-fields:popover-open, .select-popover, .select-value, .select-chevron, .inspector > header, .dialog-footer';
    return Array.from(document.querySelectorAll(selectors))
      .filter(el => !el.closest('.checkpoint-row') && el.getClientRects().length && getComputedStyle(el).visibility !== 'hidden' &&
        !closed.some(details => details.contains(el) && !details.querySelector('summary')?.contains(el)))
      .map(el => {
        const bounds = el.getBoundingClientRect(), style = getComputedStyle(el);
        const row = el.closest('.task-row');
        const precedingReviews = row ? [...row.parentElement.children].slice(0, [...row.parentElement.children].indexOf(row)).filter(el => el.matches('.checkpoint-row')).reduce((height, el) => height + el.getBoundingClientRect().height, 0) : 0;
        return {
          creationControl: !!el.closest('#tasks-area .create-popover form'),
          recordAction: !!el.closest('.record-actions-panel'),
          queueRow: row?.id || '', rowBox: el === row, precedingReviews,
          tag: el.tagName, id: el.id.replace(/workbench-select-\d+/g, 'generated-select'),
          name: el.getAttribute('name'), label: el.getAttribute('aria-label'),
          bounds: [bounds.x, bounds.y, bounds.width, bounds.height].map(value => Math.round(value * 100) / 100),
          style: Object.fromEntries(['display', 'backgroundColor', 'color', 'fontFamily', 'fontSize', 'fontWeight', 'lineHeight', 'borderRadius', 'borderWidth', 'borderColor', 'boxShadow', 'padding', 'margin', 'gap'].map(key => [key, style[key]])),
        };
      });
  });
  if (build === 'after') await page.evaluate(() => { for (const undo of window.emphasisRestore.reverse()) undo(); delete window.emphasisRestore; });
  if (build === 'after') await page.evaluate(() => { for (const [el, style] of window.spacingProbe) el.style.cssText = style; delete window.spacingProbe; });
  await page.evaluate(()=>{for(const el of document.querySelectorAll('[data-execution-preferences]')){el.style.display=el.dataset.parityDisplay;delete el.dataset.parityDisplay;}});
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
  // Seed the older schema first. The current build may migrate it after the
  // baseline measurements; an older binary cannot read a newer schema.
  const cli = (...args) => JSON.parse(execFileSync(baseline, ['--json', ...args], {cwd: fixture, encoding: 'utf8'})).data;
  cli('init-db');
  const first = cli('add', 'Preserve the existing interface', '--group', 'Interface');
  cli('add', 'Keep interactions predictable', '--group', 'Runtime');
  cli('add', 'Review interface work', '--review-targets', first.id);
  cli('memory', 'add', '--text', 'Preserve spacing, colors, and keyboard behavior.', '--created-by', 'agent');
  const engine = chromium;
  browser = await engine.launch({headless: true,
    ...(process.env.PLAYWRIGHT_CHANNEL ? {channel: process.env.PLAYWRIGHT_CHANNEL} : {}),
  });
  for (const [build, binary] of [['before', baseline], ['after', current]]) {
    results[build] = {}; groupAdditions[build] = {}; reviewLayouts[build] = {};
    const origin = await start(binary), page = await browser.newPage({viewport: {width: 1280, height: 900}});
    page.setDefaultTimeout(12000);
    await page.addInitScript(() => { window.EventSource = undefined; });
    const tasksURL = origin + '/projects/' + first.project + '/tasks?workspace=1';
    for (const theme of ['icy', 'gruvbox-light', 'gruvbox-dark', 'light', 'dark']) {
    await page.setViewportSize({width: 1280, height: 900});
    await page.goto(tasksURL);
    await page.locator('#theme-select-trigger').click();
    await page.locator(`[role=option][data-value="${theme}"]`).click();
    const capture = scene => measure(page, `${theme}-${scene}`, build);
    if (!await page.locator('#execution-tab').isVisible()) await page.locator('#toggle-execution').click();
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
  fs.writeFileSync(path.join(temporary, 'review-layouts.json'), JSON.stringify(reviewLayouts, null, 2));
  fs.writeFileSync(path.join(temporary, 'grip-layouts.json'), JSON.stringify(gripLayouts, null, 2));
  fs.writeFileSync(path.join(temporary, 'execution-preferences.json'),JSON.stringify(executionPreferences,null,2));
  fs.writeFileSync(path.join(temporary, 'group-additions.json'), JSON.stringify(groupAdditions, null, 2));
  fs.writeFileSync(path.join(temporary, 'record-action-layouts.json'), JSON.stringify(recordActionLayouts, null, 2));
  console.log('Visual artifacts: ' + temporary);
  const labels = {status: ['Status'], sort: ['Sort'], direction: ['Direction', 'Move'], group: ['Group'], target: ['Task'], workspace_id: ['Workspace']};
  for (const scene of Object.keys(results.before)) {
    if (scene.endsWith('-create')) {
      const oldCreation = creationLayouts['before-' + scene], newCreation = creationLayouts['after-' + scene];
      assert.ok(newCreation.width >= oldCreation.width, scene + ': creation got narrower');
      assert.ok(newCreation.editorHeight >= 160 && (oldCreation.editorHeight < 160
        ? newCreation.editorHeight > oldCreation.editorHeight
        : newCreation.editorHeight >= oldCreation.editorHeight - 1), scene + ': writing space regressed');
      assert.deepEqual(newCreation.fields, ['title','description'], scene + ': optional fields should start collapsed');
      assert.equal(newCreation.options, false);assert.equal(newCreation.footer, true);
    }
    const before = structuredClone(results.before[scene]).filter(control => !control.creationControl && !control.label?.startsWith('Reorder '));
    const after = structuredClone(results.after[scene]).filter(control => !control.creationControl && !control.label?.startsWith('Reorder '));
    const grips = gripLayouts['after-' + scene];
    const originalGrips = gripLayouts['before-' + scene];
    if (originalGrips.length) assert.deepEqual(grips, originalGrips, scene + ': existing queue grips changed');
    assert.equal(grips.length, after.filter(control => control.rowBox).length + reviewLayouts.after[scene].rows.length,
      scene + ': each queue row needs a grip');
    for (const grip of grips) {
      assert.ok(grip.label?.startsWith('Reorder ') && grip.visible, scene + ': grip must be named and visible');
      assert.ok(grip.width >= 10.5 && grip.width <= 11.5 && grip.height >= 12.5 && grip.height <= 13.5,
        scene + ': grip is outside the requested uniformly scaled size');
    }
    // Review rows deliberately replace 42px dividers and their bracket gutters.
    // Check this exact adoption separately, then account only for its measured
    // displacement of ordinary rows and their existing controls.
    const oldReview = reviewLayouts.before[scene], newReview = reviewLayouts.after[scene];
    assert.equal(newReview.brackets, 0, scene + ': bracket geometry returned');
    assert.equal(newReview.indent, 0, scene + ': queue retained bracket indentation');
    assert.equal(newReview.rows.length, oldReview.rows.length);
    newReview.rows.forEach((row, i) => {
      assert.equal(row.id, oldReview.rows[i].id);
      assert.equal(row.targets, oldReview.rows[i].targets);
      assert.equal(row.titleSize, '13px');
      assert.ok(row.disclosure && row.menu, scene + ': review lost disclosure/actions');
      assert.ok(row.height >= 60 && row.height <= 120, scene + ': collapsed review height');
    });
    assert.equal(after.length, before.length, scene + ': original control count changed');
    after.forEach((control, i) => {
      assert.equal(control.queueRow, before[i].queueRow);
      assert.equal(control.rowBox, before[i].rowBox);
      if (control.queueRow) {
        control.bounds[1] = Math.round((control.bounds[1] - control.precedingReviews + before[i].precedingReviews) * 100) / 100;
        if (control.rowBox) {
          const gutter = oldReview.indent - newReview.indent;
          control.bounds[0] += gutter;
          control.bounds[2] -= gutter;
        }
      }
      for (const key of ['queueRow', 'rowBox', 'precedingReviews']) { delete control[key]; delete before[i][key]; }
    });
    assert.equal(after.length, before.length, scene + ': visible control count changed');
    // Group details intentionally add one project-navigation row and one
    // 19.5px link below pellet metadata. Account only for their measured layout
    // displacement; every original size/style and every other position matches.
    const additionsBefore = groupAdditions.before[scene], additionsAfter = groupAdditions.after[scene];
    if (!additionsBefore.navigation && additionsAfter.navigation) {
      const settings = after.findIndex(control => control.label === 'Workspace group settings');
      if (scene.includes('-mobile-')) {
        assert.equal(additionsAfter.navigation, 31); // horizontal mobile navigation
      } else if (settings >= 0) {
        assert.equal(additionsAfter.navigation, 35); // plus the existing 2px row gap
        assert.equal(after[settings].bounds[1] - before[settings].bounds[1], 37);
        after[settings].bounds[1] = before[settings].bounds[1];
      }
    }
    if (!additionsBefore.details && additionsAfter.details) {
      assert.ok(scene.endsWith('-editor') || scene.endsWith('-record-actions'));
      assert.equal(additionsAfter.details, 19.5);
      const dialog = after.findIndex(control => control.tag === 'DIALOG');
      const metadataEnd = after.findIndex((control, index) => index > dialog && control.tag === 'INPUT' && control.name === 'group');
      assert.ok(dialog >= 0 && metadataEnd > dialog);
      assert.equal(after[dialog].bounds[3] - before[dialog].bounds[3], 19.5);
      for (let i = dialog; i < after.length; i++) {
        const delta = i <= metadataEnd ? -9.75 : 9.75;
        assert.ok(Math.abs(after[i].bounds[1] - before[i].bounds[1] - delta) < .011, scene + ': group link displaced an unexpected control');
        after[i].bounds[1] = before[i].bounds[1];
      }
      after[dialog].bounds[3] = before[dialog].bounds[3];
      const oldMargin = before[dialog].style.margin.split(' '), newMargin = after[dialog].style.margin.split(' ');
      assert.equal(Number.parseFloat(oldMargin[0]) - Number.parseFloat(newMargin[0]), 9.75);
      assert.deepEqual(newMargin.slice(1), oldMargin.slice(1));
      after[dialog].style.margin = before[dialog].style.margin;
    }
    // Older baselines predate the shared description tabs. Account for that
    // toolbar's measured height and dialog recentering only in those builds;
    // current baselines retain strict control comparison throughout.
    if ((scene.endsWith('-editor') || scene.endsWith('-record-actions') || scene.endsWith('-checkpoint-scope'))
        && !scene.endsWith('-memory-editor') && after.some(control => control.id === 'record-dialog')) {
      const dialog = after.findIndex(control => control.id === 'record-dialog');
      const title = after.findIndex((control, index) => index > dialog && control.tag === 'INPUT' && control.name === 'title');
      const external = after.findIndex((control, index) => index > title && control.tag === 'INPUT' && control.name === 'external_id');
      const tabs = after.map((control, index) => ({control, index})).filter(({control, index}) =>
        index > title && index < external && control.tag === 'BUTTON').map(({index}) => index);
      assert.deepEqual(tabs, [title + 1, title + 2], scene + ': description mode controls changed');
      if (!before[tabs[0]].style.borderRadius.startsWith('4px 4px 0px')) {
        const toolbarGrowth = 23;
        const dialogGrowth = after[dialog].bounds[3] - before[dialog].bounds[3];
        const capped = Math.abs(dialogGrowth) < .02;
        assert.ok(capped || Math.abs(dialogGrowth - toolbarGrowth) < .02,
          scene + ': unexpected description toolbar height');
        const outerShift = after[dialog].bounds[1] - before[dialog].bounds[1];
        assert.ok(Math.abs(outerShift - (capped ? 0 : -toolbarGrowth / 2)) < .02,
          scene + ': unexpected dialog recentering');
        const footer = after.findIndex((control, index) => index > external && control.tag === 'FOOTER');
        assert.ok(footer > external);
        assert.equal(after[tabs[0]].bounds[0], after[title].bounds[0]);
        assert.ok(Math.abs(after[tabs[1]].bounds[0] - after[tabs[0]].bounds[0] - after[tabs[0]].bounds[2]) < .02);
        assert.equal(after[tabs[0]].bounds[1], after[tabs[1]].bounds[1]);
        assert.ok(after[tabs[0]].style.borderRadius.startsWith('4px 4px 0px'));
        assert.ok(after[tabs[1]].style.borderRadius.startsWith('4px 4px 0px'));
        for (let i = dialog; i < after.length; i++) {
          if (tabs.includes(i)) continue;
          const shift = i < tabs[0] ? outerShift : capped && i >= footer ? 0 : outerShift + toolbarGrowth;
          assert.ok(Math.abs(after[i].bounds[1] - before[i].bounds[1] - shift) < .02,
            scene + ': description toolbar displaced an unexpected control');
          after[i].bounds[1] = before[i].bounds[1];
        }
        after[dialog].bounds[3] = before[dialog].bounds[3];
        const oldMargin = before[dialog].style.margin.split(' '), newMargin = after[dialog].style.margin.split(' ');
        assert.ok(Math.abs(Number.parseFloat(oldMargin[0]) - Number.parseFloat(newMargin[0]) + outerShift) < .02);
        assert.deepEqual(newMargin.slice(1), oldMargin.slice(1));
        after[dialog].style.margin = before[dialog].style.margin;
        for (const index of tabs) after[index] = before[index];
      }
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
    if (scene.endsWith('-record-actions')) {
      const original = recordActionLayouts['before-' + scene], revised = recordActionLayouts['after-' + scene];
      if (original.menu.width === 330) {
        assert.equal(revised.menu.width, 280);
        assert.ok(revised.menu.height < original.menu.height && revised.menu.height <= 230,
          scene + ': action menu did not become compact');
        assert.ok(revised.selector.width >= revised.menu.width - 30 && revised.selector.height >= 30,
          scene + ': workspace selector must fill the menu');
        assert.equal(revised.value, 'Project root');
        assert.deepEqual(revised.items, original.items, scene + ': lifecycle actions changed');
        after.forEach((control, index) => {
          assert.equal(control.recordAction, before[index].recordAction, scene + ': action menu ancestry changed');
          if (!control.recordAction) return;
          for (const key of ['tag','id','name','label']) assert.equal(control[key], before[index][key], scene + ': action control changed');
          // The measured compact menu above and app screenshots cover this exact
          // target; retain strict baseline comparison for all surrounding controls.
          after[index] = before[index];
        });
      } else {
        assert.deepEqual(revised, original, scene + ': record actions differ from current baseline');
      }
    }
    assert.deepEqual(after, before, scene + ': geometry or computed styling changed');
    console.log('PASS ' + scene + ': surrounding controls match baseline with documented review, group, and description changes');
  }
})().catch(error => { console.error(error); process.exitCode = 1; }).finally(async () => { await browser?.close(); await stop(); });
