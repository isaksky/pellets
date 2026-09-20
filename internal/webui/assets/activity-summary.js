// Presentation of complete, sanitized projection fields only. Never correlate
// separate commands or parse prose/output to infer purpose, retry or recovery.
const compact = value => (value || "").replace(/\s+/g, " ").trim();
export function activityPreview(value, limit = 180) {
  const text = compact(value);
  return text.length > limit ? text.slice(0, limit - 1) + "…" : text;
}

export function activityPath(path, root = "") {
  // Compare lexical path segments only. Dot segments, redacted roots and paths
  // outside this workspace cannot establish a project-relative location.
  const windows = /^[A-Za-z]:[\\/]|^\\\\/.test(root);
  const normalize = value => windows ? value.replace(/\\/g, "/") : value;
  const full = normalize(path), base = normalize(root).replace(/\/+$/, "");
  if (!/^(?:\/|[A-Za-z]:\/)/.test(base) || /\[(?:redacted|details truncated)/.test(full + base) ||
      full.split("/").some(part => part === ".." || part === ".")) return path;
  if (full.startsWith(base + "/")) return full.slice(base.length + 1);
  return path;
}

function pathPreview(path, root, items) {
  let text = activityPath(path, root);
  const absolute = text === path && /^(?:\/|[A-Za-z]:[\\/]|\\\\)/.test(path);
  const parts = text.split(/[\\/]/), basename = parts.at(-1);
  // For long duplicate basenames, retain the shortest distinguishing suffix.
  // The prefix marker explicitly indicates omitted directories (not a root).
  const peers = items.map(item => item.path).filter(other => other && other !== path &&
    other.split(/[\\/]/).at(-1) === basename);
  if (text.length > 100 && peers.length) {
    let keep = 2;
    for (const peer of peers) {
      const other = peer.split(/[\\/]/);
      let shared = 1;
      while (shared < parts.length && shared < other.length &&
        parts.at(-shared - 1) === other.at(-shared - 1)) shared++;
      keep = Math.max(keep, shared + 1);
    }
    if (keep < parts.length) text = "…/" + parts.slice(-keep).join("/");
  }
  // Keep both directory context and the filename; full safe path is in details.
  text = text.length > 150 ? text.slice(0, 70) + "…" + text.slice(-79) : text;
  return (absolute ? "Absolute path: " : "") + text;
}

export function activityOutcome(item) {
  return item.exit_code != null && item.exit_code !== 0 ? "failed" : item.status;
}

export function activitySummary(item, root = "", items = []) {
  const status = activityOutcome(item), failed = status === "failed" || status === "declined";
  const labels = {running: "In progress", completed: "Completed", failed: "Failed",
    declined: "Declined", awaiting_input: "Input requested", resolved: "Resolved",
    reported: "Reported", retrying: "Retry reported", not_retrying: "No retry planned (reported)"};
  let title = item.title || item.kind, operation = "", outcome = labels[status] || status || "",
    impact = "", error = "";
  if (item.kind === "message") return {title: item.title === "Agent response" ? "Agent response" : "Agent update"};
  if (item.kind === "command") {
    title = "Command";
    operation = activityPreview(item.command) || "Command not reported";
  } else if (item.kind === "file_read" || item.kind === "file_change") {
    title = item.kind === "file_read" ? "Read file" : "File change";
    operation = item.path ? pathPreview(item.path, root, items) :
      activityPreview(item.command) || "Path not reported";
  } else if (item.kind === "diff") {
    title = "Turn changes";
    operation = item.diff ? (item.truncated ? "Reported diff excerpt" : "Reported diff") : "Diff not reported";
    outcome = "";
  } else if (item.kind === "turn") {
    title = "Turn";
    if (failed) impact = "Turn ended with an error · see current execution";
  } else if (item.kind === "error") {
    title = "Runtime error";
    if (status === "failed") impact = "Impact unknown";
    else if (status === "not_retrying") impact = "See current execution for next steps";
  }
  if (item.exit_code != null) outcome += " · exit " + item.exit_code;
  else if (failed && (item.kind === "command" || item.command)) outcome += " · exit not reported";
  if (failed && !impact) impact = "Impact unknown";
  // Explicit error text is evidence; arbitrary output is not an error message.
  if (item.error || (failed && item.kind === "turn" && item.text))
    error = activityPreview(item.error || item.text, 140);
  if (status === "awaiting_input") impact = "See the run’s input and approval controls";
  return {title, operation, outcome, impact, error, failed,
    action: status === "awaiting_input"};
}
