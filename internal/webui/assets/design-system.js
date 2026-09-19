import { refreshComponents } from "./components.js";
import "./filters.js";

// Deliberately no app.js, settings transport, event stream, or database requests.
const root = document.documentElement;
const theme = document.getElementById("theme-select");
const notice = document.getElementById("ds-notice");
let noticeTimer;

function applyTheme(value) {
  root.dataset.theme = value;
  root.dataset.themeChoice = value;
  refreshComponents();
  const style = getComputedStyle(root);
  document.querySelectorAll("[data-swatch]").forEach(swatch => {
    const token = swatch.dataset.swatch;
    swatch.style.backgroundColor = `var(--${token})`;
    document.querySelector(`[data-color-value="${token}"]`).textContent = style.getPropertyValue(`--${token}`).trim();
  });
}
theme.value = root.dataset.theme || "gruvbox-light";
applyTheme(theme.value);
theme.addEventListener("change", () => applyTheme(theme.value));
document.querySelectorAll("[data-indeterminate]").forEach(input => { input.indeterminate = true; });

function notify(message, undo = false) {
  clearTimeout(noticeTimer);
  notice.querySelector("span").textContent = message;
  notice.querySelector("[data-undo]").hidden = !undo;
  notice.hidden = false;
  noticeTimer = setTimeout(() => { notice.hidden = true; }, 6000);
}

function openDialog(dialog, trigger) {
  document.querySelectorAll("details[open]").forEach(details => { details.open = false; });
  dialog.closest("pl-dialog").showModal(trigger);
}

// Use the real server-rendered record templates, but intercept every action.
// form-action 'none' also prevents submission when scripts are unavailable.
document.addEventListener("submit", event => {
  event.preventDefault();
  const dialog = event.target.closest("dialog");
  if (dialog) dialog.close();
  event.target.closest("details")?.removeAttribute("open");
  notify(`${event.submitter?.textContent.trim() || "Changes saved"} · preview only`);
});

document.addEventListener("click", event => {
  const trigger = event.target.closest("button, a");
  if (!trigger) return;
  if (trigger.dataset.record) {
    const host = document.getElementById("inspector-host");
    host.replaceChildren(document.getElementById(`ds-record-${trigger.dataset.record}`).content.cloneNode(true));
    openDialog(document.getElementById("record-dialog"), trigger);
  }
  if (trigger.dataset.openDialog) openDialog(document.getElementById(trigger.dataset.openDialog), trigger);
  if (trigger.matches("[data-close-dialog], [data-dialog-close], #inspector-host a.icon-button")) {
    event.preventDefault();
    trigger.closest("pl-dialog")?.requestClose();
  }
  if (trigger.dataset.demoAction) {
    const dialog = trigger.closest("dialog");
    if (dialog) dialog.close();
    trigger.closest("details")?.removeAttribute("open");
    notify(`${trigger.dataset.demoAction} · preview only`);
  }
  if (trigger.dataset.confirm) {
    const message = trigger.dataset.confirm === "discard" ? "Discard unsaved inspector changes?" : "Reload the saved chat? Copy any unsaved planning edits you want to keep first.";
    notify(window.confirm(message) ? "Confirmed · preview only" : "Cancelled · preview only");
  }
  if (trigger.matches("[data-toggle-split]")) {
    const split = document.querySelector(".plan-split");
    split.hidden = !split.hidden;
    if (!split.hidden) split.querySelector("textarea").focus();
  }
  if (trigger.matches("[data-demo-undo]")) notify("Checkpoint removed · preview only", true);
  if (trigger.matches("[data-undo]")) notify("Checkpoint restored · preview only");
  if (trigger.matches("[data-dismiss-notice]")) notice.hidden = true;
});

document.querySelector("#insert-dialog .scope-options").addEventListener("change", () => {
  document.querySelector("[data-insert-submit]").disabled = !document.querySelector('#insert-dialog input[name="target"]:checked');
});

const links = Array.from(document.querySelectorAll(".ds-sidebar nav a"));
const observer = new IntersectionObserver(entries => {
  for (const entry of entries) if (entry.isIntersecting) {
    links.forEach(link => {
      if (link.hash === `#${entry.target.id}`) link.setAttribute("aria-current", "location");
      else link.removeAttribute("aria-current");
    });
  }
}, {rootMargin: "-15% 0px -65% 0px"});
document.querySelectorAll(".ds-main > section[id]").forEach(section => observer.observe(section));

document.getElementById("ds-resizer").addEventListener("pl-resize", event => {
  const resizer = event.target;
  if (event.detail.phase === "reset") resizer.value = 180;
  document.querySelector(".ds-resize-preview").style.width = resizer.value + "px";
  document.getElementById("ds-resize-value").textContent = resizer.value + "px";
});
