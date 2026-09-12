// Workbench v2 integration against an embedded production build and deterministic
// Codex protocol peer. Repositories and database are disposable; no network AI.
// NODE_PATH=/path/to/node_modules PLAYWRIGHT_CHANNEL=chrome node scripts/test-web-workbench-browser.cjs
// Set PELLETS_WORKBENCH_BROWSER_CASE=dialogs to run only the modal regressions.
const assert = require("node:assert/strict");
const fs = require("node:fs"),
  os = require("node:os"),
  path = require("node:path");
const { execFileSync, spawn } = require("node:child_process");
const { chromium } = require("playwright");
const repository = path.resolve(__dirname, ".."),
  temporary = fs.mkdtempSync(
    path.join(os.tmpdir(), "pellets-workbench-browser-"),
  );
const binary = path.join(temporary, "pl"),
  peer = path.join(temporary, "codex");
const env = {
  ...process.env,
  PATH: temporary + path.delimiter + process.env.PATH,
  PELLETS_CODEX_EXECUTABLE: peer,
  PELLETS_SUPERVISOR_PEER: "1",
};
let server, browser, origin;
const repo = path.join(temporary, "wb");
fs.mkdirSync(repo);
const git = (...args) =>
  execFileSync("git", args, { cwd: repo, encoding: "utf8" }).trim();
const cli = (...args) =>
  JSON.parse(execFileSync(binary, args, { cwd: repo, env, encoding: "utf8" }))
    .data;
