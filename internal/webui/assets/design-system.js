import { refreshComponents } from "./components.js";
import "./filters.js";
import { renderMarkdown } from "./markdown.js";
import "./description-outline.js";

// Deliberately no app.js, settings transport, event stream, or database requests.
const root = document.documentElement;
// Exercise the production outline with page-local presentation state only.
const contentsReader = document.querySelector("#contents pl-description-reader");
const contentsText = "Contents follows rendered headings and keeps navigation inside this description. ";
contentsReader.querySelector(".markdown-body").replaceChildren(renderMarkdown(
  "# Reading a pellet\n\n" + contentsText.repeat(8) +
  "\n\n### Skipped heading level\n\n" + contentsText.repeat(8) +
  "\n\n## Repeated section\n\n" + contentsText.repeat(8) +
  "\n\n## Repeated section\n\n" + contentsText.repeat(8) +
  "\n\n### 日本語 and `inline code`\n\n" + contentsText.repeat(8)
));
contentsReader.refresh({mode: "view"});
const diagramExamples = [
  "flowchart LR\n  Draft --> Review\n  Review --> Ready",
  "sequenceDiagram\n  User->>Pellets: Save Mermaid source\n  Pellets-->>User: Render locally",
  "stateDiagram-v2\n  [*] --> Open\n  Open --> Active\n  Active --> Closed",
  "flowchart LR\n  A[unfinished",
  '%%{init: {"securityLevel":"loose"}}%%\nflowchart LR\nA-->B',
  'flowchart LR\nA-->B\nclick A "https://example.invalid"',
  "flowchart LR\nA[" + "oversized ".repeat(500) + "]",
];
const diagramPreview = document.getElementById("ds-diagrams");
function showDiagramExamples() {
  diagramPreview.replaceChildren(renderMarkdown(diagramExamples.map(source => "```mermaid\n" + source + "\n```").join("\n\n")));
}
showDiagramExamples();
document.getElementById("ds-diagram-fixtures").addEventListener("click", showDiagramExamples);
document.getElementById("ds-diagram-preview").addEventListener("click", () => {
  diagramPreview.replaceChildren(renderMarkdown("```mermaid\n" + document.getElementById("ds-diagram-source").value + "\n```"));
});
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
  if (trigger.closest("[data-gallery-review]")) {
    event.preventDefault();
    if (trigger.matches(".checkpoint-open")) trigger.dataset.record = "checkpoint";
    else trigger.dataset.demoAction = trigger.textContent.trim();
  }
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

// Local-only example: updating options keeps the menu and keyboard focus intact.
document.addEventListener('pl-select-action', event => {
  const select=event.target.querySelector('#ds-live-catalog');
  if(!select)return;
  if(!select.querySelector('[value=two]')) select.add(new Option('Example two','two'));
  select.dataset.menuStatus='Example refreshed locally';
  event.target.refresh();
});
