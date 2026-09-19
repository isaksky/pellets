// Real old-tab/new-server upgrade regression. Builds two isolated source
// snapshots differing only in one embedded CSS comment, then restarts on the
// same origin and database. No application source, real repository, or HTTP
// mutation result is mocked.
// NODE_PATH=/path/to/node_modules PLAYWRIGHT_CHANNEL=chrome node scripts/test-web-upgrade-browser.cjs
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');

const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-upgrade-browser-'));
const source = path.join(temporary, 'source');
const fixture = path.join(temporary, 'upgrade');
const firstBinary = path.join(temporary, process.platform === 'win32' ? 'pl-old.exe' : 'pl-old');
const secondBinary = path.join(temporary, process.platform === 'win32' ? 'pl-new.exe' : 'pl-new');
const peer = path.join(temporary, process.platform === 'win32' ? 'codex.exe' : 'codex');
const environment = {...process.env, PATH: temporary + path.delimiter + process.env.PATH,
  PELLETS_CODEX_EXECUTABLE: peer, PELLETS_SUPERVISOR_PEER: '1'};
let browser, server, origin, currentBinary = firstBinary;
const cli = (...args) => JSON.parse(execFileSync(currentBinary, args, {cwd: fixture, env: environment, encoding: 'utf8'})).data;
const until = async (predicate, message) => {
  const deadline = Date.now() + 25000;
  while (Date.now() < deadline) {
    if (await predicate()) return;
    await new Promise(resolve => setTimeout(resolve, 70));
  }
  throw new Error(message);
};

function snapshotSource() {
  // Include current tracked changes and untracked implementation files, without
  // .git, ignored output, or edits to the source being used by another build.
  const files = execFileSync('git', ['ls-files', '--cached', '--others', '--exclude-standard', '-z'], {cwd: repository, encoding: 'utf8'}).split('\0').filter(Boolean);
  for (const name of new Set(files)) {
    const from = path.join(repository, name), to = path.join(source, name);
    if (!fs.existsSync(from)) continue; // A tracked deletion is absent in the snapshot.
    fs.mkdirSync(path.dirname(to), {recursive: true});
    fs.cpSync(from, to, {dereference: false});
  }
  assert.ok(fs.existsSync(path.join(source, 'internal/webui/assets/ui-version.js')), 'UI revision client must be present before running the upgrade suite');
}

async function stopServer() {
  if (!server || server.exitCode !== null) return;
  const process = server;
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => { process.kill('SIGKILL'); reject(new Error('Server did not stop')); }, 10000);
    process.once('exit', () => {clearTimeout(timer); resolve();});
    process.kill('SIGINT');
  });
}

async function startServer(binary, port = '0') {
  currentBinary = binary;
  server = spawn(binary, ['server', '--port', String(port), '--no-open'], {cwd: fixture, env: environment});
  return new Promise((resolve, reject) => {
    let output = '', errors = '';
    const timer = setTimeout(() => reject(new Error('Server readiness timeout: ' + errors)), 15000);
    server.stderr.on('data', chunk => {errors += chunk;});
    server.stdout.on('data', chunk => {
      output += chunk;
      if (output.includes('\n')) {clearTimeout(timer); resolve(output.split('\n')[0].trim());}
    });
    server.once('error', error => {clearTimeout(timer); reject(error);});
    server.once('exit', code => {clearTimeout(timer); reject(new Error('Server exited ' + code + ': ' + errors));});
  });
}

async function openPage(context, url) {
  const page = await context.newPage();
  page.setDefaultTimeout(15000);
  await page.goto(url);
  await page.waitForFunction(() => !document.documentElement.hasAttribute('data-nonce'));
  return page;
}

async function reloadWithDrafts(page, revision) {
  await page.locator('#ui-update-notice').waitFor({state: 'visible'});
  const navigation = page.waitForNavigation({waitUntil: 'domcontentloaded'});
  await page.locator('[data-ui-reload]').click();
  await navigation;
  await page.waitForFunction(expected => document.documentElement.dataset.uiRevision === expected && !document.documentElement.hasAttribute('data-nonce'), revision);
}

