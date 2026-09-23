import { positionMenus } from "./details-menu-layout.js";
import { highlightCode } from "./code-highlight.js";
import { renderMarkdown } from "./markdown.js";
import { activityRows, activityGroupSummary } from "./activity-groups.js";
import { activitySummary } from "./activity-summary.js";
import { rememberDescriptions, refreshDescriptions } from "./description.js";
import { refreshComponents } from "./components.js";
import { saveSetting } from "./settings.js";
import { syncQueueFilters } from "./filters.js";
import * as uiVersion from "./ui-version.js";
import "./planner.js";
import "./sidebar-resize.js";
// Presentation state is ephemeral and never supplies queue/execution authority.
const root = document.documentElement;
const drafts = new Map(),
  scrolls = new Map(),
  expansions = new Map(),
  activity = new Map();
let focusReceipt = null,
  restoredReviewFocus = null,
  pendingRestore = false,
  stream = null,
  streamURL = "",
  currentActivityURL = "";
const themes = ["gruvbox-light", "gruvbox-dark", "light", "dark", "icy"];
function applyTheme(choice) {
  if (choice === "system")
    choice = matchMedia("(prefers-color-scheme: dark)").matches
      ? "dark"
      : "light";
  if (!themes.includes(choice)) choice = "gruvbox-light";
  root.dataset.themeChoice = choice;
  root.dataset.theme = choice;
  refreshComponents();
  const select = document.getElementById("theme-select");
  if (select) {
    select.value = choice;
    refreshComponents();
  }
}
function panels() {
  for (const key of ["navigation", "execution"]) {
    const hidden = root.classList.contains(key + "-collapsed"),
      el = document.getElementById(
        key === "navigation" ? "project-drawer" : "right-panel",
      ),
      button = document.getElementById("toggle-" + key);
    if (el) {
      el.hidden = hidden;
      el.inert = hidden;
    }
    if (button) {
      button.setAttribute("aria-expanded", String(!hidden));
      button.setAttribute("aria-label", (hidden ? "Show " : "Hide ") + key);
    }
  }
  updateAttention();
  window.Planner?.sync();
}
function updateAttention() {
  const button = document.getElementById("toggle-execution"),
    article = document.querySelector(".run-workspace"),
    dot = button?.querySelector(".execution-dot");
  if (!button || !article) return;
  const state =
    article.querySelector(".run-state")?.textContent.trim() ||
    article.querySelector(".schedule-state")?.textContent.trim() ||
    "Idle";
  const attention = !!article.querySelector(
    ".run-interaction, .run-follow-up, .run-notice.warning, .schedule-state",
  );
  if (dot) dot.hidden = !attention;
  button.title =
    (root.classList.contains("execution-collapsed") ? "Show" : "Hide") +
    " execution · " +
    (document.querySelector("#run-dashboard-title")?.textContent.trim() || "") +
    " · " +
    state;
  button.setAttribute("aria-label", button.title);
  window.Planner?.status(state, attention);
}
function formKey(form) {
  const req = form.querySelector('[name="request_id"]')?.value || "";
  const workspace = form.closest(".run-workspace")?.dataset.workspaceId || "";
  return form.getAttribute("action") + "|" + workspace + "|" + req + "|" + (form.id || "");
}
function remember() {
  rememberDescriptions();
  document
    .querySelectorAll(
      "#execution form, #main .create-popover form, .assignment-form, .recipient-form",
    )
    .forEach((form) => {
      if (form.dataset.dirty !== "true") return;
      const fields = [];
      form
        .querySelectorAll(
          'input:not([type="hidden"]),textarea,select,input[type="hidden"][name="version"]',
        )
        .forEach((el) => {
          // Rendered task markers belong to the Markdown source, not the draft.
          if (el.type === "password" || el.closest(".markdown-body")) return;
          fields.push({ name: el.name, value: el.value, checked: el.checked });
        });
      drafts.set(formKey(form), fields);
      while (drafts.size > 64) drafts.delete(drafts.keys().next().value);
    });
  document
    .querySelectorAll("#main,.run-workspace,.activity-panel,.review-content")
    .forEach((el) => {
      scrolls.set(scrollKey(el), {
        top: el.scrollTop,
        left: el.scrollLeft,
        following: el.scrollHeight - el.clientHeight - el.scrollTop < 32,
      });
    });
  while (scrolls.size > 96) scrolls.delete(scrolls.keys().next().value);
  document
    .querySelectorAll("#execution details[id],#main details[id]")
    .forEach((el) => expansions.set(el.id, el.open));
  const active = document.activeElement;
  const trigger = active?.matches(".select-trigger") ? active : null;
  const field = trigger
    ? document.getElementById(trigger.dataset.selectId)
    : active;
  if (
    field?.matches("input,textarea,select") &&
    field.form &&
    field.closest("#execution, #main .create-popover, .assignment-form, .recipient-form")
  ) {
    focusReceipt = {
      key: formKey(field.form),
      name: field.name,
      value: field.type === "checkbox" ? field.value : null,
      trigger: !!trigger,
      start: field.selectionStart,
      end: field.selectionEnd,
      direction: field.selectionDirection,
    };
  } else if (active?.closest(".checkpoint-row") && active.id) {
    focusReceipt = {id: active.id};
  } else focusReceipt = null;
}
function scrollKey(el) {
  const shell = document.querySelector(".app-shell");
  return el.classList.contains("activity-panel")
    ? el.id
    : (el.id || el.className) +
        ":" +
        shell?.dataset.project +
        ":" +
        shell?.dataset.workspace;
}
function restore() {
  const forms = Array.from(
    document.querySelectorAll(
      "#execution form, #main .create-popover form, .assignment-form, .recipient-form",
    ),
  );
  for (const form of forms) {
    const fields = drafts.get(formKey(form));
    if (!fields) continue;
    form.dataset.dirty = "true";
    for (const el of form.querySelectorAll(
      'input:not([type="hidden"]),textarea,select,input[type="hidden"][name="version"]',
    )) {
      if (el.closest(".markdown-body")) continue;
      const field = fields.find(
        (x) =>
          x.name === el.name &&
          (el.type !== "checkbox" || x.value === el.value),
      );
      if (!field) continue;
      if (el.type === "checkbox") el.checked = field.checked;
      else el.value = field.value;
    }
  }
  document
    .querySelectorAll("#execution details[id],#main details[id]")
    .forEach((el) => {
      if (expansions.has(el.id)) el.open = expansions.get(el.id);
    });
  document.querySelectorAll("#main,.run-workspace,.activity-panel,.review-content").forEach(el => {
    const saved = scrolls.get(scrollKey(el));
    if (saved) { el.scrollTop = saved.top; el.scrollLeft = saved.left; }
  });
  // Recreate/synchronize custom controls after values and disclosures are
  // restored. Focus is independent of draft state: a clean control can be
  // keyboard-focused when an authoritative update arrives.
  refreshComponents();
  refreshDescriptions();
  if (focusReceipt?.id) {
    const target = document.getElementById(focusReceipt.id);
    if (target?.getClientRects().length) target.focus({preventScroll: true});
  } else if (focusReceipt) {
    const form = forms.find(
      (candidate) => formKey(candidate) === focusReceipt.key,
    );
    const field =
      form &&
      Array.from(form.elements).find((el) => el.name === focusReceipt.name && (focusReceipt.value === null || el.value === focusReceipt.value));
    const target =
      focusReceipt.trigger && field
        ? document.getElementById(field.id + "-trigger")
        : field;
    if (target && !target.disabled && target.getClientRects().length) {
      target.focus({ preventScroll: true });
      if (!focusReceipt.trigger) {
        try {
          target.setSelectionRange(
            focusReceipt.start,
            focusReceipt.end,
            focusReceipt.direction,
          );
        } catch {}
      }
    }
  }
  focusReceipt = null;
}

