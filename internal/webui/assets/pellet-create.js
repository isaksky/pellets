import { resetDescriptions } from "./description.js";
import { refreshComponents } from "./components.js";

const highlights = new Map();
let lastCreated = null;
export function refreshCreationHighlights() {
  for (const [reference, expires] of highlights) {
    const row = document.getElementById("task-" + reference);
    if (expires <= Date.now()) { highlights.delete(reference); row?.classList.remove("pellet-created"); }
    else if (row) { row.classList.remove("state-changed"); row.classList.add("pellet-created"); }
  }
  refreshCreationNotice();
}

function refreshCreationNotice() {
  const notice = document.getElementById("pellet-created");
  const form = document.querySelector("[data-pellet-create]");
  if (!notice || !lastCreated || form?.action !== lastCreated.action) return;
  const reference = lastCreated.reference;
  const row = document.getElementById("task-" + reference);
  const text = "Created " + reference + "." + (row ? "" : " Hidden by the current filters. ");
  notice.hidden = false;
  notice.classList.toggle("visually-hidden", !!row);
  // Update only when visibility changes, not on every live refresh/announcement.
  if (notice.firstChild?.textContent === text) return;
  notice.replaceChildren(document.createTextNode(text));
  if (!row) {
    const link = document.createElement("a");
    link.textContent = "View pellet";
    const path = new URL(form.action).pathname.replace(/\/pellets$/, "/tasks/");
    link.href = path + encodeURIComponent(reference) + location.search;
    notice.append(link);
  }
}

// Keep the existing disclosure/draft interaction, with one scrollable body and
// a footer that stays reachable even when options or Markdown are long.
function positionCreation() {
  const form = document.querySelector(".pellet-create[open] > form");
  if (!form) return;
  const viewport = window.visualViewport;
  const bottom = Math.min(
    (viewport?.offsetTop || 0) + (viewport?.height || innerHeight),
    document.getElementById("main").getBoundingClientRect().bottom,
  );
  form.style.setProperty("--creation-height", Math.max(0, bottom - form.getBoundingClientRect().top - 8) + "px");
}
document.addEventListener("toggle", event => {
  if (!event.target.matches(".pellet-create")) return;
  if (!event.target.open) return;
  positionCreation();
  const form = event.target.querySelector("form");
  if (!form.contains(document.activeElement)) form.elements.title.focus({preventScroll: true});
}, true);
window.addEventListener("resize", positionCreation);
window.visualViewport?.addEventListener("resize", positionCreation);
window.visualViewport?.addEventListener("scroll", positionCreation);
document.addEventListener("scroll", event => {
  if (!event.target.closest?.(".pellet-create-fields")) positionCreation();
}, true);

export function completePelletCreation(form, reference, unchanged) {
  // A user can continue typing while a response is in flight. Only the exact
  // confirmed draft may be cleared; any newer input belongs to the next draft.
  if (unchanged) {
    window.Workbench.saved(form);
    delete form.dataset.dirty;
    form.reset();
    resetDescriptions(form);
    refreshComponents();
    form.querySelector(".pellet-create-options").open = false;
    form.querySelector(".pellet-create-fields").scrollTop = 0;
    const disclosure = form.closest("details");
    const main = form.closest("#main");
    const returnFocus = disclosure.contains(document.activeElement) || document.activeElement === document.body || document.activeElement === main;
    disclosure.open = false;
    if (returnFocus) {
      const opener = disclosure.querySelector(":scope > summary");
      opener.focus({preventScroll: true});
      // WebKit can drop focus after disabling the clicked submit button and
      // closing its details. Restore it once layout settles, unless the user
      // has already chosen another control.
      requestAnimationFrame(() => {
        if (opener.isConnected && (document.activeElement === document.body || document.activeElement === main)) opener.focus({preventScroll: true});
      });
    }
  }
  // The retry key survives uncertain failures, but never a confirmed creation.
  form.elements.request_id.value = "";
  const row = document.getElementById("task-" + reference);
  lastCreated = {reference, action: form.action};
  refreshCreationNotice();
  if (row) {
    // Include the first-ever row, for which there is no previous row snapshot.
    // Only background color animates: row menus must keep their viewport origin.
    highlights.set(reference, Date.now() + 1800);
    refreshCreationHighlights();
    if (unchanged) row.scrollIntoView({block: "nearest", inline: "nearest", behavior: "instant"});
    setTimeout(refreshCreationHighlights, 1800);
  }
}
