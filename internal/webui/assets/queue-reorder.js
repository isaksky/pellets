// Queue movement owns only ephemeral gesture state. The server remains the
// authority for priority, ownership, review scope, and the rendered queue.
let gesture = null;
let pending = null;
let reconciling = false;
let scrollFrame = 0;

const list = () => document.getElementById("queue-rows");
const feedback = () => document.getElementById("queue-move-feedback");
const eligibleRows = () => Array.from(list()?.children || []).filter(row => row.matches("[data-row-id]") && row.querySelector("[data-queue-handle]"));

function say(message, failed = false, visible = true) {
  const live = document.getElementById("queue-reorder-live");
  if (live) live.textContent = message;
  if (!visible) return;
  const notice = feedback();
  if (!notice) return;
  notice.hidden = false;
  notice.classList.toggle("request-failed", failed);
  notice.replaceChildren(document.createTextNode(message));
  if (failed) {
    const retry = document.createElement("button");
    retry.type = "button";
    retry.className = "quiet";
    retry.textContent = "Refresh queue";
    retry.addEventListener("click", refresh);
    notice.append(" ", retry);
  }
}

function refresh() {
  document.dispatchEvent(new CustomEvent("pellets-refresh"));
}

function snapshot(source) {
  const rows = eligibleRows();
  const context = document.getElementById("task-list");
  if (!context || context.dataset.queueSortable !== "true" || !rows.includes(source) || rows.length < 2) return null;
  const full = Array.from(document.querySelectorAll("#scope-order [data-row-id]"), node => node.dataset.rowId);
  return {
    source, handle: source.querySelector("[data-queue-handle]"),
    sourceID: source.dataset.rowId, sourceVersion: source.dataset.rowVersion,
    title: source.querySelector(".task-title,.checkpoint-open")?.textContent.trim() || source.dataset.rowId,
    rows, others: rows.filter(row => row !== source),
    versions: new Map(rows.map(row => [row.dataset.rowId, row.dataset.rowVersion])),
    full, url: context.dataset.refreshUrl,
  };
}

function destination(state) {
  if (state.slot < state.others.length) {
    const anchor = state.others[state.slot];
    return {anchor, direction: "before", label: `Before ${anchor.dataset.rowId}`};
  }
  const anchor = state.others.at(-1);
  return {anchor, direction: "after", label: `After ${anchor.dataset.rowId}`};
}

function unchanged(state, target) {
  const before = state.full;
  if (!before.includes(state.sourceID) || !before.includes(target.anchor.dataset.rowId)) return false;
  const after = before.filter(id => id !== state.sourceID);
  const index = after.indexOf(target.anchor.dataset.rowId) + (target.direction === "after" ? 1 : 0);
  after.splice(index, 0, state.sourceID);
  return after.every((id, i) => id === before[i]);
}

function marker(state) {
  if (!state.line) {
    state.line = document.createElement("div");
    state.line.className = "queue-insertion";
    state.line.setAttribute("aria-hidden", "true");
    list()?.append(state.line);
  }
  const anchor = state.slot < state.others.length ? state.others[state.slot] : state.others.at(-1);
  const edge = state.slot < state.others.length ? anchor.getBoundingClientRect().top : anchor.getBoundingClientRect().bottom;
  state.line.style.top = `${edge - list().getBoundingClientRect().top}px`;
  state.line.title = destination(state).label;
  if (state.preview) state.preview.querySelector("small").textContent = destination(state).label;
}

function showDrag(state) {
  state.started = true;
  if (state.pointer === undefined) {
    const rect = state.handle.getBoundingClientRect();
    state.x = rect.right;
    state.y = rect.top;
  }
  state.source.classList.add("queue-drag-source");
  document.documentElement.classList.add("queue-dragging");
  state.handle.setAttribute("aria-pressed", "true");
  state.preview = document.createElement("div");
  state.preview.className = "queue-drag-preview";
  const title = document.createElement("strong"), where = document.createElement("small");
  title.textContent = state.title;
  state.preview.append(title, where);
  document.body.append(state.preview);
  marker(state);
  movePreview(state);
  say(`Picked up ${state.sourceID}. ${destination(state).label}.`, false, false);
}

function movePreview(state) {
  if (!state.preview) return;
  state.preview.style.left = `${Math.min(innerWidth - 12, state.x + 12)}px`;
  state.preview.style.top = `${Math.max(8, Math.min(innerHeight - 54, state.y + 12))}px`;
}