const until = async (fn, msg) => {
  const end = Date.now() + 25000;
  while (Date.now() < end) {
    if (await fn()) return;
    await new Promise((r) => setTimeout(r, 70));
  }
  throw Error(msg);
};
async function closeDialog(page) {
  await page
    .getByRole("link", { name: "Close inspector", exact: true })
    .click();
  await page.locator("#record-dialog").waitFor({ state: "hidden" });
}
async function clickBackdrop(page, selector) {
  const box = await page.locator(selector).boundingBox();
  assert.ok(box && box.x > 1, "Dialog leaves a clickable backdrop");
  await page.mouse.click(box.x / 2, Math.max(1, box.y / 2));
}
async function dragToBackdrop(page, selector, field) {
  const inside = await field.boundingBox(), outside = await page.locator(selector).boundingBox();
  await page.mouse.move(inside.x + inside.width / 2, inside.y + inside.height / 2);
  await page.mouse.down();
  await page.mouse.move(outside.x / 2, Math.max(1, outside.y / 2));
  await page.mouse.up();
  assert.equal(await page.locator(selector).evaluate(x => x.open), true, "Dragging out of the dialog must not dismiss it");
}
async function start() {
  server = spawn(binary, ["server", "--port", "0", "--no-open"], {
    cwd: repo,
    env,
  });
  origin = await new Promise((resolve, reject) => {
    let out = "",
      err = "";
    server.stderr.on("data", (x) => (err += x));
    server.stdout.on("data", (x) => {
      out += x;
      if (out.includes("\n")) resolve(out.split("\n")[0].trim());
    });
    server.once("error", reject);
    server.once("exit", (code) => reject(Error("server " + code + " " + err)));
  });
}
async function stop() {
  if (server && server.exitCode === null) {
    const exited = new Promise((r) => server.once("exit", r));
    server.kill("SIGINT");
    await exited;
  }
}
(async () => {
  execFileSync("go", ["build", "-o", binary, "./cmd/pl"], { cwd: repository });
  execFileSync("go", ["test", "-c", "-o", peer, "./internal/app"], {
    cwd: repository,
  });
  git("init", "-q");
  git("config", "user.name", "Test");
  git("config", "user.email", "test@example.invalid");
  git("config", "commit.gpgSign", "false");
  git("commit", "--allow-empty", "-qm", "initial");
  fs.appendFileSync(
    path.join(repo, ".git", "info", "exclude"),
    "\n/fake-*\n/.agents/\n",
  );
  const a = cli(
      "add",
      "Preserve drafts during queue refresh",
      "--group",
      "web-ui",
    ),
    b = cli("add", "Keep execution ownership", "--group", "runtime"),
    c = cli("add", "Render exact scopes", "--group", "web-ui"),
    d = cli("add", "Unrelated work", "--group", "other");
  const cp = cli(
      "add",
      "Review interface work",
      "--review-targets",
      a.id + "," + c.id,
    ),
    cp2 = cli(
      "add",
      "Review overlapping runtime",
      "--review-targets",
      b.id + "," + c.id,
    );
  const memory = cli(
    "memory",
    "add",
    "--text",
    "Runtime ownership survives stopped processes.",
    "--created-by",
    "agent",
  );
  const wt = path.join(temporary, "interface");
  git("worktree", "add", "-qb", "interface", wt);
  execFileSync(binary, ["project", "show"], { cwd: wt, env });
  cli("skill", "install", "--scope", "repo", "--agent", "codex", "--yes");
  fs.writeFileSync(path.join(repo, "fake-mode"), "schedule_activity_gate");
  await start();
  browser = await chromium.launch({
    headless: true,
    ...(process.env.PLAYWRIGHT_CHANNEL
      ? { channel: process.env.PLAYWRIGHT_CHANNEL }
      : {}),
  });
  const page = await browser.newPage({
    viewport: { width: 1280, height: 800 },
  });
  page.setDefaultTimeout(12000);
  const errors = [],
    external = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("request", (r) => {
    if (!r.url().startsWith(origin)) external.push(r.url());
  });
  await page.goto(origin);
  await page.locator(".task-title").first().waitFor();
  await until(
    () =>
      page
        .locator(".scope-brackets g")
        .count()
        .then((n) => n === 2),
    "scope lanes",
  );
  assert.equal(await page.locator(".task-row").count(), 4);
  assert.equal(await page.locator(".checkpoint-row").count(), 2);
  const heights = await page
    .locator(".pellet-row")
    .evaluateAll((rows) => rows.map((x) => x.getBoundingClientRect().height));
  assert.ok(
    heights.every((n) => n === 37),
    "37px rows",
  );
  // Record dialogs open directly in edit mode. Only a click on the backdrop
  // closes them; unsaved edits use the same discard guard as explicit Close.
  for (const record of [
    {name: "pellet", open: async () => page.locator("#task-" + a.id + " .task-title").click(), field: "input[name=title]", save: "Save changes"},
    {name: "memory", open: async () => page.locator(".memory-card>a").click(), field: "textarea[name=text]", save: "Save text"},
  ]) {
    if (record.name === "memory") {
      await page.locator(".area-tabs a").nth(1).click();
      await page.locator(".memory-card>a").waitFor();
    }
    const field = page.locator("#inspector-host form.dirty-track " + record.field);
    for (const width of [1280, 390]) {
      await page.setViewportSize({width, height: 844});
      await record.open();
      await page.locator("#record-dialog[open]").waitFor();
      assert.equal(await field.isVisible(), true, record.name + " editor is visible on opening");
      assert.equal(await field.isEditable(), true);
      assert.equal(await page.locator("[data-edit-record]").count(), 0);
      await field.click();
      assert.equal(await page.locator("#record-dialog").evaluate(x => x.open), true, "Inside click closed " + record.name);
      await dragToBackdrop(page, "#record-dialog", field);
      await clickBackdrop(page, "#record-dialog");
      await page.locator("#record-dialog").waitFor({state: "hidden"});
      assert.equal(await page.locator("[data-inspector]").count(), 0, "Backdrop must close the routed inspector");
    }
    await page.setViewportSize({width: 1280, height: 800});
    await record.open();
    const savedText = "Saved directly editable " + record.name;
    await field.fill(savedText);
    const savedResponse = page.waitForResponse(r => r.request().method() === "POST" && r.url().includes("/edit"));
    await page.getByRole("button", {name: record.save, exact: true}).click();
    assert.equal((await savedResponse).status(), 200);
    await until(async () => !(await page.locator("[data-inspector].is-dirty").count()), "Saved record stayed dirty");
    assert.equal(await field.isEditable(), true, "Saving hid the editor");
    assert.equal(await field.inputValue(), savedText);
    await field.focus();
    const liveResponse = page.waitForResponse(r => r.request().headers()["pellets-target"] === "live");
    await page.evaluate(() => document.dispatchEvent(new CustomEvent("pellets-refresh")));
    await (await liveResponse).finished();
    await page.waitForTimeout(150);
    assert.equal(await field.isEditable(), true, "Live update hid the editor");
    assert.equal(await field.inputValue(), savedText);
    await field.fill("Unfinished backdrop draft for " + record.name);
    const draftURL = page.url();
    const dismissed = page.waitForEvent("dialog");
    page.once("dialog", d => d.dismiss());
    await clickBackdrop(page, "#record-dialog");
    assert.match((await dismissed).message(), /Discard unsaved inspector changes/);
    assert.equal(await page.locator("#record-dialog").evaluate(x => x.open), true);
    assert.equal(await field.inputValue(), "Unfinished backdrop draft for " + record.name);
    assert.equal(page.url(), draftURL);
    const accepted = page.waitForEvent("dialog");
    page.once("dialog", d => d.accept());
    await clickBackdrop(page, "#record-dialog");
    assert.match((await accepted).message(), /Discard unsaved inspector changes/);
    await page.locator("#record-dialog").waitFor({state: "hidden"});
    await record.open();
    assert.equal(await field.inputValue(), savedText, "Discarded backdrop draft leaked into reopened record");
    await closeDialog(page);
  }
  await page.locator(".area-tabs a").first().click();
  await page.locator("#task-" + b.id).waitFor();
  for (const [width, placement] of [[1280, "before"], [390, "after"]]) {
    await page.setViewportSize({width, height: 844});
    await page.locator("#task-" + b.id + " .row-menu summary").click();
    await page.locator("#task-" + b.id + " [data-insert-checkpoint=" + placement + "]").click();
    await page.locator("#insert-dialog[open]").waitFor();
    await page.locator("#insert-dialog input[name=title]").click();
    assert.equal(await page.locator("#insert-dialog").evaluate(x => x.open), true, "Inside insertion click closed the dialog");
    await clickBackdrop(page, "#insert-dialog");
    await page.locator("#insert-dialog").waitFor({state: "hidden"});
    assert.equal(await page.locator(".checkpoint-row").count(), 2, "Cancelling insertion created a checkpoint");
  }
  await page.setViewportSize({width: 1280, height: 800});
  assert.deepEqual(errors, []);
  console.log("PASS direct record editing, save/live editability, clean/dirty backdrop guards, insertion backdrop, desktop and narrow");
  if (process.env.PELLETS_WORKBENCH_BROWSER_CASE === "dialogs") return;
  // Exact/noncontiguous/overlapping bracket ticks derive from explicit records.
  await page.locator("#task-" + cp.id + " .checkpoint-open").focus();
  assert.deepEqual(
    await page
      .locator(".scope-highlight")
      .evaluateAll((rows) => rows.map((x) => x.dataset.rowId)),
    [a.id, c.id],
  );
  // Direct editing, conflict, live updates and native focus restoration.
  await page.locator("#task-" + a.id + " .task-title").click();
  await page.locator("#record-dialog[open]").waitFor();
  const title = page.locator("#edit-task-" + a.id + " input[name=title]");
  await title.fill("Unfinished human edit");
  await title.press("ArrowLeft");
  cli("add", "External queue update");
  await until(
    () =>
      page
        .locator(".task-row")
        .count()
        .then((n) => n === 5),
    "live queue",
  );
  assert.equal(await title.inputValue(), "Unfinished human edit");
  assert.equal(await title.evaluate((x) => x === document.activeElement), true);
  cli("edit", a.id, "--title", "Authoritative concurrent title");
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await page.locator(".conflict-state").waitFor();
  assert.match(
    await page.locator(".conflict-state").innerText(),
    /Unfinished human edit/,
  );
  assert.equal(cli("show", a.id).title, "Authoritative concurrent title");
  await closeDialog(page);
  // Keyboard menus, all palettes and independent panels keep drafts and geometry.
  await page.locator("#view-switcher summary").focus();
  await page.keyboard.press("ArrowDown");
  assert.equal(
    await page.locator("#view-switcher").evaluate((x) => x.open),
    true,
  );
  await page.keyboard.press("Escape");
  assert.equal(
    await page
      .locator("#view-switcher summary")
      .evaluate((x) => x === document.activeElement),
    true,
  );
  for (const theme of [
    "gruvbox-light",
    "gruvbox-dark",
    "light",
    "dark",
    "icy",
  ]) {
    await page.locator("#theme-select").selectOption(theme, { force: true });
    assert.equal(await page.locator("html").getAttribute("data-theme"), theme);
    const palette = {
      "gruvbox-light": "#f2e5bc",
      "gruvbox-dark": "#32302f",
      light: "#ffffff",
      dark: "#25292f",
      icy: "#e0edf7",
    };
    const actual = await page
      .locator("html")
      .evaluate((x) => getComputedStyle(x).getPropertyValue("--bg").trim());
    if (theme !== "icy")
      assert.equal(actual, palette[theme], "actual palette wins inherited CSS");
  }
  for (const width of [1280, 980, 760, 600, 390]) {
    await page.setViewportSize({ width, height: 844 });
    for (const nav of [true, false])
      for (const execution of [true, false]) {
        for (const [key, want] of [
          ["navigation", nav],
          ["execution", execution],
        ]) {
          const toggle = page.locator("#toggle-" + key);
          if (
            ((await toggle.getAttribute("aria-expanded")) === "true") !==
            want
          )
            await toggle.click();
        }
        assert.equal(
          await page
            .locator("body")
            .evaluate((x) => x.scrollWidth <= innerWidth),
          true,
          "overflow " + width,
        );
        const footer = await page.locator(".statusbar").boundingBox();
        assert.ok(footer.y + footer.height <= 845);
        const bounds = await page.locator("#main").boundingBox();
        assert.ok(bounds.width > 150 && bounds.height > 100);
        if (nav) {
          const rail = await page.locator("#project-drawer").boundingBox();
          assert.ok(
            rail.x >= 0 && rail.y >= 40 && rail.width > 100,
            "navigation is actually onscreen at " + width,
          );
          assert.equal(await page.locator(".area-tabs").isVisible(), true);
        }

        await page.screenshot({
          path: path.join(temporary, `layout-${width}-${nav}-${execution}.png`),
        });
      }
  }
  await page.setViewportSize({ width: 1280, height: 800 });
  for (const key of ["navigation", "execution"])
    if (
      (await page.locator("#toggle-" + key).getAttribute("aria-expanded")) ===
      "false"
    )
      await page.locator("#toggle-" + key).click();
  // Workspace assignments are persistent and view search cannot change selection.
  await page.locator("[aria-label=Workspaces] .workspace-link").nth(1).click();
  await page.locator("#assignment-popover summary").click();
  await page
    .locator(".assignment-form select[name=mode]")
    .selectOption("explicit", { force: true });
  await page
    .locator(".assignment-form input[name=groups]")
    .filter({ visible: true })
    .evaluateAll((xs) => xs.map((x) => x.value));
  const groupInput = page
    .locator(".assignment-form label")
    .filter({ hasText: "web-ui" })
    .locator("input[type=checkbox]");
  await groupInput.check();
  await page
    .locator(".assignment-form input[name=include_ungrouped]")
    .uncheck();
  await page
    .getByRole("button", { name: "Save assignments", exact: true })
    .click();
  await until(
    () =>
      page
        .locator(".task-row")
        .count()
        .then((n) => n === 2),
    "assigned queue",
  );
  assert.match(page.url(), /workspace=2/);
  await page.reload();
  assert.equal(await page.locator(".task-row").count(), 2);
  await page.locator(".area-tabs a").first().click();
  await until(
    () =>
      page
        .locator(".task-row")
        .count()
        .then((n) => n === 5),
    "shared queue",
  );
  // Atomic before insertion with editable explicit scope; reversible removal.
  await page.locator("#task-" + b.id + " .row-menu summary").click();
  await page
    .locator("#task-" + b.id + " [data-insert-checkpoint=before]")
    .click();
  await page.locator("#insert-dialog[open]").waitFor();
  await page
    .locator("#insert-dialog input[name=title]")
    .fill("Inserted before runtime");
  for (const checkbox of await page
    .locator("#insert-dialog .scope-options input")
    .all())
    await checkbox.uncheck();
  await page
    .locator('#insert-dialog .scope-options input[value="' + a.id + '"]')
    .check();
  await page.locator("#insert-dialog button[type=submit]").click();
  await page.locator("#insert-dialog").waitFor({ state: "hidden" });
  await page.locator("#record-dialog[open]").waitFor();
  const inserted = (await page.locator("#inspector-title").innerText()).trim();
  await closeDialog(page);
  const order = await page
    .locator(".queue-rows>[data-row-id]")
    .evaluateAll((xs) => xs.map((x) => x.dataset.rowId));
  assert.equal(order.indexOf(inserted) + 1, order.indexOf(b.id));
  await page
    .locator("#task-" + inserted + " [data-checkpoint-remove] button")
    .click();
  await page.locator("[data-checkpoint-undo]").waitFor();
  assert.equal(cli("show", inserted).status, "maybe_later");
  await page.locator("[data-checkpoint-undo] button").click();
  await until(() => cli("show", inserted).status === "open", "restore");
  // Agent memory approval remains an explicit human act.
  await page.locator(".area-tabs a").nth(1).click();
  await page.locator(".memory-card>a").click();
  await page
    .getByRole("button", { name: "Approve current text", exact: true })
    .click();
  await page.getByText("Human approved", { exact: true }).waitFor();
  await closeDialog(page);
  // Actual deterministic notifications: highlighted source/diff, stream keeps
  // steering text/caret, disclosure and older-scroll position during queue patches.
  await page.locator("[aria-label=Workspaces] .workspace-link").first().click();
  await page.getByRole("button", { name: "▷ Start next", exact: true }).click();
  await until(
    () =>
      page
        .locator(".activity-event")
        .count()
        .then((n) => n >= 5),
    "reported activity",
  );
  await page.locator(".run-follow-up textarea").waitFor();
  const steer = page.locator(".run-follow-up textarea");
  await steer.fill("Keep this unfinished instruction");
  await steer.press("ArrowLeft");
  const caret = await steer.evaluate((x) => x.selectionStart);
  await page.locator(".activity-event").first().locator("summary").click();
  await steer.focus();
  const firstEvent = await page
    .locator(".activity-event")
    .first()
    .getAttribute("id");
  cli("add", "Incremental update during execution");
  await until(
    () =>
      page
        .locator(".task-title")
        .filter({ hasText: "Incremental update" })
        .count()
        .then((n) => n === 1),
    "activity queue refresh",
  );
  assert.equal(await steer.inputValue(), "Keep this unfinished instruction");
  assert.equal(await steer.evaluate((x) => x.selectionStart), caret);
  assert.equal(
    await page.locator("#" + firstEvent).evaluate((x) => x.open),
    true,
  );
  assert.equal(await steer.evaluate((x) => x === document.activeElement), true);
  await page.locator("#theme-select").selectOption("dark", { force: true });
  assert.equal(await steer.inputValue(), "Keep this unfinished instruction");
  await page.locator("#toggle-execution").click();
  await page.locator("#toggle-execution").click();
  assert.equal(await steer.inputValue(), "Keep this unfinished instruction");
  const snapshot = await (
    await page.request.get(
      origin +
        (await page
          .locator(".activity-panel")
          .getAttribute("data-activity-url")),
    )
  ).json();
  assert.ok(snapshot.items.some((x) => x.kind === "file_change"));
  assert.ok(snapshot.items.some((x) => x.kind === "file_read" && x.output));
  assert.ok(!JSON.stringify(snapshot).includes("private-activity-value"));
  for (const item of await page.locator(".activity-event>summary").all())
    await item.click();
  assert.ok((await page.locator(".code-keyword").count()) > 0);
  assert.ok((await page.locator(".diff-added").count()) > 0);
  const activityPanel = page.locator(".activity-panel");
  const olderScroll = await activityPanel.evaluate((x) => {
    x.scrollTop = 40;
    return x.scrollTop;
  });
  assert.ok(olderScroll > 0, "expanded activity is scrollable");
  cli("add", "Preserve the activity reading position");
  await until(
    () =>
      page
        .locator(".task-title")
        .filter({ hasText: "Preserve the activity reading position" })
        .count()
        .then((x) => x === 1),
    "queue patch while reading older activity",
  );
  assert.equal(
    await activityPanel.evaluate((x) => x.scrollTop),
    olderScroll,
    "older activity scroll survives refresh",
  );
  await page.screenshot({ path: path.join(temporary, "activity-desktop.png") });
  const stoppedResponse = page.waitForResponse(
    (r) => r.request().method() === "POST" && r.url().endsWith("/stop-now"),
  );
  await page.getByRole("button", { name: "Stop now", exact: true }).click();
  assert.equal(
    (await stoppedResponse).status(),
    202,
    "Stop now is accepted, not a later peer timeout",
  );
  await page.getByRole("button", { name: "Resume", exact: true }).waitFor();
  assert.equal(
    (await page.locator(".run-state").textContent()).trim(),
    "Interrupted",
  );
  const owned = cli("show", b.id);
  assert.equal(owned.status, "in_progress");
  await stop();
  await start();
  await page.goto(origin + "/projects/wb/tasks?workspace=1");
  await page.getByRole("button", { name: "Resume", exact: true }).waitFor();
  assert.equal(cli("show", b.id).status, "in_progress");
  assert.match(
    await page.locator(".activity-availability").innerText(),
    /unavailable|foreground|restart|retained|Loading/i,
  );
  assert.equal(
    await page.locator(".run-follow-up").count(),
    0,
    "restart never resumes",
  );

  // Human questions, approval decisions, and successful steering use actual
  // request/revision receipts and continue only after explicit submission.
  cli("release", b.id);
  for (const mode of [
    "schedule_input_live",
    "schedule_approval_live",
    "schedule_followup_live",
  ]) {
    fs.writeFileSync(path.join(repo, "fake-mode"), mode);
    await page.goto(origin + "/projects/wb/tasks?workspace=1");
    await page
      .getByRole("button", { name: "▷ Start next", exact: true })
      .click();
    await page.locator(".current-pellet").waitFor();
    const target = new URL(
      await page.locator(".current-pellet").getAttribute("href"),
      origin,
    ).pathname
      .split("/")
      .pop();
    if (mode === "schedule_input_live") {
      await page
        .locator('.run-interaction textarea[name="answer.note"]')
        .fill("Preserve this answer draft");
      await page
        .locator("[aria-label=Workspaces] .workspace-link")
        .nth(1)
        .click();
      await page
        .locator("[aria-label=Workspaces] .workspace-link")
        .first()
        .click();
      await until(
        () =>
          page
            .locator('.run-interaction textarea[name="answer.note"]')
            .inputValue()
            .then((x) => x === "Preserve this answer draft"),
        "question draft survives workspace switch",
      );
      cli("add", "Background queue change during question");
      await page.setViewportSize({ width: 390, height: 844 });
      await page
        .locator('.run-interaction input[name="answer.choice"]')
        .fill("Focused");
      await page.screenshot({
        path: path.join(temporary, "question-narrow.png"),
      });
      assert.equal(
        await page
          .locator(".run-interaction form")
          .evaluate((f) => f.checkValidity()),
        true,
        "complete answer fields",
      );
      const answerResponse = page.waitForResponse(
        (r) =>
          r.request().method() === "POST" && r.url().endsWith("/interaction"),
      );
      await page
        .getByRole("button", { name: "Send exact answers", exact: true })
        .click();
      const answered = await answerResponse;
      assert.equal(answered.status(), 202, "exact answers accepted");
    } else if (mode === "schedule_approval_live") {
      await page
        .getByRole("button", { name: "Approve once", exact: true })
        .click();
    } else {
      await page
        .locator(".run-follow-up textarea")
        .fill("Keep the change focused.");
      await page
        .getByRole("button", { name: "Send follow-up", exact: true })
        .click();
    }
    try {
      await until(
        () => cli("show", target).status === "closed",
        mode + " completes only after intervention",
      );
    } catch (error) {
      console.log(await page.locator("#execution").innerText());
      console.log(await page.locator("#request-feedback").innerText());
      throw error;
    }
    await page.setViewportSize({ width: 1280, height: 800 });
  }
  assert.deepEqual(errors, []);
  assert.deepEqual(
    external.filter((x) => !/^http:\/\/127\.0\.0\.1:/.test(x)),
    [],
  );
  console.log(
    "PASS Workbench layout, themes, keyboard, conflicts, routing, checkpoints, activity, drafts, and restart",
  );
  console.log("Visual artifacts: " + temporary);
})()
  .catch((e) => {
    console.error(e);
    process.exitCode = 1;
  })
  .finally(async () => {
    if (browser) await browser.close();
    await stop();
  });
