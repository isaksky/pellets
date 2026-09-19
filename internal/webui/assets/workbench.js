import { saveSetting } from "./settings.js";
import "./dropdowns.js";
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
  window.Dropdowns?.enhance();
  const select = document.getElementById("theme-select");
  if (select) {
    select.value = choice;
    window.Dropdowns?.enhance();
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
          if (el.type === "password") return;
          fields.push({ name: el.name, value: el.value, checked: el.checked });
        });
      drafts.set(formKey(form), fields);
      while (drafts.size > 64) drafts.delete(drafts.keys().next().value);
    });
  document
    .querySelectorAll("#main,.run-workspace,.activity-panel")
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
    .querySelectorAll("#main,.run-workspace,.activity-panel")
    .forEach((el) => {
      const saved = scrolls.get(scrollKey(el));
      if (saved) {
        el.scrollTop = saved.top;
        el.scrollLeft = saved.left;
      }
    });
  document
    .querySelectorAll("#execution details[id],#main details[id]")
    .forEach((el) => {
      if (expansions.has(el.id)) el.open = expansions.get(el.id);
    });
  // Recreate/synchronize custom controls after values and disclosures are
  // restored. Focus is independent of draft state: a clean control can be
  // keyboard-focused when an authoritative update arrives.
  window.Dropdowns?.enhance();
  if (focusReceipt) {
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
  applyTheme(root.dataset.themeChoice);
  panels();
  syncDialog();
  brackets();
  connectActivity();
  assignmentMode();
  syncQueueFilters();
  positionRecipients();
}

