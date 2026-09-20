import { activityOutcome, activitySummary } from "./activity-summary.js";

// Presentation only: input order is first observation, never revision sequence.
// Unknown/action-required statuses stay independent even on a tool event.
function compatibleKind(item) {
  return ["command", "file_read", "file_change"].includes(item.kind) &&
    ["running", "completed", "failed", "declined", "reported"].includes(item.status)
    ? item.kind : "";
}

export function activityRows(items, previous = []) {
  const rows = [], retained = new Map(), used = new Set();
  for (const row of previous) {
    if (row.kind) for (const item of row.items) retained.set(item.id, row);
  }
  for (const item of items) {
    const kind = compatibleKind(item), last = rows.at(-1);
    if (kind && last?.kind === kind) last.items.push(item);
    else rows.push({id: item.id, kind, items: [item]});
  }
  for (const row of rows) {
    const prior = row.items.map(item => retained.get(item.id))
      .find(prior => prior?.kind === row.kind && !used.has(prior.id));
    // Retention may remove the first member. Keep the surviving disclosure's
    // identity (even with one member), but never reuse an unrelated group.
    if (prior) row.id = prior.id;
    else if (row.items.length === 1) row.kind = "";
    // An evicted starting item can complete later in a different group. Its
    // former group may still retain that ID as its disclosure identity.
    if (row.kind) while (used.has(row.id)) row.id += "-next";
    used.add(row.id);
  }
  return rows;
}

export function activityGroupSummary(row, root = "", context = row.items) {
  const items = row.items, count = items.length;
  const noun = {command: "command", file_read: "file read", file_change: "file change"}[row.kind];
  let title = count + " " + noun + (count === 1 ? "" : "s");
  if (row.kind !== "command") {
    const paths = new Set(items.map(item => item.path).filter(Boolean)).size;
    title += " · " + paths + " reported path" + (paths === 1 ? "" : "s");
  }
  const totals = new Map();
  for (const item of items) {
    const status = activityOutcome(item);
    totals.set(status, (totals.get(status) || 0) + 1);
  }
  const outcome = [];
  for (const [status, label] of [["running", "in progress"], ["failed", "failed"],
    ["declined", "declined"], ["reported", "reported"]]) {
    if (totals.has(status)) outcome.push(totals.get(status) + " " + label);
  }
  const active = items.filter(item => item.status === "running").at(-1),
    latest = active || items.at(-1),
    summary = activitySummary(latest, root, context),
    detail = summary.operation || summary.title,
    failed = items.filter(item => ["failed", "declined"].includes(activityOutcome(item))).at(-1),
    failure = failed && activitySummary(failed, root, context);
  return {title, outcome: outcome.join(" · ") || "Completed",
    active: totals.has("running"), failed: totals.has("failed") || totals.has("declined"),
    operation: detail ? (active ? "Current: " : "Latest: ") + detail : "",
    failure: failure ? "Last failure: " + (failure.operation || failure.title) + " · " +
      failure.outcome + (failure.error ? " · " + failure.error : "") + " · " + failure.impact : ""};
}