function saved(form) {
  if (!form?.matches) return;
  if (form.matches("[data-run-action]")) {
    for (const el of form.querySelectorAll("textarea,input:not([type=hidden])"))
      el.value = "";
  }
  drafts.delete(formKey(form));
  if (form.matches("[data-insert-form]"))
    document.getElementById("insert-dialog")?.close();
  if (form.matches(".assignment-form, .recipient-form")) form.closest("details").open = false;
}
function syncDialog() {
  const dialog = document.getElementById("record-dialog"),
    host = document.getElementById("inspector-host");
  if (!dialog || !host) return;
  const content = host.querySelector(
    "[data-inspector],.error-state,.conflict-state",
  );
  if (content && !dialog.open) {
    dialog.showModal();
    content
      .querySelector(
        '.record-edit input:not([type="hidden"]),.record-edit textarea',
      )
      ?.focus({ preventScroll: true });
  } else if (!content && dialog.open) dialog.close();

}
function closeRecord() {
  const close = document.querySelector(
    '#inspector-host [aria-label="Close inspector"]',
  );
  if (close) close.click();
  else {
    document.getElementById("record-dialog")?.close();
    document.getElementById("inspector-host").replaceChildren();
  }
}
function beforePatch() {
  if (!pendingRestore) {
    remember();
    pendingRestore = true;
  }
}
function afterPatch() {
  if (pendingRestore) {
    restore();
    pendingRestore = false;
  }
  initialize();
}
function initialize() {
  refreshDescriptions();
  applyTheme(root.dataset.themeChoice);
  panels();
  syncDialog();
  connectActivity();
  assignmentMode();
  syncQueueFilters();
  positionMenus();
  if (restoredReviewFocus) {
    // Keep focus on the restored row once its authoritative patch arrives.
    // A filtered-out row leaves focus on Search; never steal a later user focus.
    if (document.activeElement !== document.getElementById("search")) restoredReviewFocus = null;
    else {
      const target = document.getElementById(restoredReviewFocus);
      if (target) { target.focus({preventScroll: true}); restoredReviewFocus = null; }
    }
  }
}