function slotAt(state, y) {
  for (let i = 0; i < state.others.length; i++) {
    const rect = state.others[i].getBoundingClientRect();
    if (y < (rect.top + rect.bottom) / 2) return i;
  }
  return state.others.length;
}

function updatePointer(state) {
  if (!state.started) return;
  const next = slotAt(state, state.y);
  if (next !== state.slot) {
    state.slot = next;
    marker(state);
    say(destination(state).label, false, false);
  } else marker(state);
  movePreview(state);
}

function scrollQueue() {
  scrollFrame = 0;
  const state = gesture;
  if (!state?.started || state.pointer === undefined) return;
  const pane = document.getElementById("main");
  const rect = pane?.getBoundingClientRect();
  if (!rect) return;
  const edge = 46;
  const speed = state.y < rect.top + edge ? -Math.min(18, (rect.top + edge - state.y) / 3) :
    state.y > rect.bottom - edge ? Math.min(18, (state.y - rect.bottom + edge) / 3) : 0;
  if (speed) {
    pane.scrollTop += speed;
    updatePointer(state);
  }
  scrollFrame = requestAnimationFrame(scrollQueue);
}

function clearGesture(state, shouldRefresh) {
  if (!state || gesture !== state) return;
  gesture = null;
  cancelAnimationFrame(scrollFrame);
  scrollFrame = 0;
  state.line?.remove();
  state.preview?.remove();
  state.source.classList.remove("queue-drag-source");
  state.handle?.removeAttribute("aria-pressed");
  document.documentElement.classList.remove("queue-dragging");
  if (state.pointer !== undefined && state.handle?.hasPointerCapture(state.pointer)) {
    state.handle.releasePointerCapture(state.pointer);
  }
  if (shouldRefresh) refresh();
}

export function cancelGesture(message = "Reordering canceled.", shouldRefresh = true) {
  if (!gesture) return;
  const state = gesture;
  clearGesture(state, shouldRefresh);
  say(message);
}

function submit(state, target) {
  if (pending || reconciling || !state.source.isConnected || !target.anchor.isConnected) return;
  pending = {id: state.sourceID, label: target.label, form: null};
  clearGesture(state, false);
  say(`Moving ${state.sourceID} ${target.label.toLowerCase()}…`);
  const form = document.createElement("form");
  form.hidden = true;
  form.method = "post";
  form.action = `/projects/${encodeURIComponent(state.sourceID.split("-").slice(0, -1).join("-"))}/pellets/${encodeURIComponent(state.sourceID)}/move`;
  form.setAttribute("data-on:submit", "@queueMove()");
  const values = {
    _csrf: document.querySelector('[name="_csrf"]')?.value || "",
    mode: "queue", version: state.sourceVersion,
    target: target.anchor.dataset.rowId,
    target_version: state.versions.get(target.anchor.dataset.rowId),
    direction: target.direction, return_to: state.url,
  };
  for (const [name, value] of Object.entries(values)) {
    const input = document.createElement("input");
    input.type = "hidden"; input.name = name; input.value = value;
    form.append(input);
  }
  document.getElementById("tasks-area")?.append(form);
  pending.form = form;
  requestAnimationFrame(() => {
    if (pending?.form === form) form.requestSubmit();
  });
}

function commit(state) {
  if (!state || state.slot === undefined || !state.others.length) return cancelGesture();
  const target = destination(state);
  if (unchanged(state, target)) {
    clearGesture(state, true);
    say("Queue position unchanged.");
    return;
  }
  submit(state, target);
}

document.addEventListener("pointerdown", event => {
  const handle = event.target.closest?.("[data-queue-handle]");
  if (!handle || event.button !== 0 || gesture || pending || reconciling) return;
  const state = snapshot(handle.closest("[data-row-id]"));
  if (!state) return;
  event.preventDefault();
  handle.focus({preventScroll: true});
  state.pointer = event.pointerId;
  state.x = state.startX = event.clientX;
  state.y = state.startY = event.clientY;
  state.slot = state.rows.indexOf(state.source);
  gesture = state;
  handle.setPointerCapture(event.pointerId);
});

