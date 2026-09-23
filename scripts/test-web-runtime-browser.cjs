// Compiled production server and protocol peer, disposable queue only.
// NODE_PATH=... PLAYWRIGHT_CHANNEL=chrome node scripts/test-web-runtime-browser.cjs
// Set PELLETS_RUNTIME_BROWSER_CASE to run one scenario, e.g. watch_waiting.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium, webkit} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-runtime-browser-'));
const baseline = process.env.PELLETS_NAVIGATION_BASELINE;
const binary = baseline || path.join(temporary, process.platform === 'win32' ? 'pl.exe' : 'pl');
const peer = path.join(temporary, process.platform === 'win32' ? 'codex.exe' : 'codex');
const environment = {...process.env, PATH: temporary + path.delimiter + process.env.PATH,
  PELLETS_CODEX_EXECUTABLE: peer, PELLETS_SUPERVISOR_PEER: '1', GORACE: 'atexit_sleep_ms=0'};
let server, browser;
async function stop() {
  if (server && server.exitCode === null && server.signalCode === null) {
    const done = new Promise(resolve => server.once('exit', resolve)); server.kill('SIGINT'); await done;
  }
}
async function start(root) {
  server = spawn(binary, ['server', '--port', '0', '--no-open'], {cwd: root, env: environment});
  return new Promise((resolve, reject) => {
    let output = '', errors = '';
    const timer = setTimeout(() => reject(new Error('Server readiness: ' + errors)), 15000);
    server.stderr.on('data', x => {errors += x;});
    server.stdout.on('data', x => {output += x; if (output.includes('\n')) {clearTimeout(timer); resolve(output.split('\n')[0].trim());}});
    server.once('error', reject);
  });
}
async function until(check, message) {
  const deadline = Date.now() + 20000;
  while (Date.now() < deadline) {
    if (await check()) return;
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  throw new Error(message);
}
(async () => {
  if (!baseline) execFileSync('go', ['build', '-o', binary, './cmd/pl'], {cwd: repository});
  execFileSync('go', ['test', '-c', '-o', peer, './internal/app'], {cwd: repository});
  const engine = process.env.PLAYWRIGHT_BROWSER === 'webkit' ? webkit : chromium;
  browser = await engine.launch({headless: true, ...(engine === chromium && process.env.PLAYWRIGHT_CHANNEL ? {channel: process.env.PLAYWRIGHT_CHANNEL} : {}), ...(engine === webkit && process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE ? {executablePath: process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE} : {})});
  const cases = [...(process.platform === 'win32' ? [] : ['runtime_probe_gate']), 'runtime_old', 'schedule_runtime_error', 'schedule_activity_errors_gate', 'schedule_rpc_error', 'schedule_unfinished', 'schedule_fresh_choice', 'schedule_noop', 'stop_after_pellet', 'watch_waiting', 'navigation_owned'];
  const selectedCase = process.env.PELLETS_RUNTIME_BROWSER_CASE;
  assert.ok(!selectedCase || cases.includes(selectedCase), 'Unknown PELLETS_RUNTIME_BROWSER_CASE');
  for (const mode of cases.filter(mode => !selectedCase || selectedCase === mode)) {
    const root = path.join(temporary, mode); fs.mkdirSync(root);
    const git = (...args) => execFileSync('git', args, {cwd: root, stdio: 'pipe'});
    const cli = (...args) => JSON.parse(execFileSync(binary, ['--json', ...args], {cwd: root, env: environment, encoding: 'utf8'})).data;
    git('init', '-q'); git('config', 'user.name', 'Test'); git('config', 'user.email', 'test@example.invalid');
    git('config', 'commit.gpgSign', 'false'); git('commit', '--allow-empty', '-m', 'initial');
    fs.appendFileSync(path.join(root, '.git', 'info', 'exclude'), '\n/fake-*\n/.agents/\n');
    cli('init-db');
    const pellet = cli('add', 'Runtime compatibility regression');
    const binding = JSON.parse(fs.readFileSync(path.join(root, '.git', 'pellets-database.json')));
    assert.ok(fs.realpathSync(path.resolve(root, '.git', binding.path)).startsWith(fs.realpathSync(root) + path.sep));
    const queued = mode === 'stop_after_pellet' ? cli('add', 'Leave this queued after stopping') : undefined;
    cli('skill', 'install', '--scope', 'repo', '--agent', 'codex', '--yes');
    fs.writeFileSync(path.join(root, 'fake-mode'), mode === 'schedule_fresh_choice' ? 'schedule_unfinished' : mode === 'stop_after_pellet' ? 'schedule_gate' : mode === 'watch_waiting' ? 'schedule_activity_gate' : mode);
    if (mode === 'navigation_owned') cli('start', pellet.id);
    let origin = await start(root);
    const page = await browser.newPage({viewport: {width: 1280, height: 1000}, hasTouch: true, deviceScaleFactor: 1});
    const errors = []; page.on('pageerror', error => errors.push(error.message));
    const route = `/projects/${pellet.project}/workspaces/1`;
    if (mode === 'watch_waiting') {
      await page.addInitScript(() => {
        const NativeSource = window.EventSource;
        window.EventSource = class extends NativeSource {
          constructor(url, options) { super(url, options); if (url.includes('/activity?')) window.activitySource = this; }
        };
      });
    }
    await page.goto(origin + route);
    if (mode === 'navigation_owned') {
      assert.equal(await page.locator('.execution-state-label').textContent(), 'Not running');
      assert.equal(await page.getByRole('button', {name: /Start next/}).count(), 0);
      if (process.env.PELLETS_NAVIGATION_AUDIT) await require('./web-navigation-contract.cjs')({page, baseline, state: 'Not running'});
      assert.equal(cli('show', pellet.id).status, 'in_progress');
      assert.deepEqual(errors, []);
      await page.close(); await stop();
      console.log('PASS navigation ownership without a run, explicit Resume retained');
      continue;
    }
    const startForm = page.locator('form[data-schedule]').filter({has: page.locator('select[name=mode]')});
    await startForm.getByRole('combobox', {name: 'Execution intention'}).click();
    await page.getByRole('option', {name: mode === 'stop_after_pellet' ? /^Through matching queue/ : mode === 'watch_waiting' ? /^Wait for matching work/ : /^One pellet/}).click();
    assert.equal(await startForm.locator('select[name=mode]').inputValue(), mode === 'stop_after_pellet' ? 'drain' : mode === 'watch_waiting' ? 'watch' : 'run_one');
    await startForm.getByRole('button', {name: /Start next/}).click();
    if (mode === 'schedule_activity_errors_gate') {
      await require('./web-runtime-errors-contract.cjs')({page, root, origin, until});
      assert.deepEqual(errors, []);
      await page.close(); await stop();
      console.log('PASS runtime error chronology, repeated messages, item completion and reconnect');
      continue;
    }
    if (mode === 'watch_waiting') {
      const artifacts = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-watch-state-'));
      console.log('Watch visual artifacts: ' + artifacts);
      const status = page.getByRole('region', {name: 'Current execution', exact: true});
      const label = status.locator('.execution-state-label');
      const operation = status.locator('.execution-operation');
      const activity = page.locator('.activity-panel');
      const expectState = async (text, working) => {
        await until(async () => await label.textContent() === text, 'Expected execution state ' + text);
        assert.equal(await status.getAttribute('data-working'), String(working));
        assert.equal(await status.getAttribute('data-show-operation'), String(working));
        assert.equal(await label.getAttribute('role'), 'status');
        assert.equal(await status.locator('.execution-indicator').evaluate(el => getComputedStyle(el).animationName), working ? 'execution-working' : 'none');
        if (!working) assert.equal(await operation.isVisible(), false, 'Inactive state retained an active operation');
      };
      const reportOperation = () => page.evaluate(() => window.activitySource.dispatchEvent(new MessageEvent('pellets-activity', {
        data: JSON.stringify({available: true, cursor: 100, items: [{id: 'watch-operation', sequence: 100, kind: 'command',
          status: 'running', title: 'Command', command: 'go test ./internal/webui'}]})
      })));
      const waitForWork = async () => {
        await until(() => activity.locator('.activity-event').count().then(count => count >= 5), 'Reported run history');
        await until(() => status.locator('.execution-phase').textContent().then(text => text === 'Working on the pellet.'), 'Implementation phase');
        await expectState('Working', true);
        await reportOperation();
        await operation.waitFor({state: 'visible'});
      };
      const receiptURL = origin + (await page.locator('form[action$="/stop-now"]').getAttribute('action')).replace(/\/stop-now$/, '');
      const receipt = async () => (await page.request.get(receiptURL)).json();
      const completeAndWait = async completed => {
        fs.writeFileSync(path.join(root, 'fake-complete'), 'complete');
        await until(async () => {
          const schedule = await receipt();
          return schedule.state === 'waiting' && schedule.completed === completed;
        }, 'Watch did not wait after completion');
        await until(() => page.locator('.schedule-state').textContent().then(text => /waiting for a matching queue target/.test(text)), 'Waiting schedule patch');
        await page.screenshot({path: path.join(artifacts, `watch-waiting-${completed}.png`)});
        await expectState('Waiting for work', false);
      };
      const expectHistory = async (id, runID, commit) => {
        assert.equal(await activity.getAttribute('data-run-id'), runID);
        assert.equal(await page.locator('.run-facts .run-state').textContent(), 'Completed');
        assert.match(await page.locator('.run-facts').textContent(), new RegExp(id));
        assert.equal(await page.locator('.run-outcome code').textContent(), commit);
        assert.ok(await activity.locator('.activity-event').count() >= 5, 'Completed activity was removed');
      };
      await waitForWork();
      if (process.env.PELLETS_NAVIGATION_AUDIT) await require('./web-navigation-contract.cjs')({page, baseline, state: 'Working'});
      const firstRun = await activity.getAttribute('data-run-id');
      await completeAndWait(1);
      assert.equal(cli('show', pellet.id).status, 'closed');
      const firstCommit = git('rev-parse', 'HEAD').toString().trim();
      await expectHistory(pellet.id, firstRun, firstCommit);
      if (process.env.PELLETS_NAVIGATION_AUDIT) await require('./web-navigation-contract.cjs')({page, baseline, state: 'Waiting for work'});
      assert.equal(await page.locator('.run-details').evaluate(el => el.open), false);
      await reportOperation();
      await expectState('Waiting for work', false);
      // A fresh document must render the live schedule above historical run state.
      const html = await (await page.request.get(origin + route)).text();
      assert.match(html, /class="execution-state-label" role="status">Waiting for work</);
      await page.reload();
      await until(() => activity.locator('.activity-event').count().then(count => count >= 5), 'Reloaded run history');
      await expectState('Waiting for work', false);
      await expectHistory(pellet.id, firstRun, firstCommit);
      await page.locator('.run-details > summary').focus(); await page.keyboard.press('Enter');
      assert.equal(await page.locator('.run-details').evaluate(el => el.open), true);
      await page.screenshot({path: path.join(artifacts, 'watch-waiting-history.png')});
      await page.keyboard.press('Enter');
      await page.setViewportSize({width: 390, height: 900});
      await page.screenshot({path: path.join(artifacts, 'watch-waiting-phone.png')});
      await page.setViewportSize({width: 1280, height: 1000});
      // Queue changes wake this same schedule without another start or reload.
      fs.unlinkSync(path.join(root, 'fake-complete'));
      const next = cli('add', 'Wake the waiting Watch schedule');
      await until(async () => await activity.getAttribute('data-run-id') !== firstRun, 'Watch did not start the next run');
      await waitForWork();
      assert.equal(cli('show', next.id).status, 'in_progress');
      await page.screenshot({path: path.join(artifacts, 'watch-working-again.png')});
      const nextRun = await activity.getAttribute('data-run-id');
      await completeAndWait(2);
      assert.equal(cli('show', next.id).status, 'closed');
      const nextCommit = git('rev-parse', 'HEAD').toString().trim();
      await expectHistory(next.id, nextRun, nextCommit);
      // Both idle stop controls remove the live schedule, revealing Finished.
      for (const control of ['Stop now', 'Stop after']) {
        const stopForm = page.locator('form[data-schedule]').filter({has: page.getByRole('button', {name: control, exact: true})});
        const endpoint = origin + await stopForm.getAttribute('action');
        const accepted = page.waitForResponse(response => response.url() === endpoint && response.request().method() === 'POST');
        await stopForm.getByRole('button', {name: control, exact: true}).click();
        assert.equal((await accepted).status(), 202);
        await until(async () => (await (await page.request.get(endpoint.replace(/\/stop-(now|after)$/, ''))).json()).state === 'stopped', 'Idle Watch did not stop');
        await expectState('Finished', false);
        assert.equal(await page.locator('.schedule-state').count(), 0);
        await expectHistory(next.id, nextRun, nextCommit);
        if (control === 'Stop now') {
          await page.screenshot({path: path.join(artifacts, 'watch-stopped.png')});
          await startForm.getByRole('combobox', {name: 'Execution intention'}).click();
          await page.getByRole('option', {name: /^Wait for matching work/}).click();
          await startForm.getByRole('button', {name: /Start next/}).click();
          await expectState('Waiting for work', false);
        }
      }
      assert.deepEqual(errors, []);
      await page.close(); await stop();
      console.log('PASS Watch waiting with history: initial render, live completion, idle activity, automatic wake, both stop controls');
      continue;
    }
    if (mode === 'stop_after_pellet') {
      const eventsFile = path.join(root, 'fake-events.jsonl');
      const events = () => fs.existsSync(eventsFile) ? fs.readFileSync(eventsFile, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse) : [];
      await until(() => events().some(event => event.method === 'turn/start'), 'First pellet never reached the gated turn');
      const stopAfter = page.locator('form[data-schedule][action$="/stop-after"]');
      await stopAfter.waitFor();
      const endpoint = origin + await stopAfter.getAttribute('action');
      const receiptURL = endpoint.replace(/\/stop-after$/, '');
      const readReceipt = async () => (await page.request.get(receiptURL)).json();
      const before = await readReceipt();
      assert.equal(before.mode, 'drain');
      assert.equal(before.completed, 0);
      assert.equal(before.stop_after_pellet, false);
      assert.equal(cli('show', pellet.id).status, 'in_progress');
      const stopped = page.waitForResponse(response => response.url() === endpoint && response.request().method() === 'POST');
      await stopAfter.getByRole('button', {name: 'Stop after', exact: true}).click();
      const response = await stopped;
      // Waiting for terminal UI alone can falsely pass after the peer's gate
      // times out. The actual stop request must be accepted while work is held.
      assert.equal(response.status(), 202, 'Stop after must return an accepted receipt');
      assert.deepEqual([...new URLSearchParams(response.request().postData()).keys()], ['_csrf'], 'Stop controls must not include execution admission fields');
      const stopping = await readReceipt();
      assert.equal(stopping.stop_after_pellet, true);
      assert.equal(stopping.completed, 0);
      assert.equal(stopping.state, 'running', 'Stop after interrupted the active pellet');
      assert.equal(cli('show', pellet.id).status, 'in_progress');
      assert.equal(cli('show', queued.id).status, 'open');
      fs.writeFileSync(path.join(root, 'fake-complete'), 'complete');
      await until(async () => {
        const status = await readReceipt();
        return status.state === 'stopped' && status.reason === 'stop_after_pellet' && status.completed === 1;
      }, 'Stop after did not finish exactly the current pellet');
      assert.equal(cli('show', pellet.id).status, 'closed');
      assert.equal(cli('show', queued.id).status, 'open', 'Drain claimed another pellet after Stop after');
      assert.equal(events().filter(event => event.method === 'turn/start').length, 1);
      assert.equal(events().filter(event => event.method === 'turn/interrupt').length, 0);
      assert.equal(git('rev-list', '--count', 'HEAD').toString().trim(), '2', 'Expected only initial and current pellet implementation commits');
      assert.deepEqual(errors, []);
      await page.close(); await stop();
      console.log('PASS Stop after: accepted CSRF-only receipt, active turn completes, next pellet remains open');
      continue;
    }
    if (mode === 'runtime_probe_gate') {
      const deadline = Date.now() + 15000;
      while (!fs.existsSync(path.join(root, 'fake-runtime-ready')) && Date.now() < deadline) await new Promise(resolve => setTimeout(resolve, 50));
      assert.ok(fs.existsSync(path.join(root, 'fake-runtime-ready')), 'Version probe did not start');
      const eventsFile = path.join(root, 'fake-events.jsonl');
      const events = fs.readFileSync(eventsFile, 'utf8');
      const pids = events.trim().split('\n').map(JSON.parse).filter(x => x.method === 'process').map(x => x.pid);
      assert.equal(pids.length, 3);
      const done = new Promise(resolve => server.once('exit', resolve)); server.kill('SIGKILL'); await done;
      const alive = pid => {try {return !execFileSync('ps', ['-o', 'stat=', '-p', String(pid)], {encoding: 'utf8', stdio: ['ignore','pipe','ignore']}).trim().startsWith('Z');} catch {return false;}};
      const cleanupDeadline = Date.now() + 15000;
      while (pids.some(alive) && Date.now() < cleanupDeadline) await new Promise(resolve => setTimeout(resolve, 50));
      assert.ok(pids.every(pid => !alive(pid)), 'Pre-claim runtime probe escaped custody');
      assert.equal(cli('show', pellet.id).status, 'open', 'Probe crash claimed work');
      origin = await start(root); await page.goto(origin + route);
      assert.equal(await page.locator('.run-facts').count(), 0);
      assert.equal(fs.readFileSync(eventsFile, 'utf8'), events, 'Restart replayed probe');
      await page.close(); await stop(); console.log('PASS pre-claim runtime probe crash custody'); continue;
    }
    if (mode === 'runtime_old') {
      await page.getByRole('button', {name: 'Use managed runtime and continue', exact: true}).waitFor();
      assert.equal(cli('show', pellet.id).status, 'open');
      assert.equal(await page.locator('.run-facts').count(), 0);
      assert.match(await page.locator('#request-feedback').innerText(), /0\.154\.0/);
      assert.equal((await page.request.get(origin + `/projects/${pellet.project}/schedules/1`)).status(), 404);
      await page.close(); await stop(); console.log('PASS runtime failure shown before schedule creation'); continue;
    }
    const deadline = Date.now() + 20000;
    let status;
    while (Date.now() < deadline) {
      const response = await page.request.get(origin + `/projects/${pellet.project}/schedules/1`);
      if (response.status() === 200) {status = await response.json(); if (['needs_attention', 'completed'].includes(status.state)) break;}
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    assert.equal(status?.state, mode === 'schedule_noop' ? 'completed' : 'needs_attention');
    await page.reload();
    if (mode === 'schedule_noop') {
      assert.equal(cli('show', pellet.id).status, 'closed');
      assert.equal(git('rev-list', '--count', 'HEAD').toString().trim(), '1', 'Already-satisfied work created a dummy commit');
      assert.match(await page.locator('.run-facts').textContent(), /Completed/);
      assert.equal(await page.getByRole('button', {name: 'Resume', exact: true}).count(), 0);
    } else if (mode === 'runtime_old') {
      assert.equal(cli('show', pellet.id).status, 'open', 'Old runtime claimed pellet');
      assert.match(await page.locator('body').innerText(), /0\.154\.0/);
      assert.equal(await page.locator('.run-facts').count(), 0);
      assert.equal(fs.existsSync(path.join(root, 'fake-events.jsonl')), false);
    } else {
      assert.equal(cli('show', pellet.id).status, 'in_progress');
      const check = async () => {
        const summary = await page.locator('.run-activity').textContent();
        if (mode === 'schedule_unfinished' || mode === 'schedule_fresh_choice') {
          assert.match(summary, /Outside-click checks passed; browser test failed on a hidden table cell/);
        } else {
          assert.match(summary, /GPT-6 Astra requires a newer Codex version/);
          assert.match(summary, /Upgrade Codex and retry/);
        }
        assert.match(summary, /\[redacted\]/);
        const html = await page.content();
        for (const secret of ['private-runtime-token', 'sk-private', 'private-error-details', 'private-rpc-data', 'private-attention-token']) assert.ok(!html.includes(secret), secret);
        assert.equal(await page.evaluate(() => window.codexErrorInjected), undefined, 'Error HTML executed');
        assert.equal(await page.locator('script').filter({hasText: 'codexErrorInjected'}).count(), 0);
      };
      await check();
      const events = fs.readFileSync(path.join(root, 'fake-events.jsonl'), 'utf8');
      await stop(); origin = await start(root); await page.goto(origin + route); await check();
      assert.equal(fs.readFileSync(path.join(root, 'fake-events.jsonl'), 'utf8'), events, 'Restart resumed a run');
      if (mode === 'schedule_fresh_choice') {
        fs.writeFileSync(path.join(root, 'fake-mode'), 'schedule_missing_history');
        await page.getByRole('button', {name: 'Resume', exact: true}).click();
        await page.getByRole('button', {name: 'Start a fresh conversation', exact: true}).waitFor();
        assert.equal(cli('show', pellet.id).status, 'in_progress');
        assert.equal((await page.request.get(origin + `/projects/${pellet.project}/schedules/1`)).status(), 404);
        fs.writeFileSync(path.join(root, 'fake-mode'), 'schedule_success');
        await page.getByRole('button', {name: 'Start a fresh conversation', exact: true}).click();
        await page.waitForFunction(() => document.querySelector('.run-facts')?.textContent.includes('Completed'));
        assert.equal(cli('show', pellet.id).status, 'closed');
        const after = fs.readFileSync(path.join(root, 'fake-events.jsonl'), 'utf8').trim().split('\n').map(JSON.parse);
        assert.equal(after.filter(x => x.method === 'thread/start').length, 2);
        assert.equal(after.filter(x => x.method === 'thread/resume').length, 0);
        assert.ok(JSON.stringify(after).includes('user chose a fresh conversation'));
        console.log('PASS missing history offers a fresh conversation and honors the choice');
      }
      if (mode === 'schedule_unfinished') {
        cli('close', pellet.id);
        await page.reload();
        assert.equal(await page.getByRole('button', {name: 'Resume', exact: true}).count(), 0);
        assert.equal(await page.locator('.run-notice.warning').count(), 0);
        assert.match(await page.locator('.run-facts').textContent(), /Resolved/);
        assert.match(await page.locator('body').innerText(), /No active pellet/);
        await stop(); origin = await start(root); await page.goto(origin + route);
        assert.equal(await page.getByRole('button', {name: 'Resume', exact: true}).count(), 0);
        assert.equal(await page.locator('.run-notice.warning').count(), 0);
        console.log('PASS closed pellet clears stale warnings and Resume across restart');
      }
      if (mode === 'schedule_runtime_error' && process.env.PELLETS_BROWSER_SCREENSHOT) await page.screenshot({path: process.env.PELLETS_BROWSER_SCREENSHOT, fullPage: true});
      if (mode === 'schedule_runtime_error') {
        if (process.env.PELLETS_NAVIGATION_AUDIT) await require('./web-navigation-contract.cjs')({page, baseline, state: 'Failed · needs attention'});
        fs.writeFileSync(path.join(root, 'runtime-repair.txt'), 'keep this repair');
        git('add', 'runtime-repair.txt'); git('commit', '-m', 'repair runtime');
        const repairHead = git('rev-parse', 'HEAD').toString().trim();
        fs.writeFileSync(path.join(root, 'fake-mode'), 'schedule_success');
        await page.getByRole('button', {name: 'Resume', exact: true}).click();
        const deadline = Date.now() + 20000;
        let resumed;
        while (Date.now() < deadline) {
          const response = await page.request.get(origin + `/projects/${pellet.project}/schedules/1`);
          if (response.status() === 200) { resumed = await response.json(); if (['completed','needs_attention'].includes(resumed.state)) break; }
          await new Promise(resolve => setTimeout(resolve, 50));
        }
        assert.equal(resumed?.state, 'completed', JSON.stringify(resumed));
        assert.equal(cli('show', pellet.id).status, 'closed');
        assert.equal(git('rev-list', '--count', 'HEAD').toString().trim(), '3', 'Expected only initial, repair, and implementation commits');
        assert.equal(fs.readFileSync(path.join(root, 'runtime-repair.txt'), 'utf8'), 'keep this repair');
        const after = fs.readFileSync(path.join(root, 'fake-events.jsonl'), 'utf8').trim().split('\n').map(JSON.parse);
        assert.equal(after.filter(x => x.method === 'thread/start').length, 1);
        assert.equal(after.filter(x => x.method === 'thread/resume').length, 1);
        const prompt = JSON.stringify(after.filter(x => x.method === 'turn/start').at(-1));
        assert.ok(prompt.includes(repairHead)); assert.ok(prompt.includes('Commits landed since your previous attempt'));
        await page.reload(); assert.match(await page.locator('.run-facts').textContent(), /Completed/);
        console.log('PASS failure → repair commit → Resume at current HEAD');
      }

    }
    assert.deepEqual(errors, []);
    await page.close(); await stop();
    console.log('PASS ' + mode);
  }
})().catch(error => {console.error(error); process.exitCode = 1;}).finally(async () => {
  await stop(); if (browser) await browser.close(); fs.rmSync(temporary, {recursive: true, force: true});
});