window.Workbench = {
  applyTheme,
  initialize,
  beforePatch,
  afterPatch,
  syncDialog,
  saved,
};
// A backdrop click must begin and end outside the dialog. Selecting text or
// dragging a control out of a dialog must not dismiss an unfinished edit.
let backdropPress = null;
function onDialogBackdrop(event) {
  const dialog = event.target;
  if (!(dialog instanceof HTMLDialogElement) || !dialog.open) return false;
  if (dialog.closest("pl-dialog")) return false; // Preview component owns its requests.
  const bounds = dialog.getBoundingClientRect();
  return (
    event.clientX < bounds.left ||
    event.clientX > bounds.right ||
    event.clientY < bounds.top ||
    event.clientY > bounds.bottom
  );
}
function requestDialogClose(dialog) {
  if (dialog.dispatchEvent(new Event("cancel", {cancelable: true}))) dialog.close();
}
document.addEventListener("pointerdown", (event) => {
  backdropPress =
    event.button === 0 && onDialogBackdrop(event) ? event.target : null;
});
document.addEventListener("pointercancel", () => {
  backdropPress = null;
});
document.addEventListener("click", (event) => {
  const dismiss = backdropPress === event.target && onDialogBackdrop(event);
  backdropPress = null;
  if (dismiss) {
    // Use the same cancelable boundary as native Escape. Feature handlers own
    // dirty guards, pending operations and nested-viewer cleanup.
    requestDialogClose(event.target);
    event.stopPropagation();
  }
}, true);
const dismissibleDetails = ".switcher,.row-menu,.assignment-popover,.record-actions,.create-popover,.project-record";
function closeDetails(details, restoreFocus = false) {
  const focused = details.contains(document.activeElement);
  details.open = false;
  if (restoreFocus || focused) details.querySelector("summary")?.focus({preventScroll: true});
}
document.addEventListener("click", (event) => {
  const toggle = event.target.closest("[data-toggle-panel]");
  if (toggle) {
    const key = toggle.dataset.togglePanel;
    root.classList.toggle(key + "-collapsed");
    saveSetting(key + "_visible", String(!root.classList.contains(key + "-collapsed")));
    try {
      localStorage.setItem(
        "pellets-panels",
        JSON.stringify({
          navigation: !root.classList.contains("navigation-collapsed"),
          execution: !root.classList.contains("execution-collapsed"),
        }),
      );
    } catch {}
    panels();
  }
  if (event.target.closest("[data-open-execution]")) {
    window.Planner?.selectExecution();
    root.classList.remove("execution-collapsed");
    saveSetting("execution_visible", "true");
    try {
      localStorage.setItem(
        "pellets-panels",
        JSON.stringify({
          navigation: !root.classList.contains("navigation-collapsed"),
          execution: true,
        }),
      );
    } catch {}
    panels();
  }
  if (event.target.closest("[data-dialog-close]")) closeRecord();
  if (event.target.closest("[data-project-settings]")) {
    const el = document.getElementById("project-record");
    el.open = true;
    el.querySelector("input")?.focus();
  }
  const assignment = event.target.closest("[data-edit-assignment]");
  if (assignment) {
    const menu = document.getElementById("assignment-popover");
    if (menu) {
      menu.open = true;
      assignmentMode();
      const field = menu.querySelector('[name="' + assignment.dataset.editAssignment + '"]');
      const trigger = field?.closest(".select-control")?.querySelector(".select-trigger");
      (trigger || field || menu.querySelector("summary"))?.focus({preventScroll:true});
    }
  }
  const insert = event.target.closest("[data-insert-checkpoint]");
  if (insert)
    insertCheckpoint(
      insert.closest(".task-row,.checkpoint-row"),
      insert.dataset.insertCheckpoint,
    );
  const move = event.target.closest("[data-move-row]");
  if (move) moveRow(move.closest(".task-row,.checkpoint-row"), move.dataset.moveRow);
  document
    .querySelectorAll(dismissibleDetails)
    .forEach((el) => {
      if (el.open && !el.contains(event.target) && !(assignment && el.id === "assignment-popover")) closeDetails(el);
    });
});
document.addEventListener(
  "cancel",
  (event) => {
    if (event.target.id === "record-dialog") {
      event.preventDefault();
      closeRecord();
    } else if (event.target.id === "insert-dialog") {
      const form = event.target.querySelector("form");
      if (form?.getAttribute("aria-busy") === "true" ||
          (form?.dataset.dirty === "true" && !confirm("Discard unsaved checkpoint changes?")))
        event.preventDefault();
    }
  },
  true,
);
// Native details menus add roving keyboard navigation and type-ahead.
let typed = "",
  typedAt = 0;
