// Fixture CLI compatibility without Go, Git, Playwright, or a running server.
// node --test scripts/test-web-description-fixture.cjs
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {test} = require('node:test');
const vm = require('node:vm');

const filename = path.join(__dirname, 'test-web-description-browser.cjs');
const script = fs.readFileSync(filename, 'utf8');
for (const historical of [true, false]) {
  test(historical ? 'historical executable without --json' : 'current executable with human output by default', async () => {
    const temporary = path.join(os.tmpdir(), 'description-fixture-contract');
    const binary = historical ? path.join(temporary, 'old-pl') : path.join(temporary, 'pl');
    const fixture = path.join(temporary, 'markdown');
    const records = [], builds = [], errors = [];
    const fixtureReady = new Error('Stop before starting the browser server');
    const childProcess = {
      execFileSync(command, args, options) {
        if (command === 'go') { builds.push([...args]); return ''; }
        assert.equal(options.cwd, fixture);
        if (command === 'git') { assert.deepEqual([...args], ['init', '-q']); return ''; }
        assert.equal(command, binary);
        assert.equal(options.encoding, 'utf8');
        const flags = [];
        while (args[0]?.startsWith('--')) flags.push(args.shift());
        for (const flag of flags) {
          if (flag !== '--pretty' && !(flag === '--json' && !historical)) throw Error('Unknown flag: ' + flag);
        }
        let data;
        if (args[0] === 'add') {
          data = {id: `markdown-${records.length + 1}`, title: args[1], description: args[3]};
          assert.equal(args[2], '--description');
          records.push(data);
        } else {
          assert.equal(args[0], 'show');
          data = records.find(record => record.id === args[1]);
          assert.ok(data);
        }
        // Emulate the output contracts, not a specific choice of compatible flag.
        if (!historical && flags.length === 0) return `${data.id}: ${data.title}\n`;
        return JSON.stringify({schema_version: 1, data}, null, flags.includes('--pretty') ? 2 : 0);
      },
      spawn(command, args, options) {
        assert.equal(command, binary);
        assert.deepEqual([...args], ['server', '--port', '0', '--no-open']);
        assert.equal(options.cwd, fixture);
        throw fixtureReady;
      },
    };
    const context = vm.createContext({
      __dirname,
      process: {env: historical ? {PELLETS_DESCRIPTION_BASELINE: binary} : {}},
      console: {error: error => errors.push(error)},
      require(name) {
        if (name === 'node:child_process') return childProcess;
        if (name === 'node:fs') return {mkdtempSync: () => temporary, mkdirSync: directory => assert.equal(directory, fixture)};
        if (name === 'playwright') return {};
        return require(name);
      },
    });
    await vm.runInContext(script, context, {filename});
    assert.deepEqual(errors, [fixtureReady], 'Fixture setup must reach server startup');
    assert.deepEqual(builds, historical ? [] : [['build', '-o', binary, './cmd/pl']]);
    assert.deepEqual(records.map(record => record.title), ['Markdown fixture', 'Plain fixture']);
    const retrieved = vm.runInContext('cli("show", "markdown-1")', context);
    assert.equal(retrieved.id, records[0].id);
    assert.equal(retrieved.description, records[0].description);
  });
}