(async () => {
  snapshotSource();
  execFileSync('go', ['build', '-o', firstBinary, './cmd/pl'], {cwd: source});
  fs.appendFileSync(path.join(source, 'internal/webui/assets/workbench.css'), '\n/* Isolated upgrade browser revision fixture. */\n');
  execFileSync('go', ['build', '-o', secondBinary, './cmd/pl'], {cwd: source});
  execFileSync('go', ['test', '-c', '-o', peer, './internal/app'], {cwd: source});
  fs.mkdirSync(fixture);
  const git = (...args) => execFileSync('git', args, {cwd: fixture, encoding: 'utf8'});
  git('init', '-q');
  git('config', 'user.name', 'Test');
  git('config', 'user.email', 'test@example.invalid');
  git('config', 'commit.gpgSign', 'false');
  git('commit', '--allow-empty', '-m', 'initial');
  fs.appendFileSync(path.join(fixture, '.git/info/exclude'), '\n/fake-*\n/.agents/\n');
  const pellet = cli('add', 'Original pellet before upgrade', '--group', 'web-ui');
  const owned = cli('add', 'Owned without a running process');
  cli('start', owned.id);
  const memory = cli('memory', 'add', '--text', 'Original human memory', '--created-by', 'human');
  cli('skill', 'install', '--scope', 'repo', '--agent', 'codex', '--yes');
  fs.writeFileSync(path.join(fixture, 'fake-mode'), 'schedule_success');
  origin = await startServer(firstBinary);
  const port = new URL(origin).port;
  const engine = process.env.PLAYWRIGHT_BROWSER === 'webkit' ? webkit : chromium;
  browser = await engine.launch({headless: true, ...(engine === chromium && process.env.PLAYWRIGHT_CHANNEL ? {channel: process.env.PLAYWRIGHT_CHANNEL} : {}), ...(engine === webkit && process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE ? {executablePath: process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE} : {})});
  const context = await browser.newContext();
  await context.addInitScript(() => {
    window.upgradeResults = [];
    document.addEventListener('datastar-signal-patch', event => {
      if (event.detail._webResult) window.upgradeResults.push(event.detail._webResult.status);
    });
  });
  const errors = [], posts = [], assets = [], planningRequests = [];
  const observeContext = observed => {
    observed.on('page', page => page.on('pageerror', error => errors.push(error.message)));
    observed.on('request', request => {
      if (request.method() === 'POST') {
        posts.push(request.url());
        if (new URL(request.url()).pathname.endsWith('/planning')) planningRequests.push(request.postDataJSON());
      }
      if (new URL(request.url()).pathname.startsWith('/assets/')) assets.push(request.url());
    });
  };
  observeContext(context);
  const queuePath = `/projects/${pellet.project}/tasks`;
  const pelletPage = await openPage(context, origin + queuePath + '/' + pellet.id);
  const memoryPage = await openPage(context, origin + `/projects/${pellet.project}/memories/${memory.id}`);
  const cleanPage = await openPage(context, origin + queuePath);
  const assignmentPage = await openPage(context, origin + queuePath + '?workspace=1');
  const creationPage = await openPage(context, origin + queuePath);
  await creationPage.getByText('+ New pellet', {exact: true}).click();
  const creationForm = creationPage.locator('.create-popover form');
  await creationForm.locator('[name=title]').fill('Unsubmitted creation draft');
  await creationForm.locator('[name=status]').selectOption('maybe_later');
  await creationForm.locator('[name=description]').fill('Keep the open creation form too.');
  await creationForm.locator('[name=description]').press('ArrowLeft');
  const creationCaret = await creationForm.locator('[name=description]').evaluate(el => el.selectionStart);
  // A separate tab-storage context keeps planning preferences independent from
  // the record cases, while sharing the same actual server and database.
  const planningContext = await browser.newContext({viewport: {width: 1280, height: 850}});
  observeContext(planningContext);
  const planningPage = await openPage(planningContext, origin + queuePath);
  await planningPage.locator('#plan-tab').click();
  await planningPage.locator('#plan-message').waitFor();
  // Seed the manual draft through the real planner action; the empty tray is hidden.
  await planningPage.locator('[data-plan=add-draft]').evaluate(button => button.click());
  const planningDraft = planningPage.locator('.plan-card').first();
  const planningDraftID = await planningDraft.getAttribute('data-draft-id');
  await planningDraft.locator('[name=title]').fill('Saved planning draft before upgrade');
  await planningDraft.locator('[name=description]').fill('Saved planning context before upgrade.');
  const planningEndpoint = origin + `/projects/${pellet.project}/planning`;
  const readPlanning = async () => (await (await planningPage.request.get(planningEndpoint)).json()).chat;
  await until(async () => {
    const saved = await readPlanning();
    return saved?.state.drafts[0]?.description === 'Saved planning context before upgrade.';
  }, 'Manual planning fixture did not autosave to the real database');
  const savedPlanningChat = await readPlanning();
  const planningVersion = await planningDraft.locator('[name=version]').inputValue();
  await planningDraft.getByRole('button', {name: 'Close draft editor', exact: true}).click();
  const oldRevision = await cleanPage.locator('html').getAttribute('data-ui-revision');
  assert.match(oldRevision, /^[a-f0-9]{64}$/);
  assert.equal((await (await cleanPage.request.get(origin + '/ui-version')).json()).revision, oldRevision);
  assert.ok(assets.length >= 6, 'Initial page did not load its embedded module graph');
  for (const asset of assets) assert.ok(new URL(asset).pathname.startsWith('/assets/' + oldRevision + '/'), 'Unversioned initial asset: ' + asset);

  const pelletForm = pelletPage.locator('form.record-edit');
  const memoryForm = memoryPage.locator('form.record-edit');
  const assignmentForm = assignmentPage.locator('.assignment-form');
  await assignmentPage.locator('#assignment-popover > summary').click();
  const assignmentVersion = await assignmentForm.locator('[name=version]').inputValue();
  await assignmentForm.locator('select[name=mode]').selectOption('explicit', {force: true});
  await assignmentForm.locator('input[name=groups]').first().check();
  const assignmentGroup = await assignmentForm.locator('input[name=groups]').first().inputValue();
  await assignmentForm.locator('input[name=include_ungrouped]').uncheck();
  await assignmentForm.locator('input[name=new_group]').fill('Unfinished exact group');
  const pelletVersion = await pelletForm.locator('[name=version]').inputValue();
  const memoryVersion = await memoryForm.locator('[name=version]').inputValue();
  const titleDraft = 'Unfinished pellet title across upgrade';
  const descriptionDraft = 'Unfinished description\nwith another line.';
  const memoryDraft = 'Unfinished human memory\nkept across the server upgrade.';
  await pelletForm.locator('[name=title]').fill(titleDraft);
  await pelletForm.locator('[name=description]').fill(descriptionDraft);
  await pelletForm.locator('[name=description]').press('ArrowLeft');
  const caret = await pelletForm.locator('[name=description]').evaluate(el => el.selectionStart);
  await memoryForm.locator('[name=text]').fill(memoryDraft);
  await cleanPage.evaluate(() => {window.preUpgradeQueue = document.getElementById('queue-rows'); window.preUpgradeQueueText = window.preUpgradeQueue.textContent;});
  const staleForm = await pelletForm.evaluate(form => Object.fromEntries(new FormData(form)));

  await stopServer();
  // Fail a real autosave against the stopped server. This retains an exact
  // request, not a fabricated HTTP result, for explicit retry after the upgrade.
  const planningTitle = 'Unfinished manual planning draft across upgrade';
  const planningDescription = 'Planning description kept through restart.\n'.repeat(18);
  const planningAcceptance = 'The original planning draft identity and fields survive.';
  const planningInput = 'Unsent planning follow-up\nwith its caret kept in place.';
  await planningDraft.locator('.plan-open-draft').click();
  await planningDraft.locator('[name=title]').fill(planningTitle);
  await planningDraft.locator('[name=description]').fill(planningDescription);
  await planningDraft.locator('[name=acceptance]').fill(planningAcceptance);
  await planningDraft.locator('[name=group]').fill('web-ui');
  await planningDraft.getByRole('button', {name: 'Close draft editor', exact: true}).click();
  await planningDraft.locator('[data-field=selected]').uncheck();
  await planningPage.locator('#plan-message').fill(planningInput);
  await planningPage.locator('#plan-message').press('ArrowLeft');
  await planningPage.locator('[data-plan=retry]:visible').waitFor({state: 'visible'});
  const failedPlanningRequest = planningRequests.at(-1);
  assert.equal(failedPlanningRequest.action, 'save');
  assert.equal(failedPlanningRequest.chat_id, savedPlanningChat.id);
  assert.equal(failedPlanningRequest.state.input, planningInput);
  assert.equal(failedPlanningRequest.state.drafts[0].title, planningTitle);
  assert.match(failedPlanningRequest.request_id, /^[a-f0-9-]{36}$/i);
  const planningCaret = await planningPage.locator('#plan-message').evaluate(element => element.selectionStart);
  const planningScroll = await planningPage.locator('.plan-transcript').evaluate(element => {
    element.scrollTop = 47;
    return element.scrollTop;
  });
  // The conversation can be empty for a manual draft; preserve its exact scroll.
  const postCount = posts.length;
  // Change the authoritative record while the tab retains the earlier CAS
  // version. Reload may recover draft text, but must not silently refresh CAS.
  cli('edit', pellet.id, '--title', 'Authoritative edit while old UI was open');
  assert.equal(await startServer(secondBinary, port), origin, 'Upgrade changed the origin');
  const newRevision = (await (await cleanPage.request.get(origin + '/ui-version')).json()).revision;
  assert.notEqual(newRevision, oldRevision, 'Separate embedded builds did not produce different revisions');
  for (const page of [pelletPage, memoryPage, cleanPage, assignmentPage, creationPage, planningPage]) await page.locator('#ui-update-notice').waitFor({state: 'visible', timeout: 25000});
  for (const page of [pelletPage, memoryPage, cleanPage, assignmentPage, creationPage, planningPage]) assert.equal(await page.locator('html').getAttribute('data-ui-revision'), oldRevision, 'An old tab automatically reloaded');
  assert.equal(await pelletForm.locator('[name=title]').inputValue(), titleDraft);
  assert.equal(await memoryForm.locator('[name=text]').inputValue(), memoryDraft);
  assert.equal(posts.length, postCount, 'Upgrade detection automatically submitted work');
  assert.equal(await planningDraft.locator('[name=title]').inputValue(), planningTitle);
  assert.equal(await planningPage.locator('#plan-message').inputValue(), planningInput);
  const pendingQuery = 'Owned';
  await cleanPage.locator('#search').fill(pendingQuery);
  assert.equal(new URL(cleanPage.url()).searchParams.has('q'), false, 'Outdated search sent a fragment request');

  // Both explicit stale clients and legacy Datastar clients must be rejected
  // before CSRF/admission/CAS checks, even with their original form bytes.
  for (const header of [oldRevision, null]) {
    const headers = {'Origin': origin, 'Datastar-Request': 'true', 'Pellets-Target': 'inspector-host'};
    if (header) headers['Pellets-UI-Revision'] = header;
    const response = await cleanPage.request.post(origin + `/projects/${pellet.project}/pellets/${pellet.id}/edit`, {headers, form: staleForm});
    assert.equal(response.status(), 412, 'Stale/missing UI revision reached a mutation');
    assert.equal((await response.json()).code, 'ui_revision_changed');
  }
  assert.equal(cli('show', pellet.id).title, 'Authoritative edit while old UI was open');
  const staleAsset = await cleanPage.request.get(origin + '/assets/' + oldRevision + '/workbench.css');
  assert.equal(staleAsset.status(), 412, 'Old asset URL served a different build');
  const newCSS = await cleanPage.request.get(origin + '/assets/' + newRevision + '/workbench.css');
  assert.equal(newCSS.status(), 200);
  assert.match(newCSS.headers()['cache-control'], /immutable/);
  assert.match(await newCSS.text(), /Isolated upgrade browser revision fixture/);

  const beforeBlockedSubmit = posts.length;
  await pelletForm.evaluate(form => form.requestSubmit());
  await cleanPage.evaluate(() => document.dispatchEvent(new CustomEvent('pellets-refresh')));
  await cleanPage.waitForTimeout(400);
  assert.equal(posts.length, beforeBlockedSubmit, 'Outdated client submitted a new mutation');
  assert.equal(await cleanPage.evaluate(() => window.preUpgradeQueue === document.getElementById('queue-rows') && window.preUpgradeQueueText === document.getElementById('queue-rows').textContent), true, 'Restart mixed new fragments into the old page');

  // Memory permits more text than the bounded reload handoff. Refuse an
  // oversized handoff without navigating, truncating, or writing draft data.
  const oversizedMemory = 'Unfinished memory '.repeat(18000);
  await memoryForm.locator('[name=text]').fill(oversizedMemory);
  await memoryPage.locator('[data-ui-reload]').click();
  await until(async () => /too large/i.test(await memoryPage.locator('[data-ui-update-detail]').innerText()), 'Oversized draft did not explain why reload was blocked');
  assert.equal(await memoryPage.locator('html').getAttribute('data-ui-revision'), oldRevision);
  assert.equal(await memoryForm.locator('[name=text]').inputValue(), oversizedMemory, 'Oversized reload truncated unfinished text');
  assert.equal(await memoryPage.evaluate(() => sessionStorage.getItem('pellets-ui-reload-drafts')), null, 'Oversized draft escaped the storage bound');
  await memoryForm.locator('[name=text]').fill(memoryDraft);

  // The reload action belongs to the user. It restores both record kinds in
  // separate tabs and keeps the old optimistic tokens, never auto-saving them.
  const assetMark = assets.length;
  await reloadWithDrafts(pelletPage, newRevision);
  await until(async () => await pelletForm.locator('[name=title]').inputValue() === titleDraft, 'Pellet title draft was not restored');
  assert.equal(await pelletForm.locator('[name=description]').inputValue(), descriptionDraft);
  assert.equal(await pelletForm.locator('[name=version]').inputValue(), pelletVersion, 'Reload silently replaced stale pellet CAS');
  assert.equal(await pelletForm.locator('[name=description]').evaluate(el => el.selectionStart), caret, 'Reload lost the unfinished description caret');
  assert.equal(await pelletForm.locator('[name=description]').evaluate(el => document.activeElement === el), true, 'Reload lost focused unfinished input');
  assert.equal(await pelletPage.evaluate(() => sessionStorage.getItem('pellets-ui-reload-drafts')), null, 'Pellet handoff was not consumed once');
  await reloadWithDrafts(memoryPage, newRevision);
  await until(async () => await memoryForm.locator('[name=text]').inputValue() === memoryDraft, 'Memory draft was not restored');
  assert.equal(await memoryForm.locator('[name=version]').inputValue(), memoryVersion);
  assert.equal(await memoryPage.evaluate(() => sessionStorage.getItem('pellets-ui-reload-drafts')), null, 'Memory handoff was not consumed once');
  assert.equal(cli('memory', 'show', String(memory.id)).text, 'Original human memory', 'Restoring a draft automatically saved memory');
  for (const asset of assets.slice(assetMark)) assert.ok(new URL(asset).pathname.startsWith('/assets/' + newRevision + '/'), 'Reload mixed old/unversioned assets: ' + asset);
  assert.ok(assets.slice(assetMark).some(asset => asset.endsWith('/ui-version.js')), 'Reload omitted the revision client from the new graph');

  const postsBeforeCreationReload = posts.length;
  await reloadWithDrafts(creationPage, newRevision);
  assert.equal(await creationPage.locator('.create-popover').evaluate(el => el.open), true, 'Reload hid an open creation draft');
  assert.equal(await creationForm.locator('[name=title]').inputValue(), 'Unsubmitted creation draft');
  assert.equal(await creationForm.locator('[name=status]').inputValue(), 'maybe_later');
  assert.equal(await creationForm.locator('[name=description]').inputValue(), 'Keep the open creation form too.');
  assert.equal(await creationForm.locator('[name=description]').evaluate(el => el.selectionStart), creationCaret);
  assert.equal(await creationForm.locator('[name=description]').evaluate(el => el === document.activeElement), true);
  assert.equal(posts.length, postsBeforeCreationReload, 'Restoring a creation draft submitted it');

  const planningPostsBeforeReload = planningRequests.length;
  await reloadWithDrafts(planningPage, newRevision);
  await until(async () => await planningDraft.locator('[name=title]').inputValue() === planningTitle,
    'Manual planning draft was not restored after the real server upgrade');
  assert.equal(await planningDraft.getAttribute('data-draft-id'), planningDraftID, 'Reload replaced planning draft identity');
  assert.equal(await planningDraft.locator('[name=description]').inputValue(), planningDescription);
  assert.equal(await planningDraft.locator('[name=acceptance]').inputValue(), planningAcceptance);
  assert.equal(await planningDraft.locator('[name=group]').inputValue(), 'web-ui');
  assert.equal(await planningDraft.locator('[data-field=selected]').isChecked(), false);
  assert.equal(await planningDraft.locator('[name=version]').inputValue(), planningVersion, 'Planning reload replaced the retained optimistic version');
  assert.equal(await planningDraft.locator('dialog').evaluate(element => element.open), false, 'Planning reload reopened the closed draft editor');
  assert.equal(await planningPage.locator('#plan-message').inputValue(), planningInput);
  assert.equal(await planningPage.locator('#plan-message').evaluate(element => element.selectionStart), planningCaret, 'Planning reload lost composer caret');
  assert.equal(await planningPage.locator('#plan-message').evaluate(element => document.activeElement === element), true, 'Planning reload lost composer focus');
  assert.equal(await planningPage.locator('.plan-transcript').evaluate(element => element.scrollTop), planningScroll, 'Planning reload lost draft list scroll');
  await planningPage.locator('[data-plan=retry]:visible').waitFor({state: 'visible'});
  await planningPage.waitForTimeout(700);
  assert.equal(planningRequests.length, planningPostsBeforeReload, 'Planning reload automatically replayed a failed request');
  assert.deepEqual((await readPlanning()).state, savedPlanningChat.state, 'Planning reload changed saved chat before explicit retry');
  assert.equal(await planningPage.locator('#ui-recovered-drafts').count(), 0, 'Planning forms leaked into the generic reload handoff');

  await planningPage.locator('[data-plan=retry]:visible').click();
  await until(async () => (await readPlanning()).state.input === planningInput, 'Explicit planning retry did not save restored input');
  const retriedPlanningRequest = planningRequests[planningPostsBeforeReload];
  assert.equal(retriedPlanningRequest.request_id, failedPlanningRequest.request_id, 'Planning retry invented a new request ID');
  assert.equal(retriedPlanningRequest.version, failedPlanningRequest.version, 'Planning retry changed the request CAS');
  assert.deepEqual(retriedPlanningRequest.state, failedPlanningRequest.state, 'Planning retry changed the captured request payload');
  assert.equal(retriedPlanningRequest._csrf, await planningPage.locator('input[name=_csrf]').first().inputValue(), 'Planning retry reused stale CSRF');
  assert.notEqual(retriedPlanningRequest._csrf, failedPlanningRequest._csrf, 'Restart fixture did not exercise refreshed CSRF');
  const persistedPlanningDraft = (await readPlanning()).state.drafts[0];
  assert.equal(persistedPlanningDraft.id, planningDraftID);
  assert.equal(persistedPlanningDraft.title, planningTitle);
  assert.equal(persistedPlanningDraft.created_reference, undefined, 'Planning recovery implicitly created a pellet');
  assert.equal(planningRequests.some(request => ['send', 'create'].includes(request.action)), false, 'Planning reload invoked the planner or created pellets');

  await reloadWithDrafts(assignmentPage, newRevision);
  assert.equal(await assignmentPage.locator('#assignment-popover').evaluate(element => element.open), true, 'Reload lost the assignment editor disclosure');
  assert.equal(await assignmentForm.locator('select[name=mode]').inputValue(), 'explicit');
  assert.equal(await assignmentForm.locator('.explicit-groups').isVisible(), true, 'Restored explicit mode left the group choices hidden');
  assert.equal(await assignmentForm.locator('input[name=groups]').first().isChecked(), true);
  assert.equal(await assignmentForm.locator('input[name=include_ungrouped]').isChecked(), false);
  assert.equal(await assignmentForm.locator('input[name=new_group]').inputValue(), 'Unfinished exact group');
  assert.equal(await assignmentForm.locator('[name=version]').inputValue(), assignmentVersion, 'Reload replaced the unfinished assignment CAS');
  await assignmentPage.locator('#assignment-popover > summary').click();

  // A second real client saves routing while the recovered, dirty editor is
  // closed. Live patches must not pair its old choices with a fresh CAS token.
  const routingEditor = await openPage(context, origin + queuePath + '?workspace=1');
  const routingForm = routingEditor.locator('.assignment-form');
  const routingEndpoint = await routingForm.getAttribute('action');
  assert.equal(await routingForm.locator('[name=version]').inputValue(), assignmentVersion, 'Restoring an assignment draft automatically saved it');
  const refreshed = assignmentPage.waitForResponse(async response => {
    if (response.request().headers()['pellets-target'] !== 'live') return false;
    try { return (await response.text()).includes('external-group'); }
    catch { return false; } // A superseded background response has no body.
  });
  const externalSave = await routingEditor.request.post(origin + routingEndpoint, {
    headers: {'Origin': origin, 'Pellets-UI-Revision': newRevision},
    form: {_csrf: await routingForm.locator('[name=_csrf]').inputValue(), version: assignmentVersion,
      mode: 'explicit', new_group: 'external-group', include_ungrouped: 'true', return_to: queuePath + '?workspace=1'},
  });
  assert.equal(externalSave.status(), 200, 'Concurrent assignment fixture did not save through the real endpoint');
  await routingEditor.reload();
  const externalVersion = await routingForm.locator('[name=version]').inputValue();
  assert.notEqual(externalVersion, assignmentVersion);
  await assignmentPage.evaluate(() => document.dispatchEvent(new CustomEvent('pellets-refresh')));
  await (await refreshed).finished();
  await assignmentPage.waitForTimeout(150);
  assert.equal(await assignmentPage.locator('#assignment-popover').evaluate(element => element.open), false);
  assert.equal(await assignmentForm.locator('[name=version]').inputValue(), assignmentVersion, 'Closed dirty assignment adopted a fresh CAS during invalidation');
  await assignmentPage.locator('#assignment-popover > summary').click();
  assert.equal(await assignmentForm.locator('input[name=groups]:checked').inputValue(), assignmentGroup);
  assert.equal(await assignmentForm.locator('input[name=new_group]').inputValue(), 'Unfinished exact group');
  await assignmentPage.getByRole('button', {name: 'Save assignments', exact: true}).click();
  await until(() => assignmentPage.evaluate(() => window.upgradeResults.includes(409)), 'Restored stale assignment did not produce an optimistic conflict');
  assert.match(await assignmentPage.locator('#inspector-host').innerText(), /workspace assignments changed/i);
  await routingEditor.reload();
  assert.equal(await routingForm.locator('[name=version]').inputValue(), externalVersion, 'Stale assignment submission overwrote newer routing');
  await routingEditor.locator('#assignment-popover > summary').click();
  assert.equal(await routingForm.locator('input[name=groups]:checked').count(), 1);
  assert.match(await routingForm.locator('input[name=groups]:checked').evaluate(input => input.labels[0].innerText), /external-group/);
  await routingEditor.close();

  await pelletPage.getByRole('button', {name: 'Save changes', exact: true}).click();
  await pelletPage.locator('.conflict-state').waitFor();
  assert.match(await pelletPage.locator('.conflict-state').innerText(), /Unfinished pellet title across upgrade/);
  assert.equal(cli('show', pellet.id).title, 'Authoritative edit while old UI was open', 'Restored stale draft overwrote the authoritative edit');
  await memoryPage.getByRole('button', {name: 'Save text', exact: true}).click();
  await until(() => cli('memory', 'show', String(memory.id)).text === memoryDraft, 'Explicit memory save did not persist the restored draft');
  const postsBeforeFilterReload = posts.length;
  await reloadWithDrafts(cleanPage, newRevision);
  await until(async () => new URL(cleanPage.url()).searchParams.get('q') === pendingQuery &&
    (await cleanPage.locator('.task-title').allTextContents()).join('|') === 'Owned without a running process',
  'Reloaded unfinished filter did not update its URL and authoritative rows');
  assert.equal(await cleanPage.locator('#search').inputValue(), pendingQuery);
  assert.equal(await cleanPage.locator('.task-row').count(), 1);
  assert.equal(posts.length, postsBeforeFilterReload, 'Restoring an unfinished browsing filter automatically submitted a write');
  assert.equal(cli('show', owned.id).status, 'in_progress', 'Upgrade changed existing ownership');
  assert.equal(await cleanPage.locator('.run-state').count(), 0, 'Server restart started an execution run');
  assert.equal(fs.existsSync(path.join(fixture, 'fake-events.jsonl')), false, 'Server restart contacted the execution peer');
  assert.equal(posts.some(url => /\/schedules(?:\/|$)/.test(url)), false, 'Upgrade or draft recovery automatically started a schedule');
  assert.deepEqual(errors, []);
  console.log('PASS real same-origin UI upgrade: revision guard, unchanged old DOM, complete new asset graph, pellet/memory/assignment/planning drafts and old CAS, retained planning request ID and fresh CSRF, composer focus/caret/disclosure/scroll, closed-draft invalidation, explicit conflict/save/retry, no automatic execution');
})().catch(error => {console.error(error); process.exitCode = 1;}).finally(async () => {
  if (browser) await browser.close();
  await stopServer();
  fs.rmSync(temporary, {recursive: true, force: true});
});