document.addEventListener("pointermove", event => {
  const state = gesture;
  if (!state || state.pointer !== event.pointerId) return;
  state.x = event.clientX; state.y = event.clientY;
  if (!state.started && Math.hypot(state.x - state.startX, state.y - state.startY) >= 5) {
    showDrag(state);
    scrollFrame = requestAnimationFrame(scrollQueue);
  }
  updatePointer(state);
});

document.addEventListener("pointerup", event => {
  const state = gesture;
  if (!state || state.pointer !== event.pointerId) return;
  const queue = list()?.getBoundingClientRect(), pane = document.getElementById("main")?.getBoundingClientRect();
  if (!state.started || !queue || !pane || event.clientX < queue.left || event.clientX > queue.right ||
      event.clientY < Math.max(queue.top, pane.top) || event.clientY > Math.min(queue.bottom, pane.bottom)) {
    cancelGesture("Reordering canceled.");
    return;
  }
  state.y = event.clientY;
  updatePointer(state);
  commit(state);
});

document.addEventListener("pointercancel", event => {
  if (gesture?.pointer === event.pointerId) cancelGesture();
});
document.addEventListener("lostpointercapture", event => {
  if (gesture?.pointer === event.pointerId) cancelGesture();
});

document.addEventListener("keydown", event => {
  if (event.key === "Escape" && gesture) {
    event.preventDefault(); event.stopImmediatePropagation(); cancelGesture(); return;
  }
  const handle = event.target.closest?.("[data-queue-handle]");
  if (!handle || pending || reconciling) return;
  if (![" ", "Enter", "ArrowUp", "ArrowDown"].includes(event.key)) return;
  if (gesture && gesture.handle !== handle) return;
  event.preventDefault(); event.stopImmediatePropagation();
  if (!gesture) {
    if (event.key !== " " && event.key !== "Enter") return;
    const state = snapshot(handle.closest("[data-row-id]"));
    if (!state) return;
    state.slot = state.rows.indexOf(state.source);
    gesture = state;
    showDrag(state);
    return;
  }
  if (event.key === " " || event.key === "Enter") return commit(gesture);
  const delta = event.key === "ArrowUp" ? -1 : 1;
  gesture.slot = Math.max(0, Math.min(gesture.others.length, gesture.slot + delta));
  marker(gesture);
  say(destination(gesture).label, false, false);
});

export function submitRelative(row, direction) {
  if (pending || reconciling || gesture || !row) return;
  const state = snapshot(row);
  if (!state) return;
  const index = state.rows.indexOf(row), neighbor = state.rows[index + (direction === "before" ? -1 : 1)];
  if (!neighbor) return;
  row.querySelector(".row-menu")?.removeAttribute("open");
  row.querySelector(".row-menu > summary")?.focus({preventScroll: true});
  const target = {anchor: neighbor, direction, label: `${direction === "before" ? "Before" : "After"} ${neighbor.dataset.rowId}`};
  if (!unchanged(state, target)) submit(state, target);
}

export function protectsQueue() { return !!gesture || !!pending; }
export function busy() { return !!gesture || !!pending || reconciling; }

export function afterQueuePatch() {
  if (pending) document.getElementById(`task-${pending.id}`)?.querySelector("[data-queue-handle]")?.focus({preventScroll: true});
  if (reconciling) {
    reconciling = false;
    const notice = feedback();
    notice?.querySelector("button")?.remove();
  }
}

export function moveResult(result, applied) {
  if (!pending) return;
  const move = pending;
  pending = null;
  move.form?.remove();
  if (result.status < 400 && applied) {
    say(`Moved ${move.id} ${move.label.toLowerCase()}.`);
    document.getElementById(`task-${move.id}`)?.querySelector("[data-queue-handle]")?.focus({preventScroll: true});
    refresh();
  } else {
    reconciling = true;
    say(`Queue move was not applied: ${result.queueError || "the queue changed"}. Refreshing before another move.`, true);
    refresh();
  }
}

export function uncertainMove() {
  if (!pending) return;
  pending.form?.remove();
  pending = null;
  reconciling = true;
  say("Move result could not be confirmed. Refreshing the queue before another move.", true);
  refresh();
}

export function unsentMove(message) {
  if (!pending) return;
  pending.form?.remove();
  pending = null;
  say(message, true);
}

window.addEventListener("pagehide", () => cancelGesture("", false));
window.addEventListener("popstate", () => cancelGesture("", false));
document.addEventListener("pellets-ui-outdated", () => cancelGesture("", false));
