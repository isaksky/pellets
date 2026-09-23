import { renderMarkdown } from "./markdown.js";
import "./description-outline.js";

// Presentation only: source, dirty state, versions and submission remain owned
// by the existing native forms. Receipts retain presentation, not source copies.
const receipts = new Map();
const rendered = new WeakMap();
const storageKey = "pellets-description-presentation-v1";
let focusReceipt = null;
try {
  const saved = JSON.parse(sessionStorage.getItem(storageKey) || "[]");
  for (const [key, value] of saved.slice(-96))
    if (["edit", "view"].includes(value.mode)) receipts.set(key, value);
} catch {}
function key(host) { return host.dataset.descriptionKey; }
function receipt(host) {
  const id = key(host);
  if (!receipts.has(id)) receipts.set(id, {mode: host.dataset.descriptionDefault || "edit"});
  while (receipts.size > 96) receipts.delete(receipts.keys().next().value);
  return receipts.get(id);
}
function persist() {
  try { sessionStorage.setItem(storageKey, JSON.stringify([...receipts])); } catch {}
}
function hosts(scope) { return scope.querySelectorAll("[data-description]"); }
function scrollState(el) { return {top: el.scrollTop, left: el.scrollLeft}; }
function restoreScroll(el, saved) {
  if (!el || !saved) return;
  el.scrollTop = saved.top;
  el.scrollLeft = saved.left;
}
function capture(host) {
  const saved = receipt(host), field = host.querySelector("textarea"), view = host.querySelector(".markdown-body");
  if (!view) return;
  if (saved.mode === "edit") {
    saved.selection = [field.selectionStart, field.selectionEnd, field.selectionDirection];
    saved.editScroll = scrollState(field);
  } else {
    saved.viewScroll = scrollState(view);
    saved.blocks = Array.from(view.querySelectorAll("pre,.markdown-table"), scrollState);
    host.querySelector("pl-description-reader")?.capture(saved);
  }
  const outer = host.closest(".inspector-scroll,.plan-row-editor");
  if (outer?.getClientRects().length) saved.outer = scrollState(outer);
}
export function rememberDescriptions(scope = document) {
  if (scope === document) focusReceipt = null;
  for (const host of hosts(scope)) {
    capture(host);
    const active = document.activeElement;
    if (host.contains(active)) {
      const targets = Array.from(host.querySelectorAll("textarea,button,a,[tabindex]"));
      focusReceipt = {key: key(host), id: active.id, index: targets.indexOf(active)};
    }
  }
}
function draw(host) {
  const saved = receipt(host), field = host.querySelector("textarea"), label = field.closest("label");
  const titleText = host.dataset.descriptionLabel || "Description";
  let toolbar = host.querySelector(".description-toolbar"), view = host.querySelector(".markdown-body");
  if (!toolbar?.childElementCount) {
    toolbar ||= document.createElement("div");
    toolbar.className = "description-toolbar";
    toolbar.dataset.ignoreMorph = "";
    toolbar.setAttribute("role", "group");
    toolbar.setAttribute("aria-label", titleText + " display");
    const title = document.createElement("span");
    title.textContent = titleText;
    toolbar.append(title);
    for (const mode of ["edit", "view"]) {
      const button = document.createElement("button");
      button.type = "button";
      button.dataset.descriptionMode = mode;
      button.textContent = mode === "edit" ? "Edit" : host.dataset.descriptionDefault === "view" ? "View / Preview" : "Preview";
      toolbar.append(button);
    }
    host.prepend(toolbar);
  }
  if (!view) {
    view = document.createElement("div");
    view.className = "markdown-body";
    view.dataset.ignoreMorph = "";
    host.append(view);
  }
  view.tabIndex = 0;
  view.setAttribute("role", "region");
  view.setAttribute("aria-label", titleText + " preview");
  if (!field.id) field.id = "description-source-" + key(host);
  view.id = "description-view-" + key(host);
  toolbar.querySelectorAll("[data-description-mode]").forEach(button => {
    const editing = button.dataset.descriptionMode === "edit";
    button.setAttribute("aria-pressed", String(button.dataset.descriptionMode === saved.mode));
    button.setAttribute("aria-controls", editing ? field.id : view.id);
  });
  label.hidden = saved.mode !== "edit";
  label.classList.add("description-source");
  // The toolbar supplies the visible heading in both modes. Retain the native
  // source label for accessibility without repeating it above the textarea.
  // Reconcile fresh labels from live patches without wrapping existing spans.
  for (const node of Array.from(label.childNodes)) {
    if (node.nodeType !== Node.TEXT_NODE || !node.textContent.trim()) continue;
    const text = document.createElement("span");
    text.className = "visually-hidden";
    node.replaceWith(text);
    text.append(node);
  }
  view.hidden = saved.mode === "edit";
  const changed = rendered.get(view) !== field.value;
  if (changed) {
    view.replaceChildren(renderMarkdown(field.value));
    if (!field.value.trim()) {
      const empty = document.createElement("p");
      empty.className = "description-empty";
      empty.textContent = host.dataset.descriptionEmpty || "No description yet. Choose Edit to add one.";
      view.append(empty);
    }
    rendered.set(view, field.value);
  }
  if (saved.selection) field.setSelectionRange(...saved.selection);
  restoreScroll(field, saved.editScroll);
  restoreScroll(view, saved.viewScroll);
  view.querySelectorAll("pre,.markdown-table").forEach((block, index) => restoreScroll(block, saved.blocks?.[index]));
  restoreScroll(host.closest(".inspector-scroll,.plan-row-editor"), saved.outer);
  host.querySelector("pl-description-reader")?.refresh(saved, changed);
}
export function refreshDescriptions(scope = document) {
  for (const host of hosts(scope)) {
    draw(host);
    if (focusReceipt?.key === key(host)) {
      const target = (focusReceipt.id && document.getElementById(focusReceipt.id))
        || host.querySelectorAll("textarea,button,a,[tabindex]")[focusReceipt.index];
      if (target?.getClientRects().length) target.focus({preventScroll: true});
      focusReceipt = null;
    }
  }
}
document.addEventListener("click", event => {
  const button = event.target.closest("[data-description-mode]");
  if (!button) return;
  const host = button.closest("[data-description]"), saved = receipt(host);
  capture(host);
  saved.mode = button.dataset.descriptionMode;
  draw(host);
  if (saved.mode === "edit") host.querySelector("textarea").focus({preventScroll: true});
  persist();
});
// Keep the latest caret even when a property-only refresh happens during editing.
document.addEventListener("selectionchange", () => {
  const field = document.activeElement;
  if (field?.matches("[data-description] textarea")) capture(field.closest("[data-description]"));
});
document.addEventListener("input", event => {
  const host = event.target.closest("[data-description]");
  if (host) capture(host);
});
window.addEventListener("pagehide", () => { rememberDescriptions(); persist(); });