document.addEventListener(
  "keydown",
  (event) => {
    let menu = event.target.closest(dismissibleDetails);
    // Safari may leave focus outside a disclosure after pointer activation.
    // Escape still dismisses that overlay, but never one behind a modal.
    if (!menu && event.key === "Escape" && !document.querySelector("dialog[open]"))
      menu = Array.from(document.querySelectorAll(dismissibleDetails)).findLast(el => el.open);
    if (!menu) return;
    if (event.key === "Escape" && menu.open) {
      event.preventDefault();
      event.stopImmediatePropagation();
      closeDetails(menu, true);
      return;
    }
    // Editing disclosures use native field navigation, not menu type-ahead.
    if (menu.matches(".create-popover,.project-record")) return;
    if (
      !["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key) &&
      !(event.key.length === 1 && !event.ctrlKey && !event.metaKey)
    )
      return;
    // Preserve native Space/Enter activation for summaries and menu actions.
    if (event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey ||
        event.key === " " || event.target.matches("input,textarea,select")) return;
    const choices = Array.from(
      menu.querySelectorAll("[role=menuitem],.assignment-form button,.recipient-form button"),
    ).filter((x) => !x.matches(':disabled, [aria-disabled="true"]') && !x.closest("[hidden]"));
    if (!choices.length) return;
    event.preventDefault();
    menu.open = true;
    let index = choices.indexOf(document.activeElement);
    if (event.key === "Home") index = 0;
    else if (event.key === "End") index = choices.length - 1;
    else if (event.key === "ArrowDown") index = (index + 1) % choices.length;
    else if (event.key === "ArrowUp")
      index = (index - 1 + choices.length) % choices.length;
    else {
      if (Date.now() - typedAt > 700) typed = "";
      typedAt = Date.now();
      typed += event.key.toLowerCase();
      const match = choices.findIndex((x) =>
        x.textContent.trim().toLowerCase().startsWith(typed),
      );
      if (match >= 0) index = match;
    }
    choices[Math.max(0, index)].focus();
  },
  true,
);
document.addEventListener("keydown", (event) => {
  if (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10")) {
    const row = event.target.closest(".task-row,.checkpoint-row");
    if (row) {
      event.preventDefault();
      const menu = row.querySelector(".row-menu");
      menu.open = true;
      menu.querySelector("button")?.focus();
    }
  }
});
document.addEventListener("contextmenu", (event) => {
  const row = event.target.closest(".task-row,.checkpoint-row");
  if (!row) return;
  event.preventDefault();
  const menu = row.querySelector(".row-menu");
  menu.open = true;
  menu.querySelector("button")?.focus();
});
function input(name, value) {
  const el = document.createElement("input");
  el.type = "hidden";
  el.name = name;
  el.value = value ?? "";
  return el;
}
function insertCheckpoint(row, direction) {
  const dialog = document.getElementById("insert-dialog"),
    source = document.querySelector("[data-checkpoint-form]");
  if (!row || !source || !row.dataset.checkpointPriority) return;
  row.querySelector(".row-menu").open = false;
  const form = document.createElement("form");
  form.id = "insert-checkpoint-form";
  form.className = "insert-form dirty-track";
  form.action = source.action;
  form.method = "post";
  form.setAttribute("data-on:submit", "@submit()");
  form.dataset.insertForm = "";
  const header = document.createElement("header"),
    title = document.createElement("h2"),
    close = document.createElement("button");
  title.textContent = "Insert checkpoint";
  close.type = "button";
  close.textContent = "×";
  close.setAttribute("aria-label", "Cancel insertion");
  close.onclick = () => requestDialogClose(dialog);
  header.append(title, close);
  const body = document.createElement("div");
  body.className = "dialog-body";
  form.append(header, body);
  const where = document.createElement("p");
  where.textContent =
    "Insert " +
    direction +
    " " +
    row.dataset.rowId +
    ". Review scope is explicit and does not follow later reordering.";
  body.append(where);
  for (const [name, value] of Object.entries({
    _csrf: source.querySelector('[name="_csrf"]').value,
    request_id: crypto.randomUUID(),
    status: "open",
    description: "",
    external_id: "",
    group: "",
    review_targets: "",
    review_target_versions: "",
    placement_target: row.dataset.rowId,
    placement_direction: direction,
    placement_target_version: row.dataset.rowVersion,
  }))
    form.append(input(name, value));
  const label = document.createElement("label");
  label.textContent = "Checkpoint title";
  const name = document.createElement("input");
  name.name = "title";
  name.value = "Review selected changes";
  name.required = true;
  label.append(name);
  body.append(label);
  const options = document.createElement("div");
  options.className = "scope-options";
  options.append(
    document.getElementById("scope-candidates").content.cloneNode(true),
  );
  const rows = Array.from(
      document.getElementById("scope-order").content.children,
    ),
    index = rows.findIndex((x) => x.dataset.rowId === row.dataset.rowId);
  let start = index;
  while (start > 0 && rows[start - 1].dataset.kind === "ordinary") start--;
  const selected = new Set(
    rows
      .slice(start, index + (direction === "after" ? 1 : 0))
      .filter((x) => x.dataset.kind === "ordinary")
      .map((x) => x.dataset.rowId),
  );
  options.querySelectorAll("input").forEach((el) => {
    el.checked = selected.has(el.value);
  });
  body.append(options);
  const footer = document.createElement("footer"),
    actions = document.createElement("div"),
    cancel = document.createElement("button");
  footer.className = "dialog-footer";
  actions.className = "dialog-footer-actions";
  cancel.type = "button";
  cancel.className = "dialog-cancel";
  cancel.textContent = "Cancel";
  cancel.onclick = () => requestDialogClose(dialog);
  const submit = document.createElement("button");
  submit.type = "submit";
  submit.className = "primary-button";
  submit.textContent = "Insert review checkpoint";
  actions.append(cancel, submit);
  footer.append(actions);
  form.append(footer);
  const sync = () => {
    const checked = Array.from(options.querySelectorAll("input:checked"));
    form.elements.review_targets.value = checked.map((x) => x.value).join(",");
    form.elements.review_target_versions.value = checked
      .map((x) => x.value + ":" + x.dataset.version)
      .join(",");
    submit.disabled = !checked.length;
  };
  options.addEventListener("change", sync);
  sync();
  dialog.replaceChildren(form);
  const opener = row.querySelector(".row-menu > summary");
  const previousFocus = document.activeElement;
  dialog.onclose = () => {
    const focused = document.activeElement;
    if (opener.isConnected && (focused === document.body || focused === previousFocus || dialog.contains(focused)))
      opener.focus({preventScroll: true});
  };
  dialog.showModal();
  name.focus();
}
function moveRow(row, direction) {
  const rows = Array.from(
      document.querySelectorAll(".queue-rows>[data-row-id]"),
    ).filter((x) => x.dataset.checkpointPriority),
    index = rows.indexOf(row),
    target = rows[index + (direction === "before" ? -1 : 1)];
  if (!target) return;
  const menu = row.querySelector(".row-menu");
  menu.open = false;
  menu.querySelector("summary").focus({preventScroll: true});
  const form = document.createElement("form");
  form.action =
    document.querySelector("[data-checkpoint-form]").action +
    "/" +
    row.dataset.rowId +
    "/move";
  form.method = "post";
  form.setAttribute("data-on:submit", "@submit()");
  for (const [name, value] of Object.entries({
    _csrf: document.querySelector('[name="_csrf"]').value,
    version: row.dataset.rowVersion,
    target: target.dataset.rowId,
    direction,
  }))
    form.append(input(name, value));
  document.body.append(form);
  requestAnimationFrame(() => form.requestSubmit());
}
function highlight(row, enabled) {
  if (!row) return;
  const refs = new Set((row.dataset.scope || "").split(" "));
  document
    .querySelectorAll(".queue-rows>.task-row")
    .forEach((el) =>
      el.classList.toggle(
        "scope-highlight",
        enabled && refs.has(el.dataset.rowId),
      ),
    );

}
for (const [type, enabled] of [
  ["mouseover", true],
  ["mouseout", false],
  ["focusin", true],
  ["focusout", false],
])
  document.addEventListener(type, (e) => {
    const row = e.target.closest(".checkpoint-row");
    if (row && !row.contains(e.relatedTarget)) highlight(row, enabled);
  });

function element(tag, className, text) {
  const el = document.createElement(tag);
  if (className) el.className = className;
  if (text !== undefined) el.textContent = text;
  return el;
}

function updateCurrentOperation(panel) {
  const status = panel?.closest(".run-workspace")?.querySelector(".execution-status");
  if (!status) return;
  const output = status.querySelector(".execution-operation"),
    cache = activity.get(panel.dataset.activityUrl);
  let text = "";
  if (status.dataset.showOperation === "true" && cache?.available && cache.connected) {
    const items = Array.from(cache.items.values());
    // A turn ending invalidates any unfinished item snapshots from that turn.
    const ended = Math.max(0, ...items.filter(item => item.kind === "turn").map(item => item.sequence));
    const active = items.filter(item => item.status === "running" && item.sequence > ended &&
      ["command", "file_read", "file_change", "tool"].includes(item.kind))
      .sort((a, b) => b.sequence - a.sequence)[0];
    if (active) {
      const label = {command: "Running command", file_read: "Reading file", file_change: "Updating file", tool: "Using tool"}[active.kind];
      const summary = activitySummary(active, panel.dataset.workspaceRoot, items);
      const detail = summary.operation || summary.title;
      text = label + (detail ? ": " + detail : "");
    }
  }
  // Neither the operation nor the feed is a live region. Only state changes
  // are announced; each completed message/output snapshot stays readable.
  if (output.textContent !== text) output.textContent = text;
  output.hidden = !text;
}

function activityAvailability(panel, message, notice) {
  panel.querySelector(".activity-availability").textContent = message;
  const status = panel.querySelector(".activity-notice");
  status.textContent = notice ? message : "";
  status.hidden = !notice;
}

function renderActivity(panel, snapshot) {
  const events = panel.querySelector("[data-activity-events]");
  if (!events) return;
  const selection = window.getSelection(), active = document.activeElement,
    reading = (!selection?.isCollapsed && panel.contains(selection?.anchorNode)) ||
      events.contains(active),
    selected = !selection?.isCollapsed && events.contains(selection?.anchorNode) ?
      {anchor: selection.anchorNode, start: selection.anchorOffset,
        focus: selection.focusNode, end: selection.focusOffset} : null,
    workspace = panel.closest(".run-workspace"),
    workspaceTop = workspace.scrollTop,
    edge = panel.getBoundingClientRect().top,
    anchors = Array.from(events.querySelectorAll(".activity-event, .activity-group > summary"))
      .filter(node => { const box = node.getBoundingClientRect(); return box.height > 0 && box.bottom > edge + 1; })
      .map(node => ({node, offset: node.getBoundingClientRect().top - edge,
        focused: node.contains(active) || !!(selected && node.contains(selected.anchor))}))
      // A group's preview can change height above a focused child. Keep that
      // reading target steady before falling back to the first visible row.
      .sort((a, b) => Number(b.focused) - Number(a.focused));
  const following =
      !reading && panel.scrollHeight - panel.clientHeight - panel.scrollTop < 32,
    top = panel.scrollTop;
  let cache = activity.get(panel.dataset.activityUrl);
  if (!cache) {
    cache = { items: new Map(), cursor: 0 };
    activity.set(panel.dataset.activityUrl, cache);
    while (activity.size > 16) activity.delete(activity.keys().next().value);
  }
  if (snapshot.reset || !snapshot.available) cache.items.clear();
  cache.cursor = snapshot.cursor;
  cache.available = snapshot.available;
  cache.truncated = snapshot.truncated;
  cache.connected = snapshot.connected !== false;
  let availability = snapshot.message ||
    (snapshot.available
      ? "Reported activity; complete messages and output appear when available."
      : "Detailed activity is unavailable for this attempt.");
  if (snapshot.truncated && !availability.includes("truncated"))
    availability = "Earlier activity was truncated. " + availability;
  activityAvailability(panel, availability,
    !snapshot.available || snapshot.truncated || snapshot.connected === false);
  if (snapshot.available) for (const item of snapshot.items || []) cache.items.set(item.id, item);
  while (cache.items.size > 128) cache.items.delete(cache.items.keys().next().value);
  const nodes = new Map(Array.from(events.querySelectorAll(".activity-event"), node => [node.dataset.eventId, node]));
  for (const [id, node] of nodes) {
    if (cache.items.has(id)) continue;
    expansions.delete(node.id);
    node.remove();
    nodes.delete(id);
  }
  for (const item of cache.items.values()) {
    let node = nodes.get(item.id);
    if (!node) {
      node = element(item.kind === "message" ? "article" : "details",
        "activity-event" + (item.kind === "message" ? " activity-message" : ""));
      node.dataset.eventId = item.id;
      node.id = "event-" + panel.dataset.runId + "-" + item.id;
      const heading = element(item.kind === "message" ? "h4" : "summary", "event-heading");
      heading.append(element("span", "event-title"), element("small", "event-path"),
        element("small", "event-status"), element("small", "event-error"), element("small", "event-impact"));
      node.append(heading, element("div", "event-body"));
      nodes.set(item.id, node);
      if (item.kind !== "message") {
        node.open = expansions.get(node.id) ?? false;
        node.addEventListener("toggle", () => expansions.set(node.id, node.open));
      }
    }
    const body = node.querySelector(".event-body");
    const summary = activitySummary(item, panel.dataset.workspaceRoot, Array.from(cache.items.values()));
    node.dataset.failed = String(!!summary.failed);
    node.dataset.action = String(!!summary.action);
    const labels = {
      "event-title": summary.title,
      "event-path": summary.operation || "",
      "event-status": summary.outcome || "",
      "event-error": summary.error ? "Reported error: " + summary.error : "",
      "event-impact": summary.impact || "",
    };
    for (const [name, text] of Object.entries(labels)) {
      const label = node.querySelector("." + name);
      if (label.textContent !== text) label.textContent = text;
      label.hidden = !text;
    }
    // Replayed snapshots and status-only changes must not replace selected
    // message text, focused links/code, or horizontally scrolled output.
    const pending = item.kind === "message" && !item.text?.trim() && item.status === "running";
    const content = JSON.stringify([item.text, item.path, item.error, item.source, item.diff, item.command,
      item.output, item.exit_code, item.truncated, pending]);
    if (node.activityContent === content) continue;
    node.activityContent = content;
    body.replaceChildren();
    if (item.kind === "message") {
      const message = element("div", "markdown-body activity-message-text");
      if (item.text?.trim()) message.append(renderMarkdown(item.text));
      else message.append(element("p", "activity-message-pending", pending ?
        "Waiting for the complete message…" : "No message text was reported."));
      body.append(message);
    } else if (item.text) body.append(element("p", "", item.text));
    if (item.path) body.append(element("p", "event-full-path", "Reported path: " + item.path));
    if (item.error) body.append(element("p", "", "Reported error: " + item.error));
    if (item.source) body.append(highlightCode(item.source));
    if (item.diff) body.append(highlightCode(item.diff, true));
    if (item.command)
      body.append(element("pre", "command-output", "$ " + item.command));
    if (item.output) {
      if (item.kind === "file_read") {
        body.append(element("p", "", "Reported read output"));
        body.append(highlightCode(item.output));
      } else body.append(element("pre", "command-output", item.output));
    }
    if (item.exit_code != null)
      body.append(element("p", "event-result", "Exit " + item.exit_code));
    if (item.kind === "command" || item.command) {
      if (!item.output) body.append(element("p", "", "Output not reported."));
      if (item.exit_code == null) body.append(element("p", "event-result", "Exit code not reported."));
    }
    if (item.truncated)
      body.append(element("p", "", "Output limited to the retained excerpt."));
    if (!body.childNodes.length)
      body.append(element("p", "", "No additional details were reported."));
  }
  // Reconcile groups and their original children in place. Replayed snapshots
  // and ordinary appends never detach retained nodes or collapse disclosures.
  const groups = new Map(Array.from(events.querySelectorAll(".activity-group"), node => [node.dataset.groupId, node]));
  cache.rows = activityRows(cache.items.values(), cache.rows);
  const ordered = [];
  for (const row of cache.rows) {
    if (!row.kind) { ordered.push(nodes.get(row.id)); continue; }
    let group = groups.get(row.id);
    if (!group) {
      group = element("details", "activity-group");
      group.dataset.groupId = row.id;
      group.id = "activity-group-" + panel.dataset.runId + "-" + row.id;
      const summary = element("summary", "activity-group-heading");
      summary.append(element("span", "activity-group-title"),
        element("span", "activity-group-outcome"), element("span", "activity-group-operation"),
        element("span", "activity-group-failure"));
      group.append(summary, element("div", "activity-group-members"));
      // Promoting a single event must not hide an open or focused detail.
      group.open = expansions.get(group.id) ?? row.items.some(item => {
        const node = nodes.get(item.id);
        return node.open || node.contains(active) || (selected && node.contains(selected.anchor));
      });
      group.addEventListener("toggle", () => expansions.set(group.id, group.open));
    }
    groups.delete(row.id);
    const summary = activityGroupSummary(row, panel.dataset.workspaceRoot, Array.from(cache.items.values()));
    for (const key of ["title", "outcome", "operation", "failure"]) {
      const label = group.querySelector(".activity-group-" + key);
      if (label.textContent !== summary[key]) label.textContent = summary[key];
      label.hidden = !summary[key];
    }
    group.dataset.active = String(summary.active);
    group.dataset.failed = String(summary.failed);
    const members = group.querySelector(".activity-group-members");
    placeActivityNodes(members, row.items.map(item => nodes.get(item.id)));
    ordered.push(group);
  }
  placeActivityNodes(events, ordered);
  for (const group of groups.values()) {
    expansions.delete(group.id);
    group.remove();
  }
  // A newly created wrapper can require reparenting on browsers without the
  // state-preserving moveBefore API. Restore only still-retained focus/text.
  if (events.contains(active) && document.activeElement !== active) active.focus({preventScroll: true});
  if (selected && events.contains(selected.anchor) && events.contains(selected.focus) &&
      (selection.anchorNode !== selected.anchor || selection.anchorOffset !== selected.start ||
       selection.focusNode !== selected.focus || selection.focusOffset !== selected.end)) {
    selection.setBaseAndExtent(selected.anchor, selected.start, selected.focus, selected.end);
  }
  while (expansions.size > 512)
    expansions.delete(expansions.keys().next().value);
  panel.querySelector("[data-activity-count]").textContent =
    cache.items.size + " events";
  updateCurrentOperation(panel);
  if (following) panel.scrollTop = panel.scrollHeight;
  else {
    const anchor = anchors.find(({node}) => node.isConnected);
    panel.scrollTop = anchor ? panel.scrollTop + anchor.node.getBoundingClientRect().top -
      panel.getBoundingClientRect().top - anchor.offset : top;
  }
  workspace.scrollTop = workspaceTop;
}
function placeActivityNodes(parent, nodes) {
  let position = parent.firstElementChild;
  for (const node of nodes) {
    if (node !== position) {
      if (parent.moveBefore && parent.isConnected && node.isConnected) parent.moveBefore(node, position);
      else parent.insertBefore(node, position);
    }
    position = node.nextElementSibling;
  }
}
// Keyboard scrolling knows the scroll containers, but not the sticky execution
// summary that can cover them on phones. Keep grouped disclosures reachable.
document.addEventListener("focusin", event => {
  const target = event.target.closest?.(".activity-group summary");
  if (!target) return;
  requestAnimationFrame(() => {
    if (document.activeElement !== target || !target.matches(":focus-visible")) return;
    const panel = target.closest(".activity-panel"), workspace = panel.closest(".run-workspace"),
      status = workspace.querySelector(".execution-status");
    const inset = () => Math.max(workspace.getBoundingClientRect().top,
      status && getComputedStyle(status).position === "sticky" ? status.getBoundingClientRect().bottom : 0);
    if (panel.getBoundingClientRect().top < inset())
      workspace.scrollTop -= inset() - panel.getBoundingClientRect().top;
    const box = target.getBoundingClientRect(),
      top = Math.max(panel.getBoundingClientRect().top, inset()) + 3,
      bottom = Math.min(panel.getBoundingClientRect().bottom, workspace.getBoundingClientRect().bottom) - 3;
    if (box.top < top) panel.scrollTop += box.top - top;
    else if (box.bottom > bottom) panel.scrollTop += box.bottom - bottom;
  });
});
function connectActivity() {
  if (uiVersion.isOutdated()) return;
  const panel = document.querySelector(".activity-panel"),
    url = panel?.dataset.activityUrl || "";
  updateCurrentOperation(panel);
  if (url === streamURL) return;
  if (stream) stream.close();
  stream = null;
  streamURL = url;
  currentActivityURL = url;
  if (!url) return;
  const cache = activity.get(url);
  if (
    cache &&
    panel.querySelector("[data-activity-events]").childElementCount === 0
  ) {
    renderActivity(panel, {
      cursor: cache.cursor,
      items: Array.from(cache.items.values()),
      available: cache.available,
      truncated: cache.truncated,
      connected: false,
      message: "Reconnecting to reported activity…",
    });
    const saved = scrolls.get(panel.id);
    if (saved) panel.scrollTop = saved.top;
  }
  stream = new EventSource(
    url +
      "?stream=1&after=" +
      (cache?.cursor || 0) +
      "&ui_revision=" +
      encodeURIComponent(uiVersion.revision),
  );
  uiVersion.watchSource(stream);
  stream.addEventListener("pellets-activity", (event) => {
    if (uiVersion.isOutdated() || currentActivityURL !== url) return;
    const current = document.querySelector(".activity-panel");
    if (current?.dataset.activityUrl !== url) return;
    try {
      renderActivity(current, JSON.parse(event.data));
    } catch {
      const cache = activity.get(url);
      if (cache) cache.connected = false;
      updateCurrentOperation(current);
      activityAvailability(current, "Activity could not be read. Reconnect to retry.", true);
    }
  });
  stream.addEventListener("error", () => {
    const current = document.querySelector(".activity-panel");
    if (current?.dataset.activityUrl === url) {
      const cache = activity.get(url);
      if (cache) cache.connected = false;
      updateCurrentOperation(current);
      activityAvailability(current, "Activity connection interrupted. Reconnecting…", true);
    }
  });
}
document.addEventListener("pellets-ui-outdated", () => {
  stream?.close();
});
// Inline removal is a reversible domain mutation with a versioned Undo receipt.
// It keeps browsing in place and does not manufacture a completed review.
document.addEventListener(
  "submit",
  async (event) => {
    const form = event.target;
    // A completed row action must not leave its menu blocking related patches.
    const menu = form.closest?.(".row-menu");
    if (menu && !form.matches("[data-checkpoint-remove-inline]")) menu.open = false;
    if (
      !form.matches?.("[data-checkpoint-remove-inline],[data-checkpoint-undo]")
    )
      return;
    event.preventDefault();
    event.stopImmediatePropagation();
    if (uiVersion.isOutdated()) return;
    if (form.dataset.pending) return;
    form.dataset.pending = "true";
    const button = form.querySelector("button");
    const submittedWithFocus = form.contains(document.activeElement);
    button.disabled = true;
    uiVersion.beginRequest();
    try {
      const response = await fetch(form.action, {
        method: "POST",
        credentials: "same-origin",
        headers: uiVersion.headers({
          "Content-Type": "application/x-www-form-urlencoded",
        }),
        body: new URLSearchParams(new FormData(form)),
      });
      if (!uiVersion.inspectResponse(response)) return;
      if (!response.ok)
        throw Error(
          "Checkpoint changed; refresh its details before trying again.",
        );
      const content = new DOMParser().parseFromString(
        await response.text(),
        "text/html",
      );
      if (form.matches("[data-checkpoint-undo]")) {
        const returnFocus = submittedWithFocus && (document.activeElement === document.body || form.contains(document.activeElement));
        document.getElementById("checkpoint-undo")?.remove();
        if (returnFocus) {
          restoredReviewFocus = form.dataset.restoreFocus || null;
          document.getElementById("search")?.focus({preventScroll: true});
        }
      } else {
        const restore = content.querySelector("form[data-checkpoint-restore]");
        if (!restore)
          throw Error("Removal recorded; find Restore under Maybe later.");
        document.getElementById("checkpoint-undo")?.remove();
        const toast = element("div", "undo-toast");
        toast.id = "checkpoint-undo";
        toast.setAttribute("role", "status");
        toast.append(element("span", "", "Checkpoint removed"));
        const undo = document.createElement("form");
        undo.action = restore.getAttribute("action");
        undo.method = "post";
        undo.dataset.checkpointUndo = "";
        undo.dataset.restoreFocus = "review-actions-" + form.closest("[data-row-id]").dataset.rowId;
        for (const el of restore.querySelectorAll("input[type=hidden]"))
          undo.append(input(el.name, el.value));
        const restoreButton = element("button", "", "Undo");
        restoreButton.type = "submit";
        undo.append(restoreButton);
        toast.append(undo);
        const dismiss = element("button", "", "×");
        dismiss.type = "button";
        dismiss.setAttribute("aria-label", "Dismiss Undo");
        dismiss.onclick = () => toast.remove();
        toast.append(dismiss);
        document.body.append(toast);
        const menu = form.closest(".row-menu");
        if (menu) menu.open = false;
        restoreButton.focus({preventScroll: true});
      }
      document.dispatchEvent(new CustomEvent("pellets-refresh"));
    } catch (error) {
      const feedback = document.getElementById("request-feedback");
      feedback.hidden = false;
      feedback.textContent = error.message;
      feedback.classList.add("request-failed");
      document.dispatchEvent(new CustomEvent("pellets-refresh"));
    } finally {
      uiVersion.endRequest();
      button.disabled = uiVersion.isOutdated();
      delete form.dataset.pending;
    }
  },
  true,
);

function assignmentMode() {
  const form = document.querySelector(".assignment-form");
  if (!form) return;
  const groups = form.querySelector(".explicit-groups");
  if (groups) groups.hidden = form.elements.mode.value !== "explicit";
  positionMenus();
}
document.addEventListener("change", (e) => {
  if (e.target.matches('.assignment-form [name="mode"]')) assignmentMode();
});

// Title saves update only identity/revisions, never replace the context draft.
function clearTitleDirty(form) {
  delete form.dataset.dirty;
  const inspector = form.closest('[data-inspector]');
  if (!inspector.querySelector('form[data-dirty="true"]')) inspector.classList.remove('is-dirty');
}
document.addEventListener('keydown', event => {
  const field = event.target.closest?.('.group-title-input');
  if (!field || event.isComposing) return;
  if (event.key === 'Escape') {
    event.preventDefault(); event.stopImmediatePropagation();
    if (field.form.dataset.pending) return;
    field.value = field.defaultValue;
    field.form.querySelector('[role=alert]').hidden = true;
    clearTitleDirty(field.form);
    field.blur();
  } else if (event.key === 'Enter') {
    event.preventDefault(); field.form.requestSubmit();
  }
}, true);
document.addEventListener('focusout', event => {
  const field = event.target.closest?.('.group-title-input');
  if (field && field.value !== field.defaultValue && !field.form.dataset.failed) field.form.requestSubmit();
});
document.addEventListener('input', event => {
  if (event.target.matches?.('.group-title-input')) delete event.target.form.dataset.failed;
});
document.addEventListener('submit', async event => {
  const form = event.target;
  if (!form.matches?.('.group-title-editor')) return;
  event.preventDefault(); event.stopImmediatePropagation();
  if (form.dataset.pending || uiVersion.isOutdated()) return;
  const field = form.elements.name, error = form.querySelector('[role=alert]');
  if (field.value === field.defaultValue) { clearTitleDirty(form); return; }
  const revision = form.elements.revision.value;
  const inspector = form.closest('[data-inspector]');
  const saveContext = inspector.querySelector('.dialog-footer button[type=submit]');
  form.dataset.pending = 'true';
  field.readOnly = true;
  saveContext.disabled = true;
  error.hidden = true;
  uiVersion.beginRequest();
  try {
    const response = await fetch(form.action, {method:'POST', credentials:'same-origin',
      headers:uiVersion.headers({'Content-Type':'application/x-www-form-urlencoded'}),
      body:new URLSearchParams(new FormData(form))});
    if (!uiVersion.inspectResponse(response)) return;
    const content = new DOMParser().parseFromString(await response.text(), 'text/html');
    const updated = content.querySelector('[data-group-revision][data-inspector]');
    if (!response.ok) {
      if (updated) form.elements.revision.value = updated.dataset.groupRevision;
      throw Error(content.querySelector('.conflict-state > p')?.textContent || 'Name could not be saved. Try again.');
    }
    const name = content.querySelector('.group-title-input')?.value;
    if (!updated || name === undefined) throw Error('Save could not be confirmed. Reload before retrying.');
    field.value = field.defaultValue = name;
    inspector.querySelector('#inspector-title').textContent = name;
    inspector.dataset.groupRevision = updated.dataset.groupRevision;
    inspector.querySelectorAll('input[name=revision]').forEach(input => {
      if (input.value === revision) input.value = updated.dataset.groupRevision;
    });
    clearTitleDirty(form);
    delete form.dataset.failed;
  } catch (failure) {
    form.dataset.failed = 'true';
    error.textContent = failure.message;
    error.hidden = false;
  } finally {
    delete form.dataset.pending;
    field.readOnly = false;
    saveContext.disabled = uiVersion.isOutdated();
    uiVersion.endRequest();
  }
}, true);
