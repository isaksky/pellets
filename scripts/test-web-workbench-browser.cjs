// Workbench v2 integration against an embedded production build and deterministic
// Codex protocol peer. Repositories and database are disposable; no network AI.
// NODE_PATH=/path/to/node_modules PLAYWRIGHT_CHANNEL=chrome node scripts/test-web-workbench-browser.cjs
// Use PLAYWRIGHT_BROWSER=webkit for Safari's browser engine.
// Set PELLETS_WORKBENCH_BROWSER_CASE=dialogs to run only the modal regressions.
// Set PELLETS_WORKBENCH_BROWSER_CASE=filters to run only the filter regressions.
// Set PELLETS_WORKBENCH_BROWSER_CASE=creation to run creation form sizing checks.
const assert = require("node:assert/strict");
const fs = require("node:fs"),
  os = require("node:os"),
  path = require("node:path");
const { execFileSync, spawn } = require("node:child_process");
const { chromium, webkit } = require("playwright");
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
async function moreActions(page) {
  const menu = page.locator("[data-inspector] details.record-actions");
  if (!(await menu.evaluate(x => x.open))) await menu.locator("summary").click();
}
async function chooseFilter(page, name, value) {
  const select = page.locator('.filters select[name="' + name + '"]');
  const id = await select.getAttribute("id");
  await page.locator("#" + id + "-trigger").click();
  await page.locator('.select-popover [role=option][data-value="' + value + '"]').click();
}
async function filterGeometry(page) {
  return page.evaluate(() => Object.fromEntries(["#search", "#filter-summary", ".filters"].map(selector => {
    const rect = document.querySelector(selector).getBoundingClientRect();
    return [selector, [rect.x, rect.y, rect.width, rect.height]];
  })));
}
async function checkCreationWidths(page) {
  for (const area of ["Queue", "Memories"]) {
    await page.locator(".area-tabs a").filter({hasText: area}).click();
    const disclosure = page.locator(".create-popover");
    for (const width of [1280, 1092, 800, 678, 601, 600, 390]) {
      await page.setViewportSize({width, height: 859});
      await disclosure.locator("summary").click();
      const form = disclosure.locator("form");
      const bounds = await form.evaluate(node => {
        const box = node.getBoundingClientRect(), main = node.closest("#main").getBoundingClientRect();
        return {left: box.left, right: box.right, mainLeft: main.left, mainRight: main.right};
      });
      assert.ok(bounds.left >= Math.max(0, bounds.mainLeft) - 1 && bounds.right <= Math.min(width, bounds.mainRight) + 1,
        area + " creation form is clipped at " + width + "px: " + JSON.stringify(bounds));
      const submit = form.locator('button[type="submit"]');
      await submit.scrollIntoViewIfNeeded();
      assert.equal(await submit.evaluate(node => {
        const box = node.getBoundingClientRect();
        return node.contains(document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2));
      }), true, area + " creation button is reachable at " + width + "px");
      await disclosure.locator("summary").click();
    }
  }
  await page.setViewportSize({width: 1280, height: 800});
  await page.locator(".area-tabs a").filter({hasText: "Queue"}).click();
  console.log("PASS creation forms fit their content pane and remain reachable at seven viewport widths");
}
function assertStableFilters(before, after) {
  for (const selector of Object.keys(before))
    before[selector].forEach((value, index) => assert.ok(Math.abs(value - after[selector][index]) <= 1, "Opening/changing Filters moved toolbar geometry: " + selector + " before=" + JSON.stringify(before[selector]) + " after=" + JSON.stringify(after[selector])));
}
async function checkFilters(page, project, changedPellet) {
  const trigger = page.locator("#filter-summary"), outer = page.locator("#queue-filters"), panel = page.locator(".filter-fields");
  await page.setViewportSize({width: 1280, height: 900});
  await trigger.click();
  await page.locator(".filter-fields:popover-open").waitFor();
  const naturalSize = await panel.evaluate(node => ({height: node.getBoundingClientRect().height, rows: getComputedStyle(node).gridTemplateRows, children: Array.from(node.children, child => (child.matches("pl-button") ? child.firstElementChild : child).getBoundingClientRect().height)}));
  assert.ok(naturalSize.height <= 420, "Filter panel must fit its compact contents instead of stretching to the viewport: " + JSON.stringify(naturalSize));
  const fieldGaps = await panel.evaluate(node => Array.from(node.children, child => child.matches("pl-button") ? child.firstElementChild : child).map(child => child.getBoundingClientRect()).slice(1).map((box, index) => box.top - (node.children[index].matches("pl-button") ? node.children[index].firstElementChild : node.children[index]).getBoundingClientRect().bottom));
  assert.ok(fieldGaps.every(gap => Math.abs(gap - 12) <= 1), "Filter fields must keep compact, even spacing: " + JSON.stringify(fieldGaps));
  await page.setViewportSize({width: 1280, height: 360});
  await until(() => panel.evaluate(node => node.scrollHeight > node.clientHeight + 2 && node.getBoundingClientRect().bottom <= innerHeight), "Short viewport must scroll compact filter content");
  const lastFilter = panel.getByRole("link", {name: "Clear filters", exact: true});
  // Wait for the popover to settle after viewport resizing before wheeling.
  await panel.hover();
  await page.mouse.wheel(0, 800);
  await until(async () => {
    if (!await panel.evaluate(node => node.scrollTop + node.clientHeight >= node.scrollHeight - 2)) return false;
    // WebKit's async scroller can update scrollTop before hit testing catches up.
    return lastFilter.evaluate(node => {
      const box = node.getBoundingClientRect(), panel = node.closest('.filter-fields').getBoundingClientRect();
      return box.top >= panel.top - 1 && box.bottom <= panel.bottom + 1 && node.contains(document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2));
    });
  }, "Filter panel did not make its last control reachable");
  const lastControl = await lastFilter.evaluate(node => {
    const box = node.getBoundingClientRect(), panel = node.closest(".filter-fields").getBoundingClientRect();
    return {inside: box.top >= panel.top - 1 && box.bottom <= panel.bottom + 1, hit: node.contains(document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2)), box: box.toJSON(), panel: panel.toJSON()};
  });
  assert.ok(lastControl.inside && lastControl.hit, "Last filter control must remain reachable in the scrollable popover: " + JSON.stringify(lastControl));
  await page.keyboard.press("Escape");
  await panel.waitFor({state: "hidden"});
  for (const width of [1280, 390]) {
    await page.setViewportSize({width, height: 600});
    for (const execution of [true, false]) {
      const toggle = page.locator("#toggle-execution");
      if ((await toggle.getAttribute("aria-expanded") === "true") !== execution) await toggle.click();
      const closed = await filterGeometry(page);
      assert.match(await trigger.innerText(), /Filters/);
      await trigger.click();
      await page.locator(".filter-fields:popover-open").waitFor();
      assert.equal(await panel.evaluate(node => node.matches(":popover-open")), true, "Filter panel must use the browser top layer");
      assertStableFilters(closed, await filterGeometry(page));
      const bounds = await panel.boundingBox();
      assert.ok(bounds.x >= 0 && bounds.y >= 0 && bounds.x + bounds.width <= width + 1 && bounds.y + bounds.height <= 601, "Filter panel exceeds the viewport");
      assert.equal(await panel.evaluate(node => {
        const box = node.getBoundingClientRect();
        return [
          [box.left + 10, box.top + 10], [box.right - 10, box.top + 10],
          [box.left + 10, box.bottom - 10], [box.right - 10, box.bottom - 10],
          [box.left + box.width / 2, box.top + box.height / 2],
        ].every(([x,y]) => node.contains(document.elementFromPoint(x,y)));
      }), true, "Execution or queue clipping intercepts the visible filter panel");
      if (width === 390 && execution) {
        const executionBox = await page.locator("#execution").boundingBox();
        assert.ok(bounds.y + bounds.height > executionBox.y, "Narrow case must exercise overlap with execution");
      }
      const statusID = await page.locator('.filters select[name="status"]').getAttribute("id");
      const statusTrigger = page.locator("#" + statusID + "-trigger");
      await statusTrigger.click();
      await page.locator(".select-popover").waitFor();
      await page.keyboard.press("Escape");
      await page.locator(".select-popover").waitFor({state: "detached"});
      assert.equal(await outer.evaluate(node => node.open), true, "Nested Escape closed the outer Filters panel");
      assert.equal(await statusTrigger.evaluate(node => node === document.activeElement), true);
      await chooseFilter(page, "status", "in_progress");
      await until(() => new URL(page.url()).searchParams.get("status") === "in_progress", "Status did not apply immediately");
      assert.equal(await outer.evaluate(node => node.open), true, "Selecting status closed Filters");
      assert.equal(await panel.evaluate(node => node.matches(":popover-open")), true);
      assert.equal(await page.locator(".task-row").count(), 0, "Status selection did not filter real records");
      await chooseFilter(page, "status", "active");
      await until(async () => (await page.locator(".task-row").count()) === 4, "Active status did not restore the queue");
      await page.keyboard.press("Escape");
      await panel.waitFor({state: "hidden"});
      assert.equal(await outer.evaluate(node => node.open), false);
      assert.equal(await trigger.evaluate(node => node === document.activeElement), true, "Outer Escape did not restore trigger focus");
      assertStableFilters(closed, await filterGeometry(page));
      await trigger.click();
      await page.locator(".filter-fields:popover-open").waitFor();
      await page.mouse.click(2, 2);
      await panel.waitFor({state: "hidden"});
      assert.equal(await outer.evaluate(node => node.open), false, "Outside click left Filters open");
      await trigger.click();
      await page.locator(".filter-fields:popover-open").waitFor();
      await statusTrigger.click();
      await page.locator(".select-popover").waitFor();
      await page.mouse.click(2, 2);
      await page.locator(".select-popover").waitFor({state: "detached"});
      await panel.waitFor({state: "hidden"});
      assert.equal(await outer.evaluate(node => node.open), false, "Outside both menus closed only the nested selector");
    }
  }
  await page.setViewportSize({width: 1280, height: 800});
  if (await page.locator("#toggle-execution").getAttribute("aria-expanded") === "false") await page.locator("#toggle-execution").click();
  await trigger.click();
  const exact = page.locator('.filters input[name="external_id"]');
  await exact.fill("Unfinished exact filter");
  await exact.press("ArrowLeft");
  const caret = await exact.evaluate(node => node.selectionStart);
  await until(() => new URL(page.url()).searchParams.get("external_id") === "Unfinished exact filter", "Exact filter did not apply");
  cli("edit", changedPellet, "--title", "Updated while filter input is focused");
  await page.evaluate(() => document.dispatchEvent(new CustomEvent("pellets-refresh")));
  await page.waitForTimeout(700);
  assert.equal(await exact.inputValue(), "Unfinished exact filter");
  assert.equal(await exact.evaluate(node => node.selectionStart), caret);
  assert.equal(await exact.evaluate(node => node === document.activeElement), true, "Live invalidation lost filter input focus");
  // Workspace browsing uses its associated execution panel. Shared Queue keeps
  // its explicit execution selection; clearing filters must preserve both routes.
  const groupValue = await page.locator('.filters select[name="group"]').evaluate(select => Array.from(select.options).find(option => option.label === "web-ui").value);
  for (const context of [{workspace: "2", execution: "2"}, {execution: "1"}]) {
    const retained = {...context, sort: "title", direction: "desc"};
    const filteredContext = new URLSearchParams({...retained, q: "unmatched", status: "closed", group: groupValue});
    const filteredPage = await page.goto(origin + "/projects/" + project + "/tasks?" + filteredContext);
    assert.equal(filteredPage.status(), 200, "Workspace filter context must be a valid production route");
    const executionMode = page.locator('#execution select[name="mode"]');
    const executionModeID = await executionMode.getAttribute("id");
    await page.locator("#" + executionModeID + "-trigger").click();
    await page.locator('.select-popover [role=option][data-value="drain"]').click();
    assert.equal(await executionMode.inputValue(), "drain");
    await trigger.click();
    const clear = panel.getByRole("link", {name: "Clear filters", exact: true});
    const clearURL = new URL(await clear.getAttribute("href"), origin);
    for (const [key,value] of Object.entries(retained)) assert.equal(clearURL.searchParams.get(key), value, "Clear filters loses " + key);
    for (const key of ["q","status","group","external_id"]) assert.equal(clearURL.searchParams.has(key), false);
    assert.equal(clearURL.searchParams.get("workspace"), context.workspace || null);
    await clear.click();
    await until(() => !new URL(page.url()).searchParams.has("q"), "Clear filters did not navigate to the preserved context");
    assert.equal(new URL(page.url()).searchParams.get("workspace"), context.workspace || null);
    if (context.workspace) assert.equal(await page.locator('.filters input[name="workspace"]').inputValue(), context.workspace);
    else assert.equal(await page.locator('.filters input[name="workspace"]').count(), 0);
    assert.equal(await page.locator('.filters input[name="execution"]').inputValue(), context.execution);
    assert.equal(await page.locator("#execution .run-workspace").getAttribute("data-workspace-id"), context.execution);
    assert.equal(await executionMode.inputValue(), "drain", "Clear filters erased unfinished execution selection");
    assert.equal(await page.locator('.filters select[name="sort"]').inputValue(), "title");
    assert.equal(await page.locator('.filters select[name="direction"]').inputValue(), "desc");
    assert.equal(await page.locator('.filters select[name="status"]').inputValue(), "active");
  }
  await page.goto(origin);
  await page.locator(".task-title").first().waitFor();
  console.log("PASS stable top-layer Filters, nested Escape, immediate selection, outside dismissal, live input focus and preserved workspace/execution on Clear");
}
async function checkDialogFooter(page, saveLabel) {
  const footer = page.locator("[data-inspector] .dialog-footer");
  const save = footer.getByRole("button", {name: saveLabel, exact: true});
  assert.equal(await save.evaluate(button => !!button.form && !button.closest("form") && button.form.matches("form.dirty-track")), true, "Footer Save must submit the record's separate versioned form");
  const metadata = page.locator("[data-inspector] details.metadata");
  if (await metadata.count()) await metadata.locator("summary").click();
  const scroller = page.locator("[data-inspector] .inspector-scroll");
  const before = await footer.boundingBox();
  await scroller.evaluate(node => {node.scrollTop = node.scrollHeight;});
  assert.ok(await scroller.evaluate(node => node.scrollTop) > 0, "Long record should exercise body scrolling");
  const after = await footer.boundingBox(), cancel = await footer.getByRole("button", {name: "Cancel", exact: true}).boundingBox(), primary = await save.boundingBox(), more = await footer.locator("summary").boundingBox();
  assert.ok(Math.abs(after.y - before.y) <= 1, "Scrolling the body moved the footer");
  assert.ok(after.y >= 0 && after.y + after.height <= page.viewportSize().height, "Footer is outside the viewport");
  assert.ok(more.x < cancel.x && cancel.x + cancel.width <= primary.x, "Footer must place More actions left, then Cancel and Save right");
  assert.equal(await footer.evaluate(node => {
    const box = node.getBoundingClientRect(), inspector = node.closest("[data-inspector]").getBoundingClientRect();
    return Math.abs(box.bottom - inspector.bottom) <= 2;
  }), true, "Footer must stay at the bottom of the record dialog");
  await scroller.evaluate(node => {node.scrollTop = 0;});
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
      "--description",
      Array(50).fill("Long record content exercises the scrollable body while actions remain available.").join("\n"),
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
    Array(50).fill("Runtime ownership survives stopped processes.").join("\n"),
    "--created-by",
    "agent",
  );
  const wt = path.join(temporary, "interface");
  git("worktree", "add", "-qb", "interface", wt);
  execFileSync(binary, ["project", "show"], { cwd: wt, env });
  cli("skill", "install", "--scope", "repo", "--agent", "codex", "--yes");
  fs.writeFileSync(path.join(repo, "fake-mode"), "schedule_activity_gate");
  await start();
  const engine = process.env.PLAYWRIGHT_BROWSER === "webkit" ? webkit : chromium;
  browser = await engine.launch({
    headless: true,
    ...(engine === chromium && process.env.PLAYWRIGHT_CHANNEL
      ? { channel: process.env.PLAYWRIGHT_CHANNEL }
      : {}),
    ...(engine === webkit && process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE
      ? { executablePath: process.env.PLAYWRIGHT_WEBKIT_EXECUTABLE }
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
  if (!process.env.PELLETS_WORKBENCH_BROWSER_CASE || process.env.PELLETS_WORKBENCH_BROWSER_CASE === "creation") {
    await checkCreationWidths(page);
    assert.deepEqual(errors, []);
    if (process.env.PELLETS_WORKBENCH_BROWSER_CASE === "creation") return;
  }
  if (process.env.PELLETS_WORKBENCH_BROWSER_CASE !== "dialogs") {
    await checkFilters(page, a.project, d.id);
    assert.deepEqual(errors, []);
    if (process.env.PELLETS_WORKBENCH_BROWSER_CASE === "filters") return;
  }
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
      await page.setViewportSize({width, height: 600});
      await record.open();
      await page.locator("#record-dialog[open]").waitFor();
      assert.equal(await field.isVisible(), true, record.name + " editor is visible on opening");
      assert.equal(await field.isEditable(), true);
      assert.equal(await page.locator("[data-edit-record]").count(), 0);
      await checkDialogFooter(page, record.save);
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
    await field.fill("Unfinished Cancel draft for " + record.name);
    page.once("dialog", d => d.dismiss());
    await page.locator("[data-inspector] .dialog-footer").getByRole("button", {name: "Cancel", exact: true}).click();
    assert.equal(await page.locator("#record-dialog").evaluate(x => x.open), true, "Dismissed Cancel lost the dialog");
    assert.equal(await field.inputValue(), "Unfinished Cancel draft for " + record.name);
    page.once("dialog", d => d.accept());
    await page.locator("[data-inspector] .dialog-footer").getByRole("button", {name: "Cancel", exact: true}).click();
    await page.locator("#record-dialog").waitFor({state: "hidden"});
    await record.open();
    assert.equal(await field.inputValue(), savedText, "Cancel saved an unfinished draft");
    if (record.name === "memory") {
      await moreActions(page);
      const approval = page.waitForResponse(r => r.request().method() === "POST" && r.url().includes("/approve"));
      await page.getByRole("button", {name: "Approve current text", exact: true}).click();
      assert.equal((await approval).status(), 200, "More actions approval must receive its actual mutation receipt");
      await page.getByText("Human approved", {exact: true}).waitFor();
    }
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
  console.log("PASS direct editing, fixed action footer, Save/live editability, Cancel/backdrop guards, approval receipt, insertion backdrop, desktop and narrow");
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
  assert.equal(await title.inputValue(), "Unfinished human edit");
  page.once("dialog", dialog => {
    assert.match(dialog.message(), /Discard unsaved inspector changes/);
    dialog.accept();
  });
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
  await moreActions(page);
  const recordMenu = page.locator("[data-inspector] details.record-actions");
  const removeCheckpoint = recordMenu.getByRole("button", {name: "Remove checkpoint", exact: true});
  await removeCheckpoint.focus();
  await page.keyboard.press("Escape");
  assert.equal(await recordMenu.evaluate(x => x.open), false, "Escape must close More actions");
  assert.equal(await page.locator("#record-dialog").evaluate(x => x.open), true, "Menu Escape must keep the record open");
  assert.equal(await recordMenu.locator("summary").evaluate(x => x === document.activeElement), true);
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
  // The record menu uses the same reversible operations as inline removal.
  await page.locator("#task-" + inserted + " .checkpoint-open").click();
  await moreActions(page);
  const removedReceipt = page.waitForResponse(r => r.request().method() === "POST" && new URL(r.url()).pathname.endsWith("/remove"));
  await removeCheckpoint.click();
  assert.equal((await removedReceipt).status(), 200);
  await page.locator("[data-inspector] [data-checkpoint-restore]").waitFor({state: "attached"});
  assert.equal(cli("show", inserted).status, "maybe_later");
  assert.match(await page.locator(".checkpoint-management").innerText(), /No review was completed/);
  await moreActions(page);
  const restoredReceipt = page.waitForResponse(r => r.request().method() === "POST" && new URL(r.url()).pathname.endsWith("/restore"));
  await recordMenu.getByRole("button", {name: "Undo removal · Restore checkpoint", exact: true}).click();
  assert.equal((await restoredReceipt).status(), 200);
  await page.locator("[data-inspector] [data-checkpoint-remove]").waitFor({state: "attached"});
  assert.equal(cli("show", inserted).status, "open");
  await closeDialog(page);
  // The explicit approval made through More actions remains on reopening.
  await page.locator(".area-tabs a").nth(1).click();
  await page.locator(".memory-card>a").click();
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
      const approvalResponse = page.waitForResponse(r => r.request().method() === "POST" && new URL(r.url()).pathname.endsWith("/interaction"));
      await page
        .getByRole("button", { name: "Approve once", exact: true })
        .click();
      const approved = await approvalResponse;
      assert.equal(approved.status(), 202, "Approval must receive an accepted receipt: " + (approved.status() === 202 ? "" : await approved.text()));
    } else {
      // A run is already "running" while thread/turn startup still changes its
      // revision. Wait for the implementation receipt before steering that turn.
      await until(() => page.locator(".run-facts").evaluate(node =>
        Array.from(node.querySelectorAll("div")).some(row =>
          row.querySelector("dt")?.textContent === "Phase" &&
          row.querySelector("dd")?.textContent.toLowerCase() === "implementation")),
      "Follow-up requires the active implementation receipt");
      await page
        .locator(".run-follow-up textarea")
        .fill("Keep the change focused.");
      const followUpResponse = page.waitForResponse(r => r.request().method() === "POST" && new URL(r.url()).pathname.endsWith("/follow-up"));
      await page
        .getByRole("button", { name: "Send follow-up", exact: true })
        .click();
      const followed = await followUpResponse;
      assert.equal(followed.status(), 202, "Follow-up must receive an accepted receipt: " + (followed.status() === 202 ? "" : await followed.text()));
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
