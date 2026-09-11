# Managed Codex runtime

Pellets manages **Codex CLI 0.154.0**, including its app-server and companion
executables, for every Pellets release target: macOS ARM64, macOS AMD64, and
Windows AMD64. Homebrew, npm/Node, a Codex entry on PATH, and the Codex desktop
app are unnecessary. Ordinary queue, memory, and browser inspection never fetch
or launch Codex. Git and the existing agent-facing `pl`/skill prerequisites are
unchanged.

## Distribution decision

We investigated the official CLI installer, npm platform packages, individual
GitHub release binaries, and the full CLI and app-server release packages.
[Official CLI setup](https://learn.chatgpt.com/docs/codex/cli) supports standalone
installation; the [app-server contract](https://learn.chatgpt.com/docs/app-server)
describes the stdio protocol and generated schemas. The
[0.154.0 release](https://github.com/openai/codex/releases/tag/rust-v0.154.0)
explicitly adds GPT-6 Astra support.

Pellets downloads `codex-package-TARGET.tar.gz` from that exact official GitHub
release. The package includes `bin/codex`, the code-mode host, and target-specific
resources and helpers; their layout is retained. Individual binaries would omit
those companions. The standalone `codex-app-server-package` was inspected, but
its entrypoint does not implement `generate-json-schema`; the full CLI package
supports both our schema probe and `app-server --listen stdio://`. npm would add
package-manager/bootstrap requirements. Running the general installer would
modify a separate user installation and follow its update policy instead of
Pellets' reviewed version pin.

The native macOS ARM64 package was tested with a fresh `CODEX_HOME`, generated
schema checks, and an initialize/initialized stdio handshake, without account
reads or a model turn. Cross-build checks cover the other release targets;
native execution tests on Windows remain a CI responsibility.

## Setup and resolution

Start `pl server` as usual. On the first Run one, Drain, Watch with eligible work,
or explicit Resume, Pellets resolves and checks its runtime before claiming an
open pellet. This runtime-only check precedes the normal account/configuration
preflight. Authentication or workspace-policy failures can still leave an owned
pellet requiring explicit Resume, as before. An empty or unready Watch queue
never installs or probes Codex.

The runtime selection order is:

1. A one-run `RunOverrides.executable`, when supplied (empty means managed).
2. `PELLETS_CODEX_EXECUTABLE`, when present (empty means managed).
3. A saved workspace `executable`, when nonempty.
4. The managed version pinned by this Pellets release.

An executable override must be a Codex **CLI** accepting `--version`,
`app-server generate-json-schema`, and `app-server --listen stdio://`. A name
uses PATH only because it was explicitly configured; an absolute path bypasses
PATH. Arguments never pass through a shell. For example:

```sh
PELLETS_CODEX_EXECUTABLE=/path/to/new/codex pl server
# Ignore a legacy saved executable and use the managed default (POSIX shell):
PELLETS_CODEX_EXECUTABLE= pl server
```

In PowerShell, set `$env:PELLETS_CODEX_EXECUTABLE` to the full CLI executable path
before launching `pl server`. PowerShell 7.5+ also supports an explicitly empty
environment value. Removing this environment variable restores saved settings
or the managed default. Internal settings clients can clear the saved
`executable` to select managed execution on every operating system.

Both managed and override binaries must report a stable version at least
0.154.0 and pass the required generated-schema checks. This is a conservative
Pellets compatibility floor, not a claim that OpenAI guarantees every model on
that version. Model enumeration validates effort choices; it does **not** prove
client-version compatibility. Future server-side restrictions can still reject
a turn, and that error is retained for diagnosis.

Codex continues to use the inherited OS user and `CODEX_HOME`, configuration,
credentials, tools, and conversation history. Pellets does not copy credentials
or substitute a fresh home. If login is missing, the preflight message gives the
resolved CLI path: run that executable with `login` under the same `CODEX_HOME`,
then explicitly Resume. Existing local Codex authentication ordinarily works
with the managed runtime. Workspace sandbox, approval review, process custody,
and worktree ownership checks remain in effect.

## Cache, offline operation, and failures

The default cache is `<os.UserCacheDir>/pellets/codex`: normally
`~/Library/Caches/pellets/codex` on macOS and
`%LocalAppData%\pellets\codex` on Windows. Set `PELLETS_CODEX_CACHE` to an absolute
machine-local directory to relocate it. Each version/target has a downloaded
archive and an extracted directory. The entrypoint is
`VERSION/TARGET/bin/codex` (`codex.exe` on Windows).

Downloads use HTTPS, a five-minute timeout, bounded sizes, and SHA-256 values
embedded in Pellets from the official release's asset metadata. Pellets checks
the hash **before extraction**, rejects traversal/link entries, and publishes
only complete installations by rename. Concurrent installations converge on the
same verified package. Every reuse rechecks the archive and installed files,
including companions; it never falls back silently to another local binary.
This costs local disk reads proportional to the package size. Hashes establish
integrity relative to the reviewed release, not an independent signing authority.

A complete verified cache needs no network for runtime resolution. Set
`PELLETS_CODEX_OFFLINE=1` to prohibit installation downloads. This flag does not
make model service calls offline or change Codex's network policy. A cold offline
cache fails before claim. A previously downloaded archive can reconstruct a
missing extracted directory offline. To provision another offline machine,
copy the matching version/target archive into its cache; both operating system
and architecture must match. Do not copy credentials as part of runtime setup.

Download, permissions, disk, unsupported-platform, checksum, and package-layout
failures include the selected version/target and cache location in preflight
feedback. A damaged cache is refused. Stop active runs before removing the
affected version directory, then retry online, or choose a compatible override.
Cancellation removes temporary download/install files. An abrupt process death
can leave `.download-*` or `.install-*` staging files; they are never selected as
a runtime and can be removed while the server is stopped.

## Updates, saved settings, and Resume

Updating Pellets updates the reviewed managed pin when a new version is bundled
in its manifest. There is no floating `latest` lookup or automatic runtime
upgrade during a run. Old cached versions remain on disk until the operator
removes them. Arbitrary versions and download URLs are not accepted by the
managed resolver; use an explicit CLI override for an advanced deployment.

Maintainers update `ManagedVersion`, the minimum-version policy, and every
supported target's SHA-256 together in `internal/codex/managed_runtime.go` and
`runtime.go`, verify the release package layout/companions and generated schema,
and run installer, scheduler, native smoke, browser, and cross-build checks.

Portable default settings retain an empty `codex.executable`; Pellets no longer
writes a resolved absolute cache/PATH path back into that configuration. Each
new attempt separately records `settings.runtime` (executable, version, managed
status) as machine-local provenance. Model, effort, and sandbox evidence remain
captured as before.

Existing saved nonempty executables are preserved as explicit overrides: older
storage cannot reliably distinguish intentional paths from inferred defaults.
They receive the same compatibility check and may need clearing or overriding.
Old run snapshots remain unchanged, including their historical absolute paths;
missing runtime provenance is valid legacy evidence. Resume prepares the
**currently configured** runtime and records it in the new attempt, while
retaining the prior attempt, thread history, worktree/branch, ownership, filters,
and finalization evidence. It never uses an old run's path as an implicit
executable override and never resumes automatically after an update.

## Errors and verification

A failed exact turn retains its documented `turn.error.message` in the bounded
durable run summary; RPC failures retain only their message, even when failure
occurs before a turn ID is assigned. Error codes and uncertain-call recovery
semantics remain separate. ANSI/control characters are removed, common
credential forms are redacted, and text is UTF-8 bounded to 1024 bytes. Raw RPC
data, additional error details, stderr, and transcripts are excluded. Sanitizing
cannot identify every conceivable secret embedded in arbitrary prose.

The workspace's Latest activity displays that durable summary with HTML
escaping after reload and server restart. Failed terminal outcomes use warning
styling. Preflight errors before run capture appear in the foreground schedule;
no fictitious run is created to store them.

```sh
go test ./...
./scripts/verify-cross-builds.sh
# Native package smoke, isolated CODEX_HOME, no account or model calls:
PELLETS_CODEX_TEST_PACKAGE=/absolute/path/to/codex-package-TARGET.tar.gz \
  go test ./internal/codex -run '^TestManagedRuntimeNativePackage$' -v
# Playwright + disposable compiled server and deterministic Codex peer:
NODE_PATH=/path/to/node_modules PLAYWRIGHT_CHANNEL=chrome \
  node scripts/test-web-runtime-browser.cjs
```
