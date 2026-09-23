// Keep native details and their forms in place, but position menu surfaces in
// viewport coordinates so the queue/sidebar scrollports cannot clip controls.
export function positionMenus() {
  document.querySelectorAll(".switcher[open],.row-menu[open],.assignment-popover[open]").forEach(menu => {
    const panel = menu.querySelector(":scope > .switcher-menu,:scope > .row-popover,:scope > .assignment-form,:scope > .recipient-form");
    if (!panel) return;
    const anchor = menu.querySelector("summary").getBoundingClientRect();
    const viewport = window.visualViewport;
    const left = viewport?.offsetLeft || 0, top = viewport?.offsetTop || 0;
    const right = left + (viewport?.width || document.documentElement.clientWidth);
    const bottom = top + (viewport?.height || innerHeight);
    panel.style.maxWidth = Math.max(0, Math.min(360, right - left - 16)) + "px";
    panel.style.maxHeight = Math.max(0, bottom - top - 16) + "px";
    const natural = panel.getBoundingClientRect();
    const below = bottom - anchor.bottom - 12, above = anchor.top - top - 12;
    const flip = below < natural.height && above > below;
    panel.style.maxHeight = Math.max(0, flip ? above : below) + "px";
    const size = panel.getBoundingClientRect();
    const start = menu.matches(".row-menu") ? anchor.right - size.width : anchor.left;
    panel.style.left = Math.max(left + 8, Math.min(start, right - size.width - 8)) + "px";
    panel.style.top = Math.max(top + 8, flip ? anchor.top - size.height - 4 : anchor.bottom + 4) + "px";
    panel.style.right = "auto";
  });
}
document.addEventListener("toggle", event => {
  if (event.target.matches(".switcher,.row-menu,.assignment-popover")) positionMenus();
}, true);
window.addEventListener("resize", positionMenus);
window.visualViewport?.addEventListener("resize", positionMenus);
window.visualViewport?.addEventListener("scroll", positionMenus);
document.addEventListener("scroll", event => {
  if (!event.target.closest?.(".switcher-menu,.row-popover,.assignment-form,.recipient-form")) positionMenus();
}, true);
