// Compiled production server and protocol peer, disposable queue only.
// NODE_PATH=... PLAYWRIGHT_CHANNEL=chrome node scripts/test-web-runtime-browser.cjs
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {execFileSync, spawn} = require('node:child_process');
const {chromium} = require('playwright');
const repository = path.resolve(__dirname, '..');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'pellets-runtime-browser-'));
const binary = path.join(temporary, process.platform === 'win32' ? 'pl.exe' : 'pl');
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
(async () => {
  execFileSync('go', ['build', '-o', binary, './cmd/pl'], {cwd: repository});
  execFileSync('go', ['test', '-c', '-o', peer, './internal/app'], {cwd: repository});
  browser = await chromium.launch({headless: true, ...(process.env.PLAYWRIGHT_CHANNEL ? {channel: process.env.PLAYWRIGHT_CHANNEL} : {})});
  for (const mode of [...(process.platform === 'win32' ? [] : ['runtime_probe_gate']), 'runtime_old', 'schedule_runtime_error', 'schedule_rpc_error']) {
    const root = path.join(temporary, mode); fs.mkdirSync(root);
    const git = (...args) => execFileSync('git', args, {cwd: root, stdio: 'pipe'});
    const cli = (...args) => JSON.parse(execFileSync(binary, args, {cwd: root, env: environment, encoding: 'utf8'})).data;
    git('init', '-q'); git('config', 'user.name', 'Test'); git('config', 'user.email', 'test@example.invalid');
    git('config', 'commit.gpgSign', 'false'); git('commit', '--allow-empty', '-m', 'initial');
    fs.appendFileSync(path.join(root, '.git', 'info', 'exclude'), '\n/fake-*\n/.agents/\n');
    const pellet = cli('add', 'Runtime compatibility regression');
    cli('skill', 'install', '--scope', 'repo', '--agent', 'codex', '--yes');
    fs.writeFileSync(path.join(root, 'fake-mode'), mode);
    let origin = await start(root);
    const page = await browser.newPage({viewport: {width: 1280, height: 1000}});
    const errors = []; page.on('pageerror', error => errors.push(error.message));
    const route = `/projects/${pellet.project}/workspaces/1`;
    await page.goto(origin + route);
    await page.locator('form[data-schedule] button[value=run_one]').click();
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
    const deadline = Date.now() + 20000;
    let status;
    while (Date.now() < deadline) {
      const response = await page.request.get(origin + `/projects/${pellet.project}/schedules/1`);
      if (response.status() === 200) {status = await response.json(); if (status.state === 'needs_attention') break;}
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    assert.equal(status?.state, 'needs_attention');
    await page.reload();
    if (mode === 'runtime_old') {
      assert.equal(cli('show', pellet.id).status, 'open', 'Old runtime claimed pellet');
      assert.match(await page.locator('body').innerText(), /0\.154\.0/);
      assert.equal(await page.locator('.run-facts').count(), 0);
      assert.equal(fs.existsSync(path.join(root, 'fake-events.jsonl')), false);
    } else {
      assert.equal(cli('show', pellet.id).status, 'in_progress');
      const check = async () => {
        const summary = await page.locator('.run-activity').innerText();
        assert.match(summary, /GPT-6 Astra requires a newer Codex version/);
        assert.match(summary, /Upgrade Codex and retry/);
        assert.match(summary, /\[redacted\]/);
        const html = await page.content();
        for (const secret of ['private-runtime-token', 'sk-private', 'private-error-details', 'private-rpc-data']) assert.ok(!html.includes(secret), secret);
        assert.equal(await page.evaluate(() => window.codexErrorInjected), undefined, 'Error HTML executed');
        assert.equal(await page.locator('script').filter({hasText: 'codexErrorInjected'}).count(), 0);
      };
      await check();
      const events = fs.readFileSync(path.join(root, 'fake-events.jsonl'), 'utf8');
      await stop(); origin = await start(root); await page.goto(origin + route); await check();
      assert.equal(fs.readFileSync(path.join(root, 'fake-events.jsonl'), 'utf8'), events, 'Restart resumed a run');
      if (mode === 'schedule_runtime_error' && process.env.PELLETS_BROWSER_SCREENSHOT) await page.screenshot({path: process.env.PELLETS_BROWSER_SCREENSHOT, fullPage: true});
      if (mode === 'schedule_runtime_error') {
        fs.writeFileSync(path.join(root, 'runtime-repair.txt'), 'keep this repair');
        git('add', 'runtime-repair.txt'); git('commit', '-m', 'repair runtime');
        const baseline = git('rev-parse', 'HEAD').toString().trim();
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
        assert.ok(prompt.includes(baseline)); assert.ok(prompt.includes('Commits landed since your previous attempt'));
        await page.reload(); assert.match(await page.locator('.run-facts').innerText(), /Completed/);
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
