// Keep queue filters in their form while floating above independently scrolling
// panes. Native selects and Datastar remain responsible for filter state.
let current = null;
let positioning = false;
let pointerWithinFilters = false;

function closeFilters(restoreFocus = false) {
  if (!current) return;
  const { details, panel, trigger } = current;
  current = null;
  details.open = false;
  if (typeof panel.hidePopover === "function" && panel.matches(":popover-open"))
    panel.hidePopover();
  if (restoreFocus && trigger.isConnected)
    trigger.focus({ preventScroll: true });
}

function placeFilters() {
  if (!current) return;
  const { details, panel, trigger } = current;
  if (!details.isConnected || !details.open) {
    closeFilters();
    return;
  }
  const rect = trigger.getBoundingClientRect();
  const viewport = window.visualViewport;
  const leftEdge = viewport?.offsetLeft || 0;
  const topEdge = viewport?.offsetTop || 0;
  const rightEdge =
    leftEdge + (viewport?.width || document.documentElement.clientWidth);
  const bottomEdge = topEdge + (viewport?.height || window.innerHeight);
  const main = document.getElementById("main")?.getBoundingClientRect();
  if (
    !rect.width ||
    rect.bottom < Math.max(topEdge, main?.top || 0) ||
    rect.top > Math.min(bottomEdge, main?.bottom || bottomEdge)
  ) {
    closeFilters();
    return;
  }
  panel.style.maxWidth = `${Math.max(0, rightEdge - leftEdge - 16)}px`;
  panel.style.maxHeight = `${Math.max(0, bottomEdge - topEdge - 16)}px`;
  const natural = panel.getBoundingClientRect();
  const below = bottomEdge - rect.bottom - 12;
  const above = rect.top - topEdge - 12;
  const flip = below < natural.height && above > below;
  panel.style.maxHeight = `${Math.max(0, flip ? above : below)}px`;
  const size = panel.getBoundingClientRect();
  panel.style.left = `${Math.max(leftEdge + 8, Math.min(rect.right - size.width, rightEdge - size.width - 8))}px`;
  panel.style.top = `${Math.max(topEdge + 8, flip ? rect.top - size.height - 4 : rect.bottom + 4)}px`;
}

function schedulePosition() {
  if (!current || positioning) return;
  positioning = true;
  requestAnimationFrame(() => {
    positioning = false;
    placeFilters();
  });
}

export function syncQueueFilters() {
  const details = document.getElementById("queue-filters");
  const panel = details?.querySelector(".filter-fields");
  if (current && current.panel !== panel) closeFilters();
  if (!details?.open || !panel) {
    if (current) closeFilters();
    return;
  }
  const trigger = details.querySelector("summary");
  current = { details, panel, trigger };
  if (typeof panel.showPopover === "function") {
    panel.setAttribute("popover", "manual");
    if (!panel.matches(":popover-open")) panel.showPopover();
  }
  placeFilters();
}

function withinFilters(target) {
  if (!current) return false;
  if (current.details.contains(target)) return true;
  // Enhanced selects use a separate top-layer listbox; it still belongs to
  // this filter form. Selecting an option must not dismiss the outer panel.
  const listbox = target.closest(".select-popover");
  return (
    !!listbox &&
    current.panel.contains(document.getElementById(listbox.dataset.selectId))
  );
}

document.addEventListener(
  "toggle",
  (event) => {
    if (event.target.id === "queue-filters") syncQueueFilters();
  },
  true,
);
document.addEventListener("click", (event) => {
  if (current && !withinFilters(event.target))
    closeFilters(current.panel.contains(document.activeElement));
});
// Nested selects consume outside clicks. Observe the pointer before that
// capture handler, then close the outer panel after the select restores focus.
window.addEventListener(
  "pointerdown",
  (event) => {
    // Safari may focus the tabindex-bearing main pane when a mouse presses
    // a button. That transient focus is not keyboard navigation out of Filters.
    pointerWithinFilters = !!current && withinFilters(event.target);
    if (!current || event.button !== 0 || withinFilters(event.target)) return;
    const details = current.details;
    requestAnimationFrame(() => {
      if (current?.details === details)
        closeFilters(current.panel.contains(document.activeElement));
    });
  },
  true,
);
window.addEventListener("pointerup", () => {
  setTimeout(() => { pointerWithinFilters = false; }, 0);
}, true);
window.addEventListener("pointercancel", () => { pointerWithinFilters = false; }, true);
document.addEventListener("keydown", (event) => {
  pointerWithinFilters = false;
  if (current && event.key === "Escape") {
    event.preventDefault();
    event.stopImmediatePropagation();
    closeFilters(true);
  }
});
document.addEventListener("focusin", (event) => {
  if (current && !pointerWithinFilters && !withinFilters(event.target)) closeFilters();
});
document.addEventListener(
  "scroll",
  (event) => {
    if (current && !current.panel.contains(event.target)) schedulePosition();
  },
  true,
);
window.addEventListener("resize", schedulePosition);
window.visualViewport?.addEventListener("resize", schedulePosition);
window.visualViewport?.addEventListener("scroll", schedulePosition);