function positionRecipients() {
  document.querySelectorAll(".category-popover[open]").forEach(menu => {
    const form = menu.querySelector(".recipient-form");
    const anchor = menu.getBoundingClientRect();
    const width = form.getBoundingClientRect().width;
    form.style.left = Math.max(8 - anchor.left, Math.min(0, innerWidth - width - 8 - anchor.left)) + "px";
  });
}
document.addEventListener("toggle", event => {
  if (event.target.matches(".category-popover")) positionRecipients();
}, true);
window.addEventListener("resize", positionRecipients);
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
  const bounds = dialog.getBoundingClientRect();
  return (
    event.clientX < bounds.left ||
    event.clientX > bounds.right ||
    event.clientY < bounds.top ||
    event.clientY > bounds.bottom
  );
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
    if (event.target.id === "record-dialog") closeRecord();
    else event.target.close();
    return;
  }
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
    requestAnimationFrame(brackets);
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
      insert.closest(".task-row"),
      insert.dataset.insertCheckpoint,
    );
  const move = event.target.closest("[data-move-row]");
  if (move) moveRow(move.closest(".task-row"), move.dataset.moveRow);
  document
    .querySelectorAll(
      ".switcher[open],.row-menu[open],.assignment-popover[open],.record-actions[open]",
    )
    .forEach((el) => {
      if (!el.contains(event.target) && !(assignment && el.id === "assignment-popover")) el.open = false;
    });
});
document.addEventListener(
  "cancel",
  (event) => {
    if (event.target.id === "record-dialog") {
      event.preventDefault();
      closeRecord();
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
    const menu = event.target.closest(
      ".switcher,.row-menu,.assignment-popover,.record-actions",
    );
    if (!menu) return;
    if (event.key === "Escape" && menu.open) {
      event.preventDefault();
      event.stopImmediatePropagation();
      menu.open = false;
      menu.querySelector("summary").focus();
      return;
    }
    if (
      !["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key) &&
      !(event.key.length === 1 && !event.ctrlKey && !event.metaKey)
    )
      return;
    if (event.target.matches("input,textarea,select")) return;
    const choices = Array.from(
      menu.querySelectorAll("[role=menuitem],.assignment-form button,.recipient-form button"),
    ).filter((x) => !x.disabled);
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
    const row = event.target.closest(".task-row");
    if (row) {
      event.preventDefault();
      const menu = row.querySelector(".row-menu");
      menu.open = true;
      menu.querySelector("button")?.focus();
    }
  }
});
document.addEventListener("contextmenu", (event) => {
  const row = event.target.closest(".task-row");
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
  row.querySelector("details").open = false;
  const form = document.createElement("form");
  form.id = "insert-checkpoint-form";
  form.className = "insert-form";
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
  close.onclick = () => dialog.close();
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
  cancel.onclick = () => dialog.close();
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
function brackets() {
  const rows = document.getElementById("queue-rows");
  if (!rows) return;
  rows.querySelector(".scope-brackets")?.remove();
  const items = Array.from(rows.children).filter((x) => x.dataset.rowId),
    byRef = new Map(items.map((x, i) => [x.dataset.rowId, i])),
    spans = [];
  items.forEach((row, index) => {
    if (!row.dataset.scope) return;
    const refs = new Set(row.dataset.scope.split(" ")),
      members = items
        .map((x, i) =>
          refs.has(x.dataset.rowId) && !x.classList.contains("checkpoint-row")
            ? i
            : -1,
        )
        .filter((x) => x >= 0);
    const count = row.querySelector(".scope-count");
    if (count)
      count.textContent =
        members.length === refs.size
          ? String(refs.size)
          : members.length
            ? members.length + " of " + refs.size
            : refs.size + " hidden";
    if (members.length)
      spans.push({
        row,
        index,
        refs,
        members,
        first: Math.min(index, ...members),
        last: Math.max(index, ...members),
      });
  });
  spans.sort((a, b) => a.first - b.first || a.last - b.last);
  const lanes = [];
  spans.forEach((s) => {
    let lane = lanes.findIndex((end) => end < s.first);
    if (lane < 0) lane = lanes.length;
    lanes[lane] = s.last;
    s.lane = lane;
  });
  rows.style.paddingLeft = Math.max(10, lanes.length * 7 + 4) + "px";
  const ns = "http://www.w3.org/2000/svg",
    svg = document.createElementNS(ns, "svg");
  svg.classList.add("scope-brackets");
  svg.setAttribute("aria-hidden", "true");
  const box = rows.getBoundingClientRect();
  for (const span of spans) {
    const x = 3 + span.lane * 7;
    const y = (i) => {
      const b = items[i].getBoundingClientRect();
      return b.top - box.top + b.height / 2;
    };
    const g = document.createElementNS(ns, "g");
    g.dataset.checkpoint = span.row.dataset.rowId;
    for (let i = span.first; i < span.last; i++) {
      const path = document.createElementNS(ns, "path");
      path.setAttribute("d", `M ${x} ${y(i)} V ${y(i + 1)}`);
      if (!span.members.includes(i) || !span.members.includes(i + 1))
        path.classList.add("scope-muted");
      g.append(path);
    }
    for (const i of [...span.members, span.index]) {
      const path = document.createElementNS(ns, "path");
      path.setAttribute(
        "d",
        `M ${x} ${y(i)} H ${x + (i === span.index ? 12 : 5)}`,
      );
      g.append(path);
    }
    svg.append(g);
  }
  rows.prepend(svg);
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
  document
    .querySelectorAll(".scope-brackets g")
    .forEach((g) =>
      g
        .querySelectorAll("path")
        .forEach((p) =>
          p.classList.toggle(
            "scope-active",
            enabled && g.dataset.checkpoint === row.dataset.rowId,
          ),
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
window.addEventListener("resize", brackets);
function element(tag, className, text) {
  const el = document.createElement(tag);
  if (className) el.className = className;
  if (text !== undefined) el.textContent = text;
  return el;
}
function highlightCode(text, diff = false) {
  const pre = element("pre");
  pre.tabIndex = 0;
  const keywords =
    /\b(?:func|package|import|return|if|else|for|range|var|const|let|type|struct|interface|select|case|switch|break|continue|go|defer|async|await|function|class|new|throw|try|catch|export|from|true|false|nil|null|undefined)\b/;
  for (const line of String(text).split("\n")) {
    const row = element(
      "span",
      "source-line" +
        (diff && line.startsWith("+") && !line.startsWith("+++")
          ? " diff-added"
          : diff && line.startsWith("-") && !line.startsWith("---")
            ? " diff-removed"
            : ""),
    );
    const pattern =
      /(\/\/.*$|#.*$|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|`[^`]*`|\b\d+(?:\.\d+)?\b|\b[A-Za-z_$][\w$]*\b)/g;
    let last = 0;
    for (const match of line.matchAll(pattern)) {
      row.append(document.createTextNode(line.slice(last, match.index)));
      const value = match[0],
        kind = /^(\/\/|#)/.test(value)
          ? "comment"
          : /^["'`]/.test(value)
            ? "string"
            : /^\d/.test(value)
              ? "number"
              : keywords.test(value)
                ? "keyword"
                : "";
      row.append(
        kind
          ? element("span", "code-" + kind, value)
          : document.createTextNode(value),
      );
      last = match.index + value.length;
    }
    row.append(document.createTextNode(line.slice(last)));
    pre.append(row);
  }
  return pre;
}
function renderActivity(panel, snapshot) {
  const events = panel.querySelector("[data-activity-events]");
  if (!events) return;
  const following =
      panel.scrollHeight - panel.clientHeight - panel.scrollTop < 32,
    top = panel.scrollTop;
  let cache = activity.get(panel.dataset.activityUrl);
  if (!cache) {
    cache = { items: new Map(), cursor: 0 };
    activity.set(panel.dataset.activityUrl, cache);
    while (activity.size > 16) activity.delete(activity.keys().next().value);
  }
  if (snapshot.reset) {
    const retained = new Set((snapshot.items || []).map((item) => item.id));
    for (const node of Array.from(events.children)) {
      if (!retained.has(node.dataset.eventId)) {
        expansions.delete(node.id);
        node.remove();
      }
    }
    cache.items.clear();
  }
  cache.cursor = snapshot.cursor;
  panel.querySelector(".activity-availability").textContent =
    snapshot.message ||
    (snapshot.available
      ? "Reported by Codex"
      : "Detailed activity is unavailable for this attempt.");
  for (const item of snapshot.items || []) {
    cache.items.set(item.id, item);
    let node = Array.from(events.children).find(
      (x) => x.dataset.eventId === item.id,
    );
    if (!node) {
      node = element("details", "activity-event");
      node.dataset.eventId = item.id;
      node.id = "event-" + panel.dataset.runId + "-" + item.id;
      node.append(element("summary"), element("div", "event-body"));
      events.append(node);
      node.open = expansions.get(node.id) ?? false;
      node.addEventListener("toggle", () => expansions.set(node.id, node.open));
    }
    const summary = node.querySelector("summary"),
      body = node.querySelector(".event-body");
    summary.replaceChildren(
      element("span", "event-title", item.title || item.kind),
      element("small", "event-path", item.path || item.status || ""),
    );
    body.replaceChildren();
    if (item.text) body.append(element("p", "", item.text));
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
    if (item.exit_code !== undefined)
      body.append(element("p", "event-result", "Exit " + item.exit_code));
    if (item.truncated)
      body.append(element("p", "", "Output limited to the retained excerpt."));
    if (!body.childNodes.length)
      body.append(element("p", "", "No additional details were reported."));
  }
  while (events.children.length > 128) {
    const old = events.firstElementChild;
    cache.items.delete(old.dataset.eventId);
    expansions.delete(old.id);
    old.remove();
  }
  while (cache.items.size > 128)
    cache.items.delete(cache.items.keys().next().value);
  while (expansions.size > 512)
    expansions.delete(expansions.keys().next().value);
  panel.querySelector("[data-activity-count]").textContent =
    events.children.length + " events";
  if (following) panel.scrollTop = panel.scrollHeight;
  else panel.scrollTop = top;
}
function connectActivity() {
  if (uiVersion.isOutdated()) return;
  const panel = document.querySelector(".activity-panel"),
    url = panel?.dataset.activityUrl || "";
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
      available: true,
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
      current.querySelector(".activity-availability").textContent =
        "Activity could not be read. Reconnect to retry.";
    }
  });
  stream.addEventListener("error", () => {
    const current = document.querySelector(".activity-panel");
    if (current?.dataset.activityUrl === url)
      current.querySelector(".activity-availability").textContent =
        "Activity connection interrupted. Reconnecting…";
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
        document.getElementById("checkpoint-undo")?.remove();
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
}
document.addEventListener("change", (e) => {
  if (e.target.matches('.assignment-form [name="mode"]')) assignmentMode();
});
