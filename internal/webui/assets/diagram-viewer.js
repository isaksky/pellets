// Feature-owned native modal. No source parsing, persistence or network work.
// A viewer belongs to one exact connected diagram, never its index in a render.
let active, serial = 0;
const minimumZoom = 0.001, maximumZoom = 8, padding = 24;
const clamp = (value, min, max) => Math.max(min, Math.min(max, value));
const element = (name, className, text) => {
  const node = document.createElement(name);
  node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
};

export function closeDiagramViewer(host) {
  if (active?.host === host) active.close();
}

export function refreshDiagramViewer(host, svg) {
  if (active?.host !== host) return;
  if (active.source !== host.source) active.close();
  else if (svg) active.replace(svg);
}

export function openDiagramViewer(host, trigger) {
  const original = host.canvas.querySelector("svg");
  if (!original || !host.isConnected || host.dataset.state !== "ready") return;
  active?.close();
  const source = host.source, id = "pellets-diagram-viewer-" + ++serial;
  const parent = host.closest("dialog");
  const fallback = host.closest("[data-description]")?.querySelector('[data-description-mode="view"]');
  const scroll = [];
  for (let node = trigger; node; node = node.parentElement)
    scroll.push([node, node.scrollLeft, node.scrollTop]);
  const restoreScroll = () => {
    for (const [node, left, top] of scroll) if (node.isConnected) {
      node.scrollLeft = left; node.scrollTop = top;
    }
  };
  const dialog = element("dialog", "diagram-viewer");
  dialog.setAttribute("aria-labelledby", id + "-title");
  const header = element("header", "diagram-viewer-header");
  const title = element("h2", "", "Diagram viewer"); title.id = id + "-title";
  const button = (text, label, handler) => {
    const node = element("button", "", text); node.type = "button";
    if (label) node.setAttribute("aria-label", label);
    node.addEventListener("click", handler);
    return node;
  };
  const close = button("Close", "Close diagram viewer", () => finish());
  header.append(title, close);
  const toolbar = element("div", "diagram-viewer-toolbar");
  toolbar.setAttribute("role", "group"); toolbar.setAttribute("aria-label", "Diagram zoom");
  const output = element("output", "diagram-viewer-zoom");
  output.setAttribute("aria-label", "Current zoom"); output.setAttribute("aria-live", "polite"); output.setAttribute("aria-atomic", "true");
  const zoomOut = button("−", "Zoom out", () => zoomAt(zoom / 1.25));
  const zoomIn = button("+", "Zoom in", () => zoomAt(zoom * 1.25));
  toolbar.append(zoomOut, output, zoomIn,
    button("Fit", "Fit diagram to view", () => fit()),
    button("100%", "Reset zoom to 100%", () => { zoomAt(1); center(); paint(); }));
  const help = element("p", "diagram-viewer-help", "Scroll or pinch to zoom · Drag to pan · Keyboard: + / − zoom, arrows pan, F fit, 0 reset");
  help.id = id + "-help";
  const canvas = element("div", "diagram-viewer-canvas");
  canvas.tabIndex = 0; canvas.setAttribute("role", "region");
  canvas.setAttribute("aria-label", "Diagram canvas"); canvas.setAttribute("aria-describedby", help.id);
  const viewport = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  viewport.classList.add("diagram-viewer-viewport");
  viewport.setAttribute("preserveAspectRatio", "none");
  canvas.append(viewport);
  const details = element("details", "diagram-viewer-source");
  const pre = element("pre", "", source); pre.tabIndex = 0; pre.setAttribute("aria-label", "Mermaid source");
  details.append(element("summary", "", "Mermaid source"), pre);
  dialog.append(header, toolbar, help, canvas, details);

  let sheet, width, height, zoom = 1, x = 0, y = 0, availableWidth = 0, availableHeight = 0;
  let fitted = true, finished = false, gesture = null;
  const pointers = new Map();
  const clearSheet = () => {
    document.adoptedStyleSheets = document.adoptedStyleSheets.filter(item => item !== sheet);
  };
  const replace = svg => {
    // Copy the already allowlisted vector, remapping every ID, ARIA reference,
    // marker and scoped CSS selector. Never rerender or move the inline SVG.
    const copy = svg.cloneNode(true), ids = new Map();
    const nodes = [copy, ...copy.querySelectorAll("*")];
    // Inline IDs are already safe. Prefix them again so Mermaid's suffix-based
    // marker color selectors also keep matching in the isolated viewer copy.
    for (const node of nodes) if (node.id) ids.set(node.id, id + "-svg-" + ids.size + "-" + node.id);
    for (const node of nodes) for (const attr of [...node.attributes]) {
      let value = attr.value;
      if (attr.name === "id") value = ids.get(value);
      else if (["aria-labelledby", "aria-describedby"].includes(attr.name))
        value = value.split(/\s+/).map(value => ids.get(value) || value).join(" ");
      else value = value.replace(/url\(#([\w-]+)\)/g, (match, key) => ids.has(key) ? `url(#${ids.get(key)})` : match);
      node.setAttribute(attr.name, value);
    }
    clearSheet();
    sheet = new CSSStyleSheet();
    sheet.replaceSync(host.css.replace(/#([\w-]+)/g, (match, key) => ids.has(key) ? "#" + ids.get(key) : match));
    document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
    width = copy.viewBox.baseVal.width; height = copy.viewBox.baseVal.height;
    copy.setAttribute("x", "0"); copy.setAttribute("y", "0");
    viewport.replaceChildren(copy);
    if (availableWidth) { if (fitted) fit(); else paint(); }
  };
  // A small diagram stays centered. For large diagrams the padded bounds let
  // both extreme edges (and every intermediate point) reach the canvas.
  const bound = (position, size, available) => size <= available - 2 * padding
    ? (available - size) / 2 : clamp(position, available - size - padding, padding);
  const center = () => { x = (availableWidth - width * zoom) / 2; y = (availableHeight - height * zoom) / 2; };
  const paint = () => {
    x = bound(x, width * zoom, availableWidth); y = bound(y, height * zoom, availableHeight);
    viewport.setAttribute("viewBox", `${-x / zoom} ${-y / zoom} ${availableWidth / zoom} ${availableHeight / zoom}`);
    output.value = `${Number((zoom * 100).toFixed(1))}%`;
    zoomOut.disabled = zoom <= minimumZoom; zoomIn.disabled = zoom >= maximumZoom;
  };
  function zoomAt(value, px = availableWidth / 2, py = availableHeight / 2) {
    const next = clamp(value, minimumZoom, maximumZoom), ratio = next / zoom;
    x = px - (px - x) * ratio; y = py - (py - y) * ratio;
    zoom = next; fitted = false; paint();
  }
  function fit() {
    zoom = clamp(Math.min(1, (availableWidth - 2 * padding) / width, (availableHeight - 2 * padding) / height), minimumZoom, maximumZoom);
    fitted = true; center(); paint();
  }
  const resize = new ResizeObserver(() => {
    const nextWidth = canvas.clientWidth, nextHeight = canvas.clientHeight;
    if (!nextWidth || !nextHeight || (nextWidth === availableWidth && nextHeight === availableHeight)) return;
    x += (nextWidth - availableWidth) / 2; y += (nextHeight - availableHeight) / 2;
    availableWidth = nextWidth; availableHeight = nextHeight;
    if (fitted) fit(); else paint();
    pointers.clear(); canvas.classList.remove("is-panning"); gesture = null;
  });
  const local = event => {
    const rect = canvas.getBoundingClientRect();
    return {x: event.clientX - rect.left - canvas.clientLeft, y: event.clientY - rect.top - canvas.clientTop};
  };
  const metrics = () => {
    const [a, b] = [...pointers.values()];
    return b ? {x: (a.x + b.x) / 2, y: (a.y + b.y) / 2, distance: Math.hypot(a.x - b.x, a.y - b.y)} : {...a, distance: 0};
  };
  canvas.addEventListener("wheel", event => {
    event.preventDefault(); event.stopPropagation();
    const point = local(event), unit = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? availableHeight : 1;
    zoomAt(zoom * Math.exp(-clamp(event.deltaY * unit, -240, 240) * 0.002), point.x, point.y);
  }, {passive: false});
  canvas.addEventListener("pointerdown", event => {
    if (event.button !== 0) return;
    canvas.focus({preventScroll: true});
    canvas.setPointerCapture(event.pointerId);
    pointers.set(event.pointerId, local(event));
    canvas.classList.add("is-panning");
    event.preventDefault();
  });
  canvas.addEventListener("pointermove", event => {
    if (!pointers.has(event.pointerId)) return;
    const before = metrics(); pointers.set(event.pointerId, local(event)); const after = metrics();
    const next = before.distance > 0 && !gesture ? clamp(zoom * after.distance / before.distance, minimumZoom, maximumZoom) : zoom;
    const ratio = next / zoom;
    x = after.x - (before.x - x) * ratio; y = after.y - (before.y - y) * ratio;
    zoom = next; fitted = false; paint(); event.preventDefault();
  });
  const release = event => {
    pointers.delete(event.pointerId);
    if (!pointers.size) canvas.classList.remove("is-panning");
  };
  for (const name of ["pointerup", "pointercancel", "lostpointercapture"]) canvas.addEventListener(name, release);
  // Safari trackpads expose gesture events instead of Ctrl+wheel. Restrict
  // their cancellation to the canvas so browser accessibility zoom still works.
  canvas.addEventListener("gesturestart", event => {
    event.preventDefault(); gesture = {zoom, scale: event.scale || 1};
  });
  canvas.addEventListener("gesturechange", event => {
    event.preventDefault();
    if (gesture) { const point = local(event); zoomAt(gesture.zoom * event.scale / gesture.scale, point.x, point.y); }
  });
  canvas.addEventListener("gestureend", event => { event.preventDefault(); gesture = null; });
  canvas.addEventListener("keydown", event => {
    if (event.ctrlKey || event.metaKey || event.altKey) return;
    const step = event.shiftKey ? 160 : 40;
    switch (event.key) {
      case "+": case "=": zoomAt(zoom * 1.25); break;
      case "-": case "_": zoomAt(zoom / 1.25); break;
      case "f": case "F": fit(); break;
      case "0": zoomAt(1); center(); paint(); break;
      case "ArrowLeft": x += step; fitted = false; paint(); break;
      case "ArrowRight": x -= step; fitted = false; paint(); break;
      case "ArrowUp": y += step; fitted = false; paint(); break;
      case "ArrowDown": y -= step; fitted = false; paint(); break;
      default: return;
    }
    event.preventDefault(); event.stopPropagation();
  });
  // Backdrop gestures never fall through to the parent record's dirty guard.
  dialog.addEventListener("click", event => event.stopPropagation());
  dialog.addEventListener("keydown", event => {
    // The legacy inspector has document-level Escape/Tab handling. Native
    // modal focus/cancel must take precedence while this nested modal is open.
    if (event.key === "Escape" || event.key === "Tab") event.stopPropagation();
  });
  dialog.addEventListener("wheel", event => {
    if (event.target === dialog) event.preventDefault();
  }, {passive: false});
  dialog.addEventListener("cancel", event => { event.preventDefault(); event.stopPropagation(); finish(); });
  dialog.addEventListener("close", () => finish());
  function finish() {
    if (finished) return;
    finished = true; resize.disconnect(); pointers.clear(); clearSheet();
    if (active?.dialog === dialog) active = null;
    dialog.close(); dialog.remove();
    document.documentElement.classList.remove("diagram-viewer-open");
    const target = [trigger, fallback, parent].find(node => node?.isConnected && node.getClientRects().length);
    target?.focus({preventScroll: true}); restoreScroll();
  }
  replace(original);
  active = {host, source, dialog, close: finish, replace};
  trigger.focus({preventScroll: true});
  document.body.append(dialog);
  document.documentElement.classList.add("diagram-viewer-open");
  dialog.showModal();
  availableWidth = canvas.clientWidth; availableHeight = canvas.clientHeight;
  fit(); resize.observe(canvas); restoreScroll();
}
